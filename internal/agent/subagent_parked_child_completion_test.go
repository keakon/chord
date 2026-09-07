package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// TestDescendantCompletionWakesParkedWaitingMainOwner pins the parked-owner
// wake fix at the mailbox-routing level: an owner subagent parks in
// waiting_main (production escalate path, exactly as the waiting_main
// lifecycle sweep tests do) while one of its children is still live, and that
// child then completes. A child completion is a decision point the parked
// owner needs, so the descendant mailbox must wake the owner: the completion
// is delivered to a rehydrated owner runtime instead of being stranded in the
// owned queue (which used to suppress global idle forever), and it is not
// forwarded to the main inbox.
func TestDescendantCompletionWakesParkedWaitingMainOwner(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	// Waking a parked owner rehydrates its durable task record, which needs the
	// agent definition and an LLM client factory, mirroring the harness used by
	// the descendant-mailbox rehydration tests.
	configureNestedDelegationTestRuntime(a, 2)

	owner := newControllableTestSubAgent(t, a, "adhoc-woken-owner")
	owner.instanceID = "worker-woken-owner"
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[owner.instanceID] = owner
	a.subs.mu.Unlock()
	a.syncTaskRecordFromSub(owner, "")

	child := newControllableTestSubAgent(t, a, "adhoc-woken-child")
	child.instanceID = "worker-woken-child"
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
	// The completion travels through the production mailbox-event path and must
	// wake the waiting_main owner instead of spooling under its owned queue.
	completedID := "mailbox-woken-owner-1"
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

	// Delivered: the owned queue drained into a freshly rehydrated owner runtime
	// that is now running on the completion, so nothing stays spooled.
	if queued := a.ownedSubAgentMailboxes[owner.instanceID]; len(queued) != 0 {
		t.Fatalf("owned completion queue = %#v, want empty after the descendant completion woke the owner", queued)
	}
	if queued := a.ownedMailboxSpool[owner.instanceID]; len(queued) != 0 {
		t.Fatalf("owned completion spool = %#v, want empty after the descendant completion woke the owner", queued)
	}
	live := a.subAgentByTaskID(owner.taskID)
	if live == nil {
		t.Fatal("the descendant completion did not wake the parked waiting_main owner")
	}
	if live.instanceID == owner.instanceID {
		t.Fatal("woken owner must be a fresh runtime instance, not the parked one")
	}
	if live.State() != SubAgentStateRunning {
		t.Fatalf("woken owner.State() = %q, want running on the child completion", live.State())
	}
	// Delivered and consumed: the wake rehydrates a full owner runtime whose own
	// run loop is already live, so the completion is taken out of inputCh into
	// a turn as soon as the owner runs — probing inputCh directly would race
	// that loop. Observe delivery through the woken owner's context instead,
	// mirroring the rehydration wake tests.
	deadline := time.Now().Add(time.Second)
	consumed := false
	for time.Now().Before(deadline) {
		for _, msg := range live.ctxMgr.Snapshot() {
			if msg.Role == message.RoleUser && strings.Contains(msg.Content, "child task finished") {
				consumed = true
				break
			}
		}
		if consumed {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !consumed {
		t.Fatal("expected the child completion to be consumed by the woken owner runtime")
	}
	rec = a.taskRecordByTaskID(owner.taskID)
	if rec == nil || rec.RuntimeParked || SubAgentState(rec.State) != SubAgentStateRunning {
		t.Fatalf("owner task record after wake = %#v, want live running", rec)
	}

	// Not forwarded to main: the completion belongs to the woken owner, so the
	// main inbox and its staging must stay empty and no main turn may start.
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
		t.Fatal("the woken owner's completion must not start a main turn")
	}

	// A later drain must find nothing left to route or re-route for the owner.
	if a.drainOwnedSubAgentMailboxes(owner.instanceID) {
		t.Fatal("drain reported progress for a completion that was already delivered")
	}

	// The durable gate behind the wake: any parked non-terminal owner state
	// (idle, waiting_main, waiting_descendant) is rehydratable by a descendant
	// mailbox, while terminal states keep the forward-to-main behavior.
	descendantRecord := cloneDurableTaskRecord(rec)
	descendantRecord.LatestInstanceID = owner.instanceID
	descendantRecord.InstanceHistory = []string{owner.instanceID}
	for _, state := range []SubAgentState{SubAgentStateIdle, SubAgentStateWaitingMain, SubAgentStateWaitingDescendant} {
		probe := cloneDurableTaskRecord(descendantRecord)
		probe.State = string(state)
		if !probe.allowsRehydrate(taskResumeByDescendantMailbox) {
			t.Fatalf("parked owner in %s must be rehydratable by a descendant mailbox", state)
		}
	}
	for _, state := range []SubAgentState{SubAgentStateCompleted, SubAgentStateFailed, SubAgentStateCancelled} {
		probe := cloneDurableTaskRecord(descendantRecord)
		probe.State = string(state)
		if probe.allowsRehydrate(taskResumeByDescendantMailbox) {
			t.Fatalf("terminal owner in %s must not be rehydrated by a descendant mailbox (its mailboxes forward to main)", state)
		}
	}
}

// TestUnroutableOwnedMailboxDoesNotSuppressGlobalIdle pins the global-idle
// half of the parked-owner wake fix: an owned mailbox that routing refuses is
// "temporarily unroutable", not runnable mailbox work, so it must not keep
// hasRunnableMailboxWork (and therefore global idle) suppressed forever. A
// parked waiting_main owner holds a completion whose sender is no descendant of
// it, so routing refuses on every drain; global idle must still fire.
func TestUnroutableOwnedMailboxDoesNotSuppressGlobalIdle(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())

	ownerTaskID := "adhoc-unroutable-owner"
	ownerInstanceID := "worker-unroutable-owner"
	childTaskID := "adhoc-unroutable-child"
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

	// The stranded completion spools in the parked owner's owned queue. Its
	// sender has no durable task record, so it is not a descendant mailbox of
	// the owner and routing refuses it on every drain.
	msg := SubAgentMailboxMessage{
		MessageID:    "mailbox-unroutable-1",
		AgentID:      "worker-unrelated-agent",
		TaskID:       childTaskID,
		OwnerAgentID: ownerInstanceID,
		OwnerTaskID:  ownerTaskID,
		Kind:         SubAgentMailboxKindCompleted,
		Priority:     SubAgentMailboxPriorityUrgent,
		Summary:      "child task finished",
		Payload:      "child task finished",
	}
	a.enqueueOwnedSubAgentMailbox(msg)

	// Unroutable now, and by design not wakeable: a non-descendant mailbox must
	// not rehydrate the parked owner, and each routing attempt refuses it.
	if a.routeOwnedSubAgentMailbox(msg) {
		t.Fatal("non-descendant completion unexpectedly routed to the parked owner")
	}
	if a.drainOwnedSubAgentMailboxes(ownerInstanceID) {
		t.Fatal("drain reported progress for a completion that was not routable")
	}
	if queued := a.ownedSubAgentMailboxes[ownerInstanceID]; len(queued) != 1 || queued[0].MessageID != msg.MessageID {
		t.Fatalf("owned completion queue = %#v, want the non-descendant message still queued", queued)
	}
	if live := a.subAgentByTaskID(ownerTaskID); live != nil {
		t.Fatal("a non-descendant mailbox must not wake the parked owner")
	}

	// Explicit boundary: temporarily unroutable is not routable. The stranded
	// message is not counted as runnable mailbox work, so neither
	// hasRunnableMailboxWork nor hasQueuedAutomaticWork stays true for it.
	if a.ownedMailboxMessageRoutable(msg) {
		t.Fatal("ownedMailboxMessageRoutable() = true for a non-descendant completion of a parked owner")
	}
	if a.hasRunnableMailboxWork() {
		t.Fatal("hasRunnableMailboxWork() = true while only a temporarily unroutable owned completion is queued")
	}
	if a.hasQueuedAutomaticWork() {
		t.Fatal("hasQueuedAutomaticWork() = true while only a temporarily unroutable owned completion is queued")
	}

	// Global idle fires despite the stranded message, and repeated probes keep
	// the owned queue intact without flipping idle back off.
	for range 2 {
		a.emitGlobalIdleIfReady()
		if !a.globalIdle.Load() {
			t.Fatal("global idle stayed suppressed while the temporarily unroutable completion was queued")
		}
		if queued := a.ownedSubAgentMailboxes[ownerInstanceID]; len(queued) != 1 {
			t.Fatalf("owned queue = %#v, want the stranded completion to survive idle probes", queued)
		}
	}

	// Control: the very same message becomes routable once its sender is a
	// genuine descendant of the parked owner (a durable child record naming
	// this owner's task and instance), proving the unroutable verdict above is
	// about the sender relationship, not about the parked waiting_main state.
	child := &DurableTaskRecord{
		TaskID:           childTaskID,
		AgentDefName:     "worker",
		TaskDesc:         "direct child task of the parked owner",
		State:            string(SubAgentStateRunning),
		ResumePolicy:     taskResumePolicyNotify,
		OwnerAgentID:     ownerInstanceID,
		OwnerTaskID:      ownerTaskID,
		LatestInstanceID: "worker-unroutable-child",
		InstanceHistory:  []string{"worker-unroutable-child"},
	}
	a.subs.mu.Lock()
	a.subs.taskRecords[childTaskID] = child
	a.subs.mu.Unlock()
	if !a.ownedMailboxMessageRoutable(msg) {
		t.Fatal("a descendant completion of the same parked waiting_main owner must be routable")
	}
	if !a.hasRunnableMailboxWork() {
		t.Fatal("hasRunnableMailboxWork() = false while a routable descendant completion is queued")
	}
}

// TestNonDescendantMailboxStaysQueuedForParkedWaitingMainOwner pins the wake
// boundary with the strongest form of the mismatch: the sender's durable task
// record exists but names a different owner task, so the mailbox is not a
// descendant message of the parked owner. Routing must refuse it, the owner
// must stay parked, and the message must remain queued (and unroutable).
func TestNonDescendantMailboxStaysQueuedForParkedWaitingMainOwner(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())

	ownerTaskID := "adhoc-foreign-owner"
	ownerInstanceID := "worker-foreign-owner"
	foreignChildTaskID := "adhoc-other-owner-child"
	foreignOwnerTaskID := "adhoc-somewhere-else"
	parked := &DurableTaskRecord{
		TaskID:           ownerTaskID,
		AgentDefName:     "worker",
		State:            string(SubAgentStateWaitingMain),
		ResumePolicy:     taskResumePolicyNotify,
		LatestInstanceID: ownerInstanceID,
		InstanceHistory:  []string{ownerInstanceID},
		RuntimeParked:    true,
	}
	foreignChild := &DurableTaskRecord{
		TaskID:           foreignChildTaskID,
		AgentDefName:     "worker",
		State:            string(SubAgentStateCompleted),
		ResumePolicy:     taskResumePolicyNotify,
		OwnerAgentID:     "worker-other-instance",
		OwnerTaskID:      foreignOwnerTaskID,
		LatestInstanceID: "worker-other-instance",
		InstanceHistory:  []string{"worker-other-instance"},
	}
	a.subs.mu.Lock()
	a.subs.taskRecords[ownerTaskID] = parked
	a.subs.taskRecords[foreignChildTaskID] = foreignChild
	a.subs.mu.Unlock()

	msg := SubAgentMailboxMessage{
		MessageID:    "mailbox-foreign-1",
		AgentID:      "worker-other-instance",
		TaskID:       foreignChildTaskID,
		OwnerAgentID: ownerInstanceID,
		OwnerTaskID:  ownerTaskID,
		Kind:         SubAgentMailboxKindCompleted,
		Priority:     SubAgentMailboxPriorityUrgent,
		Summary:      "a child of a different owner completed",
		Payload:      "a child of a different owner completed",
	}
	a.enqueueOwnedSubAgentMailbox(msg)

	if a.routeOwnedSubAgentMailbox(msg) {
		t.Fatal("completion of a foreign task unexpectedly routed to the parked owner")
	}
	if a.drainOwnedSubAgentMailboxes(ownerInstanceID) {
		t.Fatal("drain reported progress for a foreign completion")
	}
	if queued := a.ownedSubAgentMailboxes[ownerInstanceID]; len(queued) != 1 || queued[0].MessageID != msg.MessageID {
		t.Fatalf("owned completion queue = %#v, want the foreign message still queued", queued)
	}
	if live := a.subAgentByTaskID(ownerTaskID); live != nil {
		t.Fatal("a foreign completion must not wake the parked owner")
	}
	if a.ownedMailboxMessageRoutable(msg) {
		t.Fatal("ownedMailboxMessageRoutable() = true for a completion whose sender is not the owner's descendant")
	}
	if a.hasRunnableMailboxWork() {
		t.Fatal("hasRunnableMailboxWork() = true while only the foreign completion is queued")
	}
	rec := a.taskRecordByTaskID(ownerTaskID)
	if rec == nil || SubAgentState(rec.State) != SubAgentStateWaitingMain || !rec.RuntimeParked {
		t.Fatalf("owner task record = %#v, want still parked waiting_main", rec)
	}
}
