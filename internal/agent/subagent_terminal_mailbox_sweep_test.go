// 周期 sweep 兜底回收滞留 owned mailbox 的回归测试。
package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLifecycleSweepCandidateStaysAliveForStrandedOwnedTerminalMailbox pins
// the trigger half of the stranded-owner gap: once every worker is terminal, a
// completion mailbox stranded in a finished owner's owned queue is the only
// outstanding work left. The periodic trigger must keep offering the sweep
// (which drains it) instead of stopping, so a silent all-terminal session does
// not wait for the next user message to route the mailbox.
func TestLifecycleSweepCandidateStaysAliveForStrandedOwnedTerminalMailbox(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())

	// Quiet baseline: a session that never delegated must not be a sweep
	// candidate, so the periodic trigger does not wake an idle main.
	a.emitGlobalIdleIfReady()
	if !a.globalIdle.Load() {
		t.Fatal("empty session must be globally idle")
	}
	if a.hasSubAgentLifecycleSweepCandidates() {
		t.Fatal("empty session must not be a lifecycle sweep candidate")
	}

	// All workers finished; the only residue is the child completion queued in
	// the terminal owner's owned mailbox. No live runtime, no waiting_main, no
	// Running worker remains to fire an event on its own.
	ownerInstanceID, ownerTaskID := "owner-terminal-1", "owner-terminal-task-1"
	childInstanceID, childTaskID := "child-late-1", "child-late-task-1"
	messageID := "mailbox-owned-terminal-1"
	seedTerminalOwnerAndLateChild(a, ownerInstanceID, ownerTaskID, childInstanceID, childTaskID)
	a.enqueueOwnedSubAgentMailbox(lateChildCompletion(ownerInstanceID, ownerTaskID, childInstanceID, childTaskID, messageID))

	if a.hasWaitingMainExpiryCandidates() {
		t.Fatal("terminal task records must not be waiting_main expiry candidates")
	}
	if a.hasActiveSubAgentWork() {
		t.Fatal("terminal parked task records must not count as active subagent work")
	}
	// The stranded completion is routable mailbox work (its owner is terminal,
	// so the drain would forward it to the main inbox), which keeps the loop
	// from reporting global idle at the dispatch that produced it.
	if !a.hasRunnableMailboxWork() {
		t.Fatal("owned terminal completion must be runnable mailbox work")
	}
	a.globalIdle.Store(false) // mirrors the loop's not-idle decision while the mailbox pends

	if !a.hasSubAgentLifecycleSweepCandidates() {
		t.Fatal("periodic sweep must stay armed while only a stranded owned terminal mailbox remains")
	}
}

// TestLifecycleSweepDrainsStrandedOwnedTerminalMailbox pins the disposition
// half of the stranded-owner gap: dispatching the periodic sweep drains the completion
// stranded in a terminal owner's owned queue and forwards it to the main inbox
// (the conservative terminal-owner mailbox semantic: route to main, never drop
// or kill), exactly as the existing expiry/stall risk alerts are surfaced.
func TestLifecycleSweepDrainsStrandedOwnedTerminalMailbox(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(a.sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	ownerInstanceID, ownerTaskID := "owner-terminal-2", "owner-terminal-task-2"
	childInstanceID, childTaskID := "child-late-2", "child-late-task-2"
	messageID := "mailbox-owned-terminal-2"
	seedTerminalOwnerAndLateChild(a, ownerInstanceID, ownerTaskID, childInstanceID, childTaskID)

	// The completion is stranded in the owner's owned queue; the main inbox is
	// still empty because nothing has drained it yet.
	a.enqueueOwnedSubAgentMailbox(lateChildCompletion(ownerInstanceID, ownerTaskID, childInstanceID, childTaskID, messageID))
	if queued := a.ownedSubAgentMailboxes[ownerInstanceID]; len(queued) != 1 || queued[0].MessageID != messageID {
		t.Fatalf("owned completion queue = %#v, want the stranded completion", queued)
	}
	if mainInboxMailbox(a, messageID) != nil {
		t.Fatal("stranded completion must not already be in the main inbox")
	}

	// Keep a busy main turn so dispatching the sweep drains into the inbox
	// without auto-starting an LLM turn on the staged mailbox.
	a.newTurn()
	a.dispatch(Event{Type: EventSubAgentLifecycleSweep})

	if queued := a.ownedSubAgentMailboxes[ownerInstanceID]; len(queued) != 0 {
		t.Fatalf("owned completion queue = %#v, want empty after the periodic sweep", queued)
	}
	forwarded := mainInboxMailbox(a, messageID)
	if forwarded == nil {
		t.Fatalf("completion %q was neither dropped nor routed: urgent=%#v normal=%#v", messageID, a.subAgentInbox.urgent, a.subAgentInbox.normal)
	}
	if forwarded.OwnerAgentID != "" || forwarded.OwnerTaskID != "" {
		t.Fatalf("forwarded completion owner = (%q,%q), want main-owned empty owner", forwarded.OwnerAgentID, forwarded.OwnerTaskID)
	}
	if rec := a.taskRecordByTaskID(ownerTaskID); rec == nil || rec.State != string(SubAgentStateCompleted) {
		t.Fatalf("owner task record = %#v, want untouched terminal owner", rec)
	}
	if rec := a.taskRecordByTaskID(childTaskID); rec == nil || rec.State != string(SubAgentStateCompleted) {
		t.Fatalf("child task record = %#v, want untouched terminal child", rec)
	}
}

// TestLifecycleSweepLeavesUnroutableOwnedMailboxUntouched pins the boundary
// the drain must not cross: a mailbox that routing refuses on every attempt
// (a foreign completion under a parked owner that only its own descendant may
// wake) is not runnable work, so it must not keep the periodic sweep armed,
// and a dispatched sweep must leave it queued instead of dropping or forcing
// it onto the main inbox.
func TestLifecycleSweepLeavesUnroutableOwnedMailboxUntouched(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	ownerTaskID := "parked-idle-owner-task"
	ownerInstanceID := "parked-idle-owner"
	a.subs.mu.Lock()
	a.subs.taskRecords[ownerTaskID] = &DurableTaskRecord{
		TaskID:            ownerTaskID,
		AgentDefName:      "worker",
		State:             string(SubAgentStateIdle),
		ResumePolicy:      taskResumePolicyNotify,
		LatestInstanceID:  ownerInstanceID,
		InstanceHistory:   []string{ownerInstanceID},
		RuntimeParked:     true,
		Attempt:           1,
		SettlementDurable: true,
	}
	a.subs.mu.Unlock()

	msg := SubAgentMailboxMessage{
		MessageID:    "mailbox-foreign-1",
		AgentID:      "worker-unrelated",
		TaskID:       "foreign-child-task",
		OwnerAgentID: ownerInstanceID,
		OwnerTaskID:  ownerTaskID,
		Kind:         SubAgentMailboxKindCompleted,
		Priority:     SubAgentMailboxPriorityUrgent,
		Summary:      "a child of a different owner completed",
		Payload:      "a child of a different owner completed",
	}
	a.enqueueOwnedSubAgentMailbox(msg)

	// Unroutable residue is not runnable work and must not be a sweep
	// candidate, so the periodic trigger stays silent for it.
	if a.hasRunnableMailboxWork() {
		t.Fatal("foreign completion of a parked idle owner must not be runnable mailbox work")
	}
	a.emitGlobalIdleIfReady()
	if !a.globalIdle.Load() {
		t.Fatal("a parked owner with an unroutable mailbox must still reach global idle")
	}
	if a.hasSubAgentLifecycleSweepCandidates() {
		t.Fatal("unroutable owned mailbox must not keep the periodic sweep armed")
	}

	// Even a dispatched sweep leaves the message queued; nothing is dropped or
	// force-forwarded.
	a.newTurn()
	a.dispatch(Event{Type: EventSubAgentLifecycleSweep})
	if queued := a.ownedSubAgentMailboxes[ownerInstanceID]; len(queued) != 1 || queued[0].MessageID != msg.MessageID {
		t.Fatalf("owned completion queue = %#v, want the unroutable message untouched", queued)
	}
	if mainInboxMailbox(a, msg.MessageID) != nil {
		t.Fatal("unroutable owned mailbox must not be forwarded to the main inbox")
	}
}

// seedTerminalOwnerAndLateChild pins two durable task records: an owner that
// finished before its child did, and the child that completed after its owner
// was already terminal. The child's completion is the mailbox that would be
// stranded under the terminal owner's queue.
// TestForwardToMainSettledOwnerPersistsSingleMailboxRow pins that forwarding a
// settled owner's mailbox to the main inbox does not write the durable record
// again: the record was persisted before it entered the owned queue, so routing
// it with deliverSubAgentMailbox (which never writes) must leave exactly one
// mailbox.jsonl row for its MessageID while still delivering it exactly once.
func TestForwardToMainSettledOwnerPersistsSingleMailboxRow(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	ownerInstanceID, ownerTaskID := "owner-forward-1", "owner-forward-task-1"
	childInstanceID, childTaskID := "child-forward-1", "child-forward-task-1"
	seedTerminalOwnerAndLateChild(a, ownerInstanceID, ownerTaskID, childInstanceID, childTaskID)
	const messageID = "mailbox-forward-1"
	a.enqueueSubAgentMailbox(lateChildCompletion(ownerInstanceID, ownerTaskID, childInstanceID, childTaskID, messageID))

	if rows := countMailboxLogRows(t, a.sessionDir, messageID); rows != 1 {
		t.Fatalf("mailbox.jsonl rows for %q = %d, want exactly 1 (no duplicate row from the forward)", messageID, rows)
	}
	delivered := 0
	for _, msg := range mainInboxMailboxMessages(a) {
		if msg.MessageID == messageID {
			delivered++
		}
	}
	if delivered != 1 {
		t.Fatalf("main inbox copies of %q = %d, want exactly 1", messageID, delivered)
	}
	forwarded := mainInboxMailbox(a, messageID)
	if forwarded == nil {
		t.Fatal("forwarded completion is missing from the main inbox")
	}
	if forwarded.OwnerAgentID != "" || forwarded.OwnerTaskID != "" {
		t.Fatalf("forwarded completion owner = (%q,%q), want main-owned empty owner", forwarded.OwnerAgentID, forwarded.OwnerTaskID)
	}
}

func countMailboxLogRows(t *testing.T, sessionDir, messageID string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(sessionDir, "subagents", "mailbox.jsonl"))
	if err != nil {
		t.Fatalf("ReadFile(mailbox.jsonl): %v", err)
	}
	rows := 0
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var msg SubAgentMailboxMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("decode mailbox row: %v", err)
		}
		if strings.TrimSpace(msg.MessageID) == messageID {
			rows++
		}
	}
	return rows
}

func seedTerminalOwnerAndLateChild(a *MainAgent, ownerInstanceID, ownerTaskID, childInstanceID, childTaskID string) {
	a.subs.mu.Lock()
	a.subs.taskRecords[ownerTaskID] = &DurableTaskRecord{
		TaskID:            ownerTaskID,
		AgentDefName:      "worker",
		State:             string(SubAgentStateCompleted),
		ResumePolicy:      taskResumePolicyExplicitOnly,
		LatestInstanceID:  ownerInstanceID,
		InstanceHistory:   []string{ownerInstanceID},
		RuntimeParked:     true,
		Attempt:           1,
		SettlementDurable: true,
	}
	a.subs.taskRecords[childTaskID] = &DurableTaskRecord{
		TaskID:            childTaskID,
		AgentDefName:      "worker",
		State:             string(SubAgentStateCompleted),
		ResumePolicy:      taskResumePolicyExplicitOnly,
		OwnerAgentID:      ownerInstanceID,
		OwnerTaskID:       ownerTaskID,
		LatestInstanceID:  childInstanceID,
		InstanceHistory:   []string{childInstanceID},
		RuntimeParked:     true,
		Attempt:           1,
		SettlementDurable: true,
	}
	a.subs.mu.Unlock()
}

func lateChildCompletion(ownerInstanceID, ownerTaskID, childInstanceID, childTaskID, messageID string) SubAgentMailboxMessage {
	return SubAgentMailboxMessage{
		MessageID:    messageID,
		AgentID:      childInstanceID,
		TaskID:       childTaskID,
		OwnerAgentID: ownerInstanceID,
		OwnerTaskID:  ownerTaskID,
		Kind:         SubAgentMailboxKindCompleted,
		Priority:     SubAgentMailboxPriorityUrgent,
		Summary:      "child task completed after its owner was gone",
		Payload:      "child task completed after its owner was gone",
	}
}

func mainInboxMailbox(a *MainAgent, messageID string) *SubAgentMailboxMessage {
	for i := range a.subAgentInbox.urgent {
		if a.subAgentInbox.urgent[i].MessageID == messageID {
			return &a.subAgentInbox.urgent[i]
		}
	}
	for i := range a.subAgentInbox.normal {
		if a.subAgentInbox.normal[i].MessageID == messageID {
			return &a.subAgentInbox.normal[i]
		}
	}
	return nil
}
