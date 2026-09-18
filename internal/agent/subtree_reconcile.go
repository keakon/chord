package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/keakon/golog/log"

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
		_, _, err := a.commitTerminalTask(sub, SubAgentStateCancelled, reason, reason, nil)
		if err != nil {
			log.Warnf("cascade cancel settlement failed task_id=%v error=%v", taskID, err)
		}
		status := a.terminalStatusAfterCommit(sub, SubAgentStateCancelled, err)
		// parkSubAgent repeats this cleanup on success, but it refuses to park
		// while the just-cancelled turn still has an LLM request or queued
		// input in flight — this block is the only cleanup on that path.
		a.releaseSubAgentSlot(sub)
		a.fileTrack.ReleaseAll(sub.instanceID)
		tools.StopAllJobsForAgent(sub.instanceID, "terminated with ancestor task")
		a.emitToTUI(AgentStatusEvent{AgentID: sub.instanceID, Status: string(status), Message: reason})
		a.parkSubAgent(sub.instanceID)
		return
	}

	a.settleDetachedTerminalTask(taskID, SubAgentStateCancelled, reason, reason)
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

// settleStrandedWaitingDescendantOwners settles the parked ancestors left
// behind when a user cancel terminated the joined children they were waiting
// on.
//
// The runtime walk in interruptSubAgentTurnsForUserCancel only sees workers
// that still have a live runtime, but an owner that delegated with child_join
// parks the moment it enters waiting_descendant (see enterWaitingDescendant),
// so it is never in that walk. Nothing else announces the child's
// cancellation either: a user cancel settles the child straight through
// commitTerminalTask and emits no mailbox, so the descendant-mailbox wake that
// resolves a normal child completion (see the SubAgentMailboxKindCompleted
// branch of the owned-mailbox router) never fires. Without this pass the
// owner's non-terminal waiting_descendant record strands for the rest of the
// live session — task.collect(wait) blocks until its timeout and retention
// cannot archive a non-terminal record — and only repairRestoredTaskTree,
// which runs at restore, would ever converge it.
//
// The outcome is Cancelled, not a resume: the same user action already settles
// a live waiting_descendant worker as Cancelled with "stopped by user", and a
// parked owner must not come back to life just because it had no runtime at
// the moment the user pressed cancel. repairRestoredTaskTree differs on
// purpose — it repairs a crash, where nobody asked for the work to stop.
func (a *MainAgent) settleStrandedWaitingDescendantOwners(cancelledTaskIDs []string) bool {
	if a == nil {
		return false
	}
	queue := make([]string, 0, len(cancelledTaskIDs))
	visited := make(map[string]struct{}, len(cancelledTaskIDs)+1)
	for _, taskID := range cancelledTaskIDs {
		taskID = strings.TrimSpace(taskID)
		if taskID == "" {
			continue
		}
		if _, seen := visited[taskID]; seen {
			continue
		}
		visited[taskID] = struct{}{}
		queue = append(queue, taskID)
	}
	changed := false
	// One task is consumed per iteration and only its owner is appended, and
	// every enqueued task is visited at most once, so the walk terminates even
	// if a restored registry carries a malformed ownership cycle.
	for len(queue) > 0 {
		taskID := queue[0]
		queue = queue[1:]
		rec := a.taskRecordByTaskID(taskID)
		if rec == nil {
			continue
		}
		ownerTaskID := strings.TrimSpace(rec.OwnerTaskID)
		if ownerTaskID == "" {
			continue
		}
		// A live owner is reachable by the runtime walk above (or is still
		// running on other work); only a parked record is invisible to it.
		if a.subAgentByTaskID(ownerTaskID) != nil {
			continue
		}
		owner := a.taskRecordByTaskID(ownerTaskID)
		if owner == nil || SubAgentState(strings.TrimSpace(owner.State)) != SubAgentStateWaitingDescendant {
			continue
		}
		// An owner still waiting on another joined child keeps waiting: this
		// cancellation did not resolve its wait. The check is the same
		// predicate repairRestoredTaskTree uses on the restore side.
		if len(a.outstandingJoinChildTaskIDs(ownerTaskID)) > 0 {
			continue
		}
		if _, seen := visited[ownerTaskID]; seen {
			continue
		}
		visited[ownerTaskID] = struct{}{}
		reason := fmt.Sprintf("stopped by user: joined child task %s was cancelled", taskID)
		// The guard closes the window where the owner is rehydrated between
		// the checks above and the settle: a rehydrated record is rebuilt
		// through buildTaskRecordFromSub, which clears RuntimeParked. It must
		// not take subs.mu — settleDetachedTerminalTaskGuarded calls it while
		// holding that lock.
		guard := func(current *DurableTaskRecord) bool {
			return current != nil &&
				SubAgentState(strings.TrimSpace(current.State)) == SubAgentStateWaitingDescendant &&
				current.RuntimeParked
		}
		if a.settleDetachedTerminalTaskGuarded(ownerTaskID, SubAgentStateCancelled, reason, reason, guard) != SubAgentStateCancelled {
			continue
		}
		// The settle already notified task-registry watchers, so the UI sees the
		// record change; this mirrors the status event a live worker gets from
		// the same user action. A record restored without an instance id has no
		// UI target for it, and the settlement above is what collect and
		// retention read, so the cancellation is still durable either way.
		if agentID := strings.TrimSpace(owner.LatestInstanceID); agentID != "" {
			a.emitToTUI(AgentStatusEvent{
				AgentID: agentID,
				Status:  string(SubAgentStateCancelled),
				Message: reason,
			})
		}
		log.Infof("settled parked waiting_descendant owner stranded by user cancel task_id=%v child_task_id=%v", ownerTaskID, taskID)
		changed = true
		// The owner may itself be a joined child of a parked ancestor whose
		// wait is now empty too.
		queue = append(queue, ownerTaskID)
	}
	return changed
}
