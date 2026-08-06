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
