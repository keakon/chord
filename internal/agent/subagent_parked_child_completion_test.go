package agent

import (
	"testing"

	"github.com/keakon/chord/internal/tools"
)

// TestParkedChildCompletionWhileOwnerWaitingMainStaysInOwnedQueue pins the
// P1-6 defect scenario at the mailbox-routing level: an owner subagent parks in
// waiting_main (production escalate path, exactly as the P1-5 sweep tests do)
// while one of its children is still live, and that child then completes. The
// child's completion mailbox must stay spooled in the owner's owned queue: it
// is not routed to the parked owner (only waiting_descendant owners are
// rehydratable by a descendant mailbox, task_registry.go), not forwarded to the
// main inbox (the owner record is non-terminal), and never delivered.
//
// This is a defect-characterization test: under the current code every
// assertion below is expected to PASS. When the P1-6 fix lands (letting a
// descendant completion wake a parked idle/waiting_main owner, or forwarding
// the message once it cannot be routed), the staleness assertions here must be
// flipped to assert the fixed behavior instead.
func TestParkedChildCompletionWhileOwnerWaitingMainStaysInOwnedQueue(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())

	owner := newControllableTestSubAgent(t, a, "adhoc-parked-owner")
	owner.instanceID = "worker-parked-owner"
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[owner.instanceID] = owner
	a.subs.mu.Unlock()
	a.syncTaskRecordFromSub(owner, "")

	child := newControllableTestSubAgent(t, a, "adhoc-parked-child")
	child.instanceID = "worker-parked-child"
	child.ownerAgentID = owner.instanceID
	child.ownerTaskID = owner.taskID
	child.depth = 2
	child.joinToOwner = true
	child.setState(SubAgentStateRunning, "working on the child task")
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[child.instanceID] = child
	a.subs.mu.Unlock()
	a.syncTaskRecordFromSub(child, "")

	childRecord := a.taskRecordByTaskID(child.taskID)
	if childRecord == nil || childRecord.OwnerAgentID != owner.instanceID || childRecord.OwnerTaskID != owner.taskID {
		t.Fatalf("child task record = %#v, want it owned by the running parent", childRecord)
	}

	// Reachability leg 1: the owner escalates through the production path and is
	// parked in waiting_main while its child is still live and running.
	a.handleEscalate(Event{
		Type:     EventEscalate,
		SourceID: owner.instanceID,
		Payload:  tools.AgentRequestPayload{Reason: "awaiting a main decision before the child result can be integrated"},
	})

	rec := a.taskRecordByTaskID(owner.taskID)
	if rec == nil || SubAgentState(rec.State) != SubAgentStateWaitingMain || !rec.RuntimeParked {
		t.Fatalf("owner task record after escalate = %#v, want parked waiting_main", rec)
	}
	if live := a.subAgentByTaskID(owner.taskID); live != nil {
		t.Fatal("owner must be parked (no live runtime) while waiting for the main decision")
	}
	if live := a.subAgentByID(child.instanceID); live == nil || live.State() != SubAgentStateRunning {
		t.Fatalf("child = %#v, want it to stay live and running while its owner is parked", live)
	}

	// Reachability leg 2: the child completes while the owner is still parked.
	// The completion travels through the production mailbox-event path.
	completedID := "mailbox-parked-owner-1"
	a.handleSubAgentMailboxEvent(Event{
		Type:     EventSubAgentMailbox,
		SourceID: child.instanceID,
		Payload: &SubAgentMailboxMessage{
			MessageID:    completedID,
			AgentID:      child.instanceID,
			TaskID:       child.taskID,
			OwnerAgentID: owner.instanceID,
			OwnerTaskID:  owner.taskID,
			Kind:         SubAgentMailboxKindCompleted,
			Priority:     SubAgentMailboxPriorityUrgent,
			Summary:      "child task finished",
			Payload:      "child task finished",
		},
	})

	queued := a.ownedSubAgentMailboxes[owner.instanceID]
	if len(queued) != 1 || queued[0].MessageID != completedID || queued[0].Kind != SubAgentMailboxKindCompleted {
		t.Fatalf("owned completion queue = %#v, want the child completion spooled under the parked owner", queued)
	}

	// Not delivered: routing must not rehydrate the waiting_main owner, and the
	// parked owner record must stay untouched.
	if live := a.subAgentByTaskID(owner.taskID); live != nil {
		t.Fatal("the stuck child completion rehydrated the parked waiting_main owner")
	}
	rec = a.taskRecordByTaskID(owner.taskID)
	if rec == nil || SubAgentState(rec.State) != SubAgentStateWaitingMain || !rec.RuntimeParked {
		t.Fatalf("owner task record after delivery attempt = %#v, want still parked waiting_main", rec)
	}

	// Not forwarded to main: a non-terminal owner record keeps the message out
	// of the main inbox and must not start a main turn.
	for _, msg := range append(append([]SubAgentMailboxMessage{}, a.subAgentInbox.urgent...), a.subAgentInbox.normal...) {
		if msg.MessageID == completedID {
			t.Fatalf("child completion was forwarded to the main inbox: %#v", msg)
		}
	}
	if len(a.subAgentInbox.spoolUrgent)+len(a.subAgentInbox.spoolNormal) != 0 {
		t.Fatalf("main inbox spool = urgent:%v normal:%v, want empty", a.subAgentInbox.spoolUrgent, a.subAgentInbox.spoolNormal)
	}
	if len(a.pendingSubAgentMailboxes) != 0 || len(a.activeSubAgentMailboxes) != 0 || a.activeSubAgentMailbox != nil {
		t.Fatalf("main mailbox staging not empty: pending=%v active=%v current=%#v", a.pendingSubAgentMailboxes, a.activeSubAgentMailboxes, a.activeSubAgentMailbox)
	}
	if a.currentTurn() != nil {
		t.Fatal("the stuck child completion must not start a main turn")
	}

	// Every loop-level drain attempt re-runs routing and keeps refusing, which
	// is exactly what turns the stale mailbox into permanent queued work.
	if a.drainOwnedSubAgentMailboxes(owner.instanceID) {
		t.Fatal("drain reported progress for a completion that was not routable")
	}
	queued = a.ownedSubAgentMailboxes[owner.instanceID]
	if len(queued) != 1 || queued[0].MessageID != completedID {
		t.Fatalf("owned completion queue after drain = %#v, want the message still queued", queued)
	}
	if !a.hasRunnableMailboxWork() {
		t.Fatal("hasRunnableMailboxWork() = false with an unroutable owned completion queued")
	}

	// The durable gate behind the rejection: descendant-mailbox rehydration is
	// restricted to waiting_descendant, so the waiting_main owner is the one
	// state that can neither deliver nor forward this completion.
	if rec.allowsRehydrate(taskResumeByDescendantMailbox) {
		t.Fatal("parked waiting_main owner must not be rehydratable by a descendant mailbox")
	}
	descendantRecord := cloneDurableTaskRecord(rec)
	descendantRecord.State = string(SubAgentStateWaitingDescendant)
	if !descendantRecord.allowsRehydrate(taskResumeByDescendantMailbox) {
		t.Fatal("the same parked owner in waiting_descendant is rehydratable by a descendant mailbox")
	}
}

// TestParkedChildCompletionStuckInOwnedQueueSuppressesGlobalIdle pins the
// second half of the P1-6 chain: while an unroutable child completion sits in a
// parked owner's owned queue, hasRunnableMailboxWork stays true, so
// hasQueuedAutomaticWork stays true and emitGlobalIdleIfReady keeps refusing to
// emit GlobalIdle. Removing the stuck message lets the idle fire, proving the
// stale mailbox is the sole suppressor.
//
// Like the companion test, this is a defect-characterization test expected to
// PASS on the current code; it must be re-expressed once the fix makes the
// message routable (or forwards it), because then the mailbox no longer blocks
// global idle.
func TestParkedChildCompletionStuckInOwnedQueueSuppressesGlobalIdle(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())

	ownerTaskID := "adhoc-suppressed-owner"
	ownerInstanceID := "worker-suppressed-owner"
	childTaskID := "adhoc-suppressed-child"
	childInstanceID := "worker-suppressed-child"
	parked := &DurableTaskRecord{
		TaskID:           ownerTaskID,
		AgentDefName:     "worker",
		TaskDesc:         "nested owner parked while awaiting a main decision",
		State:            string(SubAgentStateWaitingMain),
		ResumePolicy:     taskResumePolicyNotify,
		LatestInstanceID: ownerInstanceID,
		InstanceHistory:  []string{ownerInstanceID},
		RuntimeParked:    true,
	}
	a.subs.mu.Lock()
	a.subs.taskRecords[ownerTaskID] = parked
	a.subs.mu.Unlock()

	if a.currentTurn() != nil || len(a.pendingUserMessages) > 0 || a.mailboxDeliveryPaused.Load() {
		t.Fatalf("idle baseline polluted: turn=%v pending=%v paused=%v", a.currentTurn(), a.pendingUserMessages, a.mailboxDeliveryPaused.Load())
	}
	if a.hasActiveSubAgentWork() {
		t.Fatal("parked task records must not count as active subagent work")
	}

	// The stuck child completion spools in the parked owner's owned queue, the
	// exact state the companion test proves is reachable.
	a.enqueueOwnedSubAgentMailbox(SubAgentMailboxMessage{
		MessageID:    "mailbox-suppressed-1",
		AgentID:      childInstanceID,
		TaskID:       childTaskID,
		OwnerAgentID: ownerInstanceID,
		OwnerTaskID:  ownerTaskID,
		Kind:         SubAgentMailboxKindCompleted,
		Priority:     SubAgentMailboxPriorityUrgent,
		Summary:      "child task finished",
		Payload:      "child task finished",
	})

	if !a.hasRunnableMailboxWork() {
		t.Fatal("hasRunnableMailboxWork() = false with the unroutable owned completion queued")
	}
	if !a.hasQueuedAutomaticWork() {
		t.Fatal("hasQueuedAutomaticWork() = false with the unroutable owned completion queued")
	}
	for range 2 {
		if a.emitGlobalIdleIfReady() {
			t.Fatal("global idle emitted while the unroutable child completion was queued")
		}
		if a.globalIdle.Load() {
			t.Fatal("globalIdle became true while the unroutable child completion was queued")
		}
		if queued := a.ownedSubAgentMailboxes[ownerInstanceID]; len(queued) != 1 {
			t.Fatalf("owned queue = %#v, want the stuck completion to survive idle probes", queued)
		}
	}

	// Control: once the stuck message is gone nothing suppresses idle anymore.
	delete(a.ownedSubAgentMailboxes, ownerInstanceID)
	if a.hasRunnableMailboxWork() {
		t.Fatal("hasRunnableMailboxWork() = true after the owned queue drained")
	}
	if !a.emitGlobalIdleIfReady() {
		t.Fatal("global idle stayed suppressed after the stuck child completion was removed")
	}
	if !a.globalIdle.Load() {
		t.Fatal("globalIdle not recorded after the idle fired")
	}
}
