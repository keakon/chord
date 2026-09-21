package agent

import (
	"fmt"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/tools"
)

// maxUnansweredSubAgentEscalations bounds how many needs_repair escalations a
// task may leave unanswered before the engine refuses further escalations. It is
// the only convergence for a worker that keeps re-escalating the same blocker:
// every escalation re-enters waiting_main and resets the state timers the
// lifecycle sweep watches, so a timeout can never catch that loop. Two allows
// one repeat of the same request, which covers an owner that missed or misread
// the first one, while still ending the spin.
const maxUnansweredSubAgentEscalations = 2

// escalationBudgetExhaustedNotice is injected back to a worker whose escalation
// the engine refused. The refusal must leave the task running and able to close
// out on its own: the engine cannot declare the task blocked on the worker's
// behalf, and terminating the blocked side would not unblock the owner.
const escalationBudgetExhaustedNotice = "Escalation rejected: this task has already escalated its blocker more times than the engine allows without a reply. Do not escalate it again — make progress on what you can do independently, or close the task with complete and record the unanswered blocker in remaining_limitations."

// blockedEscalationError is the terminal outcome of an escalate call that
// declared kind=blocked: the worker judged the attempt a dead end, so it closes
// through the ordinary failure path (risk_alert mailbox, terminal commit, slot
// release) with the blocker as its reason. Its category stays separate from a
// contract violation and from a task that never delivered anything.
type blockedEscalationError struct {
	reason string
}

func newBlockedEscalationError(reason string) *blockedEscalationError {
	return &blockedEscalationError{reason: strings.TrimSpace(reason)}
}

func (e *blockedEscalationError) Error() string {
	if e == nil || e.reason == "" {
		return "escalated as blocked"
	}
	return "escalated as blocked: " + e.reason
}

// escalationRefusal is a needs_repair escalation the engine refused because the
// task has too many unanswered ones. It is reported after any sibling tool
// results settle, so the injected user message never lands between an assistant
// tool_calls message and its tool results.
type escalationRefusal struct {
	callID   string
	argsJSON string
	cause    error
}

// subAgentEscalationAllowed reports whether taskID may raise another needs_repair
// escalation. The count lives on the durable record, so the budget survives
// attempts, compaction, and resume. A task with no readable record is allowed:
// the budget exists to converge a repeated loop, not to gate a first escalation.
func (a *MainAgent) subAgentEscalationAllowed(taskID string) bool {
	if a == nil {
		return false
	}
	rec := a.taskRecordByTaskID(taskID)
	if rec == nil {
		return true
	}
	return rec.EscalationCount < maxUnansweredSubAgentEscalations
}

// recordSubAgentEscalation counts an escalation the owner accepted and remembers
// which mailbox carries it, so answering that exact mailbox clears the budget
// (see buildTaskRecordFromSub).
func (a *MainAgent) recordSubAgentEscalation(sub *SubAgent, mailboxID string) {
	if a == nil || sub == nil {
		return
	}
	taskID := strings.TrimSpace(sub.taskID)
	if taskID == "" {
		return
	}
	a.subs.mu.Lock()
	rec := a.subs.taskRecords[taskID]
	if rec == nil {
		a.subs.mu.Unlock()
		log.Warnf("escalation not recorded: no durable task record task_id=%v agent_id=%v", taskID, sub.instanceID)
		return
	}
	rec.EscalationCount++
	rec.EscalationMailboxID = strings.TrimSpace(mailboxID)
	a.subs.mu.Unlock()
	// Rebuild through the single record writer so the new count is persisted with
	// every other field instead of racing a parallel registry write.
	a.syncTaskRecordFromSub(sub, "")
}

// rejectInvalidEscalateArguments answers a malformed escalate call and closes
// the task. Escalate is an internal control tool whose arguments the engine must
// be able to route, and a worker that cannot name an escalation kind reliably is
// not one whose waiting_main loop is worth parking.
func (s *SubAgent) rejectInvalidEscalateArguments(callID, argsJSON string, cause error) {
	if s == nil {
		return
	}
	s.appendControlToolResult(callID, tools.NameEscalate, argsJSON, "Escalation rejected: "+cause.Error(), ToolResultStatusError)
	s.sendEvent(Event{Type: EventAgentError, Payload: cause})
}

// closeAsBlocked ends the attempt for an escalate call that reported a dead end.
func (s *SubAgent) closeAsBlocked(callID, argsJSON, reason string) {
	if s == nil {
		return
	}
	blocked := newBlockedEscalationError(reason)
	s.appendControlToolResult(callID, tools.NameEscalate, argsJSON, "Escalation ended the task as blocked: "+blocked.Error(), ToolResultStatusError)
	s.sendEvent(Event{Type: EventAgentError, Payload: blocked})
}

// refuseOverBudgetEscalation reports an escalation the engine refused because the
// task already has maxUnansweredSubAgentEscalations of them. The worker keeps
// running: no terminal state is produced, so convergence never depends on a
// waiting_main timeout the next escalation would reset anyway.
func (s *SubAgent) refuseOverBudgetEscalation(refusal *escalationRefusal) {
	if s == nil || refusal == nil {
		return
	}
	s.appendControlToolResult(refusal.callID, tools.NameEscalate, refusal.argsJSON, "Escalation rejected: "+refusal.cause.Error(), ToolResultStatusError)
	s.appendPendingUserMessage(pendingUserMessage{Content: escalationBudgetExhaustedNotice})
}

func newEscalationRefusal(callID, argsJSON string) *escalationRefusal {
	return &escalationRefusal{
		callID:   callID,
		argsJSON: argsJSON,
		cause:    fmt.Errorf("the owner has left %d escalation(s) unanswered; make progress or close out with complete", maxUnansweredSubAgentEscalations),
	}
}

// takePendingEscalateRefusal claims the refusal recorded when an escalate call
// was co-returned with regular tools, so it is reported only after those tool
// results have closed the batch.
func (s *SubAgent) takePendingEscalateRefusal() *escalationRefusal {
	if s == nil || s.pendingEscalateRefusal == nil {
		return nil
	}
	refusal := s.pendingEscalateRefusal
	s.pendingEscalateRefusal = nil
	return refusal
}

// takePendingEscalate claims the accepted needs_repair escalation co-returned
// with regular tools, so it is routed only after those tools have settled.
func (s *SubAgent) takePendingEscalate() *tools.AgentRequestPayload {
	if s == nil || s.pendingEscalateRequest == nil {
		return nil
	}
	payload := s.pendingEscalateRequest
	s.pendingEscalateRequest = nil
	return payload
}
