package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/keakon/chord/internal/tools"
)

func (a *MainAgent) reconcileTerminalTaskChildren(parentTaskID string, parentState SubAgentState, reason string) bool {
	parentTaskID = strings.TrimSpace(parentTaskID)
	if a == nil || parentTaskID == "" || parentState == SubAgentStateCompleted {
		return false
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = fmt.Sprintf("ancestor task %s became %s", parentTaskID, parentState)
	}

	a.subs.mu.Lock()
	joined := make([]*DurableTaskRecord, 0)
	detached := make([]*DurableTaskRecord, 0)
	for _, rec := range a.subs.taskRecords {
		if rec == nil || strings.TrimSpace(rec.OwnerTaskID) != parentTaskID || !isNonTerminalTaskState(rec.State) {
			continue
		}
		if rec.JoinToOwner {
			joined = append(joined, cloneDurableTaskRecord(rec))
			continue
		}
		next := cloneDurableTaskRecord(rec)
		next.OwnerAgentID = ""
		next.OwnerTaskID = ""
		next.Depth = 1
		next.JoinToOwner = false
		next.UpdatedAt = time.Now()
		a.subs.taskRecords[next.TaskID] = next
		detached = append(detached, next)
	}
	a.subs.mu.Unlock()

	changed := false
	for _, rec := range detached {
		changed = true
		if sub := a.subAgentByTaskID(rec.TaskID); sub != nil {
			sub.reparentToMain()
			a.persistSubAgentMeta(sub)
		}
	}
	for _, rec := range joined {
		changed = true
		a.cancelTaskTreeInternal(rec.TaskID, fmt.Sprintf("cancelled because %s", reason))
	}
	if changed {
		a.persistTaskRegistry()
		a.refreshSubAgentInboxSummary()
	}
	return changed
}

func (a *MainAgent) cancelTaskTreeInternal(taskID, reason string) {
	a.cancelTaskTreeVisit(taskID, reason, make(map[string]struct{}))
}

func (a *MainAgent) cancelTaskTreeVisit(taskID, reason string, visited map[string]struct{}) {
	taskID = strings.TrimSpace(taskID)
	if a == nil || taskID == "" {
		return
	}
	// A corrupted registry can contain ownership cycles; never revisit a task
	// so the walk terminates instead of overflowing the stack.
	if _, seen := visited[taskID]; seen {
		return
	}
	visited[taskID] = struct{}{}
	for _, childTaskID := range a.directChildTaskIDs(taskID) {
		a.cancelTaskTreeVisit(childTaskID, reason, visited)
	}

	rec := a.taskRecordByTaskID(taskID)
	if rec == nil || !isNonTerminalTaskState(rec.State) {
		return
	}
	if sub := a.subAgentByTaskID(taskID); sub != nil {
		sub.cancelCurrentTurnFromLoop()
		sub.setState(SubAgentStateCancelled, reason)
		a.noteSubAgentStateTransition(sub, SubAgentStateCancelled)
		// parkSubAgent repeats this cleanup on success, but it refuses to park
		// while the just-cancelled turn still has an LLM request or queued
		// input in flight — this block is the only cleanup on that path.
		a.releaseSubAgentSlot(sub)
		a.fileTrack.ReleaseAll(sub.instanceID)
		tools.StopAllSpawnedForAgent(sub.instanceID, "terminated with ancestor task")
		a.persistSubAgentMeta(sub)
		a.syncTaskRecordFromSub(sub, reason)
		a.emitToTUI(AgentStatusEvent{AgentID: sub.instanceID, Status: string(SubAgentStateCancelled), Message: reason})
		a.parkSubAgent(sub.instanceID)
		return
	}

	a.subs.mu.Lock()
	if current := a.subs.taskRecords[taskID]; current != nil && isNonTerminalTaskState(current.State) {
		next := cloneDurableTaskRecord(current)
		next.State = string(SubAgentStateCancelled)
		next.ResumePolicy = taskResumePolicyExplicitOnly
		next.LastSummary = reason
		next.ClosedReason = reason
		next.RuntimeParked = true
		next.UpdatedAt = time.Now()
		a.subs.taskRecords[taskID] = next
	}
	a.subs.mu.Unlock()
}

func repairRestoredTaskTree(records map[string]*DurableTaskRecord) bool {
	changed := false
	for _, rec := range records {
		if rec == nil || !isNonTerminalTaskState(rec.State) {
			continue
		}
		ownerTaskID := strings.TrimSpace(rec.OwnerTaskID)
		if ownerTaskID == "" {
			continue
		}
		owner := records[ownerTaskID]
		if owner != nil && isNonTerminalTaskState(owner.State) {
			continue
		}
		if rec.JoinToOwner {
			rec.State = string(SubAgentStateCancelled)
			rec.ResumePolicy = taskResumePolicyExplicitOnly
			rec.LastSummary = "cancelled during recovery because joined owner is terminal or missing"
			rec.ClosedReason = rec.LastSummary
			rec.RuntimeParked = true
		} else {
			rec.OwnerAgentID = ""
			rec.OwnerTaskID = ""
			rec.Depth = 1
			rec.JoinToOwner = false
		}
		rec.UpdatedAt = time.Now()
		changed = true
	}
	for _, rec := range records {
		if rec == nil || SubAgentState(rec.State) != SubAgentStateWaitingDescendant {
			continue
		}
		hasJoinedChild := false
		for _, child := range records {
			if child != nil && child.JoinToOwner && strings.TrimSpace(child.OwnerTaskID) == rec.TaskID && isNonTerminalTaskState(child.State) {
				hasJoinedChild = true
				break
			}
		}
		if !hasJoinedChild {
			rec.State = string(SubAgentStateIdle)
			rec.ResumePolicy = taskResumePolicyNotify
			rec.LastSummary = "recovered from waiting_descendant without active joined children"
			rec.UpdatedAt = time.Now()
			changed = true
		}
	}
	return changed
}
