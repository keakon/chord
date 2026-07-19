package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
			TaskID:       taskID,
			State:        string(SubAgentStateCancelled),
			ResumePolicy: taskResumePolicyExplicitOnly,
			UpdatedAt:    now.Add(time.Duration(i) * time.Second),
		}
	}
	records["active"] = &DurableTaskRecord{TaskID: "active", State: string(SubAgentStateRunning), UpdatedAt: now}
	records["focused"] = &DurableTaskRecord{TaskID: "focused", State: string(SubAgentStateCancelled), ResumePolicy: taskResumePolicyExplicitOnly, UpdatedAt: now.Add(-time.Hour)}
	records["unconsumed"] = &DurableTaskRecord{TaskID: "unconsumed", State: string(SubAgentStateCancelled), ResumePolicy: taskResumePolicyExplicitOnly, LastMailboxID: "pending-mailbox", UpdatedAt: now.Add(-time.Hour)}
	a.setFocusedTaskID("focused")

	retained, err := a.archiveEligibleTerminalTasks(records, a.sessionDir)
	if err != nil {
		t.Fatalf("archiveEligibleTerminalTasks: %v", err)
	}
	if retained["active"] == nil || retained["focused"] == nil || retained["unconsumed"] == nil {
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
			TaskID:           taskID,
			LatestInstanceID: fmt.Sprintf("worker-%03d", i),
			InstanceHistory:  []string{fmt.Sprintf("worker-old-%03d", i)},
			State:            string(SubAgentStateCompleted),
			ResumePolicy:     taskResumePolicyNotify,
			RuntimeParked:    true,
			UpdatedAt:        now.Add(time.Duration(i) * time.Second),
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
			if msg.Summary != "progress 139" {
				t.Fatalf("kept progress summary = %q, want latest", msg.Summary)
			}
		case SubAgentMailboxKindRiskAlert:
			urgentCount++
		}
		if msg.Consumed {
			consumedCount++
		}
	}
	if progressCount != 1 || urgentCount != 10 || consumedCount != 10 {
		t.Fatalf("compacted counts progress=%d urgent=%d consumed=%d, want 1/10/10", progressCount, urgentCount, consumedCount)
	}
}
