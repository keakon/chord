package agent

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestManualMainContinueBatchesCompletedMailboxesIntoSingleTurn(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.subAgentInbox.urgent = []SubAgentMailboxMessage{
		{MessageID: "a-1", AgentID: "worker-a", TaskID: "task-a", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent, Summary: "done a"},
		{MessageID: "b-1", AgentID: "worker-b", TaskID: "task-b", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent, Summary: "done b"},
	}

	a.handleContinueFromContext("")
	if a.turn == nil {
		t.Fatal("expected manual continue to start a main turn")
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

func TestManualMainUserMessageStagesMailboxBeforeTurn(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.subAgentInbox.urgent = []SubAgentMailboxMessage{{
		MessageID: "done-1",
		AgentID:   "worker-a",
		TaskID:    "task-a",
		Kind:      SubAgentMailboxKindCompleted,
		Priority:  SubAgentMailboxPriorityUrgent,
		Summary:   "done",
	}}

	a.handleUserMessage(Event{Type: EventUserMessage, Payload: "review the result"})

	if a.turn == nil {
		t.Fatal("manual user message did not start a main turn")
	}
	if got := len(a.pendingSubAgentMailboxes); got != 1 {
		t.Fatalf("pending mailbox batch len = %d, want 1", got)
	}
	if got := len(a.subAgentInbox.urgent); got != 0 {
		t.Fatalf("urgent inbox len = %d, want 0 after manual input", got)
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
	a.newTurn("")
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
	if got := len(a.subAgentInbox.urgent); got != 0 {
		t.Fatalf("len(urgent inbox) = %d, want 0 after staging", got)
	}
}

func TestParkedMainOwnedCompletedMailboxStillQueuesForMain(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-root")
	sub.agentDefName = "worker"
	a.newTurn("")

	a.handleAgentDone(Event{
		Type:     EventAgentDone,
		SourceID: sub.instanceID,
		Payload:  &AgentResult{Summary: "root task done"},
	})

	if got := a.subAgentByID(sub.instanceID); got != nil {
		t.Fatalf("subAgentByID(%q) = %#v, want parked worker", sub.instanceID, got)
	}
	if rec := a.taskRecordByTaskID(sub.taskID); rec == nil || !rec.RuntimeParked {
		t.Fatalf("task record = %#v, want parked durable task", rec)
	}
	foundDone := false
	for len(a.outputCh) > 0 {
		if done, ok := (<-a.outputCh).(AgentDoneEvent); ok {
			if done.AgentID != sub.instanceID || done.TaskID != sub.taskID || done.AgentType != sub.agentDefName || done.ParentAgentID != "main" || done.Summary != "root task done" {
				t.Fatalf("done event = %#v", done)
			}
			foundDone = true
		}
	}
	if !foundDone {
		t.Fatal("expected AgentDoneEvent")
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
	a.newTurn("")

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
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[subA.instanceID] = subA
	a.subs.mu.Unlock()

	subB := newControllableTestSubAgent(t, a, "task-b")
	subB.instanceID = "worker-b"
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[subB.instanceID] = subB
	a.subs.mu.Unlock()

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

// TestMainInboxProgressIsRunnableAndStagesAsOneBatch pins the progress-wake
// gate: while an idle main holds per-agent progress snapshots, they count as
// runnable mailbox work, and a single drain stages every snapshot into one
// batch instead of waking the main once per update.
func TestMainInboxProgressIsRunnableAndStagesAsOneBatch(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.replaceProgressMailboxWithinBudget(SubAgentMailboxMessage{MessageID: "p-1", AgentID: "worker-1", TaskID: "task-a", Kind: SubAgentMailboxKindProgress, Summary: "still working"})
	a.replaceProgressMailboxWithinBudget(SubAgentMailboxMessage{MessageID: "p-2", AgentID: "worker-2", TaskID: "task-b", Kind: SubAgentMailboxKindProgress, Summary: "almost done"})

	if !a.hasRunnableMailboxWork() {
		t.Fatal("hasRunnableMailboxWork() = false while progress snapshots are pending for an idle main")
	}
	if !a.stageNextSubAgentMailboxBatch() {
		t.Fatal("stageNextSubAgentMailboxBatch() = false, want progress staged")
	}
	if len(a.pendingSubAgentMailboxes) != 2 {
		t.Fatalf("pending batch = %d messages, want both snapshots merged into one batch", len(a.pendingSubAgentMailboxes))
	}
	if len(a.subAgentInbox.progress) != 0 {
		t.Fatalf("progress snapshot map = %#v, want it consumed by staging", a.subAgentInbox.progress)
	}
	if a.stageNextSubAgentMailboxBatch() {
		t.Fatal("stageNextSubAgentMailboxBatch() = true with nothing left to stage")
	}
	// Once the staged batch is taken by the request (the turn-overlay consumer)
	// and no new snapshots arrived, the work is gone and the main may go idle.
	a.pendingSubAgentMailboxes = nil
	a.activeSubAgentMailboxes = nil
	a.activeSubAgentMailbox = nil
	if a.hasRunnableMailboxWork() {
		t.Fatal("hasRunnableMailboxWork() = true after the staged batch was consumed")
	}
}

// TestProgressArrivalMergesIntoQueuedMailboxBatch pins the one-cycle merge for
// a real arrival: a progress snapshot and a queued urgent mailbox that drain
// together land in the same staged batch, so the wake handles all routable
// main-inbox messages at once.
func TestProgressArrivalMergesIntoQueuedMailboxBatch(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.replaceProgressMailboxWithinBudget(SubAgentMailboxMessage{MessageID: "p-1", AgentID: "worker-1", TaskID: "task-a", Kind: SubAgentMailboxKindProgress, Summary: "still working"})
	a.subAgentInbox.urgent = []SubAgentMailboxMessage{{
		MessageID: "u-1",
		AgentID:   "worker-2",
		TaskID:    "task-b",
		Kind:      SubAgentMailboxKindDecisionRequired,
		Priority:  SubAgentMailboxPriorityInterrupt,
		Summary:   "needs a decision",
	}}

	if !a.stageNextSubAgentMailboxBatch() {
		t.Fatal("stageNextSubAgentMailboxBatch() = false")
	}
	if len(a.pendingSubAgentMailboxes) != 2 {
		t.Fatalf("pending batch = %d messages, want the urgent mailbox and the progress snapshot merged", len(a.pendingSubAgentMailboxes))
	}
	first := a.pendingSubAgentMailboxes[0]
	if first == nil || first.MessageID != "u-1" {
		t.Fatalf("first pending mailbox = %#v, want the urgent mailbox staged first", first)
	}
}

// TestStageNextSubAgentMailboxBatchDrainsEveryQueuedMessage pins the
// whole-backlog rule: one staging cycle claims every actionable message in the
// inbox — not only a completed run — so one request carries all of them, with
// urgent messages ahead of normal ones and FIFO kept inside each class.
func TestStageNextSubAgentMailboxBatchDrainsEveryQueuedMessage(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.subAgentInbox.urgent = []SubAgentMailboxMessage{
		{MessageID: "u-1", AgentID: "worker-1", TaskID: "task-a", Kind: SubAgentMailboxKindDecisionRequired, Priority: SubAgentMailboxPriorityInterrupt, Summary: "needs a decision"},
		{MessageID: "u-2", AgentID: "worker-2", TaskID: "task-b", Kind: SubAgentMailboxKindRiskAlert, Priority: SubAgentMailboxPriorityUrgent, Summary: "risk spotted"},
	}
	a.subAgentInbox.normal = []SubAgentMailboxMessage{
		{MessageID: "n-1", AgentID: "worker-3", TaskID: "task-c", Kind: SubAgentMailboxKindBackgroundResult, Priority: SubAgentMailboxPriorityNotify, Summary: "job finished"},
		{MessageID: "n-2", AgentID: "worker-4", TaskID: "task-d", Kind: SubAgentMailboxKindBlocked, Priority: SubAgentMailboxPriorityNotify, Summary: "blocked on input"},
	}

	if !a.stageNextSubAgentMailboxBatch() {
		t.Fatal("stageNextSubAgentMailboxBatch() = false with four queued messages")
	}
	got := make([]string, 0, len(a.pendingSubAgentMailboxes))
	for _, msg := range a.pendingSubAgentMailboxes {
		if msg == nil {
			t.Fatalf("pending batch holds a nil entry: %#v", a.pendingSubAgentMailboxes)
		}
		got = append(got, msg.MessageID)
	}
	want := []string{"u-1", "u-2", "n-1", "n-2"}
	if len(got) != len(want) {
		t.Fatalf("pending batch = %v, want all four messages %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pending batch = %v, want %v (urgent first, FIFO within a class)", got, want)
		}
	}
	if len(a.subAgentInbox.urgent) != 0 || len(a.subAgentInbox.normal) != 0 {
		t.Fatalf("queues still hold messages after staging: urgent=%v normal=%v", a.subAgentInbox.urgent, a.subAgentInbox.normal)
	}
	if len(a.activeSubAgentMailboxes) != len(want) {
		t.Fatalf("active batch = %v, want the same four messages retained for closeout", a.activeSubAgentMailboxes)
	}
	if a.activeSubAgentMailbox == nil || a.activeSubAgentMailbox.MessageID != "u-1" {
		t.Fatalf("activeSubAgentMailbox = %#v, want the oldest queue head u-1", a.activeSubAgentMailbox)
	}

	// The single request assembling this batch must carry every message, not
	// just the first one it used to stop at. A background result is delivered
	// as a KindBackgroundResult message, so it is counted by content instead of
	// by the mailbox-notice helper.
	overlays := a.buildTurnOverlayMessages()
	for _, id := range []string{"u-1", "u-2", "n-2"} {
		if got := countSubAgentMailboxMessages(overlays, id); got != 1 {
			t.Fatalf("request overlays = %#v, want exactly one %s message", overlays, id)
		}
	}
	if !requestHasBackgroundResult(overlays, "job finished") {
		t.Fatalf("request overlays = %#v, want the background result n-1", overlays)
	}
	// A second staging cycle finds nothing left, so the main can go idle once
	// the request consumed the batch.
	a.pendingSubAgentMailboxes = nil
	a.activeSubAgentMailboxes = nil
	a.activeSubAgentMailbox = nil
	if a.stageNextSubAgentMailboxBatch() {
		t.Fatal("stageNextSubAgentMailboxBatch() = true with the queues drained")
	}
}

// TestIdleMailboxDrainLeavesActiveTurnAndProgressUntouched pins the idle-wake
// gate: the between-turns drain (drainSubAgentInbox) never fires while a turn is
// in flight. A progress snapshot that arrives mid-turn is delivered by the next
// request boundary instead (TestTurnContinuationStagingIncludesPendingProgress),
// so the idle drain must leave both the snapshot and the running turn alone.
func TestIdleMailboxDrainLeavesActiveTurnAndProgressUntouched(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.replaceProgressMailboxWithinBudget(SubAgentMailboxMessage{MessageID: "p-1", AgentID: "worker-1", TaskID: "task-a", Kind: SubAgentMailboxKindProgress, Summary: "update"})
	a.newTurn("")
	if a.currentTurn() == nil {
		t.Fatal("active turn was not created")
	}

	a.drainSubAgentInbox()

	if len(a.pendingSubAgentMailboxes) != 0 || len(a.activeSubAgentMailboxes) != 0 {
		t.Fatalf("idle drain staged mailbox work with an active turn: pending=%v active=%v", a.pendingSubAgentMailboxes, a.activeSubAgentMailboxes)
	}
	if got := a.subAgentInbox.progressQueue; len(got) != 1 || got[0].MessageID != "p-1" {
		t.Fatalf("progressQueue = %#v, want the snapshot left queued by an active-turn drain", got)
	}
	if a.currentTurn() == nil {
		t.Fatal("active turn was replaced by the drain")
	}
}

// TestTurnContinuationStagingIncludesPendingProgress pins the mid-turn
// delivery rule: an active turn's next-request staging claims both the
// actionable mailbox head and the pending progress snapshot into one batch,
// with the actionable head staged first, so progress/notice rides the next
// request instead of waiting for the turn to end.
func TestTurnContinuationStagingIncludesPendingProgress(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.replaceProgressMailboxWithinBudget(SubAgentMailboxMessage{MessageID: "p-1", AgentID: "worker-1", TaskID: "task-a", Kind: SubAgentMailboxKindProgress, Summary: "still working"})
	a.subAgentInbox.urgent = []SubAgentMailboxMessage{{
		MessageID: "u-1",
		AgentID:   "worker-2",
		TaskID:    "task-b",
		Kind:      SubAgentMailboxKindDecisionRequired,
		Priority:  SubAgentMailboxPriorityInterrupt,
		Summary:   "needs a decision",
	}}
	a.newTurn("")
	if a.currentTurn() == nil {
		t.Fatal("active turn was not created")
	}

	if !a.prepareSubAgentMailboxBatchForTurnContinuation() {
		t.Fatal("turn-continuation staging returned false with pending mailbox work")
	}
	if len(a.pendingSubAgentMailboxes) != 2 {
		t.Fatalf("pending batch = %#v, want the actionable head and the progress snapshot", a.pendingSubAgentMailboxes)
	}
	if first := a.pendingSubAgentMailboxes[0]; first == nil || first.MessageID != "u-1" {
		t.Fatalf("first pending mailbox = %#v, want the actionable head staged first", first)
	}
	if second := a.pendingSubAgentMailboxes[1]; second == nil || second.MessageID != "p-1" {
		t.Fatalf("second pending mailbox = %#v, want the progress snapshot staged mid-turn", second)
	}
	if len(a.subAgentInbox.progressQueue) != 0 || len(a.subAgentInbox.progressPending) != 0 {
		t.Fatalf("progress snapshot still queued after staging: queue=%#v pending=%#v", a.subAgentInbox.progressQueue, a.subAgentInbox.progressPending)
	}
}

// TestStageNextCompletedBatchFoldsQueueResidentProgress pins the completed-head
// shape under the mid-turn delivery rule: a stray queue-resident progress
// update (progress normally lives in the per-agent snapshot map, not the
// actionable queues) folds into the same staged batch as the completed head
// instead of being requeued, so it rides the next request too.
func TestStageNextCompletedBatchFoldsQueueResidentProgress(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.subAgentInbox.urgent = []SubAgentMailboxMessage{{
		MessageID: "c-1",
		AgentID:   "worker-1",
		TaskID:    "task-a",
		Kind:      SubAgentMailboxKindCompleted,
		Priority:  SubAgentMailboxPriorityUrgent,
		Summary:   "child finished",
	}}
	a.subAgentInbox.normal = []SubAgentMailboxMessage{{
		MessageID: "p-1",
		AgentID:   "worker-2",
		TaskID:    "task-b",
		Kind:      SubAgentMailboxKindProgress,
		Summary:   "still working",
	}}

	if !a.stageNextSubAgentMailboxBatch() {
		t.Fatal("stageNextSubAgentMailboxBatch() = false, want the completed head staged")
	}
	if len(a.pendingSubAgentMailboxes) != 2 {
		t.Fatalf("pending batch = %#v, want the completed head and the folded progress update", a.pendingSubAgentMailboxes)
	}
	if a.pendingSubAgentMailboxes[0] == nil || a.pendingSubAgentMailboxes[0].MessageID != "c-1" {
		t.Fatalf("first pending mailbox = %#v, want the completed head staged first", a.pendingSubAgentMailboxes[0])
	}
	if a.pendingSubAgentMailboxes[1] == nil || a.pendingSubAgentMailboxes[1].MessageID != "p-1" {
		t.Fatalf("second pending mailbox = %#v, want the queue-resident progress folded in", a.pendingSubAgentMailboxes[1])
	}
	if len(a.subAgentInbox.urgent)+len(a.subAgentInbox.normal) != 0 {
		t.Fatalf("queue still holds messages after staging: urgent=%d normal=%d", len(a.subAgentInbox.urgent), len(a.subAgentInbox.normal))
	}
}

// TestConcurrentMailboxQueueDeliveryKeepsStateConsistent hammers the shared
// main-inbox queue from the two goroutine roles that race in production — the
// event-loop delivery/drain side and the TUI-facing manual-delivery side that
// claims messages for a worker — and checks that the queue contents and the
// memory-byte counter stay consistent. Run under -race this pins the
// subAgentMailboxIDsMu discipline across store/dequeue/takeOutstanding;
// without it the counter and slice state can tear.
func TestConcurrentMailboxQueueDeliveryKeepsStateConsistent(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "race-task")

	const messagesPerProducer = 300
	const producerAgents = 3

	var stored, removed atomic.Int64
	var wg sync.WaitGroup
	enqueue := func(group int) {
		defer wg.Done()
		agentID := fmt.Sprintf("other-%d", group)
		if group == 0 {
			// This producer feeds the claim side: takeOutstandingMailboxForSub
			// matches the worker's instance ID the way a manual follow-up does.
			agentID = sub.instanceID
		}
		for i := range messagesPerProducer {
			msg := SubAgentMailboxMessage{
				MessageID: fmt.Sprintf("%s-%d", agentID, group*messagesPerProducer+i),
				AgentID:   agentID,
				TaskID:    "race-task",
				Kind:      SubAgentMailboxKindDecisionRequired,
				Priority:  SubAgentMailboxPriorityInterrupt,
				Summary:   "update",
			}
			if a.storeMailboxInMemory(msg, false) {
				stored.Add(1)
			}
		}
	}
	for group := range producerAgents {
		wg.Add(1)
		go enqueue(group)
	}
	wg.Go(func() {
		for range messagesPerProducer * producerAgents {
			if msg := a.takeOutstandingMailboxForSub(sub); msg != nil {
				removed.Add(1)
			}
		}
	})
	wg.Go(func() {
		for range messagesPerProducer * producerAgents {
			if msg := a.dequeueNextSubAgentMailbox(); msg != nil {
				removed.Add(1)
			}
		}
	})
	wg.Wait()

	a.subAgentMailboxIDsMu.Lock()
	remaining := len(a.subAgentInbox.urgent) + len(a.subAgentInbox.normal)
	var wantBytes int
	for _, msg := range a.subAgentInbox.urgent {
		wantBytes += mailboxMessageBytes(msg)
	}
	for _, msg := range a.subAgentInbox.normal {
		wantBytes += mailboxMessageBytes(msg)
	}
	gotBytes := a.subAgentInbox.memoryBytes
	a.subAgentMailboxIDsMu.Unlock()
	if got := stored.Load() - removed.Load(); got != int64(remaining) {
		t.Fatalf("queue accounting drifted: stored=%d removed=%d remaining=%d, want %d", stored.Load(), removed.Load(), remaining, got)
	}
	if gotBytes != wantBytes {
		t.Fatalf("mailbox memory counter = %d bytes, want %d (queue held %d messages)", gotBytes, wantBytes, remaining)
	}
}

// TestRequeueActiveSubAgentMailboxSkipsClaimedMessages pins the P3-1 fix: the
// active-batch requeue runs in one critical section over the live batch, so a
// message already claimed by the manual-delivery path
// (takeOutstandingMailboxForSub) is not reinserted into the queue from a stale
// snapshot. Reinserting it would deliver the same message twice — once to the
// main turn whose teardown is requeueing it, and once more after the claiming
// manual delivery replies.
func TestRequeueActiveSubAgentMailboxSkipsClaimedMessages(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "task-1") // instance ID worker-1
	m1 := &SubAgentMailboxMessage{MessageID: "m1", AgentID: "worker-1", TaskID: "task-1", Kind: SubAgentMailboxKindDecisionRequired, Priority: SubAgentMailboxPriorityInterrupt, Summary: "one"}
	m2 := &SubAgentMailboxMessage{MessageID: "m2", AgentID: "worker-2", TaskID: "task-2", Kind: SubAgentMailboxKindDecisionRequired, Priority: SubAgentMailboxPriorityInterrupt, Summary: "two"}
	a.activeSubAgentMailboxes = []*SubAgentMailboxMessage{m1, m2}
	a.activeSubAgentMailbox = m1
	a.pendingSubAgentMailboxes = []*SubAgentMailboxMessage{m1, m2}
	a.activeSubAgentMailboxAck = false

	// The manual-delivery path claims m1 for the worker's follow-up reply.
	if got := a.takeOutstandingMailboxForSub(sub); got != m1 {
		t.Fatalf("takeOutstandingMailboxForSub() = %#v, want m1", got)
	}

	a.requeueActiveSubAgentMailbox()

	queued := append([]SubAgentMailboxMessage{}, a.subAgentInbox.urgent...)
	queued = append(queued, a.subAgentInbox.normal...)
	if len(queued) != 1 || queued[0].MessageID != "m2" {
		t.Fatalf("requeued mailbox queue = %#v, want only m2 (m1 was claimed)", queued)
	}
	if len(a.subAgentInbox.progress) != 0 {
		t.Fatalf("claimed m1 was requeued into the progress map: %#v", a.subAgentInbox.progress)
	}
	if got, want := a.subAgentInbox.memoryBytes, mailboxMessageBytes(*m2); got != want {
		t.Fatalf("mailbox memory counter = %d, want %d (only m2 requeued)", got, want)
	}
}

// TestTurnContinuationStagesQueueResidentProgressHead pins that a progress
// queue head is delivered mid-turn: the turn-continuation staging claims it into
// the batch so it rides the next request, instead of returning it to the
// per-agent snapshot map.
func TestTurnContinuationStagesQueueResidentProgressHead(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.subAgentInbox.urgent = []SubAgentMailboxMessage{{
		MessageID: "p-1",
		AgentID:   "worker-1",
		TaskID:    "task-a",
		Kind:      SubAgentMailboxKindProgress,
		Summary:   "still working",
	}}
	a.newTurn("")
	if a.currentTurn() == nil {
		t.Fatal("active turn was not created")
	}

	if !a.prepareSubAgentMailboxBatchForTurnContinuation() {
		t.Fatal("mid-turn staging did not deliver a progress queue head")
	}
	if len(a.pendingSubAgentMailboxes) != 1 || a.pendingSubAgentMailboxes[0] == nil || a.pendingSubAgentMailboxes[0].MessageID != "p-1" {
		t.Fatalf("pending batch = %#v, want the progress queue head staged", a.pendingSubAgentMailboxes)
	}
	if len(a.activeSubAgentMailboxes) != 1 || a.activeSubAgentMailboxes[0] == nil || a.activeSubAgentMailboxes[0].MessageID != "p-1" {
		t.Fatalf("active batch = %#v, want the progress head recorded for closeout", a.activeSubAgentMailboxes)
	}
	if a.activeSubAgentMailbox != nil {
		t.Fatalf("activeSubAgentMailbox = %#v, want nil for a progress-only batch", a.activeSubAgentMailbox)
	}
	if len(a.subAgentInbox.urgent) != 0 {
		t.Fatalf("progress queue head not consumed from the queue: %#v", a.subAgentInbox.urgent)
	}
}

// TestTurnContinuationStagesLaterBatchAfterFirstConsumed pins that a turn
// delivers several mailbox batches: once the request consumed the first staged
// batch, a mailbox that arrives later is staged again and assembled into the
// next request's messages and ctxmgr, instead of being blocked by the active
// batch bookkeeping from the earlier delivery.
func TestTurnContinuationStagesLaterBatchAfterFirstConsumed(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn("")

	a.subAgentInbox.urgent = []SubAgentMailboxMessage{{
		MessageID: "c-1",
		AgentID:   "worker-1",
		TaskID:    "task-a",
		Kind:      SubAgentMailboxKindCompleted,
		Priority:  SubAgentMailboxPriorityUrgent,
		Summary:   "first child finished",
	}}
	if !a.prepareSubAgentMailboxBatchForTurnContinuation() {
		t.Fatal("first batch was not staged")
	}
	first := a.buildTurnOverlayMessages()
	if got := countSubAgentMailboxMessages(first, "c-1"); got != 1 {
		t.Fatalf("first request overlays = %#v, want exactly the c-1 mailbox", first)
	}

	// The request took the pending batch, so the active bookkeeping is all that
	// remains of it; a later arrival must still be staged for the next request.
	a.subAgentInbox.urgent = []SubAgentMailboxMessage{{
		MessageID: "c-2",
		AgentID:   "worker-2",
		TaskID:    "task-b",
		Kind:      SubAgentMailboxKindCompleted,
		Priority:  SubAgentMailboxPriorityUrgent,
		Summary:   "second child finished",
	}}
	if !a.prepareSubAgentMailboxBatchForTurnContinuation() {
		t.Fatal("a later mailbox was blocked by the active batch from an earlier request")
	}
	second := a.buildTurnOverlayMessages()
	if got := countSubAgentMailboxMessages(second, "c-2"); got != 1 {
		t.Fatalf("second request overlays = %#v, want exactly the c-2 mailbox", second)
	}

	ctx := a.ctxMgr.Snapshot()
	if got := countSubAgentMailboxMessages(ctx, "c-1"); got != 1 {
		t.Fatalf("ctxmgr c-1 copies = %d, want 1", got)
	}
	if got := countSubAgentMailboxMessages(ctx, "c-2"); got != 1 {
		t.Fatalf("ctxmgr c-2 copies = %d, want 1", got)
	}
	if len(a.activeSubAgentMailboxes) != 2 {
		t.Fatalf("active batch = %#v, want both delivered batches retained until closeout", a.activeSubAgentMailboxes)
	}
}

// TestTurnContinuationStagesProgressSnapshotForNextRequest pins that a progress
// snapshot arriving mid-turn is assembled into the next request's messages and
// ctxmgr, without interrupting the in-flight request or starting a new turn.
func TestTurnContinuationStagesProgressSnapshotForNextRequest(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn("")
	turnID := a.currentTurn().ID

	a.replaceProgressMailboxWithinBudget(SubAgentMailboxMessage{MessageID: "p-1", AgentID: "worker-1", TaskID: "task-a", Kind: SubAgentMailboxKindProgress, Summary: "still working"})
	if !a.prepareSubAgentMailboxBatchForTurnContinuation() {
		t.Fatal("mid-turn staging did not claim the progress snapshot")
	}
	overlays := a.buildTurnOverlayMessages()
	if got := countSubAgentMailboxMessages(overlays, "p-1"); got != 1 {
		t.Fatalf("request overlays = %#v, want the progress snapshot p-1", overlays)
	}
	if got := countSubAgentMailboxMessages(a.ctxMgr.Snapshot(), "p-1"); got != 1 {
		t.Fatalf("ctxmgr p-1 copies = %d, want 1", got)
	}
	if turn := a.currentTurn(); turn == nil || turn.ID != turnID {
		t.Fatalf("turn = %#v, want the same in-flight turn %d (no interrupt, no new turn)", turn, turnID)
	}
}

// TestTurnCloseoutAcksEveryBatchStagedInTurn pins the teardown side of
// multi-batch delivery: when a turn delivered two batches on two requests, the
// closeout acks both with the turn's reply, leaving neither unconsumed (lost)
// nor acked twice.
func TestTurnCloseoutAcksEveryBatchStagedInTurn(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn("")

	stage := func(id string) {
		a.subAgentInbox.urgent = []SubAgentMailboxMessage{{
			MessageID: id,
			AgentID:   "worker-" + id,
			TaskID:    "task-" + id,
			Kind:      SubAgentMailboxKindCompleted,
			Priority:  SubAgentMailboxPriorityUrgent,
			Summary:   id,
		}}
		if !a.prepareSubAgentMailboxBatchForTurnContinuation() {
			t.Fatalf("batch %s was not staged", id)
		}
		if msgs := a.takePendingSubAgentMailboxes(); len(msgs) != 1 || msgs[0].MessageID != id {
			t.Fatalf("pending batch for %s = %#v, want exactly %s", id, msgs, id)
		}
	}
	stage("c-1")
	stage("c-2")
	if len(a.activeSubAgentMailboxes) != 2 {
		t.Fatalf("active batch = %#v, want both staged batches retained", a.activeSubAgentMailboxes)
	}

	a.setIdleAndDrainPending()

	for _, id := range []string{"c-1", "c-2"} {
		if !a.isSubAgentMailboxConsumed(id) {
			t.Fatalf("%s was not acked at turn closeout", id)
		}
	}
	if len(a.activeSubAgentMailboxes) != 0 || a.activeSubAgentMailbox != nil || len(a.pendingSubAgentMailboxes) != 0 {
		t.Fatalf("staged batch survived closeout: pending=%v active=%v", a.pendingSubAgentMailboxes, a.activeSubAgentMailboxes)
	}
}
