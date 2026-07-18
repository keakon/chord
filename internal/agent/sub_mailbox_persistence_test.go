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

func TestMailboxMemoryBudgetSpoolsAndRehydratesCriticalMessages(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.globalConfig.Orchestration.MailboxMemoryMessages = 1
	a.globalConfig.Orchestration.MailboxMemoryBytes = 1 << 20
	a.mailboxDeliveryPaused.Store(true)

	first := SubAgentMailboxMessage{MessageID: "msg-1", AgentID: "worker-1", TaskID: "task-1", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent, Summary: "first"}
	second := SubAgentMailboxMessage{MessageID: "msg-2", AgentID: "worker-2", TaskID: "task-2", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent, Summary: "second"}
	a.enqueueSubAgentMailbox(first)
	a.enqueueSubAgentMailbox(second)

	if got := len(a.subAgentInbox.urgent); got != 1 {
		t.Fatalf("urgent memory messages = %d, want 1", got)
	}
	if got := a.subAgentInbox.spoolUrgent; len(got) != 1 || got[0] != second.MessageID {
		t.Fatalf("urgent spool = %#v, want [%q]", got, second.MessageID)
	}
	if got := a.dequeueNextSubAgentMailbox(); got == nil || got.MessageID != first.MessageID {
		t.Fatalf("first dequeue = %#v", got)
	}
	if got := a.dequeueNextSubAgentMailbox(); got == nil || got.MessageID != second.MessageID {
		t.Fatalf("spooled dequeue = %#v", got)
	}
	stats := a.OrchestrationStats()
	if stats.MailboxSpoolQueued != 1 || stats.MailboxSpoolRehydrated != 1 {
		t.Fatalf("spool stats = %+v, want queued=1 rehydrated=1", stats)
	}
}

func TestSpooledMailboxReadFailureRetainsQueueForRetry(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.globalConfig.Orchestration.MailboxMemoryMessages = 1
	a.globalConfig.Orchestration.MailboxMemoryBytes = 1 << 20
	a.mailboxDeliveryPaused.Store(true)

	first := SubAgentMailboxMessage{MessageID: "msg-1", AgentID: "worker-1", TaskID: "task-1", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent}
	second := SubAgentMailboxMessage{MessageID: "msg-2", AgentID: "worker-2", TaskID: "task-2", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent}
	a.enqueueSubAgentMailbox(first)
	a.enqueueSubAgentMailbox(second)
	if got := a.dequeueNextSubAgentMailbox(); got == nil || got.MessageID != first.MessageID {
		t.Fatalf("first dequeue = %#v", got)
	}

	validSessionDir := a.sessionDir
	a.sessionDir = filepath.Join(t.TempDir(), "missing-session")
	if got := a.dequeueNextSubAgentMailbox(); got != nil {
		t.Fatalf("dequeue during read failure = %#v, want nil", got)
	}
	if got := a.subAgentInbox.spoolUrgent; len(got) != 1 || got[0] != second.MessageID {
		t.Fatalf("urgent spool after failure = %#v, want retained %q", got, second.MessageID)
	}

	a.sessionDir = validSessionDir
	if got := a.dequeueNextSubAgentMailbox(); got == nil || got.MessageID != second.MessageID {
		t.Fatalf("dequeue after recovery = %#v", got)
	}
}

func TestSpooledMailboxIndexRefreshesAfterAppend(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	first := SubAgentMailboxMessage{MessageID: "msg-1", AgentID: "worker-1", TaskID: "task-1", Kind: SubAgentMailboxKindCompleted}
	second := SubAgentMailboxMessage{MessageID: "msg-2", AgentID: "worker-2", TaskID: "task-2", Kind: SubAgentMailboxKindCompleted}
	if err := a.persistSubAgentMailboxMessage(first); err != nil {
		t.Fatalf("persist first: %v", err)
	}
	a.subAgentInbox.spoolNormal = []string{first.MessageID}
	if got := a.dequeueSpooledSubAgentMailbox(); got == nil || got.MessageID != first.MessageID {
		t.Fatalf("first spooled dequeue = %#v", got)
	}
	if err := a.persistSubAgentMailboxMessage(second); err != nil {
		t.Fatalf("persist second: %v", err)
	}
	a.subAgentInbox.spoolNormal = []string{second.MessageID}
	if got := a.dequeueSpooledSubAgentMailbox(); got == nil || got.MessageID != second.MessageID {
		t.Fatalf("second spooled dequeue = %#v", got)
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

func TestMailboxSpoolPreservesFIFOAcrossMemoryRecovery(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.globalConfig.Orchestration.MailboxMemoryMessages = 1
	a.globalConfig.Orchestration.MailboxMemoryBytes = 1 << 20
	a.mailboxDeliveryPaused.Store(true)

	first := SubAgentMailboxMessage{MessageID: "msg-1", AgentID: "worker-1", TaskID: "task-1", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent}
	second := SubAgentMailboxMessage{MessageID: "msg-2", AgentID: "worker-2", TaskID: "task-2", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent}
	third := SubAgentMailboxMessage{MessageID: "msg-3", AgentID: "worker-3", TaskID: "task-3", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent}
	a.enqueueSubAgentMailbox(first)
	a.enqueueSubAgentMailbox(second)

	// Drain the in-memory prefix so budget frees up while msg-2 is still
	// spooled; a newer arrival must queue behind it instead of jumping ahead.
	if got := a.dequeueNextSubAgentMailbox(); got == nil || got.MessageID != first.MessageID {
		t.Fatalf("first dequeue = %#v", got)
	}
	a.enqueueSubAgentMailbox(third)
	if got := len(a.subAgentInbox.urgent); got != 0 {
		t.Fatalf("urgent memory queue = %d entries, want 0 while older messages are spooled", got)
	}
	if got := a.dequeueNextSubAgentMailbox(); got == nil || got.MessageID != second.MessageID {
		t.Fatalf("second dequeue = %#v, want spooled msg-2 before newer msg-3", got)
	}
	if got := a.dequeueNextSubAgentMailbox(); got == nil || got.MessageID != third.MessageID {
		t.Fatalf("third dequeue = %#v", got)
	}
}

func TestProgressMailboxKeepsLastKnownStatusWhenBudgetExhausted(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.globalConfig.Orchestration.MailboxMemoryMessages = 1
	a.globalConfig.Orchestration.MailboxMemoryBytes = 1 << 20
	a.mailboxDeliveryPaused.Store(true)

	progress := SubAgentMailboxMessage{MessageID: "p-1", AgentID: "worker-1", TaskID: "task-1", Kind: SubAgentMailboxKindProgress, Summary: "step 1"}
	a.enqueueSubAgentMailbox(progress)
	if got := a.subAgentInbox.progress["worker-1"]; got.MessageID != "p-1" {
		t.Fatalf("initial progress = %#v", got)
	}

	// Replacing the only tracked snapshot stays within the message budget.
	update := SubAgentMailboxMessage{MessageID: "p-2", AgentID: "worker-1", TaskID: "task-1", Kind: SubAgentMailboxKindProgress, Summary: "step 2"}
	a.enqueueSubAgentMailbox(update)
	if got := a.subAgentInbox.progress["worker-1"]; got.MessageID != "p-2" {
		t.Fatalf("replaced progress = %#v, want p-2", got)
	}

	// An update that no longer fits must keep the previous snapshot instead of
	// dropping both the old and the new status.
	a.globalConfig.Orchestration.MailboxMemoryBytes = 1
	oversized := SubAgentMailboxMessage{MessageID: "p-3", AgentID: "worker-1", TaskID: "task-1", Kind: SubAgentMailboxKindProgress, Summary: strings.Repeat("x", 256)}
	a.enqueueSubAgentMailbox(oversized)
	if got := a.subAgentInbox.progress["worker-1"]; got.MessageID != "p-2" {
		t.Fatalf("progress after oversized update = %#v, want retained p-2", got)
	}
}
