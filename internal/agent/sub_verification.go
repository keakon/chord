package agent

import "fmt"

// completionRecoveryBudgetAvailable consumes the single bounded follow-up for a
// rejected Complete call (invalid arguments). It returns
// true when the follow-up request may proceed; once spent, any later rejection
// in the same turn fails the agent instead. The budget is deliberately separate
// from SubAgentTerminalRecoveryCount (the pure-text wrap-up nudge), so a
// text-only reply that already used its nudge still leaves the model one chance
// to repair a rejected Complete, and vice versa.
func (s *SubAgent) completionRecoveryBudgetAvailable() bool {
	if s == nil || s.turn == nil {
		return false
	}
	if s.turn.SubAgentCompletionRecoveryCount >= 1 {
		return false
	}
	s.turn.SubAgentCompletionRecoveryCount++
	return true
}

// rejectInvalidCompleteArguments handles a Complete call whose arguments failed
// validation — a JSON parse error, an empty summary, an artifact outside the
// session, or an invalid typed result. Within the shared rejected-completion
// budget it appends a "Completion rejected" tool result (so the transcript
// keeps its tool-call pairing) and gives the model one follow-up request to
// call Complete again with corrected arguments.
//
// degraded is the fallback delivery for the one rejection class that leaves a
// usable completion behind (an incomplete typed-result group, see
// typedResultPairingError): once the budget is spent, settling it keeps the
// summary and the rest of the structured payload instead of destroying a
// finished task over an optional metadata field. Every other rejection class
// still fails the task, because nothing dependable is left to deliver.
func (s *SubAgent) rejectInvalidCompleteArguments(callID string, cause error, degraded *AgentResult) {
	if s == nil || s.turn == nil {
		return
	}
	if s.completionRecoveryBudgetAvailable() {
		s.appendCompleteToolResult(callID, "Completion rejected: "+cause.Error())
		s.appendPendingUserMessage(pendingUserMessage{Content: fmt.Sprintf("Completion was rejected: %v. Call Complete again with corrected, valid arguments.", cause)})
		s.asyncCallLLMWithFlightMarked(s.turn, s.ctxMgr.Snapshot())
		return
	}
	if degraded != nil {
		if err := s.finishCompletion(callID, degraded); err == nil {
			return
		} else {
			// The degraded payload failed a check of its own. Report that
			// cause rather than the pairing error, so the failure names what
			// actually blocked delivery.
			cause = err
		}
	}
	s.appendCompleteToolResult(callID, "Completion rejected: "+cause.Error())
	s.sendEvent(Event{Type: EventAgentError, Payload: fmt.Errorf("completion was rejected after retry: %w", cause)})
}
