package agent

import (
	"log/slog"
	"strings"

	"github.com/keakon/chord/internal/message"
)

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// InjectUserMessage receives user messages directly (non-blocking enqueue).
// Overflow is preserved in-memory so older messages are not silently dropped.
// This is safe to call from any goroutine (typically MainAgent's event loop).
func (s *SubAgent) InjectUserMessage(content string) {
	s.enqueueUserMessage(pendingUserMessage{Content: content})
}

func (s *SubAgent) InjectUserMessageWithMailboxAck(content, mailboxAckID string) {
	s.enqueueUserMessage(pendingUserMessage{Content: content, MailboxAckID: strings.TrimSpace(mailboxAckID)})
}

// InjectUserMessageWithParts enqueues a multi-part user message for the SubAgent.
func (s *SubAgent) InjectUserMessageWithParts(parts []message.ContentPart) {
	s.enqueueUserMessage(pendingUserMessageFromDraft("", parts))
}

func (s *SubAgent) enqueueUserMessage(input pendingUserMessage) {
	if s == nil {
		return
	}
	s.inputQueueMu.Lock()
	defer s.inputQueueMu.Unlock()
	if len(s.inputOverflow) > 0 {
		s.inputOverflow = append(s.inputOverflow, input)
		return
	}
	select {
	case s.inputCh <- input:
	case <-s.parentCtx.Done():
		return
	default:
		s.inputOverflow = append(s.inputOverflow, input)
	}
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
	slog.Warn("SubAgent: failing pending tool calls after terminal error",
		"agent", s.instanceID,
		"turn_id", s.turn.ID,
		"pending_tools", pending,
		"failed_tools", len(merged),
		"error", err,
	)
	return merged, failedExec
}

func (s *SubAgent) persistInterruptedToolResults(calls []PendingToolCall, status ToolResultStatus, cause error) int {
	if len(calls) == 0 {
		return 0
	}
	orig := len(calls)
	calls = filterPendingCallsForDeclaredTools(s.ctxMgr, calls)
	if len(calls) < orig {
		slog.Warn("SubAgent: skipping synthetic tool persistence for call_ids absent from assistant history",
			"agent", s.instanceID,
			"dropped", orig-len(calls),
		)
	}
	if len(calls) == 0 {
		return 0
	}
	msgText := toolCallFailureMessage(cause)
	if status == ToolResultStatusCancelled {
		msgText = "Cancelled"
	}

	persisted := 0
	for _, call := range calls {
		toolMsg := message.Message{
			Role:       "tool",
			ToolCallID: call.CallID,
			Content:    msgText,
			Audit:      call.Audit.Clone(),
		}
		s.ctxMgr.Append(toolMsg)
		if s.recovery != nil {
			if err := s.recovery.PersistMessage(s.instanceID, toolMsg); err != nil {
				slog.Warn("SubAgent: failed to persist interrupted tool result",
					"agent", s.instanceID, "call_id", call.CallID, "error", err)
			} else {
				persisted++
			}
			continue
		}
		persisted++
	}
	return persisted
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
}

// ContinueFromContext signals the SubAgent to re-run the LLM with its
// existing context without appending a new user message. Non-blocking.
func (s *SubAgent) ContinueFromContext() {
	select {
	case s.continueCh <- continueMsg{}:
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
			slog.Warn("SubAgent.RemoveLastMessage: failed to rewrite log",
				"agent", s.instanceID, "error", err)
		}
	}
}

// handleContinue starts a new turn and calls LLM without appending a new
// user message. Runs on the SubAgent's event loop goroutine.
func (s *SubAgent) handleContinue() {
	s.drainQueuedContextAppendsForContinue()
	turn := s.newTurn()
	messages := s.ctxMgr.Snapshot()
	s.asyncCallLLM(turn, messages)
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

// GetContextStats returns current context usage and limit for this SubAgent.
// Current = input + output + cache + reasoning from last API response.
func (s *SubAgent) GetContextStats() (current, limit int) {
	return s.ctxMgr.LastTotalContextTokens(), s.ctxMgr.GetMaxTokens()
}

// GetContextMessageCount returns the number of messages in this agent's context (for sidebar).
func (s *SubAgent) GetContextMessageCount() int {
	return s.ctxMgr.MessageCount()
}
