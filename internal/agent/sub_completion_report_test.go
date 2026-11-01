package agent

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestCompletionReportAfterWriteWithoutChecks(t *testing.T) {
	t.Parallel()
	parent, sub := newMixedBatchTestSubAgent(t)
	sub.recordTaskToolChanges(&toolResult{
		Name: tools.NameWrite, ArgsJSON: `{"path":"sample.go","content":"package sample"}`,
		FileState: &message.ToolFileState{Writes: []message.TrackedFileState{{Path: filepath.Join(sub.workDir, "sample.go"), Exists: true}}},
	}, false)
	summary := "Updated sample.go; no checks were run."
	limitation := "Integration tests were not run."
	sub.handleLLMResponse(&llmResult{turnID: sub.turn.ID, resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
		mustJSONToolCall(t, "complete-report", tools.NameComplete, map[string]any{
			"summary": summary, "remaining_limitations": []string{limitation},
		}),
	})}})
	event := waitForAgentEventType(t, parent.eventCh, EventAgentDone)
	result := event.Payload.(*AgentResult)
	if result.Summary != summary || result.Envelope == nil ||
		!reflect.DeepEqual(result.Envelope.RemainingLimitations, []string{limitation}) ||
		!reflect.DeepEqual(result.Envelope.ActualFilesChanged, []string{"sample.go"}) {
		t.Fatalf("completion = %#v, want report and observed path", result)
	}
	if sub.llmRequestInFlight.Load() || sub.turn.SubAgentCompletionRecoveryCount != 0 {
		t.Fatal("completion without checks started a repair request")
	}
	if got := toolResultForCall(t, sub, "complete-report"); got.Content != summary || got.ToolStatus != message.ToolStatusSuccess {
		t.Fatalf("complete tool result = %#v, want successful raw summary", got)
	}
	mailbox := formatSubAgentMailboxInjectionText(&SubAgentMailboxMessage{
		MessageID: "message-1", AgentID: sub.instanceID, TaskID: sub.taskID,
		Kind: SubAgentMailboxKindCompleted, Summary: result.Summary, Completion: result.Envelope,
	})
	for _, text := range []string{summary, limitation, "sample.go"} {
		if !strings.Contains(mailbox, text) {
			t.Fatalf("owner report lost %q:\n%s", text, mailbox)
		}
	}
	settlement, durable, err := parent.commitTerminalTask(sub, SubAgentStateCompleted, result.Summary, "task completed", result.Envelope)
	if err != nil || !durable || settlement == nil {
		t.Fatalf("settlement = %#v, durable = %v, error = %v", settlement, durable, err)
	}
	loaded, err := loadTaskSettlements(parent.sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	restored := loaded[taskAttemptKey{TaskID: sub.taskID, Attempt: settlement.Attempt}]
	if restored == nil || !reflect.DeepEqual(restored.Completion, result.Envelope) {
		t.Fatalf("durable settlement = %#v, want completion report preserved", restored)
	}
	data, err := json.Marshal(restored.Completion)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"verification", "verification_waived", "files_changed"} {
		if _, ok := fields[field]; ok {
			t.Fatalf("completion added %s: %s", field, data)
		}
	}
	for _, field := range []string{"verification", "verification_waived", "actual_files_changed", "reported_files_changed", "file_attribution_incomplete"} {
		complete, ok := sub.tools.Get(tools.NameComplete)
		if !ok {
			t.Fatal("complete tool missing")
		}
		if _, ok := complete.Parameters()["properties"].(map[string]any)[field]; ok {
			t.Fatalf("complete schema added %s", field)
		}
	}
}
