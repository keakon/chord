package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func testCompactContextCall() (string, string) {
	return "cc-1", `{
		"active_objective": "land the model-driven context reset",
		"completed": ["tool written", "runtime barrier wired"],
		"decisions": ["keep archival profile", "MainAgent-only"],
		"open_issues": ["gateway contract test"],
		"next_step": "run the agent tests",
		"state_files": ["internal/agent/compaction_model_driven.go"]
	}`
}

func testToolCall(id, name string) message.ToolCall {
	return message.ToolCall{ID: id, Name: name, Args: json.RawMessage(`{}`)}
}

func TestCompactContextSoleToolCallDetection(t *testing.T) {
	ccID, _ := testCompactContextCall()
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "do work"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall("sibling-1", "read")}},
		{Role: message.RoleTool, ToolCallID: "sibling-1", Content: "ok"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall(ccID, tools.NameCompactContext)}},
	}
	if !compactContextSoleToolCall(msgs, ccID) {
		t.Fatal("sole compact_context call should pass the sole-call check")
	}

	multi := []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{
			testToolCall(ccID, tools.NameCompactContext),
			testToolCall("other", "read"),
		}},
	}
	if compactContextSoleToolCall(multi, ccID) {
		t.Fatal("compact_context with a sibling tool call must fail the sole-call check")
	}
	if compactContextSoleToolCall([]message.Message{{Role: message.RoleUser, Content: "x"}}, ccID) {
		t.Fatal("missing declaring message must fail the sole-call check")
	}
}

func TestTryArmModelDrivenCheckpointAcceptsValidArgs(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn("")
	ccID, args := testCompactContextCall()
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall(ccID, tools.NameCompactContext)}})
	result, err := a.tryArmModelDrivenCheckpoint(ccID, args)
	if err != nil {
		t.Fatalf("tryArmModelDrivenCheckpoint: %v", err)
	}
	if !strings.Contains(result, "accepted") {
		t.Fatalf("result = %q, want accepted wording", result)
	}
	if a.pendingModelDriven == nil {
		t.Fatal("pendingModelDriven should be armed")
	}
	if a.pendingModelDriven.Args.ActiveObjective != "land the model-driven context reset" {
		t.Fatalf("armed objective = %q", a.pendingModelDriven.Args.ActiveObjective)
	}
	if len(a.pendingModelDriven.Args.StateFiles) != 1 {
		t.Fatalf("state_files = %#v", a.pendingModelDriven.Args.StateFiles)
	}
}

func TestTryArmModelDrivenCheckpointRejectsBadArgs(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn("")
	if _, err := a.tryArmModelDrivenCheckpoint("cc-1", `{"state_files":["/etc/passwd"]}`); err == nil {
		t.Fatal("expected rejection for absolute state path")
	}
	if a.pendingModelDriven != nil {
		t.Fatal("pendingModelDriven must not be armed on rejection")
	}
	if _, err := a.tryArmModelDrivenCheckpoint("cc-1", `{}`); err == nil {
		t.Fatal("expected rejection for missing required fields")
	}

	// A compaction already owning the slot does not reject the request: the
	// model's explicit checkpoint may override an in-flight automatic
	// compaction, and the model-driven barrier discards the running
	// compaction before starting the model's own worker. The request is
	// armed normally.
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall("cc-1", tools.NameCompactContext)}})
	a.beginCompactionState(9, compactionTarget{turnID: 1, turnEpoch: 1, sessionEpoch: a.sessionEpoch}, compactionTriggerUsageDriven, continuationPlan{kind: compactionResumeAutoContinue, turnID: 1}, 0, nil)
	defer a.resetCompactionState()
	if _, err := a.tryArmModelDrivenCheckpoint("cc-1", `{"active_objective":"a","next_step":"b"}`); err != nil {
		t.Fatalf("model checkpoint must be accepted while an automatic compaction runs: %v", err)
	}
	if a.pendingModelDriven == nil {
		t.Fatal("pendingModelDriven must be armed while a compaction is running (override path)")
	}
}
