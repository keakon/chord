package agent

import "strings"

const externalTaskInterruptedReason = "external task was interrupted by session shutdown"

// normalizeRestoredExternalTaskRecords makes an external task that survived a
// Chord shutdown visible again. External workers have no native SubAgentState
// to rebuild on restore, so without this pass a record that was still running
// keeps its last memory-only state: it is neither live nor parked, which hides
// it from the sidebar and the coordination snapshot. The task is settled as
// failed instead of silently resuming: the process that owned the checkout is
// gone from this Chord process, and only an explicit follow-up (after the
// supervisor receipt proves cleanup) may start a new attempt.
func (a *MainAgent) normalizeRestoredExternalTaskRecords() {
	if a == nil {
		return
	}
	// Receipt reads touch disk, so collect candidates before releasing the
	// registry lock rather than stalling worker persistence on a slow volume.
	a.subs.mu.RLock()
	candidates := make([]*DurableTaskRecord, 0, len(a.subs.taskRecords))
	for _, rec := range a.subs.taskRecords {
		if rec == nil || rec.ExternalSession == nil {
			continue
		}
		if isTerminalSubAgentState(SubAgentState(strings.TrimSpace(rec.State))) {
			continue
		}
		candidates = append(candidates, cloneDurableTaskRecord(rec))
	}
	a.subs.mu.RUnlock()

	provedEnded := make(map[string]bool, len(candidates))
	for _, rec := range candidates {
		provedEnded[rec.TaskID] = rec.ExternalSession.ProcessEnded || a.externalSessionReceiptProvesEnded(rec)
	}

	a.subs.mu.Lock()
	defer a.subs.mu.Unlock()
	for _, rec := range a.subs.taskRecords {
		if rec == nil || rec.ExternalSession == nil {
			continue
		}
		state := SubAgentState(strings.TrimSpace(rec.State))
		if isTerminalSubAgentState(state) {
			continue
		}
		if !rec.ExternalSession.ProcessEnded && provedEnded[rec.TaskID] {
			rec.ExternalSession.ProcessEnded = true
		}
		rec.State = string(SubAgentStateFailed)
		rec.ResumePolicy = durableTaskResumePolicy(SubAgentStateFailed)
		rec.RuntimeParked = true
		rec.ClosedReason = externalTaskInterruptedReason
		if strings.TrimSpace(rec.LastSummary) == "" {
			rec.LastSummary = externalTaskInterruptedReason
		}
	}
}
