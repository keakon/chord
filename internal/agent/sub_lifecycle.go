package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
)

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// InjectUserMessage receives user messages directly (non-blocking enqueue).
// Overflow is preserved in-memory so older messages are not silently dropped.
// This is safe to call from any goroutine (typically MainAgent's event loop).
func (s *SubAgent) InjectUserMessage(content string) bool {
	return s.enqueueUserMessage(pendingUserMessage{Content: content})
}

func (s *SubAgent) InjectManualUserMessage(content string, drainContextAppends bool) bool {
	return s.enqueueUserMessage(pendingUserMessage{Content: content, FromUser: true, DrainContextAppends: drainContextAppends})
}

func (s *SubAgent) InjectUserMessageWithMailboxAck(content, mailboxAckID string, mailbox *message.MailboxMetadata) bool {
	return s.enqueueUserMessage(pendingUserMessage{Content: content, MailboxAckID: strings.TrimSpace(mailboxAckID), Mailbox: mailbox})
}

// InjectUserMessageWithParts enqueues a multi-part user message for the SubAgent.
func (s *SubAgent) InjectUserMessageWithParts(parts []message.ContentPart) bool {
	return s.enqueueUserMessage(pendingUserMessageFromDraft("", parts))
}

func (s *SubAgent) InjectManualUserMessageWithParts(parts []message.ContentPart, drainContextAppends bool) bool {
	input := pendingUserMessageFromDraft("", parts)
	input.FromUser = true
	input.DrainContextAppends = drainContextAppends
	return s.enqueueUserMessage(input)
}

// QueuePendingUserDraft enqueues a TUI draft with its identity preserved so
// the transcript can reveal it only when the SubAgent actually consumes it.
func (s *SubAgent) QueuePendingUserDraft(draftID string, parts []message.ContentPart) bool {
	return s.enqueueUserMessage(pendingUserMessageFromDraft(draftID, parts))
}

func (s *SubAgent) UpdatePendingUserDraft(draftID string, parts []message.ContentPart) bool {
	updated := pendingUserMessageFromDraft(draftID, parts)
	return s.mutatePendingUserDraft(draftID, func(_ pendingUserMessage) (pendingUserMessage, bool) {
		return updated, true
	})
}

func (s *SubAgent) RemovePendingUserDraft(draftID string) bool {
	return s.mutatePendingUserDraft(draftID, func(input pendingUserMessage) (pendingUserMessage, bool) {
		return input, false
	})
}

func (s *SubAgent) mutatePendingUserDraft(draftID string, mutate func(pendingUserMessage) (pendingUserMessage, bool)) bool {
	if s == nil || strings.TrimSpace(draftID) == "" {
		return false
	}
	s.inputQueueMu.Lock()
	defer s.inputQueueMu.Unlock()

	queued := make([]pendingUserMessage, 0, len(s.inputCh)+len(s.inputOverflow))
	for {
		select {
		case input := <-s.inputCh:
			queued = append(queued, input)
		default:
			goto drained
		}
	}

drained:
	queued = append(queued, s.inputOverflow...)
	s.inputOverflow = nil
	found := false
	for _, input := range queued {
		if !found && input.DraftID == draftID {
			updated, keep := mutate(input)
			input = updated
			found = true
			if !keep {
				continue
			}
		}
		select {
		case s.inputCh <- input:
		default:
			s.inputOverflow = append(s.inputOverflow, input)
		}
	}
	return found
}

func (s *SubAgent) enqueueUserMessage(input pendingUserMessage) bool {
	if s == nil {
		return false
	}
	s.inputQueueMu.Lock()
	defer s.inputQueueMu.Unlock()
	if len(s.inputOverflow) > 0 {
		s.inputOverflow = append(s.inputOverflow, input)
		s.signalWake()
		return true
	}
	select {
	case s.inputCh <- input:
		s.signalWake()
		return true
	case <-s.parentCtx.Done():
		return false
	default:
		s.inputOverflow = append(s.inputOverflow, input)
		s.signalWake()
		return true
	}
}

func (s *SubAgent) signalWake() {
	if s == nil || s.wakeCh == nil {
		return
	}
	select {
	case s.wakeCh <- struct{}{}:
	default:
	}
}

func (s *SubAgent) armStartupWatchdog() {
	if s == nil || s.startupTimeout <= 0 {
		return
	}
	seq := s.startupWatchdogSeq.Add(1)
	time.AfterFunc(s.startupTimeout, func() { s.checkStartupWatchdog(seq, false) })
}

func (s *SubAgent) checkStartupWatchdog(seq uint64, wakeRetried bool) {
	if s.parentCtx.Err() != nil || s.startupWatchdogSeq.Load() != seq || s.State() != SubAgentStateRunning {
		return
	}
	if s.hasActiveTurn() || s.llmRequestInFlight.Load() || !s.hasPendingUserInput() {
		return
	}
	if !wakeRetried {
		log.Warnf("SubAgent startup watchdog retrying wake agent=%v timeout=%v", s.instanceID, s.startupTimeout)
		s.signalWake()
		time.AfterFunc(s.startupTimeout, func() { s.checkStartupWatchdog(seq, true) })
		return
	}
	s.sendEvent(Event{
		Type:    EventAgentError,
		Payload: fmt.Errorf("SubAgent startup stalled after automatic wake retry: queued input was not consumed within %s", 2*s.startupTimeout),
	})
}

func (s *SubAgent) hasActiveTurn() bool {
	if s == nil {
		return false
	}
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	return s.turn != nil
}

func (s *SubAgent) hasPendingUserInput() bool {
	if s == nil {
		return false
	}
	s.inputQueueMu.Lock()
	defer s.inputQueueMu.Unlock()
	return len(s.inputCh) > 0 || len(s.inputOverflow) > 0
}

// drainPendingToolFailureSets clears speculative stream tools and execution
// pending tools after a terminal error. emit is the union (for TUI); persist is
// execution-phase only (for JSONL — stream-only IDs are absent from assistant).
func (s *SubAgent) drainPendingToolFailureSets(err error) (emit []PendingToolCall, persist []PendingToolCall) {
	if s == nil || s.turn == nil {
		return nil, nil
	}
	streaming := s.turn.drainStreamingToolCalls()
	failedExec := s.turn.cancelPendingToolCalls()
	merged := mergePendingToolCalls(streaming, failedExec)
	if len(merged) == 0 {
		return nil, nil
	}
	pending := s.turn.PendingToolCalls.Load()
	if s.turn.activeToolBatchCancel != nil {
		s.turn.activeToolBatchCancel()
		s.turn.activeToolBatchCancel = nil
	}
	s.turn.PendingToolCalls.Store(0)
	s.turn.TotalToolCalls.Store(0)
	s.turn.toolExecutionBatches = nil
	s.turn.nextToolBatch = 0
	log.Warnf("SubAgent: failing pending tool calls after terminal error agent=%v turn_id=%v pending_tools=%v failed_tools=%v error=%v", s.instanceID, s.turn.ID, pending, len(merged), err)
	return merged, failedExec
}

func (s *SubAgent) persistInterruptedToolResults(calls []PendingToolCall, status ToolResultStatus, cause error) int {
	return persistInterruptedToolResultsInto(s.ctxMgr, calls, status, cause,
		func(dropped int) {
			log.Warnf("SubAgent: skipping synthetic tool persistence for call_ids absent from assistant history agent=%v dropped=%v", s.instanceID, dropped)
		},
		func(toolMsg message.Message) bool {
			if s.parent != nil {
				return s.parent.persistAsyncAfter(s.instanceID, toolMsg, nil)
			}
			if s.recovery != nil {
				if err := s.recovery.PersistMessage(s.instanceID, toolMsg); err != nil {
					log.Warnf("SubAgent: failed to persist interrupted tool result agent=%v call_id=%v error=%v", s.instanceID, toolMsg.ToolCallID, err)
					return false
				}
			}
			return true
		},
	)
}

// CancelCurrentTurn cancels the SubAgent's active turn and persists synthetic
// terminal tool results for any pending calls so session restore shows them as
// cancelled instead of pending forever.
func (s *SubAgent) CancelCurrentTurn() bool {
	if s == nil {
		return false
	}
	select {
	case s.continueCh <- continueMsg{cancelCurrentTurn: true}:
		return true
	default:
		return false
	}
}

// GetMessages returns a thread-safe snapshot of the SubAgent's conversation
// history (for TUI display when the user tabs to this agent).
func (s *SubAgent) GetMessages() []message.Message {
	return s.ctxMgr.Snapshot()
}

// RestoreMessages loads a previously persisted message history into the
// SubAgent's context manager. Used during session restore to rebuild a
// SubAgent's conversation without replaying LLM calls.
func (s *SubAgent) RestoreMessages(msgs []message.Message) {
	s.ctxMgr.RestoreMessages(msgs)
	s.restoreInvokedSkills(msgs)
}

// ContinueFromContext signals the SubAgent to re-run the LLM with its
// existing context without appending a new user message. Non-blocking.
func (s *SubAgent) ContinueFromContext() {
	s.continueWithContextAppends(false, false)
}

func (s *SubAgent) continueWithContextAppends(drainContextAppends, restartStoppedTurn bool) {
	select {
	case s.continueCh <- continueMsg{drainContextAppends: drainContextAppends, restartStoppedTurn: restartStoppedTurn}:
	default: // already pending
	}
}

// RemoveLastMessage removes the last message from the SubAgent's context
// and rewrites the persistence log. Only safe when idle (turn == nil),
// but since this is called from the TUI goroutine and the actual mutation
// happens on the runLoop goroutine via handleContinue, we use DropLastMessage
// which is mutex-protected. The persistence rewrite is best-effort.
func (s *SubAgent) RemoveLastMessage() {
	s.ctxMgr.DropLastMessage()
	if s.recovery != nil {
		remaining := s.ctxMgr.Snapshot()
		if err := s.recovery.RewriteLog(s.instanceID, remaining); err != nil {
			log.Warnf("SubAgent.RemoveLastMessage: failed to rewrite log agent=%v error=%v", s.instanceID, err)
		}
	}
}

// handleContinue starts a new turn and calls LLM without appending a new
// user message. Runs on the SubAgent's event loop goroutine.
func (s *SubAgent) handleContinue() {
	s.drainQueuedContextAppendsForContinue()
	s.newTurn()
	s.continueLLMWithPendingUserMessages()
}

func (s *SubAgent) drainQueuedContextAppendsForContinue() {
	for {
		msg, ok := s.tryReceiveContextAppend()
		if !ok {
			break
		}
		s.appendContextOnly(msg)
		s.refillContextAppendChannelFromOverflow()
	}
}

// GetContextStats returns current input-context usage and usable input budget for this SubAgent.
// current is the full prompt-side burden from the most recent API call: input tokens plus cache-write tokens.
func (s *SubAgent) GetContextStats() (current, limit int) {
	return s.ctxMgr.LastTotalContextTokens(), s.ctxMgr.GetUsableInputBudget()
}

// GetContextMessageCount returns the number of messages in this agent's context (for sidebar).
func (s *SubAgent) GetContextMessageCount() int {
	return s.ctxMgr.MessageCount()
}

func (s *SubAgent) GetContextBytes() int {
	s.llmMu.RLock()
	toolDefs := append([]message.ToolDefinition(nil), s.frozenToolDefs...)
	s.llmMu.RUnlock()
	return s.ctxMgr.ContextPayloadBytes() + toolDefinitionBytes(toolDefs)
}

func (s *SubAgent) GetContextReductionStats() ContextReductionStats {
	return ContextReductionStats{}
}
