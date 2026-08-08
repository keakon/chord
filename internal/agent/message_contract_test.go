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

func TestNormalizeAgentMessageContractKeepsLifecycleSeparate(t *testing.T) {
	msg := SubAgentMailboxMessage{
		MessageID: "msg-1", TaskID: "task-a", Attempt: 2, OwnerTaskID: "task-owner",
		Kind: SubAgentMailboxKindDecisionRequired,
	}
	normalizeAgentMessageContract(&msg)
	if msg.LifecycleKind != SubAgentMailboxKindDecisionRequired || msg.MessageType != AgentMessageTypeRequest || msg.Subtype != "" {
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
	if len(msgs) != 1 || msgs[0].LifecycleKind != SubAgentMailboxKindProgress || msgs[0].MessageType != AgentMessageTypeProgress || msgs[0].Durability != "best_effort" || msgs[0].Subtype != "" {
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
		MessageID: "msg-1", Kind: SubAgentMailboxKindDecisionRequired, LifecycleKind: SubAgentMailboxKindDecisionRequired,
		MessageType: AgentMessageTypeRequest, Subtype: "api_contract", SourceTaskID: "task-a", SourceAttempt: 1,
		TargetTaskID: "task-b", TargetAttempt: 2, CorrelationID: "corr-1", InReplyTo: "msg-0",
	}
	meta := mailboxMetadata(msg)
	if meta == nil || meta.MessageType != "request" || meta.Subtype != "api_contract" || meta.SourceAttempt != 1 || meta.TargetAttempt != 2 || meta.CorrelationID != "corr-1" || meta.InReplyTo != "msg-0" {
		t.Fatalf("metadata = %#v", meta)
	}
}
