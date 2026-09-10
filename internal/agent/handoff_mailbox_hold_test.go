package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// deliverMainOwnedMailboxEvent drives the production mailbox-event path for a
// main-owned (owner-less) message, which is delivered into the main inbox and
// would otherwise auto-start a mailbox turn when the main is idle.
func deliverMainOwnedMailboxEvent(a *MainAgent, messageID string) {
	a.handleSubAgentMailboxEvent(Event{
		Type: EventSubAgentMailbox,
		Payload: &SubAgentMailboxMessage{
			MessageID: messageID,
			AgentID:   "worker-mailbox-hold",
			TaskID:    "task-mailbox-hold",
			Kind:      SubAgentMailboxKindCompleted,
			Priority:  SubAgentMailboxPriorityNotify,
			Summary:   "worker finished",
			Payload:   "worker finished",
		},
	})
}

func assertNoDeferredHandoffSettled(t *testing.T, a *MainAgent) {
	t.Helper()
	for _, msg := range a.ctxMgr.Snapshot() {
		if msg.Role == message.RoleTool && msg.ToolCallID == "handoff-1" {
			t.Fatalf("the deferred handoff result was settled before the user decided: %+v", msg)
		}
	}
}

func mailboxStagedIn(a *MainAgent, messageID string) bool {
	for _, msg := range a.pendingSubAgentMailboxes {
		if msg != nil && msg.MessageID == messageID {
			return true
		}
	}
	for _, msg := range a.activeSubAgentMailboxes {
		if msg != nil && msg.MessageID == messageID {
			return true
		}
	}
	return a.activeSubAgentMailbox != nil && a.activeSubAgentMailbox.MessageID == messageID
}

// TestPendingHandoffHoldsMainInboxMailboxDelivery pins the hold on automatic
// main-inbox delivery while a handoff user wait is open. A mailbox arrival must
// not start a main turn: that turned the open wait into an abandoned one, whose
// deferred result the model reads as a user cancellation. The message stays
// queued (no ack, no drop) and rides the post-decision request.
func TestPendingHandoffHoldsMainInboxMailboxDelivery(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	setupHandoffTurn(t, a, "docs/plans/example.md")
	if a.pendingHandoff == nil || a.turn != nil {
		t.Fatalf("handoff wait baseline = pending:%v turn:%v, want pending with no active turn", a.pendingHandoff != nil, a.turn != nil)
	}

	const messageID = "mailbox-held-main-1"
	deliverMainOwnedMailboxEvent(a, messageID)

	if a.turn != nil {
		t.Fatal("a mailbox arrival during a pending handoff must not start a main turn")
	}
	if a.pendingHandoff == nil {
		t.Fatal("a mailbox arrival abandoned the pending handoff")
	}
	assertNoDeferredHandoffSettled(t, a)
	if mainInboxMailbox(a, messageID) == nil {
		t.Fatalf("held message %q is not queued in the main inbox: urgent=%#v normal=%#v", messageID, a.subAgentInbox.urgent, a.subAgentInbox.normal)
	}
	if a.hasRunnableMailboxWork() {
		t.Fatal("held mailbox work must not count as runnable while the handoff wait is open")
	}
}

// TestPendingHandoffHoldsSettledOwnerMailboxForward pins the forward-to-main
// leg of the hold: a completion for a terminal owner would normally be
// forwarded into the main inbox and immediately drain into a new turn, which
// must wait for the handoff decision instead of abandoning it.
func TestPendingHandoffHoldsSettledOwnerMailboxForward(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	setupHandoffTurn(t, a, "docs/plans/example.md")

	const (
		ownerInstanceID = "owner-held-1"
		ownerTaskID     = "owner-held-task-1"
		childInstanceID = "child-held-1"
		childTaskID     = "child-held-task-1"
		messageID       = "mailbox-held-owned-1"
	)
	seedTerminalOwnerAndLateChild(a, ownerInstanceID, ownerTaskID, childInstanceID, childTaskID)

	msg := lateChildCompletion(ownerInstanceID, ownerTaskID, childInstanceID, childTaskID, messageID)
	a.handleSubAgentMailboxEvent(Event{Type: EventSubAgentMailbox, SourceID: childInstanceID, Payload: &msg})

	if a.turn != nil {
		t.Fatal("forwarding a settled owner's mailbox during a pending handoff must not start a main turn")
	}
	if a.pendingHandoff == nil {
		t.Fatal("forwarding a settled owner's mailbox abandoned the pending handoff")
	}
	assertNoDeferredHandoffSettled(t, a)
	if queued := a.ownedSubAgentMailboxes[ownerInstanceID]; len(queued) != 1 || queued[0].MessageID != messageID {
		t.Fatalf("owned queue = %#v, want the completion held under its terminal owner", queued)
	}
	if mainInboxMailbox(a, messageID) != nil {
		t.Fatal("held completion must not be forwarded to the main inbox")
	}
}

// TestHandoffCancelReleasesHeldMailboxDelivery pins the cancel release: once
// the decision clears the wait, the next request boundary delivers the message
// that was held instead of stranding it.
func TestHandoffCancelReleasesHeldMailboxDelivery(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	setupHandoffTurn(t, a, "docs/plans/example.md")
	reqID := a.pendingHandoff.RequestID

	const messageID = "mailbox-held-cancel-1"
	deliverMainOwnedMailboxEvent(a, messageID)
	if a.turn != nil {
		t.Fatal("mailbox delivery was not held before the cancel")
	}

	a.handleHandoffResolveEvent(Event{Type: EventHandoffResolve, Payload: &handoffResolvePayload{
		RequestID: reqID,
		Action:    handoffResolveCancel,
	}})

	if a.pendingHandoff != nil {
		t.Fatal("cancel must clear the pending handoff")
	}
	if a.turn != nil {
		t.Fatal("cancel must not start a turn on its own")
	}

	// The dispatch idle path is what carries the released mailbox into the next
	// request boundary.
	a.emitGlobalIdleIfReady()
	if a.turn == nil {
		t.Fatal("the released mailbox must start a delivery turn at the next request boundary")
	}
	if !mailboxStagedIn(a, messageID) {
		t.Fatalf("released message %q was not staged for the new turn: pending=%v active=%v", messageID, a.pendingSubAgentMailboxes, a.activeSubAgentMailbox)
	}
}

// TestHandoffDenyStagesHeldMailboxIntoContinuation pins the deny release: the
// denial continuation stages the mailbox that was held, so the held message
// rides the continuation request instead of being lost.
func TestHandoffDenyStagesHeldMailboxIntoContinuation(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	setupHandoffTurn(t, a, "docs/plans/example.md")
	reqID := a.pendingHandoff.RequestID

	const messageID = "mailbox-held-deny-1"
	deliverMainOwnedMailboxEvent(a, messageID)
	if a.turn != nil {
		t.Fatal("mailbox delivery was not held before the denial")
	}

	a.handleHandoffResolveEvent(Event{Type: EventHandoffResolve, Payload: &handoffResolvePayload{
		RequestID:  reqID,
		Action:     handoffResolveDeny,
		DenyReason: "not now",
	}})

	if a.pendingHandoff != nil {
		t.Fatal("deny must clear the pending handoff")
	}
	if a.turn == nil {
		t.Fatal("deny must continue from context")
	}
	if !mailboxStagedIn(a, messageID) {
		t.Fatalf("held message %q must ride the denial continuation: pending=%v active=%v", messageID, a.pendingSubAgentMailboxes, a.activeSubAgentMailbox)
	}
}

// TestHandoffApproveKeepsHeldMailboxOutOfExecutionSession pins the approve
// boundary: the held message must not flow into the execution session, and its
// durable row must stay unconsumed (no ack without an append) so resuming the
// planner session replays it.
func TestHandoffApproveKeepsHeldMailboxOutOfExecutionSession(t *testing.T) {
	projectRoot := t.TempDir()
	planPath := writePlanFile(t, projectRoot)
	a := newTestMainAgent(t, projectRoot)
	prepareExecutableHandoffAgent(t, a)
	setupHandoffTurn(t, a, planPath)
	reqID := a.pendingHandoff.RequestID
	plannerSessionDir := a.SessionDir()
	if err := a.recoveryManager().PersistMessage(identity.MainAgentID, a.ctxMgr.Snapshot()[0]); err != nil {
		t.Fatalf("persist planner tool call: %v", err)
	}

	const messageID = "mailbox-held-approve-1"
	deliverMainOwnedMailboxEvent(a, messageID)
	if a.turn != nil {
		t.Fatal("mailbox delivery was not held before the approval")
	}
	drainAgentEvents(a.outputCh)

	a.handleHandoffResolveEvent(Event{Type: EventHandoffResolve, Payload: &handoffResolvePayload{
		RequestID: reqID,
		Action:    handoffResolveApprove,
		AgentName: "builder",
	}})

	if a.SessionDir() == plannerSessionDir {
		t.Fatal("approval must switch to a fresh execution session")
	}
	if len(a.subAgentInbox.normal) != 0 || len(a.subAgentInbox.urgent) != 0 {
		t.Fatalf("held mailbox state leaked into the execution session: urgent=%#v normal=%#v", a.subAgentInbox.urgent, a.subAgentInbox.normal)
	}
	if rows := countMailboxLogRows(t, plannerSessionDir, messageID); rows != 1 {
		t.Fatalf("planner mailbox.jsonl rows for %q = %d, want the single held row", messageID, rows)
	}
	acks, err := loadSubAgentMailboxAcks(plannerSessionDir)
	if err != nil {
		t.Fatalf("load planner mailbox acks: %v", err)
	}
	if ack, ok := acks[messageID]; ok && ack.Outcome == mailboxAckOutcomeConsumed {
		t.Fatalf("held message was acked without being appended: %+v", ack)
	}
	// The switch must tell the TUI to drop the replaced session's scoped state
	// (the held message's waiting row), exactly like every other session
	// switch, or the row lingers in the execution session's pending area.
	switchStarted := false
	for _, evt := range drainAgentEvents(a.outputCh) {
		if started, ok := evt.(SessionSwitchStartedEvent); ok {
			switchStarted = true
			if started.Kind != sessionSwitchKindPlanExecution {
				t.Fatalf("execution switch kind = %q, want %q", started.Kind, sessionSwitchKindPlanExecution)
			}
		}
	}
	if !switchStarted {
		t.Fatal("the execution switch must emit SessionSwitchStartedEvent so the TUI drops the replaced session's mailbox rows")
	}
}

// TestPendingHandoffKeepsLiveOwnerMailboxRouting pins that the hold does not
// block the owner side: a live owner still receives its descendant mailbox
// while the handoff wait is open, and no main turn starts for it.
func TestPendingHandoffKeepsLiveOwnerMailboxRouting(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	setupHandoffTurn(t, a, "docs/plans/example.md")

	owner := newControllableTestSubAgent(t, a, "adhoc-held-live-owner")
	owner.instanceID = "owner-live-hold-1"
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[owner.instanceID] = owner
	a.subs.mu.Unlock()
	a.syncTaskRecordFromSub(owner, "")
	if !owner.setState(SubAgentStateRunning, "working") {
		t.Fatal("failed to mark the live owner running")
	}

	const messageID = "mailbox-live-owner-hold-1"
	a.enqueueOwnedSubAgentMailbox(SubAgentMailboxMessage{
		MessageID:    messageID,
		AgentID:      "owner-live-hold-child",
		TaskID:       "owner-live-hold-child-task",
		OwnerAgentID: owner.instanceID,
		OwnerTaskID:  owner.taskID,
		Kind:         SubAgentMailboxKindProgress,
		Priority:     SubAgentMailboxPriorityNotify,
		Summary:      "child still working",
	})
	if queued := a.ownedSubAgentMailboxes[owner.instanceID]; len(queued) != 1 {
		t.Fatalf("owned queue = %#v, want the descendant progress queued", queued)
	}

	a.drainRunnableMailboxWork()

	if queued := a.ownedSubAgentMailboxes[owner.instanceID]; len(queued) != 0 {
		t.Fatalf("owned queue = %#v, want the live owner's progress delivered despite the handoff wait", queued)
	}
	if a.turn != nil {
		t.Fatal("owner-side routing must not start a main turn")
	}
	if a.pendingHandoff == nil {
		t.Fatal("owner-side routing abandoned the pending handoff")
	}
	owner.drainContextAppendsBeforeTurn()
	delivered := false
	for _, msg := range owner.ctxMgr.Snapshot() {
		if strings.Contains(msg.Content, "child still working") {
			delivered = true
			break
		}
	}
	if !delivered {
		t.Fatalf("live owner did not receive the routed progress: %+v", owner.ctxMgr.Snapshot())
	}
}

// TestPendingHandoffKeepsParkedOwnerWakeRouting pins the other owner-side leg:
// a descendant completion still wakes a parked owner while the handoff wait is
// open, so the wake path is not accidentally held with the main-inbox path.
func TestPendingHandoffKeepsParkedOwnerWakeRouting(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	setupHandoffTurn(t, a, "docs/plans/example.md")
	configureNestedDelegationTestRuntime(a, 2)

	owner := newControllableTestSubAgent(t, a, "adhoc-held-parked-owner")
	owner.instanceID = "owner-parked-hold-1"
	owner.writeScope = tools.WriteScope{PathPrefix: []string{"internal/agent"}}
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[owner.instanceID] = owner
	a.subs.mu.Unlock()
	a.syncTaskRecordFromSub(owner, "")

	child := newControllableTestSubAgent(t, a, "adhoc-held-parked-child")
	child.instanceID = "child-parked-hold-1"
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

	a.handleEscalate(Event{
		Type:     EventEscalate,
		SourceID: owner.instanceID,
		Payload:  tools.AgentRequestPayload{Reason: "awaiting a main decision before integrating the child result"},
	})
	if rec := a.taskRecordByTaskID(owner.taskID); rec == nil || !rec.RuntimeParked {
		t.Fatalf("owner task record = %#v, want it parked before the child completion", rec)
	}

	const messageID = "mailbox-parked-hold-1"
	msg := lateChildCompletion(owner.instanceID, owner.taskID, child.instanceID, child.taskID, messageID)
	a.handleSubAgentMailboxEvent(Event{Type: EventSubAgentMailbox, SourceID: child.instanceID, Payload: &msg})

	if queued := a.ownedSubAgentMailboxes[owner.instanceID]; len(queued) != 0 {
		t.Fatalf("owned queue = %#v, want the descendant completion to wake the parked owner", queued)
	}
	live := a.subAgentByTaskID(owner.taskID)
	if live == nil || live.State() != SubAgentStateRunning {
		t.Fatalf("parked owner was not woken by its descendant completion during the handoff wait: %#v", live)
	}
	if a.turn != nil {
		t.Fatal("waking a parked owner must not start a main turn")
	}
	if a.pendingHandoff == nil {
		t.Fatal("waking a parked owner abandoned the pending handoff")
	}
	if mainInboxMailbox(a, messageID) != nil {
		t.Fatal("the descendant completion must not be forwarded to the main inbox")
	}
}

// TestHandoffAbandonEmitsCancelledEvent pins the runtime cancellation signal: a
// handoff wait that is discarded before the user decides must tell the TUI which
// selector to close, while still settling the deferred result as cancelled.
func TestHandoffAbandonEmitsCancelledEvent(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	setupHandoffTurn(t, a, "docs/plans/example.md")
	reqID := a.pendingHandoff.RequestID
	if reqID == "" {
		t.Fatal("setup did not promote the handoff wait")
	}
	drainAgentEvents(a.outputCh)

	a.newTurn()

	if a.pendingHandoff != nil {
		t.Fatal("newTurn must clear the pending handoff")
	}
	var got *HandoffCancelledEvent
	for _, evt := range drainAgentEvents(a.outputCh) {
		if cancelled, ok := evt.(HandoffCancelledEvent); ok {
			got = &cancelled
		}
	}
	if got == nil {
		t.Fatal("abandoning an open handoff wait must emit HandoffCancelledEvent")
	}
	if got.RequestID != reqID {
		t.Fatalf("HandoffCancelledEvent.RequestID = %q, want %q", got.RequestID, reqID)
	}
	if got.Reason != handoffCancelledReasonSuperseded {
		t.Fatalf("HandoffCancelledEvent.Reason = %q, want %q", got.Reason, handoffCancelledReasonSuperseded)
	}
	settled := false
	for _, msg := range a.ctxMgr.Snapshot() {
		if msg.Role == message.RoleTool && msg.ToolCallID == "handoff-1" && msg.ToolStatus == string(ToolResultStatusCancelled) {
			settled = true
		}
	}
	if !settled {
		t.Fatal("abandoning the handoff must still settle its deferred result as cancelled")
	}
}

// TestSettlePendingHandoffAtShutdownDoesNotEmitCancelledEvent pins that the
// process-exit settlement stays silent: production calls it after the event
// loop and output channel are gone, so there is no UI left to notify.
func TestSettlePendingHandoffAtShutdownDoesNotEmitCancelledEvent(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	setupHandoffTurn(t, a, "docs/plans/example.md")
	drainAgentEvents(a.outputCh)

	a.settlePendingHandoffAtShutdown()

	if a.pendingHandoff != nil {
		t.Fatal("shutdown settlement must clear the pending handoff")
	}
	for _, evt := range drainAgentEvents(a.outputCh) {
		if cancelled, ok := evt.(HandoffCancelledEvent); ok {
			t.Fatalf("shutdown settlement must not emit HandoffCancelledEvent: %+v", cancelled)
		}
	}
	settled := false
	for _, msg := range a.ctxMgr.Snapshot() {
		if msg.Role == message.RoleTool && msg.ToolCallID == "handoff-1" && msg.ToolStatus == string(ToolResultStatusCancelled) {
			settled = true
		}
	}
	if !settled {
		t.Fatal("shutdown settlement must still close the deferred handoff call")
	}
}

// TestHandoffCancelDeliversHeldSettledOwnerForwardInSameDrain pins the release
// path for the forward-to-main leg: once the decision clears the wait, the same
// owner drain that forwards the held completion into the main inbox must also
// deliver it, instead of leaving it queued for the next lifecycle sweep.
func TestHandoffCancelDeliversHeldSettledOwnerForwardInSameDrain(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	setupHandoffTurn(t, a, "docs/plans/example.md")
	reqID := a.pendingHandoff.RequestID

	const (
		ownerInstanceID = "owner-held-forward-1"
		ownerTaskID     = "owner-held-forward-task-1"
		childInstanceID = "child-held-forward-1"
		childTaskID     = "child-held-forward-task-1"
		messageID       = "mailbox-held-forward-1"
	)
	seedTerminalOwnerAndLateChild(a, ownerInstanceID, ownerTaskID, childInstanceID, childTaskID)
	msg := lateChildCompletion(ownerInstanceID, ownerTaskID, childInstanceID, childTaskID, messageID)
	a.handleSubAgentMailboxEvent(Event{Type: EventSubAgentMailbox, SourceID: childInstanceID, Payload: &msg})

	if a.turn != nil {
		t.Fatal("the held completion must not start a main turn before the decision")
	}
	if queued := a.ownedSubAgentMailboxes[ownerInstanceID]; len(queued) != 1 || queued[0].MessageID != messageID {
		t.Fatalf("owned queue = %#v, want the completion held under its terminal owner", queued)
	}

	a.handleHandoffResolveEvent(Event{Type: EventHandoffResolve, Payload: &handoffResolvePayload{
		RequestID: reqID,
		Action:    handoffResolveCancel,
	}})
	if a.pendingHandoff != nil {
		t.Fatal("cancel must clear the pending handoff")
	}
	if a.turn != nil {
		t.Fatal("cancel must not start a turn on its own")
	}

	// One owner-drain dispatch after the decision must both forward the held
	// completion into the main inbox and start the delivery turn for it.
	a.drainRunnableMailboxWork()

	if queued := a.ownedSubAgentMailboxes[ownerInstanceID]; len(queued) != 0 {
		t.Fatalf("owned queue = %#v, want the completion forwarded and delivered", queued)
	}
	if a.turn == nil {
		t.Fatal("the released completion must start a delivery turn in the same drain that forwarded it")
	}
	if !mailboxStagedIn(a, messageID) {
		t.Fatalf("released message %q was not staged for the new turn: pending=%v active=%v", messageID, a.pendingSubAgentMailboxes, a.activeSubAgentMailbox)
	}
}

// TestReliableOutputEventLogHandoffEvents pins that the handoff prompt and its
// cancellation use the blocking-delivery path: dropping either when the TUI
// output channel is full would leave an open handoff wait with no way to be
// shown or released.
func TestReliableOutputEventLogHandoffEvents(t *testing.T) {
	events := []AgentEvent{
		HandoffEvent{PlanPath: "docs/plans/example.md", RequestID: "handoff-1"},
		HandoffCancelledEvent{RequestID: "handoff-1", Reason: handoffCancelledReasonSuperseded},
	}
	for _, evt := range events {
		if _, _, ok := reliableOutputEventLog(evt); !ok {
			t.Errorf("reliableOutputEventLog(%T) = not reliable, want blocking delivery", evt)
		}
	}
}
