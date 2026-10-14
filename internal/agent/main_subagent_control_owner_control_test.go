package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestReservedInputBlocksParking(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-reserve-park")
	sub.setState(SubAgentStateIdle, "waiting")
	if !sub.canPark() {
		t.Fatal("expected an idle SubAgent with empty queues to be parkable")
	}

	reservation := sub.reserveUserMessage(pendingUserMessage{Content: "queued follow-up"})
	if reservation == nil {
		t.Fatal("reserveUserMessage rejected a message within limits")
	}
	if sub.canPark() {
		t.Fatal("outstanding input reservation must block parking")
	}

	reservation.Cancel()
	if !sub.canPark() {
		t.Fatal("cancelled reservation should make the SubAgent parkable again")
	}
}

func TestReservationCommitFailsAfterSubAgentShutdown(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-reserve-shutdown")

	reservation := sub.reserveUserMessage(pendingUserMessage{Content: "late delivery"})
	if reservation == nil {
		t.Fatal("reserveUserMessage rejected a message within limits")
	}
	sub.cancel()

	if reservation.Commit() {
		t.Fatal("Commit should fail once the SubAgent context is cancelled")
	}
	if sub.hasPendingUserInput() {
		t.Fatal("failed commit must not leave queued input or reservations behind")
	}
	sub.inputQueueMu.Lock()
	reserved := sub.inputQueueReservedMessages
	reservedBytes := sub.inputQueueReservedBytes
	queueBytes := sub.inputQueueBytes
	sub.inputQueueMu.Unlock()
	if reserved != 0 || reservedBytes != 0 || queueBytes != 0 {
		t.Fatalf("queue accounting after failed commit = reserved:%d reservedBytes:%d queueBytes:%d, want zeros", reserved, reservedBytes, queueBytes)
	}
}

func TestResponseMailboxReservationIsIdempotent(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-response-idempotent")
	input := pendingUserMessage{Content: "decision", Mailbox: &message.MailboxMetadata{
		MessageID: "response-corr-1", MessageType: string(AgentMessageTypeResponse),
	}}
	first := sub.reserveUserMessage(input)
	if first == nil || !first.Commit() {
		t.Fatal("first response reservation was not committed")
	}
	second := sub.reserveUserMessage(input)
	if second == nil || !second.duplicate || !second.Commit() {
		t.Fatalf("duplicate reservation = %#v, want successful no-op", second)
	}
	if got := len(sub.inputCh) + len(sub.inputOverflow); got != 1 {
		t.Fatalf("queued response count = %d, want 1", got)
	}
}

func TestDirectOwnerOnlyControlAppliesToLiveChildAndCompletedRehydrate(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 2, MaxDepth: 2}

	handle, err := a.CreateSubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID), tools.SubAgentRequest{Description: "child work", AgentType: "worker", ExpectedWriteScope: tools.WriteScope{PathPrefix: []string{"internal/agent/main.go"}}})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	child := a.subAgentByTaskID(handle.TaskID)
	if child == nil {
		t.Fatal("expected child SubAgent to exist")
	}

	liveHandle, err := a.NotifySubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID), child.taskID, "continue", "follow_up")
	if err != nil {
		t.Fatalf("NotifySubAgent(live direct owner): %v", err)
	}
	if liveHandle.TaskID != child.taskID {
		t.Fatalf("liveHandle.TaskID = %q, want %q", liveHandle.TaskID, child.taskID)
	}
	if _, err := a.NotifySubAgent(context.Background(), child.taskID, "ancestor override", "follow_up"); err == nil {
		t.Fatal("expected ancestor/main caller to be rejected for descendant control")
	}

	child.setState(SubAgentStateCompleted, "done")
	a.noteSubAgentStateTransition(child, SubAgentStateCompleted)
	a.persistSubAgentMeta(child)
	a.syncTaskRecordFromSub(child, "task completed")
	a.closeSubAgent(child.instanceID)

	rehydrated, err := a.NotifySubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID), handle.TaskID, "follow up", "follow_up")
	if err != nil {
		t.Fatalf("NotifySubAgent(completed direct owner): %v", err)
	}
	if !rehydrated.Rehydrated {
		t.Fatalf("rehydrated.Rehydrated = false, want true")
	}
	if _, err := a.NotifySubAgent(context.Background(), handle.TaskID, "ancestor override", "follow_up"); err == nil {
		t.Fatal("expected ancestor/main caller to be rejected for completed-task rehydrate control")
	}
}

func TestDirectOwnerOnlyStopRejectsAncestorCaller(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 2, MaxDepth: 2}

	handle, err := a.CreateSubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID), tools.SubAgentRequest{Description: "child work", AgentType: "worker"})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	if _, err := a.CancelSubAgent(context.Background(), handle.TaskID, "ancestor override"); err == nil {
		t.Fatal("expected ancestor/main caller to be rejected for descendant stop")
	}
	if _, err := a.CancelSubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID), handle.TaskID, "stop child"); err != nil {
		t.Fatalf("CancelSubAgent(direct owner): %v", err)
	}
}

func TestCancelSubAgentCancelsDirectDescendantsRecursively(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 3)

	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.instanceID = "worker-parent"
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 2, MaxDepth: 3}
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[parent.instanceID] = parent
	a.subs.mu.Unlock()
	a.syncTaskRecordFromSub(parent, "")

	child := newControllableTestSubAgent(t, a, "adhoc-child")
	child.instanceID = "worker-child"
	child.ownerAgentID = parent.instanceID
	child.ownerTaskID = parent.taskID
	child.depth = 2
	child.joinToOwner = true
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[child.instanceID] = child
	a.subs.mu.Unlock()
	a.syncTaskRecordFromSub(child, "")

	grand := newControllableTestSubAgent(t, a, "adhoc-grand")
	grand.instanceID = "worker-grand"
	grand.ownerAgentID = child.instanceID
	grand.ownerTaskID = child.taskID
	grand.depth = 3
	grand.joinToOwner = true
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[grand.instanceID] = grand
	a.subs.mu.Unlock()
	a.syncTaskRecordFromSub(grand, "")

	if _, err := a.CancelSubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID), child.taskID, "stop subtree"); err != nil {
		t.Fatalf("CancelSubAgent: %v", err)
	}
	if child.State() != SubAgentStateCancelled {
		t.Fatalf("child.State() = %q, want %q", child.State(), SubAgentStateCancelled)
	}
	record := a.taskRecordByTaskID(grand.taskID)
	if record == nil || record.State != string(SubAgentStateCancelled) {
		t.Fatalf("grandchild record state = %#v, want cancelled", record)
	}
}

func TestCallerTaskIDAllowsRehydratedParentToControlChild(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)

	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.instanceID = "worker-parent-1"
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 2, MaxDepth: 2}
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[parent.instanceID] = parent
	a.subs.mu.Unlock()
	a.syncTaskRecordFromSub(parent, "")

	child := newControllableTestSubAgent(t, a, "adhoc-child")
	child.instanceID = "worker-child-1"
	child.ownerAgentID = parent.instanceID
	child.ownerTaskID = parent.taskID
	child.depth = 2
	child.joinToOwner = true
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[child.instanceID] = child
	a.subs.mu.Unlock()
	a.syncTaskRecordFromSub(child, "")

	parent2 := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent2.instanceID = "worker-parent-2"
	parent2.depth = 1
	parent2.delegation = parent.delegation
	a.subs.mu.Lock()
	delete(a.subs.subAgents, parent.instanceID)
	a.subs.subAgents[parent2.instanceID] = parent2
	a.subs.mu.Unlock()
	a.syncTaskRecordFromSub(parent2, "")

	handle, err := a.NotifySubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent2.instanceID), parent2.taskID), child.taskID, "continue from rehydrated parent", "follow_up")
	if err != nil {
		t.Fatalf("NotifySubAgent: %v", err)
	}
	if handle.TaskID != child.taskID {
		t.Fatalf("handle.TaskID = %q, want %q", handle.TaskID, child.taskID)
	}
}

func TestCancelSubAgentCancelsPendingToolAndReleasesSlot(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-2")
	sub.ctxMgr.Append(message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{{
			ID:   "call-1",
			Name: "read",
			Args: []byte(`{"path":"README.md"}`),
		}},
	})
	sub.turn = &Turn{
		ID:              1,
		Ctx:             context.Background(),
		Cancel:          func() {},
		PendingToolMeta: map[string]PendingToolCall{"call-1": {CallID: "call-1", Name: "read", ArgsJSON: `{"path":"README.md"}`, AgentID: sub.instanceID}},
	}
	sub.turn.PendingToolCalls.Store(1)
	sub.semHeld = true
	a.sem <- struct{}{}
	a.focusedAgent.Store(sub)

	handle, err := a.CancelSubAgent(context.Background(), "adhoc-2", "task superseded")
	if err != nil {
		t.Fatalf("CancelSubAgent: %v", err)
	}
	if handle.Status != "cancelled" {
		t.Fatalf("handle.Status = %q, want cancelled", handle.Status)
	}
	if sub.State() != SubAgentStateCancelled {
		t.Fatalf("sub.State() = %q, want cancelled", sub.State())
	}
	if sub.semHeld {
		t.Fatal("expected stopped worker to release semaphore slot")
	}
	if got := len(a.sem); got != 0 {
		t.Fatalf("len(a.sem) = %d, want 0", got)
	}
	if focused := a.focusedAgent.Load(); focused != nil {
		t.Fatalf("focusedAgent = %v, want nil", focused.instanceID)
	}
	msgs := sub.ctxMgr.Snapshot()
	last := msgs[len(msgs)-1]
	if last.Role != "tool" || last.ToolCallID != "call-1" || last.Content != "Cancelled" {
		t.Fatalf("last message = %#v, want cancelled tool result for call-1", last)
	}
}

func TestCancelSubAgentAdmissionsDuringCreateReleasesSlotOnce(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	entered := make(chan struct{})
	release := make(chan struct{})
	a.taskRegistryPersistHook = func() {
		close(entered)
		<-release
	}
	type outcome struct {
		handle tools.TaskHandle
		err    error
	}
	result := make(chan outcome, 1)
	go func() {
		handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{Description: "cancel vs create", AgentType: "worker", PlanTaskRef: "plan-race", SemanticTaskKey: "semantic-race"})
		result <- outcome{handle: handle, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("create did not reach durable persistence")
	}
	// Cancel while the create is inside registration persistence: the admission
	// is still recorded as holding the slot, so the canceller releases it once.
	// The create's post-persistence recheck then backs off without a second
	// release (the admission is already gone from the registry).
	a.cancelSubAgentAdmissions()
	close(release)
	got := <-result
	if got.err == nil || !strings.Contains(got.err.Error(), "admission was cancelled after persistence") {
		t.Fatalf("CreateSubAgent() = (%#v, %v), want post-persistence cancellation", got.handle, got.err)
	}
	if pending := len(a.subs.admissions); pending != 0 {
		t.Fatalf("pending admissions = %d, want 0", pending)
	}
	if live := len(a.subs.snapshotSubAgents()); live != 0 {
		t.Fatalf("live SubAgents = %d, want 0", live)
	}
	if rec := a.taskRecordByTaskID("adhoc-1"); rec != nil {
		t.Fatalf("task record = %#v, want nil after cancellation", rec)
	}
	if got := a.governor.snapshot().RuntimeInUse; got != 0 {
		t.Fatalf("runtime in use = %d, want 0: the slot must be released exactly once across cancel and create rollback", got)
	}
}
