package agent

import (
	"fmt"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/tools"
)

func (a *MainAgent) grantSubAgentWriteScope(callerAgentID, callerTaskID, taskID string, grant tools.WriteScope) error {
	grant = grant.Normalized()
	if len(grant.Files) == 0 && len(grant.PathPrefix) == 0 && len(grant.Modules) == 0 {
		return nil
	}
	epoch := a.admissionEpoch.Load()
	a.admissionMu.Lock()
	defer a.admissionMu.Unlock()
	if a.shuttingDown.Load() || a.admissionPaused.Load() || a.admissionEpoch.Load() != epoch {
		return fmt.Errorf("scope grant invalidated by session or lifecycle change")
	}
	taskID = strings.TrimSpace(taskID)
	// Parking snapshots the live scope and publishes a detached record. Keep
	// that handoff outside the grant's persist/publish window.
	if sub := a.subAgentByTaskID(taskID); sub != nil {
		sub.lifecycleMu.Lock()
		defer sub.lifecycleMu.Unlock()
	}
	record, err := a.canCallerControlTask(callerAgentID, callerTaskID, taskID)
	if err != nil {
		return err
	}
	taskID = record.TaskID
	if a.agentRoleRegistersNoFileWriteTools(record.AgentDefName) {
		return fmt.Errorf("task %s runs under a role that registers no file-modifying tools, so it cannot write files even with a grant; delegate the writing work as a new task under a role that can write files", taskID)
	}
	if grant.AddsNothingTo(record.ExpectedWriteScope) {
		return fmt.Errorf("task %s already covers every path in the grant", taskID)
	}
	widened := tools.WidenWriteScope(record.ExpectedWriteScope, grant)
	if ownerTaskID := record.OwnerTaskID; ownerTaskID != "" {
		// The target is write-capable here (the no-write-role case was rejected
		// above) and widened is never empty, so the containment check only
		// exercises the declared-path branch.
		if owner := a.taskRecordByTaskID(ownerTaskID); owner != nil &&
			!childWriteScopeWithinParent(owner.ExpectedWriteScope, widened, false, a.writeScopeBaseDir()) {
			return fmt.Errorf("the widened scope for task %s would be broader than its parent task %s", taskID, ownerTaskID)
		}
	}

	a.taskRegistryPersistMu.Lock()
	defer a.taskRegistryPersistMu.Unlock()
	a.subs.mu.RLock()
	conflict := a.findWriteScopeGrantConflictLocked(record, widened)
	records := cloneDurableTaskRecordMap(a.subs.taskRecords)
	a.subs.mu.RUnlock()
	if conflict != "" {
		a.orchestrationMetrics.scopeConflicts.Add(1)
		return fmt.Errorf("the widened scope for task %s overlaps with active task %s; serialize the writing work", taskID, conflict)
	}
	if records == nil {
		records = make(map[string]*DurableTaskRecord)
	}
	updated := cloneDurableTaskRecord(record)
	updated.ExpectedWriteScope = widened
	records[taskID] = updated
	if hook := a.taskRegistryPersistHook; hook != nil {
		hook()
	}
	if err := persistDurableTaskRecords(a.sessionDir, records); err != nil {
		return fmt.Errorf("persist widened write scope for %s: %w", taskID, err)
	}

	// Keep the persistence lock until publication so another registry writer
	// cannot replace the committed grant with the still-old live scope.
	a.subs.mu.Lock()
	if current := a.subs.taskRecords[taskID]; current != nil {
		updated = cloneDurableTaskRecord(current)
	}
	if sub := a.subs.subAgentByTaskIDLocked(taskID); sub != nil {
		sub.widenWriteScope(grant)
	}
	updated.ExpectedWriteScope = widened
	a.subs.taskRecords[taskID] = updated
	a.subs.notifyTaskChangeLocked()
	a.subs.mu.Unlock()
	log.Infof("SubAgent write scope widened task_id=%v scope=%v", taskID, widened.Summary())
	return nil
}

func (a *MainAgent) findWriteScopeGrantConflictLocked(target *DurableTaskRecord, widened tools.WriteScope) string {
	lineage := a.taskOwnerLineageLocked(target.TaskID)
	conflicts := func(taskID, ownerTaskID, siblingAgentType string, scope tools.WriteScope) bool {
		if _, ancestor := lineage[strings.TrimSpace(taskID)]; ancestor {
			return false
		}
		if _, descendant := a.taskOwnerLineageLocked(ownerTaskID)[target.TaskID]; descendant {
			return false
		}
		return a.taskScopesConflict(target.AgentDefName, widened, siblingAgentType, scope, a.writeScopeBaseDir())
	}
	for taskID, rec := range a.subs.taskRecords {
		if rec != nil && isNonTerminalTaskState(rec.State) && conflicts(taskID, rec.OwnerTaskID, rec.AgentDefName, rec.ExpectedWriteScope) {
			return taskID
		}
	}
	for taskID, pending := range a.subs.admissions {
		if pending != nil && conflicts(taskID, pending.ownerTaskID, pending.agentType, pending.expectedWriteScope) {
			return taskID
		}
	}
	return ""
}
