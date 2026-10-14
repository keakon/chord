package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestHandleAgentDonePersistsCompletionMailboxBeforeTerminalCommit pins the
// durable ordering fix for the "settled but never notified" crash window: the
// completion mailbox must be persisted before the terminal commit, because a
// crash after the commit but before the mailbox delivery would otherwise leave
// a durable completed task whose owner never receives the completion. After
// handleAgentDone returns, the completion mailbox must already be in the
// mailbox log and the task record already terminal, even though the queued
// mailbox delivery event has not been dispatched yet. The later event only
// delivers the already-durable message and must not write a second copy.
func TestHandleAgentDonePersistsCompletionMailboxBeforeTerminalCommit(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(a.sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	sub := newControllableTestSubAgent(t, a, "adhoc-durable-first")
	a.newTurn("") // keep a busy turn so dispatching delivery does not start an LLM turn

	a.handleAgentDone(Event{
		Type:     EventAgentDone,
		SourceID: sub.instanceID,
		Payload:  &AgentResult{Summary: "durable-first done"},
	})

	msgs, err := loadSubAgentMailboxMessages(a.sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxMessages: %v", err)
	}
	mailbox := completedMailboxMessage(msgs, sub.taskID)
	if mailbox == nil || mailbox.Summary != "durable-first done" || mailbox.Completion == nil {
		t.Fatalf("completion mailbox not durable after handleAgentDone (before delivery), log=%#v", msgs)
	}
	rec := a.taskRecordByTaskID(sub.taskID)
	if rec == nil || rec.State != string(SubAgentStateCompleted) {
		t.Fatalf("task record after handleAgentDone = %#v, want terminal completed", rec)
	}

	dispatchQueuedEvents(t, a)

	rec = a.taskRecordByTaskID(sub.taskID)
	if rec == nil || rec.State != string(SubAgentStateCompleted) || rec.LastMailboxID != mailbox.MessageID {
		t.Fatalf("task record after delivery = %#v, want completed with LastMailboxID %q", rec, mailbox.MessageID)
	}
	if got := len(a.subAgentInbox.urgent); got != 1 {
		t.Fatalf("len(urgent inbox) after delivery = %d, want one completed mailbox", got)
	}
	if a.subAgentInbox.urgent[0].MessageID != mailbox.MessageID {
		t.Fatalf("delivered mailbox id = %q, want %q", a.subAgentInbox.urgent[0].MessageID, mailbox.MessageID)
	}
	raw, err := os.ReadFile(filepath.Join(a.sessionDir, "subagents", "mailbox.jsonl"))
	if err != nil {
		t.Fatalf("read mailbox log: %v", err)
	}
	entryCount := 0
	for line := range strings.SplitSeq(string(raw), "\n") {
		if strings.Contains(line, `"message_id":`) {
			entryCount++
		}
	}
	if entryCount != 1 {
		t.Fatalf("mailbox log entries = %d, want exactly one (delivery must not rewrite the message)", entryCount)
	}
}

// TestHandleAgentDoneTerminalCommitSurvivesMailboxPersistFailure pins the
// best-effort constraint of the ordering fix: when the completion mailbox
// cannot be persisted (mailbox.jsonl is blocked here), the terminal commit
// must still succeed — persistence failure never leaves the task stuck in a
// non-terminal state.
func TestHandleAgentDoneTerminalCommitSurvivesMailboxPersistFailure(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-best-effort")
	subagentsDir := filepath.Join(a.sessionDir, "subagents")
	if err := os.MkdirAll(subagentsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	blockedMailboxPath := filepath.Join(subagentsDir, "mailbox.jsonl")
	if err := os.Mkdir(blockedMailboxPath, 0o755); err != nil {
		t.Fatalf("block mailbox log with a directory: %v", err)
	}

	a.handleAgentDone(Event{
		Type:     EventAgentDone,
		SourceID: sub.instanceID,
		Payload:  &AgentResult{Summary: "best-effort done"},
	})

	rec := a.taskRecordByTaskID(sub.taskID)
	if rec == nil || rec.State != string(SubAgentStateCompleted) || !rec.RuntimeParked {
		t.Fatalf("task record after degraded mailbox persist = %#v, want parked completed task", rec)
	}
	onDisk, err := loadDurableTaskRecords(a.sessionDir)
	if err != nil {
		t.Fatalf("loadDurableTaskRecords: %v", err)
	}
	if got := onDisk[sub.taskID]; got == nil || got.State != string(SubAgentStateCompleted) {
		t.Fatalf("durable task record after degraded mailbox persist = %#v, want completed", got)
	}
}

// TestAgentDoneCompletionMailboxEventDeliversExactlyOnceWithoutRewriting pins
// the delivery semantics of the completion event queued by handleAgentDone
// after it persisted the completion mailbox ahead of the terminal commit:
// dispatching that event must deliver the already-durable message exactly once
// without persisting it a second time (the mailbox log keeps a single entry),
// and a repeated dispatch of the same event must not queue a second copy for
// the owner. The duplicate-restore side of the same discriminator (a message
// already queued by restore is dropped) is pinned by
// TestRestoredMailboxEventDeduplicatesQueuedMessageID.
func TestAgentDoneCompletionMailboxEventDeliversExactlyOnceWithoutRewriting(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(a.sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	sub := newControllableTestSubAgent(t, a, "adhoc-deliver-once")
	a.newTurn("") // keep a busy turn so dispatch does not auto-drain into a new LLM turn

	a.handleAgentDone(Event{
		Type:     EventAgentDone,
		SourceID: sub.instanceID,
		Payload:  &AgentResult{Summary: "deliver once done"},
	})

	var evt Event
	for {
		select {
		case queued := <-a.eventCh:
			evt = queued
		default:
			t.Fatal("expected completion mailbox event queued on eventCh")
		}
		if evt.Type == EventSubAgentMailbox {
			break
		}
	}
	mailbox, ok := evt.Payload.(*SubAgentMailboxMessage)
	if !ok || mailbox == nil {
		t.Fatalf("queued mailbox payload = %#v, want *SubAgentMailboxMessage", evt.Payload)
	}
	messageID := strings.TrimSpace(mailbox.MessageID)
	if messageID == "" {
		t.Fatal("completion mailbox event carries no MessageID")
	}
	// Precondition of the deliver-only dispatch: the message is already
	// durable (persisted by handleAgentDone) but not yet queued anywhere.
	if a.hasQueuedMailboxMessage(messageID) {
		t.Fatalf("completion message %q unexpectedly already queued before dispatch", messageID)
	}

	a.dispatch(evt)
	if got := len(a.subAgentInbox.urgent); got != 1 {
		t.Fatalf("len(urgent) after first dispatch = %d, want 1", got)
	}
	if got := a.subAgentInbox.urgent[0].MessageID; got != messageID {
		t.Fatalf("delivered mailbox id = %q, want %q", got, messageID)
	}

	// A repeated dispatch of the same event must not double-deliver: the
	// message is now queued, so the duplicate is dropped.
	a.dispatch(evt)
	if got := len(a.subAgentInbox.urgent); got != 1 {
		t.Fatalf("len(urgent) after re-dispatch = %d, want 1 (deliver-only dispatch must be exactly-once)", got)
	}

	raw, err := os.ReadFile(filepath.Join(a.sessionDir, "subagents", "mailbox.jsonl"))
	if err != nil {
		t.Fatalf("read mailbox log: %v", err)
	}
	entryCount := 0
	for line := range strings.SplitSeq(string(raw), "\n") {
		if strings.Contains(line, `"message_id":`) {
			entryCount++
		}
	}
	if entryCount != 1 {
		t.Fatalf("mailbox log entries = %d, want exactly one (neither dispatch may rewrite the message)", entryCount)
	}
}

// TestHandleAgentErrorPersistsRiskAlertMailboxBeforeTerminalCommit pins the
// durable ordering fix for the failure half of the "settled but never
// notified" crash window: the risk_alert mailbox must be persisted before the
// terminal commit, because a crash after the Failed commit but before the
// mailbox delivery would otherwise leave a durable failed task whose owner
// never receives the failure notification. After handleAgentError returns, the
// risk_alert mailbox must already be in the mailbox log and the task record
// already terminal Failed, and the queued mailbox delivery event must not
// write a second copy.
func TestHandleAgentErrorPersistsRiskAlertMailboxBeforeTerminalCommit(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(a.sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	sub := newControllableTestSubAgent(t, a, "adhoc-risk-durable-first")
	a.newTurn("") // keep a busy turn so dispatching delivery does not start an LLM turn

	a.handleAgentError(Event{Type: EventAgentError, SourceID: sub.instanceID, Payload: context.DeadlineExceeded})

	msgs, err := loadSubAgentMailboxMessages(a.sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxMessages: %v", err)
	}
	var alert *SubAgentMailboxMessage
	for i := range msgs {
		if msgs[i].Kind == SubAgentMailboxKindRiskAlert && strings.TrimSpace(msgs[i].TaskID) == sub.taskID {
			alert = &msgs[i]
		}
	}
	if alert == nil {
		t.Fatalf("risk_alert mailbox not durable after handleAgentError (before delivery), log=%#v", msgs)
	}
	rec := a.taskRecordByTaskID(sub.taskID)
	if rec == nil || rec.State != string(SubAgentStateFailed) {
		t.Fatalf("task record after handleAgentError = %#v, want terminal failed", rec)
	}
	if rec.LastMailboxID != alert.MessageID {
		t.Fatalf("task record LastMailboxID = %q, want risk_alert %q (mailbox must be persisted before the terminal commit)", rec.LastMailboxID, alert.MessageID)
	}

	dispatchQueuedEvents(t, a)

	raw, err := os.ReadFile(filepath.Join(a.sessionDir, "subagents", "mailbox.jsonl"))
	if err != nil {
		t.Fatalf("read mailbox log: %v", err)
	}
	entryCount := 0
	for line := range strings.SplitSeq(string(raw), "\n") {
		if strings.Contains(line, `"risk_alert"`) {
			entryCount++
		}
	}
	if entryCount != 1 {
		t.Fatalf("risk_alert mailbox log entries = %d, want exactly one (delivery must not rewrite the message)", entryCount)
	}
}

// TestWaitingMainExpiryPersistsRiskAlertMailboxBeforeTerminalCommit pins the
// same durable ordering for the live-worker branch of the WaitingMain expiry
// sweep: the sweep must persist the expiry risk_alert mailbox before its
// terminal Cancelled commit, so the owner notification cannot be lost to a
// crash between the commit and the queued mailbox delivery.
func TestWaitingMainExpiryPersistsRiskAlertMailboxBeforeTerminalCommit(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(a.sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	sub := newControllableTestSubAgent(t, a, "adhoc-expiry-durable")
	sub.setState(SubAgentStateWaitingMain, "need answer")
	a.noteSubAgentStateTransition(sub, SubAgentStateWaitingMain)
	a.waitingMainExpiry = waitingMainExpiryPolicy{turns: 1 << 30, minWait: time.Hour, maxWait: time.Nanosecond}
	a.mailboxDeliveryPaused.Store(true) // keep the expiry mailbox out of a live turn

	a.sweepSubAgentLifecycle()

	msgs, err := loadSubAgentMailboxMessages(a.sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxMessages: %v", err)
	}
	var alert *SubAgentMailboxMessage
	for i := range msgs {
		if msgs[i].Kind == SubAgentMailboxKindRiskAlert && strings.TrimSpace(msgs[i].TaskID) == sub.taskID {
			alert = &msgs[i]
		}
	}
	if alert == nil {
		t.Fatalf("expiry risk_alert mailbox not durable after the sweep (before delivery), log=%#v", msgs)
	}
	if !strings.Contains(alert.Summary, "expired waiting for main reply") {
		t.Fatalf("expiry risk_alert summary = %q, want the expiry reason", alert.Summary)
	}
	rec := a.taskRecordByTaskID(sub.taskID)
	if rec == nil || rec.State != string(SubAgentStateCancelled) {
		t.Fatalf("task record after expiry sweep = %#v, want terminal cancelled", rec)
	}
	if rec.LastMailboxID != alert.MessageID {
		t.Fatalf("task record LastMailboxID = %q, want expiry risk_alert %q (mailbox must be persisted before the terminal commit)", rec.LastMailboxID, alert.MessageID)
	}
}
