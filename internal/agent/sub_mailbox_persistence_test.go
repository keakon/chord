package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersistSubAgentMailboxMessageReportsOpenFailure(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	blockedRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a.sessionDir = blockedRoot

	err := a.persistSubAgentMailboxMessage(SubAgentMailboxMessage{MessageID: "msg-1", TaskID: "task-1"})
	if err == nil {
		t.Fatal("persistSubAgentMailboxMessage() error = nil, want open failure")
	}
}

func TestMailboxPersistenceFailureDefersCriticalDeliveryUntilRetrySucceeds(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	validSessionDir := a.sessionDir
	blockedRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a.sessionDir = blockedRoot
	msg := SubAgentMailboxMessage{
		AgentID:  "worker-1",
		TaskID:   "task-1",
		Kind:     SubAgentMailboxKindCompleted,
		Priority: SubAgentMailboxPriorityUrgent,
		Summary:  "done",
	}

	a.enqueueSubAgentMailbox(msg)
	if len(a.subAgentInbox.urgent) != 1 || !a.subAgentInbox.urgent[0].persistPending {
		t.Fatalf("urgent inbox = %#v, want pending durable message", a.subAgentInbox.urgent)
	}
	if a.stageNextSubAgentMailboxBatch() {
		t.Fatal("mailbox became deliverable while persistence still failed")
	}
	if len(a.subAgentInbox.urgent) != 1 {
		t.Fatalf("urgent inbox length = %d, want retained message", len(a.subAgentInbox.urgent))
	}

	a.sessionDir = validSessionDir
	if !a.stageNextSubAgentMailboxBatch() {
		t.Fatal("mailbox did not become deliverable after persistence recovered")
	}
	if a.activeSubAgentMailbox == nil || a.activeSubAgentMailbox.persistPending {
		t.Fatalf("active mailbox = %#v, want durable message", a.activeSubAgentMailbox)
	}
}

func TestMailboxAckFailureDoesNotMarkMessageConsumed(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	blockedRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a.sessionDir = blockedRoot

	if err := a.markSubAgentMailboxConsumed("msg-1"); err == nil {
		t.Fatal("markSubAgentMailboxConsumed() error = nil, want open failure")
	}
	if a.isSubAgentMailboxConsumed("msg-1") {
		t.Fatal("mailbox was marked consumed despite ack persistence failure")
	}
}

func TestMailboxAckFailureRequeuesActiveMailbox(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	blockedRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a.sessionDir = blockedRoot
	a.activeSubAgentMailboxes = []*SubAgentMailboxMessage{{
		MessageID: "msg-1",
		AgentID:   "worker-1",
		TaskID:    "task-1",
		Kind:      SubAgentMailboxKindCompleted,
		Priority:  SubAgentMailboxPriorityUrgent,
	}}
	a.activeSubAgentMailbox = a.activeSubAgentMailboxes[0]
	a.activeSubAgentMailboxAck = true

	a.setIdleAndDrainPending()

	if len(a.subAgentInbox.urgent) != 1 || a.subAgentInbox.urgent[0].MessageID != "msg-1" {
		t.Fatalf("urgent inbox = %#v, want failed ack mailbox requeued", a.subAgentInbox.urgent)
	}
	if a.isSubAgentMailboxConsumed("msg-1") {
		t.Fatal("mailbox was marked consumed despite failed ack")
	}
}

func TestNotifyAckFailureRequeuesOutstandingMailbox(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "task-1")
	sub.setState(SubAgentStateWaitingMain, "need decision")
	msg := SubAgentMailboxMessage{
		MessageID: "msg-1",
		AgentID:   sub.instanceID,
		TaskID:    sub.taskID,
		Kind:      SubAgentMailboxKindDecisionRequired,
		Priority:  SubAgentMailboxPriorityUrgent,
	}
	a.subAgentInbox.urgent = []SubAgentMailboxMessage{msg}
	sub.setLastMailboxID(msg.MessageID)
	blockedRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a.sessionDir = blockedRoot

	if _, err := a.NotifySubAgent(context.Background(), sub.taskID, "continue", "reply"); err == nil {
		t.Fatal("NotifySubAgent() error = nil, want ack persistence failure")
	}
	if len(a.subAgentInbox.urgent) != 1 || a.subAgentInbox.urgent[0].MessageID != msg.MessageID {
		t.Fatalf("urgent inbox = %#v, want outstanding mailbox restored", a.subAgentInbox.urgent)
	}
	if sub.State() != SubAgentStateWaitingMain {
		t.Fatalf("sub.State() = %q, want waiting_main", sub.State())
	}
	if held, _ := sub.slotState(); held {
		t.Fatal("worker retained slot after failed Notify")
	}
}

func TestNotifyDefersAckUntilPendingMailboxPersists(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "task-1")
	sub.setState(SubAgentStateWaitingMain, "need decision")
	msg := SubAgentMailboxMessage{
		MessageID:      "msg-1",
		AgentID:        sub.instanceID,
		TaskID:         sub.taskID,
		Kind:           SubAgentMailboxKindDecisionRequired,
		Priority:       SubAgentMailboxPriorityUrgent,
		persistPending: true,
	}
	a.subAgentInbox.urgent = []SubAgentMailboxMessage{msg}
	sub.setLastMailboxID(msg.MessageID)
	blockedRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a.sessionDir = blockedRoot

	if _, err := a.NotifySubAgent(context.Background(), sub.taskID, "continue", "reply"); err == nil || !strings.Contains(err.Error(), "persist target mailbox") {
		t.Fatalf("NotifySubAgent error = %v, want pending mailbox persistence failure", err)
	}
	if a.isSubAgentMailboxConsumed(msg.MessageID) {
		t.Fatal("pending mailbox was acknowledged before message persistence")
	}
	if len(a.subAgentInbox.urgent) != 1 || !a.subAgentInbox.urgent[0].persistPending {
		t.Fatalf("urgent inbox = %#v, want pending mailbox retained", a.subAgentInbox.urgent)
	}
}
