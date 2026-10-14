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

func TestSendMessageToWaitingMainWorkerResumesExecution(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-1")
	sub.setState(SubAgentStateWaitingMain, "need approval")
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
	foundNotify := false
	for len(a.outputCh) > 0 {
		if notify, ok := (<-a.outputCh).(AgentNotifyEvent); ok {
			if notify.AgentID != "main" || notify.TargetAgentID != sub.instanceID || notify.TargetTaskID != sub.taskID || notify.Kind != "reply" || notify.Message != "continue with option B" {
				t.Fatalf("notify event = %#v", notify)
			}
			foundNotify = true
		}
	}
	if !foundNotify {
		t.Fatal("expected targeted AgentNotifyEvent")
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

func TestNotifySubAgentConsumedMessageCarriesMailboxMetadata(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-notify")
	sub.setState(SubAgentStateWaitingMain, "need approval")

	handle, err := a.NotifySubAgent(context.Background(), sub.taskID, "continue with option B", "follow_up")
	if err != nil {
		t.Fatalf("NotifySubAgent: %v", err)
	}
	if handle.Status != "resumed" {
		t.Fatalf("handle.Status = %q, want resumed", handle.Status)
	}
	var input pendingUserMessage
	select {
	case input = <-sub.inputCh:
	default:
		t.Fatal("expected resumed worker to receive the queued notify message")
	}
	if got := pendingUserMessageText(input); got != "[follow_up] continue with option B" {
		t.Fatalf("queued message = %q, want %q", got, "[follow_up] continue with option B")
	}
	if input.Mailbox == nil {
		t.Fatal("expected the queued notify message to carry mailbox metadata")
	}
	if input.Mailbox.AgentID != "main" || input.Mailbox.TaskID != "" || input.Mailbox.Kind != "follow_up" || input.Mailbox.MessageID != "" {
		t.Fatalf("queued mailbox metadata = %#v, want main/follow_up without a message id", input.Mailbox)
	}
	sub.appendPendingUserMessage(input)
	msgs := sub.GetMessages()
	if len(msgs) == 0 {
		t.Fatal("expected the consumed notify message to be appended to the worker context")
	}
	last := msgs[len(msgs)-1]
	if last.Kind != message.KindSubAgentMailbox {
		t.Fatalf("consumed row kind = %q, want %q", last.Kind, message.KindSubAgentMailbox)
	}
	if last.Content != "[follow_up] continue with option B" {
		t.Fatalf("consumed row content = %q, want the notify text with its kind prefix", last.Content)
	}
	if last.Mailbox == nil || last.Mailbox.AgentID != "main" || last.Mailbox.TaskID != "" || last.Mailbox.Kind != "follow_up" || last.Mailbox.MessageID != "" {
		t.Fatalf("consumed row mailbox = %#v, want main/follow_up without a message id", last.Mailbox)
	}
}

func TestNotifySubAgentDoesNotAckMailboxWhenSlotUnavailable(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-slot")
	sub.setState(SubAgentStateWaitingMain, "need approval")
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

func TestNotifySubAgentQueueRejectionLeavesWaitingWorkerAndMailboxUnchanged(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-queue-reject")
	sub.setState(SubAgentStateWaitingMain, "need approval")
	sub.queueByteLimit = 1
	a.enqueueSubAgentMailbox(SubAgentMailboxMessage{
		MessageID:   "worker-1-queue-reject",
		AgentID:     sub.instanceID,
		TaskID:      sub.taskID,
		Kind:        SubAgentMailboxKindDecisionRequired,
		Priority:    SubAgentMailboxPriorityInterrupt,
		Summary:     "need approval",
		RequiresAck: true,
	})

	if _, err := a.NotifySubAgent(context.Background(), sub.taskID, "continue with option D", "reply"); err == nil {
		t.Fatal("expected NotifySubAgent to reject the oversized resume message")
	}
	if sub.State() != SubAgentStateWaitingMain {
		t.Fatalf("sub.State() = %q, want waiting_main", sub.State())
	}
	if held, _ := sub.slotState(); held {
		t.Fatal("queue rejection left the waiting worker holding a runtime slot")
	}
	if got := len(a.sem); got != 0 {
		t.Fatalf("runtime slots in use = %d, want 0", got)
	}
	if got := len(a.subAgentInbox.urgent); got != 1 || a.subAgentInbox.urgent[0].MessageID != "worker-1-queue-reject" {
		t.Fatalf("urgent inbox = %#v, want the original mailbox message", a.subAgentInbox.urgent)
	}
	acks, err := loadSubAgentMailboxAcks(a.sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxAcks: %v", err)
	}
	if _, ok := acks["worker-1-queue-reject"]; ok {
		t.Fatal("queue rejection persisted a mailbox acknowledgement")
	}
}

func TestOwnedMailboxQueueRejectionRollsBackReactivation(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	owner := newControllableTestSubAgent(t, a, "adhoc-owner-queue-reject")
	owner.setState(SubAgentStateWaitingMain, "waiting")
	owner.queueByteLimit = 1

	if a.routeOwnedSubAgentMailbox(SubAgentMailboxMessage{
		MessageID:    "child-queue-reject",
		AgentID:      "child",
		TaskID:       "adhoc-child",
		OwnerAgentID: owner.instanceID,
		OwnerTaskID:  owner.taskID,
		Kind:         SubAgentMailboxKindDecisionRequired,
		Summary:      "needs a decision",
	}) {
		t.Fatal("oversized owned mailbox unexpectedly delivered")
	}
	if owner.State() != SubAgentStateWaitingMain {
		t.Fatalf("owner state = %q, want waiting_main", owner.State())
	}
	if held, _ := owner.slotState(); held {
		t.Fatal("rejected owned mailbox left the owner holding a runtime slot")
	}
	if got := len(a.sem); got != 0 {
		t.Fatalf("runtime slots in use = %d, want 0", got)
	}
}

func TestManualDeliveryRoutesOwnedMailboxesBeforeManualMessage(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-drain-order")
	a.ownedSubAgentMailboxes = map[string][]SubAgentMailboxMessage{
		sub.instanceID: {
			{
				MessageID:    "child-progress-1",
				AgentID:      "child-1",
				TaskID:       "adhoc-child",
				OwnerAgentID: sub.instanceID,
				OwnerTaskID:  sub.taskID,
				Kind:         SubAgentMailboxKindProgress,
				Priority:     SubAgentMailboxPriorityNotify,
				Summary:      "child halfway done",
				Payload:      "child halfway done",
				CreatedAt:    time.Now(),
			},
			{
				MessageID:    "child-report-1",
				AgentID:      "child-1",
				TaskID:       "adhoc-child",
				OwnerAgentID: sub.instanceID,
				OwnerTaskID:  sub.taskID,
				Kind:         SubAgentMailboxKindCompleted,
				Priority:     SubAgentMailboxPriorityUrgent,
				Summary:      "child finished the refactor",
				Payload:      "child finished the refactor",
				CreatedAt:    time.Now(),
			},
		},
	}

	status, _, err := a.deliverManualMessageToSubAgent(sub, "please integrate the child result", "reply")
	if err != nil {
		t.Fatalf("deliverManualMessageToSubAgent: %v", err)
	}
	if status != "queued" {
		t.Fatalf("status = %q, want queued", status)
	}
	if got := len(a.ownedSubAgentMailboxes[sub.instanceID]); got != 0 {
		t.Fatalf("owned mailboxes left undrained = %d, want 0", got)
	}
	ctxMsg, ok := sub.tryReceiveContextAppend()
	if !ok || !strings.Contains(ctxMsg.Content, "child halfway done") {
		t.Fatalf("context append = (%q, %v), want the queued progress report", ctxMsg.Content, ok)
	}
	first, ok := sub.tryReceiveUserInput()
	if !ok || first.MailboxAckID != "child-report-1" || !strings.Contains(first.Content, "child finished the refactor") {
		t.Fatalf("first queued input = (%q, ack:%q, %v), want the child completion report", first.Content, first.MailboxAckID, ok)
	}
	second, ok := sub.tryReceiveUserInput()
	if !ok || !strings.Contains(second.Content, "please integrate the child result") {
		t.Fatalf("second queued input = (%q, %v), want the manual message", second.Content, ok)
	}
}

func TestRestoreMessagesRebuildsResponseMailboxIdempotency(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-response-restore")
	sub.RestoreMessages([]message.Message{{Role: "user", Content: "decision", Mailbox: &message.MailboxMetadata{
		MessageID: "response-corr-1", MessageType: string(AgentMessageTypeResponse),
	}}})
	if !sub.hasAcceptedMailbox("response-corr-1") {
		t.Fatal("restored response mailbox ID was not indexed")
	}
	if sub.hasAcceptedMailbox("other") {
		t.Fatal("unexpected mailbox ID was indexed")
	}
}

func TestOwnedCompletedMailboxReactivatesWaitingDescendantParent(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.instanceID = "worker-parent"
	parent.setState(SubAgentStateWaitingDescendant, "waiting for child")
	parent.setPendingCompleteIntent(&AgentResult{Summary: "final summary"})
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
	child.setState(SubAgentStateCompleted, "child done")
	child.semHeld = true
	a.sem <- struct{}{}
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[child.instanceID] = child
	a.subs.mu.Unlock()
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
	pending := parent.PendingCompleteIntent()
	if pending != nil {
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

func TestOwnedCompletedMailboxQueueRejectionReturnsTransferredSlot(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	parent := newControllableTestSubAgent(t, a, "adhoc-parent-transfer-reject")
	parent.instanceID = "worker-parent-transfer-reject"
	parent.setState(SubAgentStateWaitingDescendant, "waiting for child")
	parent.queueByteLimit = 1
	child := newControllableTestSubAgent(t, a, "adhoc-child-transfer-reject")
	child.instanceID = "worker-child-transfer-reject"
	child.ownerAgentID = parent.instanceID
	child.ownerTaskID = parent.taskID
	child.joinToOwner = true
	child.setState(SubAgentStateCompleted, "done")
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[parent.instanceID] = parent
	a.subs.subAgents[child.instanceID] = child
	a.subs.taskRecords[child.taskID] = buildTaskRecordFromSub(child, nil, "task completed", 0, time.Now())
	a.subs.mu.Unlock()
	if err := a.acquireSubAgentSlot(child); err != nil {
		t.Fatalf("acquire child slot: %v", err)
	}

	if a.routeOwnedSubAgentMailbox(SubAgentMailboxMessage{
		MessageID:    "child-complete-transfer-reject",
		AgentID:      child.instanceID,
		TaskID:       child.taskID,
		OwnerAgentID: parent.instanceID,
		OwnerTaskID:  parent.taskID,
		Kind:         SubAgentMailboxKindCompleted,
		Summary:      "child completed with a payload too large for the parent queue",
	}) {
		t.Fatal("oversized child completion unexpectedly delivered")
	}
	if held, _ := parent.slotState(); held {
		t.Fatal("parent retained the transferred slot after queue rejection")
	}
	if held, _ := child.slotState(); !held {
		t.Fatal("child did not recover the slot after parent queue rejection")
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
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[parent.instanceID] = parent
	a.subs.mu.Unlock()
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

// TestOwnerRoutedMailboxEmitsTranscriptAppendedEvent pins the TUI-visible half
// of an owner-routed context append. The append is written straight into the
// owner's ctxmgr, so without a MailboxTranscriptAppendedEvent the queued
// mailbox line stays in the TUI until a session switch clears it — the consumed
// ack only retires the durable row. The event must carry the transcript index
// the row landed on, taken before the append.
func TestOwnerRoutedMailboxEmitsTranscriptAppendedEvent(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	parent := newControllableTestSubAgent(t, a, "adhoc-parent-event")
	parent.instanceID = "worker-parent-event"
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[parent.instanceID] = parent
	a.subs.mu.Unlock()
	a.syncTaskRecordFromSub(parent, "")

	childMsg := SubAgentMailboxMessage{
		MessageID:    "worker-child-event-1",
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
		t.Fatal("expected owner-routed mailbox to enqueue a context append for the direct parent")
	}

	timeout := time.After(2 * time.Second)
	for {
		select {
		case evt := <-a.outputCh:
			appended, ok := evt.(MailboxTranscriptAppendedEvent)
			if !ok || appended.Message.Mailbox == nil ||
				strings.TrimSpace(appended.Message.Mailbox.MessageID) != childMsg.MessageID {
				continue
			}
			if appended.TargetAgentID != parent.instanceID {
				t.Fatalf("appended event target = %q, want %q", appended.TargetAgentID, parent.instanceID)
			}
			msgs := parent.ctxMgr.Snapshot()
			if appended.MessageIndex < 0 || appended.MessageIndex >= len(msgs) ||
				msgs[appended.MessageIndex].Mailbox == nil ||
				strings.TrimSpace(msgs[appended.MessageIndex].Mailbox.MessageID) != childMsg.MessageID {
				t.Fatalf("MessageIndex %d does not point at the appended mailbox row: %s",
					appended.MessageIndex, summarizeMessages(msgs))
			}
			return
		case <-timeout:
			t.Fatal("MailboxTranscriptAppendedEvent never emitted for the context-only append")
		}
	}
}

func TestBusyOwnerMailboxQueuesForNextRequest(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	owner := newControllableTestSubAgent(t, a, "adhoc-parent-busy")
	owner.instanceID = "worker-parent-busy"
	owner.setState(SubAgentStateRunning, "working")
	owner.llmRequestInFlight.Store(true)
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[owner.instanceID] = owner
	a.subs.mu.Unlock()

	if !a.routeOwnedSubAgentMailbox(SubAgentMailboxMessage{
		MessageID:    "worker-child-complete",
		AgentID:      "worker-child",
		TaskID:       "adhoc-child",
		OwnerAgentID: owner.instanceID,
		OwnerTaskID:  owner.taskID,
		Kind:         SubAgentMailboxKindCompleted,
		Priority:     SubAgentMailboxPriorityUrgent,
		Summary:      "child completed",
	}) {
		t.Fatal("routeOwnedSubAgentMailbox() = false")
	}
	select {
	case pending := <-owner.inputCh:
		if text := pendingUserMessageText(pending); !strings.Contains(text, "child completed") {
			t.Fatalf("queued owner message = %q, want child completion", text)
		}
	default:
		t.Fatal("expected busy owner mailbox to queue for the next request")
	}
	select {
	case msg := <-owner.ctxAppendCh:
		t.Fatalf("unexpected context-only mailbox while owner busy: %#v", msg)
	default:
	}
}

func TestOwnerRoutedWakeBypassesSemaphoreWhenOwnerMustResume(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.instanceID = "worker-parent"
	parent.setState(SubAgentStateWaitingDescendant, "waiting for child")
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[parent.instanceID] = parent
	a.subs.mu.Unlock()
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
	if !parent.semHeld || !parent.semBorrowed || parent.semBypassed {
		t.Fatalf("parent slot flags = held:%v borrowed:%v bypassed:%v, want borrowed grant", parent.semHeld, parent.semBorrowed, parent.semBypassed)
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
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[parent.instanceID] = parent
	a.subs.mu.Unlock()
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
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[parent.instanceID] = parent
	a.subs.mu.Unlock()
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
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[parent.instanceID] = parent
	a.subs.mu.Unlock()
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
	parent.setPendingCompleteIntent(&AgentResult{Summary: "final summary"})
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
	child.setState(SubAgentStateCompleted, "child done")
	child.semHeld = true
	a.sem <- struct{}{}
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[child.instanceID] = child
	a.subs.mu.Unlock()
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

	snap, err := a.recoveryManager().Recover()
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

func TestNotifySubAgentUsesEventLoopWhenStarted(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	startMainAgentLoopForTest(t, a)
	sub := newControllableTestSubAgent(t, a, "adhoc-loop")
	sub.setState(SubAgentStateWaitingMain, "need approval")
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

func TestSubAgentTerminalErrorQueuesRiskAlertAndWakesMain(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-terminal-error")

	a.handleAgentError(Event{Type: EventAgentError, SourceID: sub.instanceID, Payload: context.DeadlineExceeded})

	select {
	case evt := <-a.eventCh:
		if evt.Type != EventSubAgentMailbox {
			t.Fatalf("event = %#v, want SubAgent mailbox", evt)
		}
		msg, ok := evt.Payload.(*SubAgentMailboxMessage)
		if !ok || msg.Kind != SubAgentMailboxKindRiskAlert || msg.Priority != SubAgentMailboxPriorityInterrupt {
			t.Fatalf("mailbox = %#v, want interrupt risk alert", evt.Payload)
		}
		a.dispatch(evt)
	case <-time.After(time.Second):
		t.Fatal("terminal SubAgent error did not queue a mailbox")
	}

	if a.turn == nil {
		t.Fatal("risk alert mailbox did not wake MainAgent")
	}
	if len(a.pendingSubAgentMailboxes) != 1 || a.pendingSubAgentMailboxes[0].Kind != SubAgentMailboxKindRiskAlert {
		t.Fatalf("pending mailboxes = %#v, want one risk alert", a.pendingSubAgentMailboxes)
	}
	if rec := a.taskRecordByTaskID(sub.taskID); rec == nil || rec.State != string(SubAgentStateFailed) {
		t.Fatalf("task record = %#v, want failed", rec)
	}
	foundBlockedNotice := false
	for {
		select {
		case output := <-a.outputCh:
			notify, ok := output.(AgentNotifyEvent)
			if ok && notify.AgentID == sub.instanceID && notify.TargetAgentID == "main" && notify.Kind == string(SubAgentMailboxKindRiskAlert) && strings.Contains(notify.Message, "failed") {
				foundBlockedNotice = true
			}
		default:
			if !foundBlockedNotice {
				t.Fatal("terminal SubAgent error did not emit owner-visible risk notification")
			}
			return
		}
	}
}

func TestOwnedProgressMailboxPersistsDurableMetadata(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	owner := newControllableTestSubAgent(t, a, "adhoc-owner")
	owner.setState(SubAgentStateRunning, "running")
	mailbox := SubAgentMailboxMessage{
		MessageID:    "worker-child-1",
		AgentID:      "worker-child",
		TaskID:       "adhoc-child",
		OwnerAgentID: owner.instanceID,
		OwnerTaskID:  owner.taskID,
		Kind:         SubAgentMailboxKindProgress,
		Summary:      "halfway",
	}
	if !a.routeOwnedSubAgentMailbox(mailbox) {
		t.Fatal("routeOwnedSubAgentMailbox() = false")
	}
	owner.drainContextAppendsBeforeTurn()
	a.flushPersist()
	msgs := owner.ctxMgr.Snapshot()
	if len(msgs) != 1 || msgs[0].Kind != message.KindSubAgentMailbox || msgs[0].Mailbox == nil {
		t.Fatalf("owner context = %#v, want durable mailbox message", msgs)
	}
	if msgs[0].Mailbox.MessageID != mailbox.MessageID || msgs[0].Mailbox.Kind != string(SubAgentMailboxKindProgress) {
		t.Fatalf("mailbox metadata = %#v", msgs[0].Mailbox)
	}
	persisted, err := a.recoveryManager().LoadMessages(owner.instanceID)
	if err != nil {
		t.Fatalf("LoadMessages(owner): %v", err)
	}
	if len(persisted) != 1 || persisted[0].Mailbox == nil || persisted[0].Mailbox.MessageID != mailbox.MessageID {
		t.Fatalf("persisted owner context = %#v", persisted)
	}
}

// TestExpiredAgentRequestRejectsLateNotify pins the consumer-facing effect of
// the new "expired" ledger write: a reply to an expired request is refused with
// an explicit state instead of being treated as pending.
func TestExpiredAgentRequestRejectsLateNotify(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-expired-notify")
	request, err := a.createAgentRequest(sub, tools.AgentRequestPayload{Reason: "need a decision"})
	if err != nil {
		t.Fatalf("createAgentRequest: %v", err)
	}
	// Stand in for the expiry sweep's terminal settle (commitTerminalTask with
	// SubAgentStateCancelled), which is the only state under which the request
	// ledger moves to expired.
	a.handleSubAgentCloseRequestedEvent(Event{
		Type:     EventSubAgentCloseRequested,
		SourceID: sub.instanceID,
		Payload: &SubAgentCloseRequestedPayload{
			Reason:       "expired waiting for main reply",
			ClosedReason: "expired waiting for main reply",
			FinalState:   SubAgentStateCancelled,
		},
	})
	a.expireAgentRequestsAfterCancellation(sub.taskID)

	_, err = a.NotifySubAgentMessage(context.Background(), tools.AgentResponseRequest{
		TargetTaskID: sub.taskID, CorrelationID: request.CorrelationID, Message: "too late",
	})
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("late NotifySubAgentMessage error = %v, want expired-state rejection", err)
	}
}

// TestHandleAgentNotifyIgnoresLateNotifyForTerminalRuntime pins the terminal
// barrier for queued/late progress notices: once a durable terminal outcome is
// committed, the runtime mirrors it (commitTerminalTask flips the live runtime
// before updating the record), so a notify event dispatched afterwards must not
// resurrect the cancelled/completed attempt. Legal terminal reuse opens a new
// attempt through resetForAttempt/rehydration instead of a plain Running
// transition, so dropping here cannot block legitimate follow-up work.
func TestHandleAgentNotifyIgnoresLateNotifyForTerminalRuntime(t *testing.T) {
	for _, terminal := range []SubAgentState{SubAgentStateCancelled, SubAgentStateCompleted, SubAgentStateFailed} {
		t.Run(string(terminal), func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			sub := newControllableTestSubAgent(t, a, "adhoc-terminal-notify")
			sub.setState(terminal, "settled")
			a.syncTaskRecordFromSub(sub, "settled")
			if !isTerminalSubAgentState(sub.State()) {
				t.Fatalf("runtime state = %q, want terminal %q", sub.State(), terminal)
			}

			a.handleAgentNotify(Event{SourceID: sub.instanceID, Payload: tools.AgentNotifyPayload{Message: "late progress update"}})

			if got := sub.State(); got != terminal {
				t.Fatalf("runtime state after late notify = %q, want unchanged terminal %q", got, terminal)
			}
			select {
			case evt := <-a.eventCh:
				t.Fatalf("late notify queued a mailbox event for the settled runtime: %v", evt.Type)
			default:
			}
			if rec := a.taskRecordByTaskID(sub.taskID); rec == nil || SubAgentState(rec.State) != terminal {
				t.Fatalf("task record = %#v, want terminal %q", rec, terminal)
			}
		})
	}
}

// TestHandleAgentNotifyResumesWaitingForMainRuntime pins the legal side of the
// terminal barrier: a worker parked in waiting_main (or quiescent in idle)
// that reports continued progress must still resume to Running and queue its
// progress mailbox for the main inbox.
func TestHandleAgentNotifyResumesWaitingForMainRuntime(t *testing.T) {
	for _, resumed := range []SubAgentState{SubAgentStateWaitingMain, SubAgentStateIdle} {
		t.Run(string(resumed), func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			sub := newControllableTestSubAgent(t, a, "adhoc-resume-notify")
			sub.setState(resumed, "waiting on an owner decision")
			a.syncTaskRecordFromSub(sub, "")

			a.handleAgentNotify(Event{SourceID: sub.instanceID, Payload: tools.AgentNotifyPayload{Message: "owner approved; continuing"}})

			if got := sub.State(); got != SubAgentStateRunning {
				t.Fatalf("runtime state after notify = %q, want running", got)
			}
			deadline := time.After(time.Second)
			for {
				select {
				case evt := <-a.eventCh:
					mailbox, ok := evt.Payload.(*SubAgentMailboxMessage)
					if !ok || evt.Type != EventSubAgentMailbox {
						continue
					}
					if mailbox.Kind != SubAgentMailboxKindProgress || mailbox.Summary != "owner approved; continuing" {
						t.Fatalf("mailbox = %#v, want a progress update carrying the notify", mailbox)
					}
					return
				case <-deadline:
					t.Fatal("timed out waiting for the queued progress mailbox")
				}
			}
		})
	}
}
