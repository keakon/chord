package agent

import (
	"errors"
	"fmt"
)

// maxResultContractRecoveryAttempts bounds how many times a completion may be
// rejected by its declared result contract before the violation becomes the
// task's terminal outcome. One attempt is enough because the engine reports
// every violation of a pass at once: missing fields and wrong types arrive in
// the same rejection, so the worker is not spending rounds discovering the
// contract one field at a time.
const maxResultContractRecoveryAttempts = 1

// finishCompletion is shared by regular and degraded Complete calls, after any
// sibling tools settle. New input invalidates delivery before checking child
// joins; dropping optional metadata must not bypass it.
func (s *SubAgent) finishCompletion(callID string, result *AgentResult) {
	if pending := s.takePendingUserMessagesForContinuation(); len(pending) > 0 {
		s.appendCompleteToolResult(callID, "Completion deferred: received new user input before completion.", ToolResultStatusSuccess)
		s.clearPendingCompleteIntent()
		s.drainContextAppendsBeforeTurn()
		s.appendPendingUserMessages(pending)
		s.asyncCallLLMWithFlightMarked(s.turn, s.ctxMgr.Snapshot())
		return
	}
	if outstanding := s.parent.outstandingJoinChildTaskIDs(s.taskID); len(outstanding) > 0 {
		s.appendCompleteToolResult(callID, deferredCompleteResult(len(outstanding)), ToolResultStatusSuccess)
		s.setPendingCompleteIntent(result)
		s.enterWaitingDescendant(deferredCompleteResult(len(outstanding)))
		return
	}
	s.clearPendingCompleteIntent()
	result = s.enrichCompletionResult(result)
	s.appendCompleteToolResult(callID, result.Summary, ToolResultStatusSuccess)
	s.sendEvent(Event{Type: EventAgentDone, Payload: result})
}

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

// rejectInvalidCompleteArguments handles a Complete call the engine refused —
// a JSON parse error, an empty summary, an artifact outside the session, an
// invalid typed result, or a result that violates the task's declared contract.
// Within the rejected-completion budget it appends a "Completion rejected" tool
// result (so the transcript keeps its tool-call pairing) and gives the model one
// follow-up request to call Complete again with corrected arguments.
//
// degraded is the fallback delivery for the one rejection class that leaves a
// usable completion behind (an incomplete typed-result group, see
// typedResultPairingError): once the budget is spent, settling it keeps the
// summary and the rest of the structured payload instead of destroying a
// finished task over an optional metadata field. Every other rejection class
// still fails the task, because nothing dependable is left to deliver.
//
// A contract violation and an unreadable result store are refused through this
// same entry point but have their own budgets and terminal shapes: the argument
// budget is about a malformed call, the contract budget is about a delivered
// result, and the two failure classes must not consume each other's one repair.
func (s *SubAgent) rejectInvalidCompleteArguments(callID string, cause error, degraded *AgentResult) {
	if s == nil || s.turn == nil {
		return
	}
	if violation, ok := errors.AsType[*ResultContractViolationError](cause); ok {
		s.rejectResultContractViolation(callID, violation)
		return
	}
	if integrity, ok := errors.AsType[*ResultContractIntegrityError](cause); ok {
		s.failResultContractIntegrity(callID, integrity)
		return
	}
	if s.completionRecoveryBudgetAvailable() {
		s.appendCompleteToolResult(callID, "Completion rejected: "+cause.Error(), ToolResultStatusError)
		s.appendPendingUserMessage(pendingUserMessage{Content: fmt.Sprintf("Completion was rejected: %v. Call Complete again with corrected, valid arguments.", cause)})
		s.asyncCallLLMWithFlightMarked(s.turn, s.ctxMgr.Snapshot())
		return
	}
	if degraded != nil {
		s.finishCompletion(callID, degraded)
		return
	}
	s.appendCompleteToolResult(callID, "Completion rejected: "+cause.Error(), ToolResultStatusError)
	s.sendEvent(Event{Type: EventAgentError, Payload: fmt.Errorf("completion was rejected after retry: %w", cause)})
}

// resultContractRecoveryBudgetAvailable consumes the single bounded follow-up
// for a completion rejected by its declared result contract. Once spent, a
// later violation in the same turn fails the task instead. The budget is
// deliberately separate from SubAgentCompletionRecoveryCount (invalid
// arguments) and SubAgentTerminalRecoveryCount (the pure-text wrap-up nudge):
// the three failure classes are independent, and the comments on those counters
// record why each needs its own one-shot repair.
func (s *SubAgent) resultContractRecoveryBudgetAvailable() bool {
	if s == nil || s.turn == nil {
		return false
	}
	if s.turn.SubAgentResultContractRecoveryCount >= maxResultContractRecoveryAttempts {
		return false
	}
	s.turn.SubAgentResultContractRecoveryCount++
	return true
}

// rejectResultContractViolation refuses a completion whose delivered result
// does not satisfy the contract declared at delegation. Within its own budget it
// returns the violations to the worker for one corrected attempt without ending
// the turn; once the budget is spent the violation is recorded for the
// settlement and becomes the task's terminal outcome, so an unsatisfied
// contract is never silently accepted as a successful completion.
func (s *SubAgent) rejectResultContractViolation(callID string, violation *ResultContractViolationError) {
	if s == nil || s.turn == nil {
		return
	}
	if s.resultContractRecoveryBudgetAvailable() {
		s.appendCompleteToolResult(callID, "Completion rejected: "+violation.Error(), ToolResultStatusError)
		s.appendPendingUserMessage(pendingUserMessage{Content: fmt.Sprintf("Completion was rejected: %v. Call Complete again with a result that satisfies the task's result contract.", violation)})
		s.asyncCallLLMWithFlightMarked(s.turn, s.ctxMgr.Snapshot())
		return
	}
	s.setResultContractFailure(violation.Diagnostics())
	s.appendCompleteToolResult(callID, "Completion rejected: "+violation.Error(), ToolResultStatusError)
	s.sendEvent(Event{Type: EventAgentError, Payload: violation})
}

// failResultContractIntegrity closes a task whose validated result_ref could not
// be read back. The store is outside the tool chain at that point, so a retry
// cannot repair anything and the contract budget is not spent.
func (s *SubAgent) failResultContractIntegrity(callID string, integrity *ResultContractIntegrityError) {
	if s == nil || s.turn == nil {
		return
	}
	s.setResultContractFailure(integrity.Diagnostics())
	s.appendCompleteToolResult(callID, "Completion rejected: "+integrity.Error(), ToolResultStatusError)
	s.sendEvent(Event{Type: EventAgentError, Payload: integrity})
}
