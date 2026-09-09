package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/message"
)

// seedMailboxRuntimeResidue populates every in-memory mailbox queue a busy
// main turn can leave behind: main-inbox urgent/normal/progress entries,
// durable-spool id queues, per-owner queues and their spool, a staged/active
// batch, the idempotency set, and the mailbox memory budget.
func seedMailboxRuntimeResidue(t *testing.T, a *MainAgent) {
	t.Helper()
	normal := SubAgentMailboxMessage{
		MessageID: "mail-normal-1", AgentID: "worker-1", TaskID: "task-1",
		Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent,
		Summary: "queued completion",
	}
	progress := SubAgentMailboxMessage{
		MessageID: "mail-progress-1", AgentID: "worker-2", TaskID: "task-2",
		Kind: SubAgentMailboxKindProgress, Priority: SubAgentMailboxPriorityNotify,
		Summary: "stale progress",
	}
	owned := SubAgentMailboxMessage{
		MessageID: "mail-owned-1", AgentID: "child-1", TaskID: "task-child-1",
		OwnerAgentID: "owner-1", OwnerTaskID: "task-owner-1",
		Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent,
		Summary: "child completion",
	}
	active := SubAgentMailboxMessage{
		MessageID: "mail-active-1", AgentID: "worker-3", TaskID: "task-3",
		Kind: SubAgentMailboxKindRiskAlert, Priority: SubAgentMailboxPriorityInterrupt,
		Summary: "staged alert",
	}
	a.subAgentMailboxIDsMu.Lock()
	defer a.subAgentMailboxIDsMu.Unlock()
	if a.subAgentInbox.progress == nil {
		a.subAgentInbox.progress = make(map[string]SubAgentMailboxMessage)
	}
	if a.subAgentMailboxIDs == nil {
		a.subAgentMailboxIDs = make(map[string]struct{})
	}
	a.subAgentInbox.urgent = append(a.subAgentInbox.urgent, active)
	a.subAgentInbox.normal = append(a.subAgentInbox.normal, normal)
	a.subAgentInbox.memoryBytes += mailboxMessageBytes(active) + mailboxMessageBytes(normal)
	a.subAgentInbox.progress[progress.AgentID] = progress
	a.subAgentInbox.memoryBytes += mailboxMessageBytes(progress)
	a.subAgentInbox.spoolUrgent = append(a.subAgentInbox.spoolUrgent, "mail-spool-1")
	a.ownedSubAgentMailboxes = map[string][]SubAgentMailboxMessage{}
	a.ownedSubAgentMailboxes[owned.OwnerAgentID] = []SubAgentMailboxMessage{owned}
	a.subAgentInbox.memoryBytes += mailboxMessageBytes(owned)
	a.ownedMailboxSpool = map[string][]string{owned.OwnerAgentID: {"mail-owned-spool-1"}}
	staged := active
	a.pendingSubAgentMailboxes = []*SubAgentMailboxMessage{&staged}
	a.activeSubAgentMailboxes = []*SubAgentMailboxMessage{&staged}
	a.activeSubAgentMailbox = &staged
	a.activeSubAgentMailboxAck = true
	a.subAgentMailboxIDs[normal.MessageID] = struct{}{}
}

// assertEmptySubAgentMailboxRuntime fails when any in-memory mailbox pipeline
// state survives a session boundary.
func assertEmptySubAgentMailboxRuntime(t *testing.T, a *MainAgent) {
	t.Helper()
	a.subAgentMailboxIDsMu.Lock()
	defer a.subAgentMailboxIDsMu.Unlock()
	if len(a.subAgentInbox.urgent) != 0 || len(a.subAgentInbox.normal) != 0 || len(a.subAgentInbox.progress) != 0 ||
		len(a.subAgentInbox.spoolUrgent) != 0 || len(a.subAgentInbox.spoolNormal) != 0 ||
		len(a.ownedSubAgentMailboxes) != 0 || len(a.ownedMailboxSpool) != 0 ||
		len(a.pendingSubAgentMailboxes) != 0 || len(a.activeSubAgentMailboxes) != 0 ||
		a.activeSubAgentMailbox != nil || a.subAgentInbox.memoryBytes != 0 ||
		len(a.subAgentMailboxIDs) != 0 || len(a.subAgentMailboxConsumed) != 0 {
		t.Fatalf("mailbox runtime not empty after session switch: urgent=%v normal=%v progress=%v spool=%v/%v owned=%v/%v pending=%v active=%v head=%v mem=%d seen=%v consumed=%v",
			len(a.subAgentInbox.urgent), len(a.subAgentInbox.normal), len(a.subAgentInbox.progress),
			a.subAgentInbox.spoolUrgent, a.subAgentInbox.spoolNormal,
			a.ownedSubAgentMailboxes, a.ownedMailboxSpool,
			len(a.pendingSubAgentMailboxes), len(a.activeSubAgentMailboxes), a.activeSubAgentMailbox,
			a.subAgentInbox.memoryBytes, a.subAgentMailboxIDs, a.subAgentMailboxConsumed)
	}
}

// TestNewSessionCommandDropsBusyMailboxRuntimeResidue pins the /new half of
// the cross-session mailbox leak: a main turn that is busy when the user runs
// /new can leave mailbox messages queued (main-inbox queues, per-owner queues,
// a staged/active batch, spool ids, and their memory budget). Without the
// reset, the first idle drain of the new session would stage that residue and
// auto-start a new-session turn, and the same messages would later be replayed
// again from the old session's mailbox log on resume. After /new the new
// session must start with an empty mailbox pipeline, must not auto-start a
// turn, and the old session's durable mailbox rows must survive for its own
// resume replay.
func TestNewSessionCommandDropsBusyMailboxRuntimeResidue(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	oldSessionDir := a.sessionDir
	if err := os.MkdirAll(filepath.Join(oldSessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(old subagents): %v", err)
	}
	// A completion durably recorded in the old session before the switch: it
	// belongs to the old session's mailbox-log replay, never to the new
	// session's in-memory pipeline.
	durable := SubAgentMailboxMessage{
		MessageID: "mail-durable-1", AgentID: "worker-1", TaskID: "task-1",
		Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent,
		Summary: "old session completion",
	}
	if err := a.persistSubAgentMailboxMessage(durable); err != nil {
		t.Fatalf("persistSubAgentMailboxMessage: %v", err)
	}

	seedMailboxRuntimeResidue(t, a)
	a.subAgentMailboxIDsMu.Lock()
	seeded := len(a.subAgentInbox.normal) > 0 && len(a.subAgentInbox.progress) > 0 &&
		len(a.ownedSubAgentMailboxes) > 0 && len(a.pendingSubAgentMailboxes) > 0 &&
		a.activeSubAgentMailbox != nil && a.subAgentInbox.memoryBytes > 0
	a.subAgentMailboxIDsMu.Unlock()
	if !seeded {
		t.Fatal("seed did not populate the busy-session mailbox runtime")
	}

	a.handleNewSessionCommand()

	if a.sessionDir == oldSessionDir {
		t.Fatal("sessionDir was not switched")
	}
	assertEmptySubAgentMailboxRuntime(t, a)
	// The new session must not auto-start a main turn from the old session's
	// residue: an idle drain right after the switch stages nothing.
	if a.currentTurn() != nil {
		t.Fatal("session switch left a live main turn")
	}
	a.drainSubAgentInbox()
	if a.currentTurn() != nil {
		t.Fatal("old-session mailbox residue staged a new-session main turn")
	}
	// The old session's durable row survives for its own resume replay, while
	// the new session has no mailbox log of its own.
	oldMsgs, err := loadSubAgentMailboxMessages(oldSessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxMessages(old): %v", err)
	}
	if len(oldMsgs) != 1 || oldMsgs[0].MessageID != "mail-durable-1" {
		t.Fatalf("old session mailbox log = %#v, want the durable completion retained", oldMsgs)
	}
	if _, err := os.Stat(filepath.Join(a.sessionDir, "subagents", "mailbox.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("new session mailbox log exists (stat err=%v), want absent", err)
	}
}

// TestForkSessionCommandDropsBusyMailboxRuntimeResidue pins the fork half of
// the cross-session mailbox leak: forking a new session from a busy main turn
// must drop the replaced session's queued mailbox state instead of letting the
// forked session's first idle drain stage it into a new turn.
func TestForkSessionCommandDropsBusyMailboxRuntimeResidue(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	a.ctxMgr.RestoreMessages([]message.Message{
		{Role: message.RoleUser, Content: "history one"},
		{Role: message.RoleAssistant, Content: "history reply"},
		{Role: message.RoleUser, Content: "fork from here"},
		{Role: message.RoleAssistant, Content: "tail reply"},
	})
	oldSessionDir := a.sessionDir
	seedMailboxRuntimeResidue(t, a)

	a.handleForkSessionCommand(2)

	if a.sessionDir == oldSessionDir {
		t.Fatal("fork did not switch to a new session")
	}
	assertEmptySubAgentMailboxRuntime(t, a)
	if a.currentTurn() != nil {
		t.Fatal("fork left a live main turn")
	}
	a.drainSubAgentInbox()
	if a.currentTurn() != nil {
		t.Fatal("old-session mailbox residue staged a forked-session main turn")
	}
}

// TestRemoveSubAgentMailboxStateReleasesProgressMemory pins the progress half
// of the mailbox memory-accounting leak: dropping a closed agent's per-agent
// progress snapshot must release the snapshot's memory-budget bytes exactly
// like the urgent/normal queue entries and the per-owner queue entries do, so
// repeated delegate/close cycles never inflate memoryBytes.
func TestRemoveSubAgentMailboxStateReleasesProgressMemory(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	agentID := "worker-progress-1"
	progress := SubAgentMailboxMessage{
		MessageID: "mail-progress", AgentID: agentID, TaskID: "task-progress",
		Kind: SubAgentMailboxKindProgress, Priority: SubAgentMailboxPriorityNotify,
		Summary: "progress sample",
	}
	urgent := SubAgentMailboxMessage{
		MessageID: "mail-urgent", AgentID: agentID, TaskID: "task-progress",
		Kind: SubAgentMailboxKindRiskAlert, Priority: SubAgentMailboxPriorityInterrupt,
		Summary: "alert sample",
	}
	a.subAgentMailboxIDsMu.Lock()
	if a.subAgentInbox.progress == nil {
		a.subAgentInbox.progress = make(map[string]SubAgentMailboxMessage)
	}
	a.subAgentInbox.progress[agentID] = progress
	a.subAgentInbox.urgent = append(a.subAgentInbox.urgent, urgent)
	a.subAgentInbox.memoryBytes += mailboxMessageBytes(progress) + mailboxMessageBytes(urgent)
	a.subAgentMailboxIDsMu.Unlock()

	for range 3 {
		a.removeSubAgentMailboxState(agentID)
	}

	a.subAgentMailboxIDsMu.Lock()
	defer a.subAgentMailboxIDsMu.Unlock()
	if len(a.subAgentInbox.progress) != 0 || len(a.subAgentInbox.urgent) != 0 {
		t.Fatalf("queue residue after close: progress=%#v urgent=%#v", a.subAgentInbox.progress, a.subAgentInbox.urgent)
	}
	if a.subAgentInbox.memoryBytes != 0 {
		t.Fatalf("memoryBytes after close = %d, want 0 (progress snapshot must release its bytes)", a.subAgentInbox.memoryBytes)
	}
}
