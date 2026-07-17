package agent

import (
	"context"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
)

func newSubtreeTestSubAgent(t *testing.T, parent *MainAgent, instanceID, taskID string) *SubAgent {
	t.Helper()
	ctx, cancel := context.WithCancel(parent.parentCtx)
	sub := NewSubAgent(SubAgentConfig{
		InstanceID:   instanceID,
		TaskID:       taskID,
		AgentDefName: "worker",
		TaskDesc:     "do work",
		LLMClient:    newTestLLMClient(),
		Recovery:     parent.recovery,
		Parent:       parent,
		ParentCtx:    ctx,
		Cancel:       cancel,
		BaseTools:    parent.tools,
		WorkDir:      parent.projectRoot,
		SessionDir:   parent.sessionDir,
		ModelName:    "test-model",
	})
	parent.subs.add(sub)
	parent.syncTaskRecordFromSub(sub, "")
	return sub
}

func TestTerminalParentCancelsJoinedDescendants(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 3)
	parent := newSubtreeTestSubAgent(t, a, "worker-parent", "parent-task")
	parent.ownerMu.Lock()
	parent.depth = 1
	parent.ownerMu.Unlock()

	child := newSubtreeTestSubAgent(t, a, "worker-child", "child-task")
	child.ownerMu.Lock()
	child.ownerAgentID = parent.instanceID
	child.ownerTaskID = parent.taskID
	child.depth = 2
	child.joinToOwner = true
	child.ownerMu.Unlock()
	a.syncTaskRecordFromSub(child, "")

	a.handleSubAgentCloseRequestedEvent(Event{SourceID: parent.instanceID, Payload: &SubAgentCloseRequestedPayload{
		Reason: "parent failed", ClosedReason: "parent failed", FinalState: SubAgentStateFailed,
	}})

	if got := a.taskRecordByTaskID(child.taskID); got == nil || got.State != string(SubAgentStateCancelled) {
		t.Fatalf("joined child record = %#v, want cancelled", got)
	}
}

func TestTerminalParentReparentsDetachedChildToMain(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 3)
	parent := newSubtreeTestSubAgent(t, a, "worker-parent", "parent-task")
	child := newSubtreeTestSubAgent(t, a, "worker-child", "child-task")
	child.ownerMu.Lock()
	child.ownerAgentID = parent.instanceID
	child.ownerTaskID = parent.taskID
	child.depth = 2
	child.joinToOwner = false
	child.ownerMu.Unlock()
	a.syncTaskRecordFromSub(child, "")

	a.handleSubAgentCloseRequestedEvent(Event{SourceID: parent.instanceID, Payload: &SubAgentCloseRequestedPayload{
		Reason: "parent failed", ClosedReason: "parent failed", FinalState: SubAgentStateFailed,
	}})

	if child.OwnerAgentID() != "" || child.OwnerTaskID() != "" || child.Depth() != 1 || child.JoinToOwner() {
		t.Fatalf("detached child owner = (%q, %q, depth=%d, join=%v), want main", child.OwnerAgentID(), child.OwnerTaskID(), child.Depth(), child.JoinToOwner())
	}
	if got := a.taskRecordByTaskID(child.taskID); got == nil || got.OwnerAgentID != "" || got.OwnerTaskID != "" || got.JoinToOwner {
		t.Fatalf("detached child record = %#v, want main ownership", got)
	}
}

func TestRepairRestoredTaskTree(t *testing.T) {
	records := map[string]*DurableTaskRecord{
		"terminal": {TaskID: "terminal", State: string(SubAgentStateFailed)},
		"joined":   {TaskID: "joined", OwnerTaskID: "terminal", OwnerAgentID: "old", JoinToOwner: true, State: string(SubAgentStateRunning)},
		"detached": {TaskID: "detached", OwnerTaskID: "terminal", OwnerAgentID: "old", JoinToOwner: false, State: string(SubAgentStateRunning), Depth: 2},
		"waiting":  {TaskID: "waiting", State: string(SubAgentStateWaitingDescendant)},
	}
	if !repairRestoredTaskTree(records) {
		t.Fatal("repairRestoredTaskTree() = false, want repairs")
	}
	if records["joined"].State != string(SubAgentStateCancelled) {
		t.Fatalf("joined state = %q, want cancelled", records["joined"].State)
	}
	if records["detached"].OwnerTaskID != "" || records["detached"].OwnerAgentID != "" || records["detached"].Depth != 1 {
		t.Fatalf("detached record = %#v, want main ownership", records["detached"])
	}
	if records["waiting"].State != string(SubAgentStateIdle) || records["waiting"].ResumePolicy != taskResumePolicyNotify {
		t.Fatalf("waiting record = %#v, want resumable idle", records["waiting"])
	}
}

func TestTerminalParentUsesRecordJoinPolicy(t *testing.T) {
	cfg := config.DelegationConfig{}
	if cfg.ChildJoinEnabled() != true {
		t.Fatal("default child join policy changed; subtree tests assume joined by default")
	}
}

func TestCancelTaskTreeInternalTerminatesOnOwnershipCycle(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	now := time.Now()
	a.subs.mu.Lock()
	a.subs.taskRecords["task-a"] = &DurableTaskRecord{TaskID: "task-a", OwnerTaskID: "task-b", State: string(SubAgentStateIdle), UpdatedAt: now}
	a.subs.taskRecords["task-b"] = &DurableTaskRecord{TaskID: "task-b", OwnerTaskID: "task-a", State: string(SubAgentStateIdle), UpdatedAt: now}
	a.subs.mu.Unlock()

	done := make(chan struct{})
	go func() {
		a.cancelTaskTreeInternal("task-a", "cycle test")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelTaskTreeInternal did not terminate on a cyclic registry")
	}
	for _, id := range []string{"task-a", "task-b"} {
		if rec := a.taskRecordByTaskID(id); rec == nil || rec.State != string(SubAgentStateCancelled) {
			t.Fatalf("record %s = %#v, want cancelled", id, rec)
		}
	}
}
