package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tools"
)

const externalResultContractSchema = `{"type":"object","required":["summary"],"properties":{"summary":{"type":"string"}}}`

// writeContractFakeClaude writes a fake CLI that records the initialize frame
// (the channel carrying appendSystemPrompt) and then answers with one final
// result, so a test can assert both what the process was told and how the
// delivered result was judged.
func writeContractFakeClaude(t *testing.T, capturePath, result string) string {
	t.Helper()
	encodedResult, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal fake result: %v", err)
	}
	path := filepath.Join(t.TempDir(), "fake-claude")
	script := "#!/bin/sh\n" +
		"IFS= read -r init\n" +
		"printf '%s' \"$init\" > '" + capturePath + "'\n" +
		"case \"$init\" in *'\"subtype\":\"initialize\"'*) ;; *) exit 8 ;; esac\n" +
		"printf '%s\\n' '{\"type\":\"control_response\",\"response\":{\"subtype\":\"success\",\"request_id\":\"chord-initialize\",\"response\":{}}}'\n" +
		"IFS= read -r prompt\n" +
		"printf '%s\\n' '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"11111111-1111-4111-8111-111111111111\"}'\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":" + string(encodedResult) + "}'\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return path
}

func readInitializeFrame(t *testing.T, path string) (subtype, appendSystemPrompt string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read captured initialize frame: %v", err)
	}
	var frame struct {
		Request struct {
			Subtype            string `json:"subtype"`
			AppendSystemPrompt string `json:"appendSystemPrompt"`
		} `json:"request"`
	}
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatalf("decode captured initialize frame %q: %v", data, err)
	}
	return frame.Request.Subtype, frame.Request.AppendSystemPrompt
}

func TestExternalWorkerResultContractAcceptsConformingResult(t *testing.T) {
	root := t.TempDir()
	capture := filepath.Join(root, "init.json")
	script := writeContractFakeClaude(t, capture, `{"summary":"all good"}`)
	a := newTestMainAgent(t, root)
	configureExternalTestRuntime(t, a, script, `"*": allow`)

	_, canonical, err := tools.CompileResultSchema(json.RawMessage(externalResultContractSchema))
	if err != nil {
		t.Fatalf("CompileResultSchema: %v", err)
	}
	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{
		Description:  "review with a contract",
		AgentType:    "external-worker",
		ResultSchema: json.RawMessage(externalResultContractSchema),
	})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	record := driveExternalTask(t, a, handle.TaskID)
	if record.State != string(SubAgentStateCompleted) {
		t.Fatalf("task state = %q, want completed (closed_reason=%q summary=%q)", record.State, record.ClosedReason, record.LastSummary)
	}
	if record.LastSummary != `{"summary":"all good"}` {
		t.Fatalf("LastSummary = %q, want the delivered JSON result", record.LastSummary)
	}
	if string(record.ResultSchema) != string(canonical) {
		t.Fatalf("recorded result_schema = %s, want the canonical contract %s", record.ResultSchema, canonical)
	}
	subtype, prompt := readInitializeFrame(t, capture)
	if subtype != "initialize" {
		t.Fatalf("captured frame subtype = %q, want initialize", subtype)
	}
	if !strings.Contains(prompt, "### Result contract") {
		t.Fatalf("initialize prompt lacks the result contract block: %q", prompt)
	}
	if !strings.Contains(prompt, string(canonical)) {
		t.Fatalf("initialize prompt lacks the canonical schema %s: %q", canonical, prompt)
	}
}

func TestExternalWorkerResultContractRejectsNonJSONResult(t *testing.T) {
	root := t.TempDir()
	script := writeContractFakeClaude(t, filepath.Join(root, "init.json"), "I finished the review; the findings are above.")
	a := newTestMainAgent(t, root)
	configureExternalTestRuntime(t, a, script, `"*": allow`)

	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{
		Description:  "review with a contract",
		AgentType:    "external-worker",
		ResultSchema: json.RawMessage(externalResultContractSchema),
	})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	record := driveExternalTask(t, a, handle.TaskID)
	if record.State != string(SubAgentStateFailed) {
		t.Fatalf("task state = %q, want failed (summary=%q)", record.State, record.LastSummary)
	}
	if !strings.Contains(record.LastSummary, "result contract violation") ||
		!strings.Contains(record.LastSummary, "machine-readable JSON object") {
		t.Fatalf("LastSummary = %q, want a missing-result contract violation", record.LastSummary)
	}
}

func TestExternalWorkerResultContractRejectsSchemaViolation(t *testing.T) {
	root := t.TempDir()
	script := writeContractFakeClaude(t, filepath.Join(root, "init.json"), `{"notes":"no required field here"}`)
	a := newTestMainAgent(t, root)
	configureExternalTestRuntime(t, a, script, `"*": allow`)

	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{
		Description:  "review with a contract",
		AgentType:    "external-worker",
		ResultSchema: json.RawMessage(externalResultContractSchema),
	})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	record := driveExternalTask(t, a, handle.TaskID)
	if record.State != string(SubAgentStateFailed) {
		t.Fatalf("task state = %q, want failed (summary=%q)", record.State, record.LastSummary)
	}
	if !strings.Contains(record.LastSummary, "result contract violation") ||
		!strings.Contains(record.LastSummary, "result.summary is required") {
		t.Fatalf("LastSummary = %q, want a violation naming the missing required field", record.LastSummary)
	}
}
