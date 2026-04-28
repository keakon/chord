package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func newControllableTestSubAgent(t *testing.T, parent *MainAgent, taskID string) *SubAgent {
	t.Helper()
	ctx, cancel := context.WithCancel(parent.parentCtx)
	sub := NewSubAgent(SubAgentConfig{
		InstanceID:   "worker-1",
		TaskID:       taskID,
		AgentDefName: "worker",
		TaskDesc:     "do work",
		LLMClient:    newTestLLMClient(),
		Recovery:     parent.recovery,
		Parent:       parent,
		ParentCtx:    ctx,
		Cancel:       cancel,
		BaseTools:    parent.tools,
		WorkDir:      t.TempDir(),
		SessionDir:   parent.sessionDir,
		ModelName:    "test-model",
	})
	parent.mu.Lock()
	parent.subAgents[sub.instanceID] = sub
	parent.mu.Unlock()
	parent.syncTaskRecordFromSub(sub, "")
	return sub
}

func configureNestedDelegationTestRuntime(a *MainAgent, maxDepth int) {
	a.llmFactory = func(systemPrompt string, agentModels []string, variant string) *llm.Client {
		return newTestLLMClient()
	}
	a.agentConfigs = map[string]*config.AgentConfig{
		"worker": {
			Name:        "worker",
			Mode:        "subagent",
			Models:      []string{"sample/test-model"},
			Delegation:  config.DelegationConfig{MaxChildren: 10, MaxDepth: maxDepth},
			Description: "Nested worker",
		},
	}
	a.activeConfig = &config.AgentConfig{
		Name:       "builder",
		Delegation: config.DelegationConfig{MaxChildren: 10, MaxDepth: maxDepth},
	}
}

func startMainAgentLoopForTest(t *testing.T, a *MainAgent) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = a.Run(ctx)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !a.started.Load() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for MainAgent event loop to start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() {
		cancel()
		<-a.done
	})
	return cancel
}

func TestCompletedMailboxesAreBatchedIntoSingleMainTurn(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.subAgentInbox.urgent = []SubAgentMailboxMessage{
		{MessageID: "a-1", AgentID: "worker-a", TaskID: "task-a", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent, Summary: "done a"},
		{MessageID: "b-1", AgentID: "worker-b", TaskID: "task-b", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent, Summary: "done b"},
	}

	a.drainSubAgentInbox()
	if a.turn == nil {
		t.Fatal("expected drainSubAgentInbox to start a main turn")
	}
	if got := len(a.pendingSubAgentMailboxes); got != 2 {
		t.Fatalf("pending mailbox batch len = %d, want 2", got)
	}
	if got := len(a.activeSubAgentMailboxes); got != 2 {
		t.Fatalf("active mailbox batch len = %d, want 2", got)
	}
	if a.pendingSubAgentMailboxes[0].MessageID != "a-1" || a.pendingSubAgentMailboxes[1].MessageID != "b-1" {
		t.Fatalf("unexpected pending mailbox batch order: %#v", a.pendingSubAgentMailboxes)
	}
	if got := len(a.subAgentInbox.urgent); got != 0 {
		t.Fatalf("len(urgent inbox) = %d, want 0 after batching", got)
	}
}

func TestPrepareSubAgentMailboxBatchForTurnContinuationStagesDecisionMailbox(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.subAgentInbox.urgent = []SubAgentMailboxMessage{{
		MessageID:   "wake-1",
		AgentID:     "worker-a",
		TaskID:      "task-a",
		Kind:        SubAgentMailboxKindDecisionRequired,
		Priority:    SubAgentMailboxPriorityInterrupt,
		Summary:     "need main decision",
		RequiresAck: true,
	}}
	a.newTurn()
	turnID := a.turn.ID

	if !a.prepareSubAgentMailboxBatchForTurnContinuation() {
		t.Fatal("expected busy-turn mailbox staging to succeed")
	}
	if a.turn == nil || a.turn.ID != turnID {
		t.Fatalf("turn changed during mailbox staging, got %#v want turn %d", a.turn, turnID)
	}
	if got := len(a.pendingSubAgentMailboxes); got != 1 {
		t.Fatalf("pending mailbox batch len = %d, want 1", got)
	}
	if got := len(a.activeSubAgentMailboxes); got != 1 {
		t.Fatalf("active mailbox batch len = %d, want 1", got)
	}
	if a.activeSubAgentMailbox == nil || a.activeSubAgentMailbox.MessageID != "wake-1" {
		t.Fatalf("activeSubAgentMailbox = %#v, want wake-1", a.activeSubAgentMailbox)
	}
	if !a.activeSubAgentMailboxAck {
		t.Fatal("expected staged mailbox batch to await ack")
	}
	if got := len(a.subAgentInbox.urgent); got != 0 {
		t.Fatalf("len(urgent inbox) = %d, want 0 after staging", got)
	}
}

func TestClosedMainOwnedCompletedMailboxStillQueuesForMain(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-root")
	a.newTurn()

	a.handleAgentDone(Event{
		Type:     EventAgentDone,
		SourceID: sub.instanceID,
		Payload:  &AgentResult{Summary: "root task done"},
	})

	if got := a.subAgentByID(sub.instanceID); got != nil {
		t.Fatalf("subAgentByID(%q) = %#v, want nil after close", sub.instanceID, got)
	}

	var evt Event
	select {
	case evt = <-a.eventCh:
	default:
		t.Fatal("expected completion mailbox event queued on eventCh")
	}
	if evt.Type != EventSubAgentMailbox {
		t.Fatalf("queued event type = %q, want %q", evt.Type, EventSubAgentMailbox)
	}

	a.dispatch(evt)

	if got := len(a.subAgentInbox.urgent); got != 1 {
		t.Fatalf("len(urgent inbox) = %d, want 1 completed mailbox", got)
	}
	msg := a.subAgentInbox.urgent[0]
	if msg.AgentID != sub.instanceID {
		t.Fatalf("mailbox AgentID = %q, want %q", msg.AgentID, sub.instanceID)
	}
	if msg.TaskID != sub.taskID {
		t.Fatalf("mailbox TaskID = %q, want %q", msg.TaskID, sub.taskID)
	}
	if msg.OwnerAgentID != "" || msg.OwnerTaskID != "" {
		t.Fatalf("mailbox owner = (%q,%q), want main-owned empty owner", msg.OwnerAgentID, msg.OwnerTaskID)
	}
	if msg.Kind != SubAgentMailboxKindCompleted {
		t.Fatalf("mailbox Kind = %q, want completed", msg.Kind)
	}
}

func TestClosedUnknownCompletedMailboxIsDropped(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()

	a.handleSubAgentMailboxEvent(Event{
		Type:     EventSubAgentMailbox,
		SourceID: "worker-missing",
		Payload: &SubAgentMailboxMessage{
			MessageID: "missing-1",
			AgentID:   "worker-missing",
			TaskID:    "adhoc-missing",
			Kind:      SubAgentMailboxKindCompleted,
			Priority:  SubAgentMailboxPriorityUrgent,
			Summary:   "done",
		},
	})

	if got := len(a.subAgentInbox.urgent) + len(a.subAgentInbox.normal) + len(a.subAgentInbox.progress); got != 0 {
		t.Fatalf("unexpected mailbox accepted for unknown closed worker, total=%d", got)
	}
}

func TestSetIdleAndDrainPendingConsumesAllActiveCompletedMailboxes(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.activeSubAgentMailboxes = []*SubAgentMailboxMessage{
		{MessageID: "a-1", AgentID: "worker-a", TaskID: "task-a", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent, Summary: "done a"},
		{MessageID: "b-1", AgentID: "worker-b", TaskID: "task-b", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent, Summary: "done b"},
	}
	a.activeSubAgentMailbox = a.activeSubAgentMailboxes[0]
	a.activeSubAgentMailboxAck = true
	a.ctxMgr.Append(message.Message{Role: "assistant", Content: "Handled both completed workers."})

	a.setIdleAndDrainPending()

	acks, err := loadSubAgentMailboxAcks(a.sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxAcks: %v", err)
	}
	for _, messageID := range []string{"a-1", "b-1"} {
		ack, ok := acks[messageID]
		if !ok {
			t.Fatalf("ack for %q not found", messageID)
		}
		if ack.Outcome != "consumed" {
			t.Fatalf("ack[%s].Outcome = %q, want consumed", messageID, ack.Outcome)
		}
	}
}

func TestTakeOutstandingMailboxForSubPreservesSiblingCompletedMailboxes(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	subA := newControllableTestSubAgent(t, a, "task-a")
	subA.instanceID = "worker-a"
	a.mu.Lock()
	delete(a.subAgents, "worker-1")
	a.subAgents[subA.instanceID] = subA
	a.mu.Unlock()

	subB := newControllableTestSubAgent(t, a, "task-b")
	subB.instanceID = "worker-b"
	a.mu.Lock()
	delete(a.subAgents, "worker-1")
	a.subAgents[subB.instanceID] = subB
	a.mu.Unlock()

	a.activeSubAgentMailboxes = []*SubAgentMailboxMessage{
		{MessageID: "a-1", AgentID: "worker-a", TaskID: "task-a", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent, Summary: "done a"},
		{MessageID: "b-1", AgentID: "worker-b", TaskID: "task-b", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent, Summary: "done b"},
	}
	a.activeSubAgentMailbox = a.activeSubAgentMailboxes[0]
	a.pendingSubAgentMailboxes = append([]*SubAgentMailboxMessage(nil), a.activeSubAgentMailboxes...)

	got := a.takeOutstandingMailboxForSub(subA)
	if got == nil || got.MessageID != "a-1" {
		t.Fatalf("takeOutstandingMailboxForSub() = %#v, want mailbox a-1", got)
	}
	if len(a.activeSubAgentMailboxes) != 1 || a.activeSubAgentMailboxes[0].MessageID != "b-1" {
		t.Fatalf("active mailbox batch = %#v, want only b-1 remaining", a.activeSubAgentMailboxes)
	}
	if a.activeSubAgentMailbox == nil || a.activeSubAgentMailbox.MessageID != "b-1" {
		t.Fatalf("activeSubAgentMailbox = %#v, want b-1", a.activeSubAgentMailbox)
	}
	if len(a.pendingSubAgentMailboxes) != 1 || a.pendingSubAgentMailboxes[0].MessageID != "b-1" {
		t.Fatalf("pending mailbox batch = %#v, want only b-1 remaining", a.pendingSubAgentMailboxes)
	}
}

func TestSendMessageToWaitingPrimaryWorkerResumesExecution(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-1")
	sub.setState(SubAgentStateWaitingPrimary, "need approval")
	a.enqueueSubAgentMailbox(SubAgentMailboxMessage{
		MessageID:   "worker-1-1",
		AgentID:     sub.instanceID,
		TaskID:      sub.taskID,
		Kind:        SubAgentMailboxKindDecisionRequired,
		Priority:    SubAgentMailboxPriorityInterrupt,
		Summary:     "need approval",
		RequiresAck: true,
	})

	handle, err := a.NotifySubAgent(context.Background(), "adhoc-1", "continue with option B", "reply")
	if err != nil {
		t.Fatalf("NotifySubAgent: %v", err)
	}
	if handle.Status != "resumed" {
		t.Fatalf("handle.Status = %q, want resumed", handle.Status)
	}
	if sub.State() != SubAgentStateRunning {
		t.Fatalf("sub.State() = %q, want running", sub.State())
	}
	if !sub.semHeld {
		t.Fatal("expected resumed worker to hold semaphore slot")
	}
	select {
	case msg := <-sub.inputCh:
		if got := pendingUserMessageText(msg); got != "[reply] continue with option B" {
			t.Fatalf("queued message = %q, want %q", got, "[reply] continue with option B")
		}
	default:
		t.Fatal("expected resumed worker to receive queued follow-up message")
	}
	if got := len(a.subAgentInbox.urgent); got != 0 {
		t.Fatalf("len(urgent) = %d, want 0 after direct reply consumes mailbox", got)
	}
	acks, err := loadSubAgentMailboxAcks(a.sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxAcks: %v", err)
	}
	ack, ok := acks["worker-1-1"]
	if !ok {
		t.Fatal("expected ack for consumed mailbox")
	}
	if ack.ReplyKind != "reply" {
		t.Fatalf("ack.ReplyKind = %q, want reply", ack.ReplyKind)
	}
	if ack.ReplyToMailboxID != "worker-1-1" {
		t.Fatalf("ack.ReplyToMailboxID = %q, want worker-1-1", ack.ReplyToMailboxID)
	}
	if ack.ReplyMessageID == "" {
		t.Fatal("expected reply_message_id to be recorded")
	}
}

func TestNotifySubAgentDoesNotAckMailboxWhenSlotUnavailable(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-slot")
	sub.setState(SubAgentStateWaitingPrimary, "need approval")
	a.enqueueSubAgentMailbox(SubAgentMailboxMessage{
		MessageID:   "worker-1-2",
		AgentID:     sub.instanceID,
		TaskID:      sub.taskID,
		Kind:        SubAgentMailboxKindDecisionRequired,
		Priority:    SubAgentMailboxPriorityInterrupt,
		Summary:     "need approval",
		RequiresAck: true,
	})
	for i := 0; i < cap(a.sem); i++ {
		a.sem <- struct{}{}
	}

	if _, err := a.NotifySubAgent(context.Background(), "adhoc-slot", "continue with option C", "reply"); err == nil {
		t.Fatal("expected NotifySubAgent to fail when semaphore is exhausted")
	}
	if got := len(a.subAgentInbox.urgent); got != 1 {
		t.Fatalf("len(urgent) = %d, want 1 after failed resume", got)
	}
	acks, err := loadSubAgentMailboxAcks(a.sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxAcks: %v", err)
	}
	if _, ok := acks["worker-1-2"]; ok {
		t.Fatal("unexpected mailbox ack recorded when resume failed before worker restart")
	}
}

func TestCreateSubAgentFromSubAgentContextSetsOwnerAndDepth(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 2, MaxDepth: 2}

	handle, err := a.CreateSubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID), "child work", "worker", "", "", tools.WriteScope{})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	if handle.Status != "started" {
		t.Fatalf("handle.Status = %q, want started", handle.Status)
	}
	child := a.subAgentByTaskID(handle.TaskID)
	if child == nil {
		t.Fatal("expected child SubAgent to exist")
	}
	if child.ownerAgentID != parent.instanceID {
		t.Fatalf("child.ownerAgentID = %q, want %q", child.ownerAgentID, parent.instanceID)
	}
	if child.ownerTaskID != parent.taskID {
		t.Fatalf("child.ownerTaskID = %q, want %q", child.ownerTaskID, parent.taskID)
	}
	if child.depth != 2 {
		t.Fatalf("child.depth = %d, want 2", child.depth)
	}
	rec := a.taskRecordByTaskID(handle.TaskID)
	if rec == nil {
		t.Fatal("expected task record for child")
	}
	if rec.OwnerAgentID != parent.instanceID || rec.OwnerTaskID != parent.taskID {
		t.Fatalf("task record owner = (%q,%q), want (%q,%q)", rec.OwnerAgentID, rec.OwnerTaskID, parent.instanceID, parent.taskID)
	}
	if !rec.JoinToOwner {
		t.Fatal("expected child task record to default to join-to-owner for SubAgent caller")
	}
}

func TestCreateSubAgentReturnsChildLimitReachedForDirectOwner(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 1, MaxDepth: 2}

	first, err := a.CreateSubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID), "child one", "worker", "", "", tools.WriteScope{})
	if err != nil {
		t.Fatalf("CreateSubAgent(first): %v", err)
	}
	if first.Status != "started" {
		t.Fatalf("first.Status = %q, want started", first.Status)
	}
	second, err := a.CreateSubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID), "child two", "worker", "", "", tools.WriteScope{})
	if err != nil {
		t.Fatalf("CreateSubAgent(second): %v", err)
	}
	if second.Status != "child_limit_reached" {
		t.Fatalf("second.Status = %q, want child_limit_reached", second.Status)
	}
}

func TestCreateSubAgentIgnoresNonActiveDirectChildrenForLimit(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 1, MaxDepth: 2}

	first, err := a.CreateSubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID), "child one", "worker", "", "", tools.WriteScope{})
	if err != nil {
		t.Fatalf("CreateSubAgent(first): %v", err)
	}
	child := a.subAgentByTaskID(first.TaskID)
	if child == nil {
		t.Fatal("expected first child SubAgent to exist")
	}
	child.setState(SubAgentStateWaitingPrimary, "need decision")
	a.noteSubAgentStateTransition(child, SubAgentStateWaitingPrimary)
	a.syncTaskRecordFromSub(child, "")

	second, err := a.CreateSubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID), "child two", "worker", "", "", tools.WriteScope{})
	if err != nil {
		t.Fatalf("CreateSubAgent(second): %v", err)
	}
	if second.Status != "started" {
		t.Fatalf("second.Status = %q, want started", second.Status)
	}
}

func TestCreateSubAgentCapsActiveChildrenAtTen(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 20, MaxDepth: 2}

	ctx := tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID)
	for i := 0; i < 10; i++ {
		handle, err := a.CreateSubAgent(ctx, "child work", "worker", "", "", tools.WriteScope{})
		if err != nil {
			t.Fatalf("CreateSubAgent(%d): %v", i, err)
		}
		if handle.Status != "started" {
			t.Fatalf("handle.Status[%d] = %q, want started", i, handle.Status)
		}
	}

	overflow, err := a.CreateSubAgent(ctx, "child overflow", "worker", "", "", tools.WriteScope{})
	if err != nil {
		t.Fatalf("CreateSubAgent(overflow): %v", err)
	}
	if overflow.Status != "child_limit_reached" {
		t.Fatalf("overflow.Status = %q, want child_limit_reached", overflow.Status)
	}
}

func TestDirectOwnerOnlyControlAppliesToLiveChildAndCompletedRehydrate(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 2, MaxDepth: 2}

	handle, err := a.CreateSubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID), "child work", "worker", "", "", tools.WriteScope{})
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
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 2, MaxDepth: 2}

	handle, err := a.CreateSubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID), "child work", "worker", "", "", tools.WriteScope{})
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
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 3)

	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.instanceID = "worker-parent"
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 2, MaxDepth: 3}
	a.mu.Lock()
	delete(a.subAgents, "worker-1")
	a.subAgents[parent.instanceID] = parent
	a.mu.Unlock()
	a.syncTaskRecordFromSub(parent, "")

	child := newControllableTestSubAgent(t, a, "adhoc-child")
	child.instanceID = "worker-child"
	child.ownerAgentID = parent.instanceID
	child.ownerTaskID = parent.taskID
	child.depth = 2
	child.joinToOwner = true
	a.mu.Lock()
	delete(a.subAgents, "worker-1")
	a.subAgents[child.instanceID] = child
	a.mu.Unlock()
	a.syncTaskRecordFromSub(child, "")

	grand := newControllableTestSubAgent(t, a, "adhoc-grand")
	grand.instanceID = "worker-grand"
	grand.ownerAgentID = child.instanceID
	grand.ownerTaskID = child.taskID
	grand.depth = 3
	grand.joinToOwner = true
	a.mu.Lock()
	delete(a.subAgents, "worker-1")
	a.subAgents[grand.instanceID] = grand
	a.mu.Unlock()
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

func TestOwnedCompletedMailboxReactivatesWaitingDescendantParent(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.instanceID = "worker-parent"
	parent.setState(SubAgentStateWaitingDescendant, "waiting for child")
	parent.setPendingCompleteIntent("final summary")
	a.mu.Lock()
	delete(a.subAgents, "worker-1")
	a.subAgents[parent.instanceID] = parent
	a.mu.Unlock()
	a.syncTaskRecordFromSub(parent, "")

	child := newControllableTestSubAgent(t, a, "adhoc-child")
	child.instanceID = "worker-child"
	child.ownerAgentID = parent.instanceID
	child.ownerTaskID = parent.taskID
	child.depth = 2
	child.joinToOwner = true
	child.setState(SubAgentStateCompleted, "child done")
	child.semHeld = true
	a.sem <- struct{}{}
	a.mu.Lock()
	delete(a.subAgents, "worker-1")
	a.subAgents[child.instanceID] = child
	a.mu.Unlock()
	a.syncTaskRecordFromSub(child, "")

	ok := a.routeOwnedSubAgentMailbox(SubAgentMailboxMessage{
		AgentID:      child.instanceID,
		TaskID:       child.taskID,
		OwnerAgentID: parent.instanceID,
		OwnerTaskID:  parent.taskID,
		Kind:         SubAgentMailboxKindCompleted,
		Summary:      "child done",
		Payload:      "child done",
	})
	if !ok {
		t.Fatal("routeOwnedSubAgentMailbox() = false, want true")
	}
	if parent.State() != SubAgentStateRunning {
		t.Fatalf("parent.State() = %q, want running", parent.State())
	}
	if !parent.semHeld {
		t.Fatal("expected parent to hold transferred slot after child completion")
	}
	if child.semHeld {
		t.Fatal("expected child slot to be transferred away on final joined completion")
	}
	intent, _ := parent.PendingCompleteIntent()
	if intent {
		t.Fatal("expected pending complete intent to be cleared after reactivation")
	}
	select {
	case msg := <-parent.inputCh:
		text := pendingUserMessageText(msg)
		if !strings.Contains(text, "Parent pending completion intent:") || !strings.Contains(text, "child done") {
			t.Fatalf("parent queued message = %q, want pending summary and child completion details", text)
		}
	default:
		t.Fatal("expected parent to receive a resumed child-completion message")
	}
}

func TestWaitingDescendantDirectOwnerResumeReacquiresSemaphore(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 2, MaxDepth: 2}
	parent.setState(SubAgentStateWaitingDescendant, "waiting for child")
	a.releaseSubAgentSlot(parent)

	handle, err := a.NotifySubAgent(context.Background(), parent.taskID, "resume after child event", "follow_up")
	if err != nil {
		t.Fatalf("NotifySubAgent: %v", err)
	}
	if handle.Status != "resumed" {
		t.Fatalf("handle.Status = %q, want resumed", handle.Status)
	}
	if !parent.semHeld {
		t.Fatal("expected waiting_descendant owner to reacquire semaphore slot on manual resume")
	}
}

func TestOwnerRoutedMailboxIsAckedConsumed(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.instanceID = "worker-parent"
	a.mu.Lock()
	delete(a.subAgents, "worker-1")
	a.subAgents[parent.instanceID] = parent
	a.mu.Unlock()
	a.syncTaskRecordFromSub(parent, "")

	childMsg := SubAgentMailboxMessage{
		MessageID:    "worker-child-1",
		AgentID:      "worker-child",
		TaskID:       "adhoc-child",
		OwnerAgentID: parent.instanceID,
		OwnerTaskID:  parent.taskID,
		Kind:         SubAgentMailboxKindProgress,
		Priority:     SubAgentMailboxPriorityNotify,
		Summary:      "child progress",
	}
	a.enqueueSubAgentMailbox(childMsg)

	select {
	case msg := <-parent.ctxAppendCh:
		parent.appendContextOnly(msg)
	default:
		t.Fatal("expected owner-routed mailbox to enqueue a context append for direct parent")
	}

	msgs, err := loadSubAgentMailboxMessages(a.sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxMessages: %v", err)
	}
	if len(msgs) == 0 || msgs[len(msgs)-1].MessageID != "worker-child-1" {
		t.Fatalf("mailbox messages = %#v, want worker-child-1 persisted", msgs)
	}
	acks, err := loadSubAgentMailboxAcks(a.sessionDir)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err != nil {
			t.Fatalf("loadSubAgentMailboxAcks: %v", err)
		}
		if ack, ok := acks["worker-child-1"]; ok && ack.Outcome == "consumed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ack = %#v, want consumed ack for owner-routed mailbox", acks["worker-child-1"])
		}
		time.Sleep(10 * time.Millisecond)
		acks, err = loadSubAgentMailboxAcks(a.sessionDir)
	}
}

func TestOwnerRoutedWakeBypassesSemaphoreWhenOwnerMustResume(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.instanceID = "worker-parent"
	parent.setState(SubAgentStateWaitingDescendant, "waiting for child")
	a.mu.Lock()
	delete(a.subAgents, "worker-1")
	a.subAgents[parent.instanceID] = parent
	a.mu.Unlock()
	a.syncTaskRecordFromSub(parent, "")
	for i := 0; i < cap(a.sem); i++ {
		a.sem <- struct{}{}
	}

	msg := SubAgentMailboxMessage{
		MessageID:    "worker-child-2",
		AgentID:      "worker-child",
		TaskID:       "adhoc-child",
		OwnerAgentID: parent.instanceID,
		OwnerTaskID:  parent.taskID,
		Kind:         SubAgentMailboxKindDecisionRequired,
		Priority:     SubAgentMailboxPriorityInterrupt,
		Summary:      "need owner decision",
	}
	a.enqueueSubAgentMailbox(msg)

	if got := len(a.subAgentInbox.urgent) + len(a.subAgentInbox.normal) + len(a.subAgentInbox.progress); got != 0 {
		t.Fatalf("main inbox unexpectedly received owner-routed mailbox, total=%d", got)
	}
	if parent.State() != SubAgentStateRunning {
		t.Fatalf("parent.State() = %q, want %q", parent.State(), SubAgentStateRunning)
	}
	if !parent.semHeld || !parent.semBypassed {
		t.Fatalf("parent slot flags = held:%v bypassed:%v, want held:true bypassed:true", parent.semHeld, parent.semBypassed)
	}
	if queued := a.ownedSubAgentMailboxes[parent.instanceID]; len(queued) != 0 {
		t.Fatalf("ownedSubAgentMailboxes = %#v, want empty after wake bypass", queued)
	}
	select {
	case pending := <-parent.inputCh:
		if text := pendingUserMessageText(pending); !strings.Contains(text, "need owner decision") {
			t.Fatalf("parent queued message = %q, want owner decision text", text)
		}
	default:
		t.Fatal("expected owner-routed wake to resume parent immediately")
	}
}

func TestCloseSubAgentReleasesWakeBypassWithoutDrainingSemaphore(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.semHeld = true
	parent.semBypassed = true
	parent.instanceID = "worker-parent"
	a.mu.Lock()
	delete(a.subAgents, "worker-1")
	a.subAgents[parent.instanceID] = parent
	a.mu.Unlock()
	for i := 0; i < cap(a.sem); i++ {
		a.sem <- struct{}{}
	}

	a.closeSubAgent(parent.instanceID)

	if got := len(a.sem); got != cap(a.sem) {
		t.Fatalf("len(a.sem) = %d, want %d", got, cap(a.sem))
	}
}

func TestCloseSubAgentRemovesOwnedPendingMailboxState(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.instanceID = "worker-parent"
	a.mu.Lock()
	delete(a.subAgents, "worker-1")
	a.subAgents[parent.instanceID] = parent
	a.mu.Unlock()
	a.syncTaskRecordFromSub(parent, "")

	a.enqueueOwnedSubAgentMailbox(SubAgentMailboxMessage{
		MessageID:    "worker-child-queued",
		AgentID:      "worker-child",
		TaskID:       "adhoc-child",
		OwnerAgentID: parent.instanceID,
		OwnerTaskID:  parent.taskID,
		Kind:         SubAgentMailboxKindDecisionRequired,
		Priority:     SubAgentMailboxPriorityInterrupt,
		Summary:      "need decision",
	})
	if got := len(a.ownedSubAgentMailboxes[parent.instanceID]); got != 1 {
		t.Fatalf("len(ownedSubAgentMailboxes[parent]) = %d, want 1 before close", got)
	}

	a.closeSubAgent(parent.instanceID)

	if got := len(a.ownedSubAgentMailboxes[parent.instanceID]); got != 0 {
		t.Fatalf("len(ownedSubAgentMailboxes[parent]) = %d, want 0 after close", got)
	}
}

func TestOwnedMailboxNotifyDoesNotInflateUrgentCount(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.instanceID = "worker-parent"
	a.mu.Lock()
	delete(a.subAgents, "worker-1")
	a.subAgents[parent.instanceID] = parent
	a.mu.Unlock()
	a.syncTaskRecordFromSub(parent, "")

	a.enqueueOwnedSubAgentMailbox(SubAgentMailboxMessage{
		MessageID:    "worker-child-progress",
		AgentID:      "worker-child",
		TaskID:       "adhoc-child",
		OwnerAgentID: parent.instanceID,
		OwnerTaskID:  parent.taskID,
		Kind:         SubAgentMailboxKindProgress,
		Priority:     SubAgentMailboxPriorityNotify,
		Summary:      "child progress",
	})
	a.refreshSubAgentInboxSummary()
	if got := a.subAgentUrgentInboxCountLocked(parent.instanceID); got != 0 {
		t.Fatalf("subAgentUrgentInboxCountLocked(parent) = %d, want 0 for notify/progress owned mailbox", got)
	}
}

func TestOwnerReactivationSavesFreshSnapshot(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.instanceID = "worker-parent"
	parent.setState(SubAgentStateWaitingDescendant, "waiting for child")
	parent.setPendingCompleteIntent("final summary")
	a.mu.Lock()
	delete(a.subAgents, "worker-1")
	a.subAgents[parent.instanceID] = parent
	a.mu.Unlock()
	a.syncTaskRecordFromSub(parent, "")

	child := newControllableTestSubAgent(t, a, "adhoc-child")
	child.instanceID = "worker-child"
	child.ownerAgentID = parent.instanceID
	child.ownerTaskID = parent.taskID
	child.depth = 2
	child.joinToOwner = true
	child.setState(SubAgentStateCompleted, "child done")
	child.semHeld = true
	a.sem <- struct{}{}
	a.mu.Lock()
	delete(a.subAgents, "worker-1")
	a.subAgents[child.instanceID] = child
	a.mu.Unlock()
	a.syncTaskRecordFromSub(child, "")

	ok := a.routeOwnedSubAgentMailbox(SubAgentMailboxMessage{
		MessageID:    "worker-child-3",
		AgentID:      child.instanceID,
		TaskID:       child.taskID,
		OwnerAgentID: parent.instanceID,
		OwnerTaskID:  parent.taskID,
		Kind:         SubAgentMailboxKindCompleted,
		Summary:      "child done",
		Payload:      "child done",
	})
	if !ok {
		t.Fatal("routeOwnedSubAgentMailbox() = false, want true")
	}

	snap, err := a.recovery.Recover()
	if err != nil {
		t.Fatalf("Recover(): %v", err)
	}
	if snap == nil {
		t.Fatal("expected recovery snapshot")
	}
	found := false
	for _, as := range snap.ActiveAgents {
		if as.InstanceID != parent.instanceID {
			continue
		}
		found = true
		if as.State != string(SubAgentStateRunning) {
			t.Fatalf("snapshot state = %q, want %q", as.State, SubAgentStateRunning)
		}
		break
	}
	if !found {
		t.Fatalf("expected parent agent in snapshot: %#v", snap.ActiveAgents)
	}
}

func TestCallerTaskIDAllowsRehydratedParentToControlChild(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)

	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.instanceID = "worker-parent-1"
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 2, MaxDepth: 2}
	a.mu.Lock()
	delete(a.subAgents, "worker-1")
	a.subAgents[parent.instanceID] = parent
	a.mu.Unlock()
	a.syncTaskRecordFromSub(parent, "")

	child := newControllableTestSubAgent(t, a, "adhoc-child")
	child.instanceID = "worker-child-1"
	child.ownerAgentID = parent.instanceID
	child.ownerTaskID = parent.taskID
	child.depth = 2
	child.joinToOwner = true
	a.mu.Lock()
	delete(a.subAgents, "worker-1")
	a.subAgents[child.instanceID] = child
	a.mu.Unlock()
	a.syncTaskRecordFromSub(child, "")

	parent2 := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent2.instanceID = "worker-parent-2"
	parent2.depth = 1
	parent2.delegation = parent.delegation
	a.mu.Lock()
	delete(a.subAgents, parent.instanceID)
	a.subAgents[parent2.instanceID] = parent2
	a.mu.Unlock()
	a.syncTaskRecordFromSub(parent2, "")

	handle, err := a.NotifySubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent2.instanceID), parent2.taskID), child.taskID, "continue from rehydrated parent", "follow_up")
	if err != nil {
		t.Fatalf("NotifySubAgent: %v", err)
	}
	if handle.TaskID != child.taskID {
		t.Fatalf("handle.TaskID = %q, want %q", handle.TaskID, child.taskID)
	}
}

func TestNotifySubAgentUsesEventLoopWhenStarted(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	startMainAgentLoopForTest(t, a)
	sub := newControllableTestSubAgent(t, a, "adhoc-loop")
	sub.setState(SubAgentStateWaitingPrimary, "need approval")
	a.enqueueSubAgentMailbox(SubAgentMailboxMessage{
		MessageID:   "worker-1-3",
		AgentID:     sub.instanceID,
		TaskID:      sub.taskID,
		Kind:        SubAgentMailboxKindDecisionRequired,
		Priority:    SubAgentMailboxPriorityInterrupt,
		Summary:     "need approval",
		RequiresAck: true,
	})

	handle, err := a.NotifySubAgent(context.Background(), "adhoc-loop", "continue with option D", "reply")
	if err != nil {
		t.Fatalf("NotifySubAgent: %v", err)
	}
	if handle.Status != "resumed" {
		t.Fatalf("handle.Status = %q, want resumed", handle.Status)
	}
	acks, err := loadSubAgentMailboxAcks(a.sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxAcks: %v", err)
	}
	if _, ok := acks["worker-1-3"]; !ok {
		t.Fatal("expected mailbox ack recorded through event-loop path")
	}
}

func TestSendMessageToCompletedWorkerCreatesFollowupWithoutNewTaskID(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-3")
	sub.setState(SubAgentStateCompleted, "finished initial pass")
	sub.setLastMailboxID("worker-1-9")

	handle, err := a.NotifySubAgent(context.Background(), "adhoc-3", "follow up on edge cases", "follow_up")
	if err != nil {
		t.Fatalf("NotifySubAgent: %v", err)
	}
	if handle.AgentID != sub.instanceID {
		t.Fatalf("handle.AgentID = %q, want %q", handle.AgentID, sub.instanceID)
	}
	if handle.TaskID != sub.taskID {
		t.Fatalf("handle.TaskID = %q, want %q", handle.TaskID, sub.taskID)
	}
	if sub.State() != SubAgentStateRunning {
		t.Fatalf("sub.State() = %q, want running", sub.State())
	}
	if !sub.semHeld {
		t.Fatal("expected completed worker follow-up to reacquire semaphore")
	}
}

func TestFocusedCompletedWorkerDirectInputResumesSameWorker(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-7")
	sub.setState(SubAgentStateCompleted, "finished initial pass")
	a.SwitchFocus(sub.instanceID)

	a.SendUserMessage("follow up on edge cases")

	if sub.State() != SubAgentStateRunning {
		t.Fatalf("sub.State() = %q, want running", sub.State())
	}
	if !sub.semHeld {
		t.Fatal("expected focused completed worker direct input to reacquire semaphore")
	}
	select {
	case msg := <-sub.inputCh:
		if got := pendingUserMessageText(msg); got != "[follow_up] follow up on edge cases" {
			t.Fatalf("queued message = %q, want %q", got, "[follow_up] follow up on edge cases")
		}
	default:
		t.Fatal("expected focused completed worker to receive direct follow-up input")
	}
}

func TestCancelSubAgentCancelsPendingToolAndReleasesSlot(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-2")
	sub.ctxMgr.Append(message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{{
			ID:   "call-1",
			Name: "Read",
			Args: []byte(`{"path":"README.md"}`),
		}},
	})
	sub.turn = &Turn{
		ID:              1,
		Ctx:             context.Background(),
		Cancel:          func() {},
		PendingToolMeta: map[string]PendingToolCall{"call-1": {CallID: "call-1", Name: "Read", ArgsJSON: `{"path":"README.md"}`, AgentID: sub.instanceID}},
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

func TestInvalidTaskIDReturnsErrorWithoutImplicitSpawn(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	if _, err := a.NotifySubAgent(context.Background(), "missing-task", "hello", "reply"); err == nil {
		t.Fatal("expected NotifySubAgent to fail for missing task_id")
	}
	if _, err := a.CancelSubAgent(context.Background(), "missing-task", "stop"); err == nil {
		t.Fatal("expected CancelSubAgent to fail for missing task_id")
	}
}

func TestSessionSwitchInvalidatesOldTaskID(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	_ = newControllableTestSubAgent(t, a, "adhoc-4")
	if abandoned := a.abandonSubAgentsForSessionSwitch(); abandoned != 1 {
		t.Fatalf("abandonSubAgentsForSessionSwitch() = %d, want 1", abandoned)
	}
	if _, err := a.NotifySubAgent(context.Background(), "adhoc-4", "continue", "reply"); err == nil {
		t.Fatal("expected old task_id to be invalid after session switch abandonment")
	}
}

func TestCompletedWorkerClosesImmediatelyAndPersistsTaskRecord(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-5")
	a.handleAgentDone(Event{
		SourceID: sub.instanceID,
		Payload:  &AgentResult{Summary: "done"},
	})

	if got := a.subAgentByID(sub.instanceID); got != nil {
		t.Fatal("expected completed worker to close immediately")
	}
	record := a.taskRecordByTaskID(sub.taskID)
	if record == nil {
		t.Fatal("expected durable task record for completed worker")
	}
	if record.State != string(SubAgentStateCompleted) {
		t.Fatalf("record.State = %q, want %q", record.State, SubAgentStateCompleted)
	}
}

func TestSendMessageToCompletedTaskRehydratesClosedWorker(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"restorer": {
			Name:   "restorer",
			Mode:   "subagent",
			Models: []string{"test/test-model"},
		},
	})
	a.SetLLMFactory(func(systemPrompt string, agentModels []string, variant string) *llm.Client {
		return newTestLLMClient()
	})
	sub := newControllableTestSubAgent(t, a, "adhoc-13")
	sub.agentDefName = "restorer"
	sub.taskDesc = "Investigate issue"
	sub.ctxMgr.Append(message.Message{Role: "user", Content: "Investigate issue"})
	if err := a.recovery.PersistMessage(sub.instanceID, message.Message{Role: "user", Content: "Investigate issue"}); err != nil {
		t.Fatalf("PersistMessage(sub): %v", err)
	}
	oldInstanceID := sub.instanceID
	a.handleAgentDone(Event{
		SourceID: sub.instanceID,
		Payload:  &AgentResult{Summary: "done"},
	})

	handle, err := a.NotifySubAgent(context.Background(), "adhoc-13", "follow up on edge cases", "follow_up")
	if err != nil {
		t.Fatalf("NotifySubAgent: %v", err)
	}
	if !handle.Rehydrated {
		t.Fatal("expected rehydrated handle")
	}
	if handle.PreviousAgentID != oldInstanceID {
		t.Fatalf("handle.PreviousAgentID = %q, want %q", handle.PreviousAgentID, oldInstanceID)
	}
	if handle.AgentID == oldInstanceID {
		t.Fatalf("handle.AgentID = %q, want new instance", handle.AgentID)
	}
	restored := a.subAgentByTaskID("adhoc-13")
	if restored == nil {
		t.Fatal("expected rehydrated live worker")
	}
	if restored.instanceID != handle.AgentID {
		t.Fatalf("restored.instanceID = %q, want %q", restored.instanceID, handle.AgentID)
	}
	if restored.State() != SubAgentStateRunning {
		t.Fatalf("restored.State() = %q, want running", restored.State())
	}
	record := a.taskRecordByTaskID("adhoc-13")
	if record == nil {
		t.Fatal("expected durable task record")
	}
	if record.LatestInstanceID != handle.AgentID {
		t.Fatalf("record.LatestInstanceID = %q, want %q", record.LatestInstanceID, handle.AgentID)
	}
	if len(record.InstanceHistory) != 2 {
		t.Fatalf("len(record.InstanceHistory) = %d, want 2", len(record.InstanceHistory))
	}
}

func TestWaitingPrimaryLifecycleExpiresAfterUserTurns(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-6")
	sub.setState(SubAgentStateWaitingPrimary, "need answer")
	a.noteSubAgentStateTransition(sub, SubAgentStateWaitingPrimary)

	for i := uint64(0); i < waitingPrimaryExpiryUserTurns; i++ {
		a.explicitUserTurnCount++
	}
	a.sweepSubAgentLifecycle()

	if got := a.subAgentByID(sub.instanceID); got != nil {
		t.Fatal("expected waiting_primary worker to expire and close")
	}
}
