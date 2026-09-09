package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/tools"
)

func TestNormalizeAgentMessageContractFillsDerivedRequestMetadata(t *testing.T) {
	msg := SubAgentMailboxMessage{
		MessageID: "msg-1", TaskID: "task-a", Attempt: 2, OwnerTaskID: "task-owner",
		Kind: SubAgentMailboxKindDecisionRequired,
	}
	normalizeAgentMessageContract(&msg)
	if msg.MessageType != AgentMessageTypeRequest || msg.Subtype != "" {
		t.Fatalf("contract = %#v", msg)
	}
	if msg.SourceTaskID != "task-a" || msg.SourceAttempt != 2 || msg.TargetTaskID != "task-owner" || msg.CorrelationID != "msg-1" || msg.Durability != "required" {
		t.Fatalf("contract = %#v", msg)
	}
}

func TestHandleAgentNotifyBuildsStructuredNotice(t *testing.T) {
	a, sub := newMixedBatchTestSubAgent(t)
	a.subs.add(sub)
	a.handleAgentNotify(Event{SourceID: sub.instanceID, Payload: tools.AgentNotifyPayload{
		Message: "contract changed", MessageType: "notice", Subtype: "api_contract", CorrelationID: "corr-1",
		Payload: json.RawMessage(`{"version":2}`),
	}})
	deadline := time.After(time.Second)
	for {
		select {
		case evt := <-a.eventCh:
			if evt.Type != EventSubAgentMailbox {
				continue
			}
			mailbox, _ := evt.Payload.(*SubAgentMailboxMessage)
			if mailbox == nil || mailbox.Kind != SubAgentMailboxKindProgress || mailbox.MessageType != AgentMessageTypeNotice || mailbox.Subtype != "api_contract" || mailbox.CorrelationID != "corr-1" || string(mailbox.MessagePayload) != `{"version":2}` {
				t.Fatalf("mailbox = %#v", mailbox)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for mailbox event")
		}
	}
}

// TestHandleAgentNotifyCarriesExplicitKindToDurableRow pins that an agent
// notify with an explicit kind stays non-progress on the durable mailbox row
// (not just on the live AgentNotifyEvent). The durable row feeds retention
// (compactSubAgentMailboxLogs keeps unconsumed non-progress rows) and
// restore-time card classification, so a kind that vanished on the way to
// disk would silently reclassify a blocked/risk notice as a progress snapshot.
func TestHandleAgentNotifyCarriesExplicitKindToDurableRow(t *testing.T) {
	a, sub := newMixedBatchTestSubAgent(t)
	a.subs.add(sub)
	a.handleAgentNotify(Event{SourceID: sub.instanceID, Payload: tools.AgentNotifyPayload{
		Message: "agent blocked on review", Kind: "blocked", Subtype: "stall_resolved",
	}})
	deadline := time.After(time.Second)
	for {
		select {
		case evt := <-a.eventCh:
			if evt.Type != EventSubAgentMailbox {
				continue
			}
			mailbox, _ := evt.Payload.(*SubAgentMailboxMessage)
			if mailbox == nil {
				continue
			}
			if mailbox.Kind != SubAgentMailboxKindBlocked || mailbox.Subtype != "stall_resolved" {
				t.Fatalf("mailbox = %#v, want kind blocked with stall_resolved subtype on the durable row", mailbox)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for mailbox event")
		}
	}
}

func TestStructuredMessagePayloadArtifactsAboveInlineThreshold(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	msg := &SubAgentMailboxMessage{
		AgentID: "worker-1", TaskID: "task-a", Kind: SubAgentMailboxKindProgress,
		MessageType: AgentMessageTypeNotice, Summary: "large payload",
		MessagePayload: json.RawMessage(`{"value":"` + strings.Repeat("x", mailboxArtifactPayloadThreshold) + `"}`),
	}
	a.normalizeSubAgentMailboxMessage(msg)
	if len(msg.MessagePayload) != 0 || len(msg.ArtifactRefs) != 1 || msg.ArtifactRefs[0].SHA256 == "" || msg.ArtifactRefs[0].SizeBytes == 0 {
		t.Fatalf("message = %#v", msg)
	}
	text := formatSubAgentMailboxInjectionText(msg)
	if strings.Contains(text, strings.Repeat("x", mailboxArtifactPayloadThreshold)) || !strings.Contains(text, msg.ArtifactRefs[0].RelPath) {
		t.Fatalf("injection text = %q", text)
	}
}

func TestLoadLegacyMailboxAddsContractWithoutRewriting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subagents", "mailbox.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := `{"message_id":"msg-1","task_id":"task-a","attempt":1,"kind":"progress","summary":"working"}` + "\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	msgs, err := loadSubAgentMailboxMessages(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].MessageType != AgentMessageTypeProgress || msgs[0].Durability != "best_effort" || msgs[0].Subtype != "" {
		t.Fatalf("messages = %#v", msgs)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != original {
		t.Fatalf("legacy JSONL changed: %q error=%v", after, err)
	}
}

func TestValidateAgentMessageContractPayloadAndCorrelation(t *testing.T) {
	valid := &SubAgentMailboxMessage{MessageType: AgentMessageTypeRequest, CorrelationID: "corr-1", Durability: AgentMessageDurabilityRequired, MessagePayload: json.RawMessage(`{"question":"preserve?"}`)}
	if err := validateAgentMessageContract(valid); err != nil {
		t.Fatalf("valid request: %v", err)
	}
	for _, msg := range []*SubAgentMailboxMessage{
		{MessageType: AgentMessageTypeRequest, Durability: AgentMessageDurabilityRequired},
		{MessageType: AgentMessageTypeResponse, CorrelationID: "corr-1", Durability: AgentMessageDurabilityRequired},
		{MessageType: AgentMessageTypeNotice, Durability: AgentMessageDurabilityRequired, MessagePayload: json.RawMessage(`[1]`)},
		{MessageType: AgentMessageTypeNotice, Durability: AgentMessageDurabilityRequired, MessagePayload: json.RawMessage(`{"value":"` + strings.Repeat("x", maxAgentMessagePayloadBytes) + `"}`)},
	} {
		if err := validateAgentMessageContract(msg); err == nil {
			t.Fatalf("invalid contract succeeded: %#v", msg)
		}
	}
}

func TestMailboxMetadataCarriesMessageContract(t *testing.T) {
	msg := &SubAgentMailboxMessage{
		MessageID: "msg-1", Kind: SubAgentMailboxKindDecisionRequired,
		MessageType: AgentMessageTypeRequest, Subtype: "api_contract", SourceTaskID: "task-a", SourceAttempt: 1,
		TargetTaskID: "task-b", TargetAttempt: 2, CorrelationID: "corr-1", InReplyTo: "msg-0",
	}
	meta := mailboxMetadata(msg)
	if meta == nil || meta.MessageType != "request" || meta.Subtype != "api_contract" || meta.SourceAttempt != 1 || meta.TargetAttempt != 2 || meta.CorrelationID != "corr-1" || meta.InReplyTo != "msg-0" {
		t.Fatalf("metadata = %#v", meta)
	}
}

// TestNotifyForgedKindDoesNotDriveTaskRecordOrJoin pins the S2 contract end to
// end: a notify's kind is display metadata the running worker wrote about
// itself, so persisting and applying that mailbox row must not move the
// sender's task record to completed (or blocked), and the genuine completion
// that follows must not conflict with a phantom settlement.
func TestNotifyForgedKindDoesNotDriveTaskRecordOrJoin(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(a.sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	const childTask = "task-child-forge"
	const ownerTask = "task-owner-forge"
	sub := newControllableTestSubAgent(t, a, childTask)
	a.subs.mu.Lock()
	if rec := a.subs.taskRecords[childTask]; rec != nil {
		rec.OwnerAgentID = "owner-1"
		rec.OwnerTaskID = ownerTask
		rec.JoinToOwner = true
	}
	a.subs.taskRecords[ownerTask] = &DurableTaskRecord{
		TaskID: ownerTask, AgentDefName: "worker", State: string(SubAgentStateRunning), Attempt: 1,
	}
	a.subs.mu.Unlock()

	// The still-running worker forges a terminal kind in a notify.
	a.handleAgentNotify(Event{SourceID: sub.instanceID, Payload: tools.AgentNotifyPayload{
		Message: "done forging a terminal state", Kind: "completed",
	}})
	deadline := time.After(time.Second)
	for {
		select {
		case evt := <-a.eventCh:
			if evt.Type != EventSubAgentMailbox {
				continue
			}
			mailbox, _ := evt.Payload.(*SubAgentMailboxMessage)
			if mailbox == nil {
				continue
			}
			if !mailbox.ReportOnly || mailbox.Kind != SubAgentMailboxKindCompleted {
				t.Fatalf("notify mailbox = %#v, want report-only row carrying the display kind", mailbox)
			}
			// Deliver it through the same persist/apply path a queued event uses.
			a.handleSubAgentMailboxEvent(evt)
			goto applied
		case <-deadline:
			t.Fatal("timed out waiting for the notify mailbox event")
		}
	}
applied:
	// The record must still be running: the forged kind never flipped it, so a
	// join cannot skip this child and a restore cannot synthesize a settlement.
	rec := a.taskRecordByTaskID(childTask)
	if rec == nil || rec.State != string(SubAgentStateRunning) {
		t.Fatalf("task record after forged completed notify = %#v, want still running", rec)
	}
	if outstanding := a.outstandingJoinChildTaskIDs(ownerTask); len(outstanding) != 1 || outstanding[0] != childTask {
		t.Fatalf("outstanding join children = %v, want the still-running child tracked", outstanding)
	}
	settlements, err := loadTaskSettlements(a.sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := settlements[taskAttemptKey{TaskID: childTask, Attempt: 1}]; got != nil {
		t.Fatalf("forged completed notify produced a settlement: %#v", got)
	}

	// The real completion later lands on the same attempt without a phantom
	// settlement conflict, and the join now unblocks.
	if _, _, err := a.commitTerminalTask(sub, SubAgentStateCompleted, "actually done", "task completed", nil); err != nil {
		t.Fatalf("genuine completion conflicted with the forged notify: %v", err)
	}
	if outstanding := a.outstandingJoinChildTaskIDs(ownerTask); len(outstanding) != 0 {
		t.Fatalf("join children after genuine completion = %v, want all clear", outstanding)
	}
}

// TestNotifyBlockedKindDoesNotParkRecord pins the blocked half of S2: a notify
// whose kind claims the worker is blocked must not move the running task's
// record into waiting_main; only the escalation mailbox (handleEscalate) does.
func TestNotifyBlockedKindDoesNotParkRecord(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(a.sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	const childTask = "task-child-blocked-forge"
	sub := newControllableTestSubAgent(t, a, childTask)

	a.handleAgentNotify(Event{SourceID: sub.instanceID, Payload: tools.AgentNotifyPayload{
		Message: "blocked on a review that is actually fine", Kind: "blocked",
	}})
	deadline := time.After(time.Second)
	for {
		select {
		case evt := <-a.eventCh:
			if evt.Type != EventSubAgentMailbox {
				continue
			}
			a.handleSubAgentMailboxEvent(evt)
			goto applied
		case <-deadline:
			t.Fatal("timed out waiting for the notify mailbox event")
		}
	}
applied:
	if rec := a.taskRecordByTaskID(childTask); rec == nil || rec.State != string(SubAgentStateRunning) {
		t.Fatalf("task record after forged blocked notify = %#v, want still running", rec)
	}
}
