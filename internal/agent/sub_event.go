package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
)

// runLoop is the SubAgent's event loop. It runs in an independent goroutine.
// All state modifications happen in this single thread; user messages arrive
// via the inputCh channel.
func (s *SubAgent) runLoop() {
	log.Debugf("SubAgent event loop started instance=%v task_id=%v agent_def=%v", s.instanceID, s.taskID, s.agentDefName)
	defer func() {
		// Stop the silence watchdog before joining request goroutines: an
		// aborted-but-still-exiting request goroutine may still signal wake.
		s.stopLLMSilenceTimer()
		// Join any in-flight LLM-request goroutine before signalling done. The
		// loop below returns as soon as parentCtx is cancelled, but a request
		// goroutine unblocking concurrently still performs post-call synchronous
		// writes (usage ledger, hooks) into the session directory. Closing done
		// first would let shutdown return while those writes are in progress,
		// racing session-directory removal. Every Add originates from this
		// goroutine, whose body has returned by the time this defer runs, so no
		// Add can race this Wait.
		s.llmWG.Wait()
		s.doneOnce.Do(func() { close(s.done) })
		log.Debugf("SubAgent event loop stopped instance=%v", s.instanceID)
	}()

	for {
		s.refillInputChannelFromOverflow()
		s.refillContextAppendChannelFromOverflow()
		// Issue a queued silence-recovery request as soon as the aborted
		// request goroutine releases the in-flight gate. Bound the wait by the
		// watchdog grace window below in case the goroutine ignores
		// cancellation entirely.
		if s.pendingLLMSilenceRecovery != "" {
			if s.parentCtx.Err() != nil {
				s.pendingLLMSilenceRecovery = ""
				s.llmSilenceEscalatedAt = time.Time{}
			} else if !s.llmRequestInFlight.Load() && s.State() == SubAgentStateRunning && s.turn != nil {
				s.issueLLMSilenceRecovery()
				continue
			}
		}
		if s.handleLLMSilenceIfDue() {
			// A silent request was escalated (bounded recovery is pending) or
			// abandoned with a terminal error. Re-evaluate from the top so a
			// freed in-flight gate issues the recovery request immediately.
			continue
		}
		if msg, ok := s.tryReceiveContextAppend(); ok {
			s.appendContextOnly(msg)
			continue
		}
		if s.canStartUserTurn() {
			if input, ok := s.tryReceiveUserInput(); ok {
				s.resetIdleTimer()
				s.handleUserInput(input)
				continue
			}
		}
		if s.tryHandleContinueSignal() {
			continue
		}
		if s.tryHandlePendingContinue() {
			continue
		}
		if result, ok := s.dequeuePromotedToolResult(); ok {
			s.resetIdleTimer()
			s.handleToolResult(result)
			continue
		}

		var idleCh <-chan time.Time
		if s.idleTimer != nil {
			idleCh = s.idleTimer.C
		}
		var inputCh <-chan pendingUserMessage
		if s.canStartUserTurn() {
			inputCh = s.inputCh
		}
		// Arm the in-flight LLM silence watchdog for the current deadline; the
		// case below fires when the request stays silent past its budget.
		var silenceCh <-chan time.Time
		if deadline, armed := s.llmSilenceWatchdogDeadline(); armed {
			if d := time.Until(deadline); d > 0 {
				s.armLLMSilenceTimer(d)
				silenceCh = s.llmSilenceTimer.C
			} else {
				// Deadline crossed between the top-of-loop check and arming;
				// loop again so handleLLMSilenceIfDue escalates it.
				s.stopLLMSilenceTimer()
				continue
			}
		} else {
			s.stopLLMSilenceTimer()
		}

		select {
		case input := <-inputCh:
			s.accountDequeuedUserMessage(input)
			s.resetIdleTimer()
			s.handleUserInput(input)
			s.refillInputChannelFromOverflow()

		case msg := <-s.ctxAppendCh:
			s.accountDequeuedContextAppend(msg)
			s.appendContextOnly(msg)
			s.refillContextAppendChannelFromOverflow()

		case msg := <-s.continueCh:
			s.handleContinueSignal(msg)

		case <-s.wakeCh:
			// State transitions and queue producers use wakeCh to make this
			// select rebuild state-gated channels such as inputCh.

		case result := <-s.llmCh:
			if s.pendingLLMSilenceRecovery != "" || (s.llmSilenceAbandonedTurnID != 0 && result.turnID == s.llmSilenceAbandonedTurnID) {
				// A result posted by a request this loop already aborted for
				// silence (for example the provider surfaced the cancellation
				// as an error). Drop it: the pending bounded recovery or the
				// terminal failure owns the outcome, and handling this late
				// result would double-recover or double-fail.
				s.finishLLMRequest()
				continue
			}
			s.finishLLMRequest()
			s.handleLLMResponse(result)
			if s.canStartUserTurn() {
				s.refillInputChannelFromOverflow()
			}

		case result := <-s.toolCh:
			s.resetIdleTimer()
			s.handleToolResult(result)

		case <-idleCh:
			s.sendEvent(Event{Type: EventAgentIdle, Payload: s.idleTimeout})
			s.idleTimer = nil

		case <-silenceCh:
			s.stopLLMSilenceTimer()
			s.handleLLMSilenceIfDue()

		case <-s.parentCtx.Done():
			s.stopLLMSilenceTimer()
			if s.idleTimer != nil {
				s.idleTimer.Stop()
			}
			if s.turn != nil {
				cancelledExec := s.turn.cancelPendingToolCalls()
				cancelledStream := s.turn.drainStreamingToolCalls()
				merged := mergePendingToolCalls(cancelledExec, cancelledStream)
				merged = s.turn.filterCompletedToolCalls(merged)
				if len(merged) > 0 {
					persistedResults := finalizeInterruptedToolCalls(s.ctxMgr, s.parent.emitToTUI, s.persistInterruptedToolResults, merged, ToolResultStatusCancelled, context.Canceled)
					if persistedResults > 0 {
						log.Debugf("SubAgent: persisted interrupted tool-call results during shutdown agent=%v count=%v", s.instanceID, persistedResults)
					}
					s.parent.emitActivity(s.instanceID, ActivityIdle, "")
				}
			}
			return
		}
	}
}

// ---------------------------------------------------------------------------
// In-flight LLM silence watchdog
// ---------------------------------------------------------------------------

// llmSilenceWatchdogDeadline returns the next instant the silence watchdog
// must fire, or (time.Time{}, false) when nothing is being watched. While a
// request is in flight the deadline trails the last real activity (request
// start, stream delta, response handling); once a recovery is pending it
// bounds the wait for the aborted request goroutine to release the in-flight
// gate, so a provider that ignores context cancellation still terminates.
func (s *SubAgent) llmSilenceWatchdogDeadline() (time.Time, bool) {
	if s == nil || s.llmSilenceBudget <= 0 {
		return time.Time{}, false
	}
	if s.pendingLLMSilenceRecovery != "" {
		return s.llmSilenceEscalatedAt.Add(s.llmSilenceBudget), true
	}
	if !s.llmRequestInFlight.Load() || s.turn == nil {
		return time.Time{}, false
	}
	return s.StateChangedAt().Add(s.llmSilenceBudget), true
}

// handleLLMSilenceIfDue escalates a wedged in-flight request when its silence
// deadline has already passed, and reports whether the loop must restart its
// evaluation so a queued recovery is issued the moment the gate clears.
func (s *SubAgent) handleLLMSilenceIfDue() bool {
	deadline, armed := s.llmSilenceWatchdogDeadline()
	if !armed {
		return false
	}
	now := time.Now()
	if now.Before(deadline) {
		return false
	}
	return s.handleLLMSilenceDeadline(now)
}

// handleLLMSilenceDeadline runs when an in-flight request has produced nothing
// (no stream delta, no completion) for the full silence budget. It cancels the
// wedged request and either queues one bounded recovery attempt or abandons
// the turn with EventAgentError once the recovery budget is exhausted. It
// reports whether it changed state (so the caller re-evaluates the loop).
func (s *SubAgent) handleLLMSilenceDeadline(now time.Time) bool {
	if s == nil || s.parentCtx.Err() != nil || s.llmSilenceBudget <= 0 {
		return false
	}
	if s.pendingLLMSilenceRecovery != "" {
		if s.llmRequestInFlight.Load() {
			// The earlier escalation cancelled the turn, yet the request
			// goroutine never released the in-flight gate — it is ignoring
			// context cancellation. Recovering again cannot help; fail.
			s.failTurnForLLMSilence("request goroutine did not abort after cancellation within the silence budget")
		} else {
			// The gate cleared but the recovery was never issued (the worker
			// left Running in the meantime); drop the stale pending recovery
			// instead of looping on it.
			s.pendingLLMSilenceRecovery = ""
			s.llmSilenceEscalatedAt = time.Time{}
		}
		return true
	}
	if !s.llmRequestInFlight.Load() || s.State() != SubAgentStateRunning || s.turn == nil {
		return false
	}
	silentFor := now.Sub(s.StateChangedAt())
	if silentFor < s.llmSilenceBudget {
		// A delta or boundary arrived between arming and firing, so the
		// request is alive again; re-arm against the fresh deadline.
		return false
	}
	if s.llmSilenceRecoveries >= maxSubAgentLLMSilentRecoveries {
		s.failTurnForLLMSilence(fmt.Sprintf("no output within %s across %d recovery attempt(s)", s.llmSilenceBudget.Round(time.Second), s.llmSilenceRecoveries))
		return true
	}
	s.llmSilenceRecoveries++
	turn := s.turn
	log.Warnf("SubAgent LLM request silent agent=%v turn_id=%v silent_for=%v recovery=%v budget=%v", s.instanceID, turn.ID, silentFor.Round(time.Second), s.llmSilenceRecoveries, s.llmSilenceBudget)
	turn.Cancel()
	// Never drop text the request did manage to stream before stalling.
	s.preserveInterruptedPartial()
	s.pendingLLMSilenceRecovery = subAgentLLMSilenceRecoveryInstruction()
	s.llmSilenceEscalatedAt = time.Now()
	return true
}

// issueLLMSilenceRecovery starts one bounded recovery turn carrying the
// silence instruction once the aborted request goroutine has released the
// in-flight gate.
func (s *SubAgent) issueLLMSilenceRecovery() {
	instruction := s.pendingLLMSilenceRecovery
	s.pendingLLMSilenceRecovery = ""
	s.llmSilenceEscalatedAt = time.Time{}
	log.Infof("SubAgent issuing bounded recovery after silent LLM request agent=%v recovery=%v", s.instanceID, s.llmSilenceRecoveries)
	turn := s.newTurn()
	s.appendPendingUserMessage(pendingUserMessage{Content: instruction})
	s.llmRequestInFlight.Store(true)
	s.asyncCallLLMWithFlightMarked(turn, s.prepareContextForLLM(s.ctxMgr.Snapshot()))
}

// subAgentLLMSilenceRecoveryInstruction tells the model the previous request
// was abandoned after a silent timeout so it resumes the work instead of
// duplicating it.
func subAgentLLMSilenceRecoveryInstruction() string {
	return "System note: the previous model request produced no output for an extended period and was abandoned. Re-check the current task state and continue the delegated work now, then finish coordination (Complete / Escalate / Notify) instead of stopping after plain text."
}

// failTurnForLLMSilence abandons the silent request with a terminal
// EventAgentError, which the main agent surfaces as a risk alert to the
// worker's owner. Bounded, conservative: the worker is not killed here.
func (s *SubAgent) failTurnForLLMSilence(detail string) {
	if s == nil {
		return
	}
	log.Errorf("SubAgent abandoning silent LLM request agent=%v detail=%v", s.instanceID, detail)
	s.pendingLLMSilenceRecovery = ""
	s.llmSilenceEscalatedAt = time.Time{}
	if s.turn != nil {
		s.llmSilenceAbandonedTurnID = s.turn.ID
		s.turn.Cancel()
	}
	s.llmRequestInFlight.Store(false)
	s.llmSilenceRecoveries = 0
	s.sendEvent(Event{
		Type:    EventAgentError,
		Payload: fmt.Errorf("SubAgent %s: LLM request stalled silently (%s)", s.instanceID, detail),
	})
}

func (s *SubAgent) armLLMSilenceTimer(d time.Duration) {
	if d <= 0 {
		d = time.Nanosecond
	}
	if s.llmSilenceTimer == nil {
		s.llmSilenceTimer = time.NewTimer(d)
		return
	}
	if !s.llmSilenceTimer.Stop() {
		select {
		case <-s.llmSilenceTimer.C:
		default:
		}
	}
	s.llmSilenceTimer.Reset(d)
}

func (s *SubAgent) stopLLMSilenceTimer() {
	if s.llmSilenceTimer == nil {
		return
	}
	if !s.llmSilenceTimer.Stop() {
		select {
		case <-s.llmSilenceTimer.C:
		default:
		}
	}
}

func (s *SubAgent) finishLLMRequest() {
	if s == nil {
		return
	}
	s.llmRequestInFlight.Store(false)
	s.parent.sendEvent(Event{Type: EventSubAgentRequestBoundary, SourceID: s.instanceID})
}

func (s *SubAgent) canStartUserTurn() bool {
	if s == nil || s.State() != SubAgentStateRunning || s.llmRequestInFlight.Load() {
		return false
	}
	if s.turn == nil || s.idleTimer != nil {
		return true
	}
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	return s.turn.PendingToolCalls.Load() == 0 && s.turn.activeToolBatchCancel == nil
}

func (s *SubAgent) canPark() bool {
	if s == nil || s.llmRequestInFlight.Load() {
		return false
	}
	switch s.State() {
	case SubAgentStateIdle, SubAgentStateWaitingMain, SubAgentStateWaitingDescendant, SubAgentStateCompleted, SubAgentStateFailed, SubAgentStateCancelled:
	default:
		return false
	}
	if len(s.inputCh) > 0 || len(s.ctxAppendCh) > 0 || len(s.llmCh) > 0 || len(s.toolCh) > 0 || len(s.continueCh) > 0 {
		return false
	}
	s.inputQueueMu.Lock()
	inputOverflow := len(s.inputOverflow)
	reservedInputs := s.inputQueueReservedMessages
	s.inputQueueMu.Unlock()
	s.ctxAppendQueueMu.Lock()
	contextOverflow := len(s.ctxAppendOverflow)
	s.ctxAppendQueueMu.Unlock()
	if inputOverflow > 0 || contextOverflow > 0 || reservedInputs > 0 {
		return false
	}
	return true
}

func (s *SubAgent) dequeuePromotedToolResult() (*toolResult, bool) {
	if s == nil || len(s.promotedToolQueue) == 0 {
		return nil, false
	}
	result := s.promotedToolQueue[0]
	s.promotedToolQueue[0] = nil
	s.promotedToolQueue = s.promotedToolQueue[1:]
	return result, true
}

func (s *SubAgent) refillInputChannelFromOverflow() {
	if s == nil {
		return
	}
	s.inputQueueMu.Lock()
	defer s.inputQueueMu.Unlock()
	for len(s.inputOverflow) > 0 {
		select {
		case s.inputCh <- s.inputOverflow[0]:
			s.inputOverflow = s.inputOverflow[1:]
		default:
			return
		}
	}
}

func (s *SubAgent) refillContextAppendChannelFromOverflow() {
	if s == nil {
		return
	}
	s.ctxAppendQueueMu.Lock()
	defer s.ctxAppendQueueMu.Unlock()
	for len(s.ctxAppendOverflow) > 0 {
		select {
		case s.ctxAppendCh <- s.ctxAppendOverflow[0]:
			s.ctxAppendOverflow = s.ctxAppendOverflow[1:]
		default:
			return
		}
	}
}

func (s *SubAgent) tryReceiveUserInput() (pendingUserMessage, bool) {
	if s == nil {
		return pendingUserMessage{}, false
	}
	select {
	case input := <-s.inputCh:
		s.accountDequeuedUserMessage(input)
		return input, true
	default:
		return pendingUserMessage{}, false
	}
}

func (s *SubAgent) tryReceiveContextAppend() (message.Message, bool) {
	if s == nil {
		return message.Message{}, false
	}
	select {
	case msg := <-s.ctxAppendCh:
		s.accountDequeuedContextAppend(msg)
		return msg, true
	default:
		return message.Message{}, false
	}
}

func (s *SubAgent) tryHandleContinueSignal() bool {
	if s == nil {
		return false
	}
	select {
	case msg := <-s.continueCh:
		s.handleContinueSignal(msg)
		return true
	default:
		return false
	}
}

func (s *SubAgent) handleContinueSignal(msg continueMsg) {
	if msg.cancelCurrentTurn {
		s.pendingContinue = nil
		s.cancelCurrentTurnFromLoop()
		return
	}
	if s.llmRequestInFlight.Load() {
		if msg.restartStoppedTurn {
			pending := msg
			s.pendingContinue = &pending
		}
		return
	}
	if s.turn == nil || msg.restartStoppedTurn {
		s.handleContinueMessage(msg)
	}
}

func (s *SubAgent) tryHandlePendingContinue() bool {
	if s.pendingContinue == nil || s.llmRequestInFlight.Load() {
		return false
	}
	msg := *s.pendingContinue
	s.pendingContinue = nil
	s.handleContinueMessage(msg)
	return true
}

func (s *SubAgent) handleContinueMessage(msg continueMsg) {
	if msg.drainContextAppends {
		s.drainContextAppendsBeforeTurn()
	}
	s.handleContinue()
}
