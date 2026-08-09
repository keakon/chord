package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/keakon/golog/log"
)

func isTerminalSubAgentState(state SubAgentState) bool {
	switch state {
	case SubAgentStateCompleted, SubAgentStateFailed, SubAgentStateCancelled:
		return true
	default:
		return false
	}
}

// terminalStatusAfterCommit returns the state to report after attempting a
// terminal commit: the requested state on success, or the record's actual
// terminal state when an existing settlement won the conflict — reporting the
// requested state then would contradict the record (for example announcing a
// cancel for a task that had already completed).
func (a *MainAgent) terminalStatusAfterCommit(taskID string, requested SubAgentState, err error) SubAgentState {
	if err == nil {
		return requested
	}
	if rec := a.taskRecordByTaskID(taskID); rec != nil && isTerminalSubAgentState(SubAgentState(rec.State)) {
		return SubAgentState(rec.State)
	}
	return requested
}

func (a *MainAgent) commitTerminalTask(sub *SubAgent, state SubAgentState, summary, closedReason string, completion *CompletionEnvelope) (*TaskSettlement, bool, error) {
	if a == nil || sub == nil {
		return nil, false, fmt.Errorf("missing task runtime")
	}
	if !isTerminalSubAgentState(state) {
		return nil, false, fmt.Errorf("state %q is not terminal", state)
	}
	taskID := strings.TrimSpace(sub.taskID)
	if taskID == "" {
		return nil, false, fmt.Errorf("missing task ID")
	}
	summary = strings.TrimSpace(summary)
	completion = normalizeCompletionEnvelope(completion)
	if completion != nil && summary == "" {
		summary = completion.Summary
	}

	a.settlementJournalMu.Lock()
	defer a.settlementJournalMu.Unlock()

	a.subs.mu.RLock()
	previous := cloneDurableTaskRecord(a.subs.taskRecords[taskID])
	a.subs.mu.RUnlock()
	if previous == nil {
		previous = buildTaskRecordFromSub(sub, nil, "", a.explicitUserTurnCount.Load(), time.Now())
	}
	if previous.Attempt == 0 {
		previous.Attempt = 1
	}
	key := taskAttemptKey{TaskID: taskID, Attempt: previous.Attempt}
	a.subs.mu.RLock()
	existing := cloneTaskSettlement(a.subs.settlements[key])
	a.subs.mu.RUnlock()
	terminalRevision := previous.LifecycleRevision + 1
	if existing != nil {
		terminalRevision = existing.TerminalRevision
	}
	settlement := &TaskSettlement{
		TaskID:           taskID,
		Attempt:          previous.Attempt,
		TerminalRevision: terminalRevision,
		Outcome:          string(state),
		Summary:          summary,
		Completion:       completion,
		SettledAt:        time.Now(),
	}
	if completion != nil {
		settlement.ArtifactRefs = completion.Artifacts
		if completion.ResultRef != nil {
			ref := *completion.ResultRef
			settlement.ResultRef = &ref
		}
	}

	durable := false
	var persistErr error
	if existing != nil {
		if !taskSettlementContentEqual(existing, settlement) {
			return nil, false, fmt.Errorf("conflicting terminal settlement for task %s attempt %d", taskID, previous.Attempt)
		}
		settlement = existing
		durable = previous.SettlementDurable
		if !durable {
			persistErr = appendTaskSettlement(a.sessionDir, settlement)
			durable = persistErr == nil
		}
	} else {
		persistErr = appendTaskSettlement(a.sessionDir, settlement)
		durable = persistErr == nil
	}

	sub.setState(state, summary)
	a.noteSubAgentStateTransition(sub, state)
	a.persistSubAgentMeta(sub)
	now := time.Now()
	a.subs.mu.Lock()
	current := a.subs.taskRecords[taskID]
	if current != nil && current.Attempt != 0 && current.Attempt != settlement.Attempt {
		a.subs.mu.Unlock()
		return nil, false, fmt.Errorf("task %s attempt changed during terminal commit", taskID)
	}
	rec := buildTaskRecordFromSub(sub, current, closedReason, a.explicitUserTurnCount.Load(), now)
	rec.Attempt = settlement.Attempt
	if rec.LifecycleRevision < settlement.TerminalRevision {
		rec.LifecycleRevision = settlement.TerminalRevision
	}
	rec.State = settlement.Outcome
	rec.ResumePolicy = durableTaskResumePolicy(state)
	rec.LastSummary = settlement.Summary
	rec.LastCompletion = normalizeCompletionEnvelope(settlement.Completion)
	rec.LastArtifactRefs = mergeArtifactRefs(rec.LastArtifactRefs, settlement.ArtifactRefs)
	rec.LatestSettlement = cloneTaskSettlement(settlement)
	rec.SettlementDurable = durable
	a.subs.taskRecords[taskID] = rec
	a.subs.settlements[key] = cloneTaskSettlement(settlement)
	a.subs.notifyTaskChangeLocked()
	a.subs.mu.Unlock()

	if err := a.persistTaskRegistry(); err != nil && persistErr == nil {
		persistErr = err
	}
	if persistErr != nil {
		log.Warnf("task settlement durability degraded task_id=%v attempt=%v error=%v", taskID, settlement.Attempt, persistErr)
	}
	return cloneTaskSettlement(settlement), durable, persistErr
}

// settleDetachedTerminalTask commits a terminal outcome for a task whose
// SubAgent runtime is gone (parked or already released). It maintains the
// invariant that a terminal record state always has a matching settlement —
// collect and retention only trust settlements, so a bare record-state flip
// would leave waiters blocked until timeout and the record unarchivable. If
// the attempt was already settled, the existing settlement wins and the record
// mirrors its outcome instead of the requested state: an earlier real
// completion must not be rewritten into a cancel, and restore-time repair
// would revert such a rewrite anyway.
func (a *MainAgent) settleDetachedTerminalTask(taskID string, state SubAgentState, summary, closedReason string) SubAgentState {
	return a.settleDetachedTerminalTaskGuarded(taskID, state, summary, closedReason, nil)
}

// settleDetachedTerminalTaskGuarded is settleDetachedTerminalTask with an
// optional precondition. The guard re-runs against the current record under
// settlementJournalMu — once after the record is read and again inside the
// final registry commit — so a caller that decided to settle from a stale
// scan (the WaitingMain expiry sweep) backs off when the task was revived
// in between instead of minting a cancel for a live attempt.
func (a *MainAgent) settleDetachedTerminalTaskGuarded(taskID string, state SubAgentState, summary, closedReason string, guard func(*DurableTaskRecord) bool) SubAgentState {
	taskID = strings.TrimSpace(taskID)
	if a == nil || taskID == "" || !isTerminalSubAgentState(state) {
		return ""
	}
	summary = strings.TrimSpace(summary)

	a.settlementJournalMu.Lock()
	defer a.settlementJournalMu.Unlock()

	a.subs.mu.RLock()
	previous := cloneDurableTaskRecord(a.subs.taskRecords[taskID])
	a.subs.mu.RUnlock()
	if previous == nil {
		return ""
	}
	if guard != nil && !guard(previous) {
		return ""
	}
	attempt := previous.Attempt
	if attempt == 0 {
		attempt = 1
	}
	key := taskAttemptKey{TaskID: taskID, Attempt: attempt}
	a.subs.mu.RLock()
	existing := cloneTaskSettlement(a.subs.settlements[key])
	a.subs.mu.RUnlock()
	if existing == nil && previous.LatestSettlement != nil && previous.LatestSettlement.Attempt == attempt {
		existing = cloneTaskSettlement(previous.LatestSettlement)
	}

	settlement := existing
	durable := previous.SettlementDurable
	var persistErr error
	if settlement == nil {
		settlement = &TaskSettlement{
			TaskID:           taskID,
			Attempt:          attempt,
			TerminalRevision: previous.LifecycleRevision + 1,
			Outcome:          string(state),
			Summary:          summary,
			SettledAt:        time.Now(),
		}
		// A terminal record without a settlement is a durability gap (for
		// example a completion mailbox replayed after a crash), not a live
		// task: mint the settlement from the record's own outcome so a later
		// cancel or sweep cannot rewrite a real completion — the same
		// existing-wins rule applied when a settlement survives.
		if recorded := SubAgentState(previous.State); isTerminalSubAgentState(recorded) && recorded != state {
			settlement.Outcome = previous.State
			settlement.Summary = strings.TrimSpace(previous.LastSummary)
			settlement.Completion = normalizeCompletionEnvelope(previous.LastCompletion)
			settlement.ArtifactRefs = mergeArtifactRefs(nil, previous.LastArtifactRefs)
			if settlement.Completion != nil && settlement.Completion.ResultRef != nil {
				ref := *settlement.Completion.ResultRef
				settlement.ResultRef = &ref
			}
		}
		persistErr = appendTaskSettlement(a.sessionDir, settlement)
		durable = persistErr == nil
	} else if !durable {
		persistErr = appendTaskSettlement(a.sessionDir, settlement)
		durable = persistErr == nil
	}

	a.subs.mu.Lock()
	rec := a.subs.taskRecords[taskID]
	if rec == nil || (rec.Attempt != 0 && rec.Attempt != attempt) || (guard != nil && !guard(rec)) {
		a.subs.mu.Unlock()
		return ""
	}
	next := cloneDurableTaskRecord(rec)
	next.Attempt = attempt
	if next.LifecycleRevision < settlement.TerminalRevision {
		next.LifecycleRevision = settlement.TerminalRevision
	}
	next.State = settlement.Outcome
	next.ResumePolicy = durableTaskResumePolicy(SubAgentState(settlement.Outcome))
	if settlement.Summary != "" {
		next.LastSummary = settlement.Summary
	}
	next.LastCompletion = normalizeCompletionEnvelope(settlement.Completion)
	next.LastArtifactRefs = mergeArtifactRefs(next.LastArtifactRefs, settlement.ArtifactRefs)
	if settlement.Outcome == string(state) {
		next.ClosedReason = blankToDefault(closedReason, next.ClosedReason)
	} else if strings.TrimSpace(next.ClosedReason) == "" {
		next.ClosedReason = settlement.Summary
	}
	next.LatestSettlement = cloneTaskSettlement(settlement)
	next.SettlementDurable = durable
	next.RuntimeParked = true
	next.UpdatedAt = time.Now()
	a.subs.taskRecords[taskID] = next
	a.subs.settlements[key] = cloneTaskSettlement(settlement)
	a.subs.notifyTaskChangeLocked()
	a.subs.mu.Unlock()

	if err := a.persistTaskRegistry(); err != nil && persistErr == nil {
		persistErr = err
	}
	if persistErr != nil {
		log.Warnf("detached task settlement durability degraded task_id=%v attempt=%v error=%v", taskID, attempt, persistErr)
	}
	return SubAgentState(settlement.Outcome)
}

func (a *MainAgent) retryTaskSettlementDurability(taskID string) (*DurableTaskRecord, error) {
	taskID = strings.TrimSpace(taskID)
	a.settlementJournalMu.Lock()
	defer a.settlementJournalMu.Unlock()
	a.subs.mu.RLock()
	rec := cloneDurableTaskRecord(a.subs.taskRecords[taskID])
	a.subs.mu.RUnlock()
	if rec == nil {
		return nil, fmt.Errorf("unknown task_id %q", taskID)
	}
	if rec.SettlementDurable {
		return rec, nil
	}
	settlement := cloneTaskSettlement(rec.LatestSettlement)
	if settlement == nil {
		if !isTerminalSubAgentState(SubAgentState(rec.State)) {
			return rec, fmt.Errorf("task %s has no complete terminal settlement to checkpoint", taskID)
		}
		revision := rec.LifecycleRevision
		if revision == 0 {
			revision = 1
		}
		settledAt := rec.UpdatedAt
		if settledAt.IsZero() {
			settledAt = rec.CreatedAt
		}
		if settledAt.IsZero() {
			settledAt = time.Now()
		}
		settlement = &TaskSettlement{
			TaskID:           rec.TaskID,
			Attempt:          rec.Attempt,
			TerminalRevision: revision,
			Outcome:          rec.State,
			Summary:          rec.LastSummary,
			Completion:       rec.LastCompletion,
			ArtifactRefs:     rec.LastArtifactRefs,
			SettledAt:        settledAt,
		}
		if rec.LastCompletion != nil && rec.LastCompletion.ResultRef != nil {
			ref := *rec.LastCompletion.ResultRef
			settlement.ResultRef = &ref
		}
	}
	if err := appendTaskSettlement(a.sessionDir, settlement); err != nil {
		return rec, err
	}
	a.subs.mu.Lock()
	current := cloneDurableTaskRecord(a.subs.taskRecords[taskID])
	if current == nil || current.Attempt != settlement.Attempt {
		a.subs.mu.Unlock()
		return rec, fmt.Errorf("task %s attempt changed during settlement checkpoint", taskID)
	}
	current.LatestSettlement = cloneTaskSettlement(settlement)
	current.SettlementDurable = true
	if current.LifecycleRevision < settlement.TerminalRevision {
		current.LifecycleRevision = settlement.TerminalRevision
	}
	a.subs.taskRecords[taskID] = current
	a.subs.settlements[taskAttemptKey{TaskID: taskID, Attempt: settlement.Attempt}] = cloneTaskSettlement(settlement)
	a.subs.notifyTaskChangeLocked()
	a.subs.mu.Unlock()
	if err := a.persistTaskRegistry(); err != nil {
		log.Warnf("settlement journal recovered but task registry cache remains stale task_id=%v error=%v", taskID, err)
	}
	return cloneDurableTaskRecord(current), nil
}
