package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArchiveEligibleTerminalTasksPreservesUnsafeRecords(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	now := time.Now()
	records := make(map[string]*DurableTaskRecord)
	for i := range maxRetainedTerminalTasks + 2 {
		taskID := fmt.Sprintf("terminal-%03d", i)
		records[taskID] = &DurableTaskRecord{
			TaskID:            taskID,
			State:             string(SubAgentStateCancelled),
			ResumePolicy:      taskResumePolicyExplicitOnly,
			SettlementDurable: true,
			UpdatedAt:         now.Add(time.Duration(i) * time.Second),
		}
	}
	records["active"] = &DurableTaskRecord{TaskID: "active", State: string(SubAgentStateRunning), UpdatedAt: now}
	records["focused"] = &DurableTaskRecord{TaskID: "focused", State: string(SubAgentStateCancelled), ResumePolicy: taskResumePolicyExplicitOnly, UpdatedAt: now.Add(-time.Hour)}
	records["unconsumed"] = &DurableTaskRecord{TaskID: "unconsumed", State: string(SubAgentStateCancelled), ResumePolicy: taskResumePolicyExplicitOnly, LastMailboxID: "pending-mailbox", UpdatedAt: now.Add(-time.Hour)}
	records["non-durable"] = &DurableTaskRecord{TaskID: "non-durable", State: string(SubAgentStateCompleted), ResumePolicy: taskResumePolicyNotify, UpdatedAt: now.Add(-2 * time.Hour)}
	a.setFocusedTaskID("focused")

	retained, err := a.archiveEligibleTerminalTasks(records, a.sessionDir)
	if err != nil {
		t.Fatalf("archiveEligibleTerminalTasks: %v", err)
	}
	if retained["active"] == nil || retained["focused"] == nil || retained["unconsumed"] == nil || retained["non-durable"] == nil {
		t.Fatalf("unsafe records were removed: %#v", retained)
	}
	terminalCount := 0
	for taskID := range retained {
		if len(taskID) >= len("terminal-") && taskID[:len("terminal-")] == "terminal-" {
			terminalCount++
		}
	}
	if terminalCount != maxRetainedTerminalTasks {
		t.Fatalf("retained terminal count = %d, want %d", terminalCount, maxRetainedTerminalTasks)
	}
	f, err := os.Open(durableTaskArchivePath(a.sessionDir))
	if err != nil {
		t.Fatalf("open task archive: %v", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	archived := 0
	for scanner.Scan() {
		var entry archivedTaskRecord
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatalf("decode archive entry: %v", err)
		}
		archived++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan task archive: %v", err)
	}
	if archived != 2 {
		t.Fatalf("archived count = %d, want 2", archived)
	}
}

func TestArchiveEligibleTerminalTasksBoundsCompletedTasksAndSupportsLookup(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	now := time.Now()
	records := make(map[string]*DurableTaskRecord, maxRetainedTerminalTasks+2)
	for i := range maxRetainedTerminalTasks + 2 {
		taskID := fmt.Sprintf("completed-%03d", i)
		records[taskID] = &DurableTaskRecord{
			TaskID:            taskID,
			LatestInstanceID:  fmt.Sprintf("worker-%03d", i),
			InstanceHistory:   []string{fmt.Sprintf("worker-old-%03d", i)},
			State:             string(SubAgentStateCompleted),
			ResumePolicy:      taskResumePolicyNotify,
			RuntimeParked:     true,
			SettlementDurable: true,
			UpdatedAt:         now.Add(time.Duration(i) * time.Second),
		}
	}

	retained, err := a.archiveEligibleTerminalTasks(records, a.sessionDir)
	if err != nil {
		t.Fatalf("archiveEligibleTerminalTasks: %v", err)
	}
	if len(retained) != maxRetainedTerminalTasks {
		t.Fatalf("retained tasks = %d, want %d", len(retained), maxRetainedTerminalTasks)
	}
	a.setTaskRecords(retained)
	byTask := a.taskRecordByTaskID("completed-000")
	if byTask == nil || byTask.LatestInstanceID != "worker-000" {
		t.Fatalf("archived task lookup = %#v, want completed-000", byTask)
	}
	byInstance := a.taskRecordByInstanceID("worker-old-001")
	if byInstance == nil || byInstance.TaskID != "completed-001" {
		t.Fatalf("archived instance lookup = %#v, want completed-001", byInstance)
	}
	a.SwitchFocus("worker-000")
	if focused := a.focusedDurableTask(); focused == nil || focused.TaskID != "completed-000" {
		t.Fatalf("focused archived task = %#v, want completed-000", focused)
	}
	found := false
	for _, info := range a.GetSubAgents() {
		if info.TaskID == "completed-000" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("focused archived task was not exposed to the sidebar")
	}
}

func TestCompactSubAgentMailboxLogsPreservesUnconsumedAndLatestProgress(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "session")
	subagentsDir := filepath.Join(sessionDir, "subagents")
	if err := os.MkdirAll(subagentsDir, 0o700); err != nil {
		t.Fatalf("mkdir subagents: %v", err)
	}
	mailboxFile, err := os.Create(filepath.Join(subagentsDir, "mailbox.jsonl"))
	if err != nil {
		t.Fatalf("create mailbox log: %v", err)
	}
	mailboxEncoder := json.NewEncoder(mailboxFile)
	ackFile, err := os.Create(filepath.Join(subagentsDir, "mailbox-acks.jsonl"))
	if err != nil {
		_ = mailboxFile.Close()
		t.Fatalf("create ack log: %v", err)
	}
	ackEncoder := json.NewEncoder(ackFile)
	writeMessage := func(msg SubAgentMailboxMessage, consumed bool) {
		t.Helper()
		if err := mailboxEncoder.Encode(msg); err != nil {
			t.Fatalf("encode mailbox: %v", err)
		}
		if consumed {
			if err := ackEncoder.Encode(SubAgentMailboxAckRecord{MessageID: msg.MessageID, Outcome: "consumed", AckedAt: time.Now()}); err != nil {
				t.Fatalf("encode ack: %v", err)
			}
		}
	}
	for i := range 900 {
		writeMessage(SubAgentMailboxMessage{MessageID: fmt.Sprintf("old-%04d", i), TaskID: "old", Kind: SubAgentMailboxKindCompleted}, true)
	}
	for i := range 140 {
		writeMessage(SubAgentMailboxMessage{MessageID: fmt.Sprintf("progress-%04d", i), TaskID: "task-progress", AgentID: "worker-1", Kind: SubAgentMailboxKindProgress, Summary: fmt.Sprintf("progress %d", i)}, false)
	}
	for i := range 10 {
		writeMessage(SubAgentMailboxMessage{MessageID: fmt.Sprintf("urgent-%04d", i), TaskID: "urgent", Kind: SubAgentMailboxKindRiskAlert}, false)
	}
	for i := range 10 {
		writeMessage(SubAgentMailboxMessage{MessageID: fmt.Sprintf("recent-%04d", i), TaskID: "recent", Kind: SubAgentMailboxKindCompleted}, true)
	}
	if err := mailboxFile.Close(); err != nil {
		t.Fatalf("close mailbox log: %v", err)
	}
	if err := ackFile.Close(); err != nil {
		t.Fatalf("close ack log: %v", err)
	}

	msgs, err := loadSubAgentMailboxMessages(sessionDir)
	if err != nil {
		t.Fatalf("load mailbox messages before compaction: %v", err)
	}
	if err := compactSubAgentMailboxLogs(sessionDir, msgs); err != nil {
		t.Fatalf("compactSubAgentMailboxLogs: %v", err)
	}
	msgs, err = loadSubAgentMailboxMessages(sessionDir)
	if err != nil {
		t.Fatalf("load compacted mailbox messages: %v", err)
	}
	progressCount, urgentCount, consumedCount := 0, 0, 0
	for _, msg := range msgs {
		switch msg.Kind {
		case SubAgentMailboxKindProgress:
			progressCount++
		case SubAgentMailboxKindRiskAlert:
			urgentCount++
		}
		if msg.Consumed {
			consumedCount++
		}
	}
	if progressCount != 140 || urgentCount != 10 || consumedCount != 10 {
		t.Fatalf("compacted counts progress=%d urgent=%d consumed=%d, want 140/10/10", progressCount, urgentCount, consumedCount)
	}
}

// TestCompactSubAgentMailboxLogsKeepsTerminalEvidenceForCurrentAttempt pins
// the retention half of restore's notification coverage: a consumed terminal
// mailbox older than the sliding window survives compaction while its task
// record still names the same attempt, so restore can prove the notification
// was delivered (restoredTerminalNotificationCovered) instead of synthesizing
// a duplicate. The message and its ack must both survive: the ack is what
// re-marks it consumed on load, which is what keeps restore from redelivering
// it. Consumed messages without a matching record age out.
func TestCompactSubAgentMailboxLogsKeepsTerminalEvidenceForCurrentAttempt(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(a.sessionDir, "subagents"), 0o700); err != nil {
		t.Fatalf("mkdir subagents: %v", err)
	}
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"evidence-task": {
			TaskID:            "evidence-task",
			State:             string(SubAgentStateCompleted),
			ResumePolicy:      taskResumePolicyNotify,
			SettlementDurable: true,
			Attempt:           1,
			UpdatedAt:         time.Now(),
		},
	})
	if err := a.persistTaskRegistry(); err != nil {
		t.Fatalf("persist task registry: %v", err)
	}

	mailboxFile, err := os.Create(filepath.Join(a.sessionDir, "subagents", "mailbox.jsonl"))
	if err != nil {
		t.Fatalf("create mailbox log: %v", err)
	}
	mailboxEncoder := json.NewEncoder(mailboxFile)
	ackFile, err := os.Create(filepath.Join(a.sessionDir, "subagents", "mailbox-acks.jsonl"))
	if err != nil {
		_ = mailboxFile.Close()
		t.Fatalf("create ack log: %v", err)
	}
	ackEncoder := json.NewEncoder(ackFile)
	writeMessage := func(msg SubAgentMailboxMessage) {
		t.Helper()
		if err := mailboxEncoder.Encode(msg); err != nil {
			t.Fatalf("encode mailbox: %v", err)
		}
		if err := ackEncoder.Encode(SubAgentMailboxAckRecord{MessageID: msg.MessageID, Outcome: "consumed", AckedAt: time.Now()}); err != nil {
			t.Fatalf("encode ack: %v", err)
		}
	}
	// evidence-* is this task's attempt-1 completion: the delivery evidence
	// that must outlive the sliding window. filler-* belongs to a task with no
	// durable record, so nothing anchors it past the window.
	for i := range 10 {
		writeMessage(SubAgentMailboxMessage{
			MessageID: fmt.Sprintf("evidence-%04d", i),
			TaskID:    "evidence-task",
			Attempt:   1,
			Kind:      SubAgentMailboxKindCompleted,
			Priority:  SubAgentMailboxPriorityUrgent,
			Summary:   "task completed",
		})
	}
	for i := range 1490 {
		writeMessage(SubAgentMailboxMessage{
			MessageID: fmt.Sprintf("filler-%04d", i),
			TaskID:    "other-task",
			Attempt:   1,
			Kind:      SubAgentMailboxKindCompleted,
			Priority:  SubAgentMailboxPriorityUrgent,
			Summary:   "completed",
		})
	}
	if err := mailboxFile.Close(); err != nil {
		t.Fatalf("close mailbox log: %v", err)
	}
	if err := ackFile.Close(); err != nil {
		t.Fatalf("close ack log: %v", err)
	}

	msgs, err := loadSubAgentMailboxMessages(a.sessionDir)
	if err != nil {
		t.Fatalf("load mailbox messages before compaction: %v", err)
	}
	if err := compactSubAgentMailboxLogs(a.sessionDir, msgs); err != nil {
		t.Fatalf("compactSubAgentMailboxLogs: %v", err)
	}
	msgs, err = loadSubAgentMailboxMessages(a.sessionDir)
	if err != nil {
		t.Fatalf("load compacted mailbox messages: %v", err)
	}
	if len(msgs) != mailboxConsumedHistoryKeep+10 {
		t.Fatalf("compacted messages = %d, want window %d + evidence 10", len(msgs), mailboxConsumedHistoryKeep)
	}
	evidence, filler := 0, 0
	for _, msg := range msgs {
		if strings.HasPrefix(msg.MessageID, "evidence-") {
			evidence++
			if !msg.Consumed {
				t.Fatalf("evidence message %s reloaded unconsumed: its ack must survive so restore does not redeliver it", msg.MessageID)
			}
		} else if strings.HasPrefix(msg.MessageID, "filler-") {
			filler++
			if !msg.Consumed {
				t.Fatalf("kept filler message %s reloaded unconsumed", msg.MessageID)
			}
		}
	}
	if evidence != 10 || filler != mailboxConsumedHistoryKeep {
		t.Fatalf("compacted evidence=%d filler=%d, want evidence 10 and window %d of filler", evidence, filler, mailboxConsumedHistoryKeep)
	}
	for _, msg := range msgs {
		if msg.MessageID == "evidence-0000" {
			if msg.TaskID != "evidence-task" || msg.Attempt != 1 || msg.Kind != SubAgentMailboxKindCompleted {
				t.Fatalf("kept evidence = %#v, want the original completed attempt-1 message", msg)
			}
			return
		}
	}
	t.Fatal("evidence-0000 was dropped: current-attempt terminal evidence must survive compaction")
}
