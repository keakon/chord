package agent

// maxConsecutiveIdleWakes bounds how many consecutive idle turns a mailbox
// delivery may open with no user input between them. An unbounded chain lets a
// model keep a job/delegation cycle alive by itself: each completion wakes a
// turn that starts another job, which wakes the next. The pending mailbox rows
// are durable, so once the budget is spent they stay queued and are delivered on
// the next user turn instead of starting a turn of their own. Interrupt/urgent
// deliveries — a parked worker's decision request, a risk alert — always bypass
// the bound so a worker can never deadlock waiting on the owner.
const maxConsecutiveIdleWakes = 3

// mailboxHeadState reports whether an interrupt/urgent message leads the queues
// and whether any actionable (non-progress) message is queued at all. Progress
// snapshots are informational and are not counted as actionable. The queues are
// shared with the TUI-facing manual delivery path, so the peek runs under
// subAgentMailboxIDsMu.
func (a *MainAgent) mailboxHeadState() (urgent, actionable bool) {
	a.subAgentMailboxIDsMu.Lock()
	defer a.subAgentMailboxIDsMu.Unlock()
	urgent = len(a.subAgentInbox.urgent) > 0 || len(a.subAgentInbox.spoolUrgent) > 0
	actionable = urgent || len(a.subAgentInbox.normal) > 0 || len(a.subAgentInbox.spoolNormal) > 0
	return urgent, actionable
}

// idleMailboxWakeAllowed reports whether the idle mailbox drain may open a new
// turn under the consecutive-wake budget. A batch with an interrupt/urgent head
// always proceeds; a progress-only batch is informational and does not consume
// the budget either. Only a normal actionable head (a completion or background
// result) is subject to it.
func idleMailboxWakeAllowed(urgent, actionable bool, wakes int32) bool {
	if urgent || !actionable {
		return true
	}
	return wakes < maxConsecutiveIdleWakes
}

// idleMainInboxWakeBlocked reports whether the consecutive-wake budget is what
// keeps the main inbox from draining. A message held this way is not runnable
// work: the budget is only ever spent by an idle wake, and only a user turn, a
// continue, or a session switch clears it, so the periodic lifecycle sweep
// drains through the same gate. hasRunnableMailboxWork must therefore ignore a
// budget-held message, or the main could never report global idle and
// GlobalIdleEvent, the OnIdle hook, and parkQuiescentSubAgents would all wait
// on a turn the user or an explicit continue starts.
//
// It reads the queues through mailboxHeadState, which takes
// subAgentMailboxIDsMu, so callers must not already hold that lock.
func (a *MainAgent) idleMainInboxWakeBlocked() bool {
	urgent, actionable := a.mailboxHeadState()
	return !idleMailboxWakeAllowed(urgent, actionable, a.consecutiveIdleWakes.Load())
}
