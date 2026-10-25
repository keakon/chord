package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/keakon/chord/internal/externalclaude"
)

func mustExternalToolFingerprint(t *testing.T, name, input string) [32]byte {
	t.Helper()
	fingerprint, err := externalToolFingerprint(name, json.RawMessage(input))
	if err != nil {
		t.Fatalf("fingerprint(%s, %s): %v", name, input, err)
	}
	return fingerprint
}

// Claude Code may re-serialize a tool input between the assistant tool_use and
// the can_use_tool request. The fingerprint must compare values, not the bytes
// of one particular serialization.
func TestExternalToolFingerprintCanonicalizesEquivalentInput(t *testing.T) {
	tests := []struct {
		name           string
		toolA, toolB   string
		inputA, inputB string
	}{
		{
			name:   "reordered bash timeout input",
			toolA:  "Bash",
			toolB:  "Bash",
			inputA: `{"command":"sleep 2","description":"Wait briefly.","timeout":120000}`,
			inputB: `{"command":"sleep 2","timeout":120000,"description":"Wait briefly."}`,
		},
		{
			name:  "reordered nested question input",
			toolA: "AskUserQuestion",
			toolB: "AskUserQuestion",
			inputA: `{"questions":[{"header":"Choice","question":"Choose one","multiSelect":false,` +
				`"options":[{"label":"Alpha","description":"First"},{"label":"Beta","description":"Second"}]}]}`,
			inputB: `{"questions":[{"options":[{"description":"First","label":"Alpha"},` +
				`{"description":"Second","label":"Beta"}],"multiSelect":false,"question":"Choose one","header":"Choice"}]}`,
		},
		{
			name:   "whitespace differences",
			toolA:  "Read",
			toolB:  "Read",
			inputA: `{"file_path":"sample.txt","limit":20}`,
			inputB: "{ \"limit\" : 20,\n\t\"file_path\" : \"sample.txt\" }",
		},
		{
			name:   "task and agent aliases",
			toolA:  "Task",
			toolB:  "Agent",
			inputA: `{"description":"inspect","prompt":"Inspect the fixture.","subagent_type":"Explore"}`,
			inputB: `{"subagent_type":"Explore","prompt":"Inspect the fixture.","description":"inspect"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := mustExternalToolFingerprint(t, tt.toolA, tt.inputA)
			if got := mustExternalToolFingerprint(t, tt.toolB, tt.inputB); got != want {
				t.Fatal("equivalent inputs produced different fingerprints")
			}
		})
	}
}

func TestExternalToolFingerprintDistinguishesArguments(t *testing.T) {
	base := `{"file_path":"sample.txt","offset":0,"limit":10}`
	want := mustExternalToolFingerprint(t, "Read", base)
	for _, other := range []string{
		`{"file_path":"other.txt","offset":0,"limit":10}`,
		`{"file_path":"sample.txt","offset":1,"limit":10}`,
		`{"file_path":"sample.txt","offset":0,"limit":-1}`,
	} {
		if got := mustExternalToolFingerprint(t, "Read", other); got == want {
			t.Fatalf("fingerprint(%s) equals the fingerprint of different arguments", other)
		}
	}
	small := mustExternalToolFingerprint(t, "Read", `{"file_path":"sample.txt","offset":9007199254740992,"limit":10}`)
	large := mustExternalToolFingerprint(t, "Read", `{"file_path":"sample.txt","offset":9007199254740993,"limit":10}`)
	if small == large {
		t.Fatal("adjacent integers beyond float64 precision produced the same fingerprint")
	}
	if got := mustExternalToolFingerprint(t, "Write", base); got == want {
		t.Fatal("distinct tool names produced the same fingerprint")
	}
}

func TestExternalToolFingerprintRejectsInvalidInput(t *testing.T) {
	for _, input := range []string{"", "{", `{"file_path":"sample.txt"} trailing`, `[] []`} {
		if _, err := externalToolFingerprint("Read", json.RawMessage(input)); !errors.Is(err, errExternalToolInputInvalid) {
			t.Fatalf("fingerprint(%q) error = %v, want invalid input", input, err)
		}
	}
}

func TestExternalToolBindingCanonicalizesInput(t *testing.T) {
	w := &externalWorker{taskID: "task-1", instanceID: "external-1", toolBindings: map[string]*externalToolBinding{}}
	const id = "tool-1"
	toolUseInput := json.RawMessage(`{"command":"sleep 2","description":"Wait briefly.","timeout":120000}`)
	requestInput := json.RawMessage(`{"command":"sleep 2","timeout":120000,"description":"Wait briefly."}`)

	if err := w.bindExternalTool(id, "Bash", toolUseInput, false); err != nil {
		t.Fatalf("bind tool_use: %v", err)
	}
	if err := w.bindExternalTool(id, "Bash", requestInput, true); err != nil {
		t.Fatalf("bind reserialized permission request: %v", err)
	}
	if binding := w.toolBindings[id]; binding == nil || !binding.requested {
		t.Fatalf("binding = %+v, want the permission request recorded", binding)
	}
	if err := w.bindExternalTool(id, "Bash", requestInput, true); !errors.Is(err, errExternalToolDuplicateRequest) {
		t.Fatalf("second permission request error = %v, want a duplicate rejection", err)
	}
	if err := w.bindExternalTool(id, "Bash", json.RawMessage(`{"command":"sleep 3","timeout":120000,"description":"Wait briefly."}`), true); !errors.Is(err, errExternalToolIdentityMismatch) {
		t.Fatalf("changed arguments error = %v, want an identity mismatch", err)
	}
}

func TestExternalPermissionAcceptsReserializedInput(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureExternalTestRuntime(t, a, "claude", `"*": allow`)
	w, err := a.newExternalWorker(externalWorkerConfig{
		agentDef: a.agentConfigs["external-worker"], instanceID: "external-1", taskID: "task-1",
		ctx: context.Background(), cancel: func() {},
	})
	if err != nil {
		t.Fatal(err)
	}
	w.handleExternalToolUse(externalclaude.ContentBlock{
		ID: "tool-bash", Name: "Bash",
		Input: json.RawMessage(`{"command":"sleep 2","description":"Wait briefly.","timeout":120000}`),
	})
	frame := w.evaluateExternalPermission(context.Background(), externalclaude.Event{RequestID: "perm-bash", Request: &externalclaude.ControlRequest{
		ToolName: "Bash", ToolUseID: "tool-bash",
		Input: json.RawMessage(`{"command":"sleep 2","timeout":120000,"description":"Wait briefly."}`),
	}})
	var decision externalclaude.PermissionDecision
	if err := json.Unmarshal(frame.Response.Response, &decision); err != nil {
		t.Fatal(err)
	}
	if decision.Behavior != externalclaude.PermissionBehaviorAllow {
		t.Fatalf("reserialized tool input was not authorized: %+v", decision)
	}
}

func TestExternalPermissionBindsTaskAgentAliases(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureExternalTestRuntime(t, a, "claude", "\"*\": deny\nAgent: allow")
	w, err := a.newExternalWorker(externalWorkerConfig{
		agentDef: a.agentConfigs["external-worker"], instanceID: "external-1", taskID: "task-1",
		ctx: context.Background(), cancel: func() {},
	})
	if err != nil {
		t.Fatal(err)
	}
	w.handleExternalToolUse(externalclaude.ContentBlock{
		ID: "tool-task", Name: "Task",
		Input: json.RawMessage(`{"description":"inspect","prompt":"Inspect the fixture.","subagent_type":"Explore"}`),
	})
	frame := w.evaluateExternalPermission(context.Background(), externalclaude.Event{RequestID: "perm-task", Request: &externalclaude.ControlRequest{
		ToolName: "Agent", ToolUseID: "tool-task",
		Input: json.RawMessage(`{"subagent_type":"Explore","prompt":"Inspect the fixture.","description":"inspect"}`),
	}})
	var decision externalclaude.PermissionDecision
	if err := json.Unmarshal(frame.Response.Response, &decision); err != nil {
		t.Fatal(err)
	}
	if decision.Behavior != externalclaude.PermissionBehaviorAllow {
		t.Fatalf("Task and Agent aliases were not bound to one tool call: %+v", decision)
	}
}
