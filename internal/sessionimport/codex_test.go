package sessionimport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/recovery"
)

func TestConvertCodexRollout_ParsesBasicJSONL(t *testing.T) {
	data := []byte(`{"timestamp":"2026-01-01T00:00:00Z","item":{"type":"EventMsg","session_id":"sess-1","role":"user","content":"hi"}}
{"timestamp":"2026-01-01T00:00:01Z","item":{"type":"ResponseItem","role":"assistant","content":"hello"}}
{"timestamp":"2026-01-01T00:00:02Z","item":{"FunctionCall":{"name":"bash","arguments":{"command":"ls"}}}}
{"timestamp":"2026-01-01T00:00:03Z","item":{"FunctionCallOutput":{"output":"file1"}}}
{"timestamp":"2026-01-01T00:00:04Z","item":{"type":"Reasoning","text":"secret"}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}

	// Expected: user "hi", unsupported tool call text, unsupported tool result text, assistant "hello"
	// "bash" is not in the tool mapping → downgraded to text fallback.
	if len(msgs) != 4 {
		t.Fatalf("msgs len=%d, want 4", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "hi" {
		t.Fatalf("msg0=%+v", msgs[0])
	}
	// msg[1] should be the unsupported tool call fallback text.
	if msgs[1].Role != "assistant" || !strings.Contains(msgs[1].Content, "unsupported tool call") {
		t.Fatalf("msg1=%+v", msgs[1])
	}
	// msg[2] should be the orphaned tool result (call_ids don't match in legacy format).
	if msgs[2].Role != "assistant" || !strings.Contains(msgs[2].Content, "orphaned tool result") {
		t.Fatalf("msg2=%+v", msgs[2])
	}
	// msg[3] should be assistant "hello".
	if msgs[3].Role != "assistant" || msgs[3].Content != "hello" {
		t.Fatalf("msg3=%+v", msgs[3])
	}
	if report.SourceSessionID != "sess-1" {
		t.Fatalf("SourceSessionID=%q, want sess-1", report.SourceSessionID)
	}
	if report.ReasoningBlocksSkipped == 0 {
		t.Fatalf("ReasoningBlocksSkipped=%d, want >0", report.ReasoningBlocksSkipped)
	}
	if report.UnsupportedToolCalls != 1 {
		t.Fatalf("UnsupportedToolCalls=%d, want 1", report.UnsupportedToolCalls)
	}
	if report.MissingToolDeclarations != 1 {
		t.Fatalf("MissingToolDeclarations=%d, want 1", report.MissingToolDeclarations)
	}
	if report.ToolEntriesRendered != 2 {
		t.Fatalf("ToolEntriesRendered=%d, want 2", report.ToolEntriesRendered)
	}
}

func TestConvertCodexRollout_ParsesNewPayloadSchema(t *testing.T) {
	data := []byte(`{"timestamp":"2026-05-09T04:43:46Z","type":"session_meta","payload":{"id":"019e0955-00ce-73a2-bc23-213802de80d6"}}
{"timestamp":"2026-05-09T04:43:47Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"internal guidance"}]}}
{"timestamp":"2026-05-09T04:43:48Z","type":"event_msg","payload":{"type":"user_message","message":"hello"}}
{"timestamp":"2026-05-09T04:43:49Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}
{"timestamp":"2026-05-09T04:43:50Z","type":"event_msg","payload":{"type":"agent_message","message":"hi there"}}
{"timestamp":"2026-05-09T04:43:51Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi there"}],"phase":"commentary"}}
{"timestamp":"2026-05-09T04:43:52Z","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":\"pwd\"}","call_id":"call_1"}}
{"timestamp":"2026-05-09T04:43:53Z","type":"response_item","payload":{"type":"function_call_output","output":"/tmp","call_id":"call_1"}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	// Expected: user "hello", assistant tool call (Shell), tool result, assistant "hi there"
	if len(msgs) != 4 {
		t.Fatalf("msgs len=%d, want 4", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "hello" {
		t.Fatalf("msg0=%+v", msgs[0])
	}
	// msg[1] should be assistant with structured tool call.
	if msgs[1].Role != "assistant" || len(msgs[1].ToolCalls) != 1 {
		t.Fatalf("msg1=%+v", msgs[1])
	}
	if msgs[1].ToolCalls[0].Name != "Shell" {
		t.Fatalf("tool name=%q, want Shell", msgs[1].ToolCalls[0].Name)
	}
	// msg[2] should be tool result.
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "call_1" {
		t.Fatalf("msg2=%+v", msgs[2])
	}
	// msg[3] should be assistant text after tools.
	if msgs[3].Role != "assistant" || msgs[3].Content != "hi there" {
		t.Fatalf("msg3=%+v", msgs[3])
	}
	if report.SourceSessionID != "019e0955-00ce-73a2-bc23-213802de80d6" {
		t.Fatalf("SourceSessionID=%q", report.SourceSessionID)
	}
	if report.StructuredToolCalls != 1 {
		t.Fatalf("StructuredToolCalls=%d, want 1", report.StructuredToolCalls)
	}
	if report.StructuredToolResults != 1 {
		t.Fatalf("StructuredToolResults=%d, want 1", report.StructuredToolResults)
	}
	if report.ToolEntriesRendered != 2 {
		t.Fatalf("ToolEntriesRendered=%d, want 2", report.ToolEntriesRendered)
	}
	if report.SkippedEntries < 3 {
		t.Fatalf("SkippedEntries=%d, want at least 3", report.SkippedEntries)
	}
}

func TestConvertCodexRollout_StructuredShellTool(t *testing.T) {
	data := []byte(`{"timestamp":"2026-05-09T04:43:46Z","type":"session_meta","payload":{"id":"test-session"}}
{"timestamp":"2026-05-09T04:43:49Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"run pwd"}]}}
{"timestamp":"2026-05-09T04:43:52Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"pwd\",\"workdir\":\"/tmp\"}","call_id":"call_sh"}}
{"timestamp":"2026-05-09T04:43:53Z","type":"response_item","payload":{"type":"function_call_output","output":"/tmp","call_id":"call_sh"}}
{"timestamp":"2026-05-09T04:43:54Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"output is /tmp"}]}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}

	// Expected: user, tool call, tool result, assistant
	if len(msgs) != 4 {
		t.Fatalf("msgs len=%d, want 4", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Fatalf("msg0 role=%q, want user", msgs[0].Role)
	}
	if msgs[1].Role != "assistant" || len(msgs[1].ToolCalls) != 1 {
		t.Fatalf("msg1=%+v", msgs[1])
	}
	tc := msgs[1].ToolCalls[0]
	if tc.Name != "Shell" {
		t.Fatalf("tool name=%q, want Shell", tc.Name)
	}
	// Verify normalized args contain command and workdir.
	var args map[string]any
	if err := json.Unmarshal(tc.Args, &args); err != nil {
		t.Fatalf("unmarshal args: %v", err)
	}
	if args["command"] != "pwd" {
		t.Fatalf("command=%v", args["command"])
	}
	if args["workdir"] != "/tmp" {
		t.Fatalf("workdir=%v", args["workdir"])
	}
	if msgs[2].Role != "tool" {
		t.Fatalf("msg2 role=%q, want tool", msgs[2].Role)
	}
	if msgs[2].ToolCallID != "call_sh" {
		t.Fatalf("msg2 ToolCallID=%q, want call_sh", msgs[2].ToolCallID)
	}
	if msgs[3].Role != "assistant" || msgs[3].Content != "output is /tmp" {
		t.Fatalf("msg3=%+v", msgs[3])
	}
	if report.StructuredToolCalls != 1 {
		t.Fatalf("StructuredToolCalls=%d, want 1", report.StructuredToolCalls)
	}
	if report.StructuredToolResults != 1 {
		t.Fatalf("StructuredToolResults=%d, want 1", report.StructuredToolResults)
	}
	if report.ToolEntriesRendered != 2 {
		t.Fatalf("ToolEntriesRendered=%d, want 2", report.ToolEntriesRendered)
	}
}

func TestConvertCodexRollout_UnsupportedToolDowngrade(t *testing.T) {
	data := []byte(`{"timestamp":"2026-05-09T04:43:46Z","type":"session_meta","payload":{"id":"test-session"}}
{"timestamp":"2026-05-09T04:43:49Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"do something"}]}}
{"timestamp":"2026-05-09T04:43:52Z","type":"response_item","payload":{"type":"function_call","name":"unknown_tool","arguments":"{\"foo\":\"bar\"}","call_id":"call_un"}}
{"timestamp":"2026-05-09T04:43:53Z","type":"response_item","payload":{"type":"function_call_output","output":"some result","call_id":"call_un"}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}

	// Expected: user, unsupported tool call text, unsupported tool result text
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Fatalf("msg0 role=%q, want user", msgs[0].Role)
	}
	if !strings.Contains(msgs[1].Content, "unsupported tool call") {
		t.Fatalf("msg1 should contain 'unsupported tool call': %s", msgs[1].Content)
	}
	if !strings.Contains(msgs[1].Content, "unknown_tool") {
		t.Fatalf("msg1 should mention tool name: %s", msgs[1].Content)
	}
	if !strings.Contains(msgs[2].Content, "unsupported tool result") {
		t.Fatalf("msg2 should contain 'unsupported tool result': %s", msgs[2].Content)
	}
	if report.UnsupportedToolCalls != 1 {
		t.Fatalf("UnsupportedToolCalls=%d, want 1", report.UnsupportedToolCalls)
	}
	if report.UnsupportedToolResults != 1 {
		t.Fatalf("UnsupportedToolResults=%d, want 1", report.UnsupportedToolResults)
	}
	if report.ToolEntriesRendered != 2 {
		t.Fatalf("ToolEntriesRendered=%d, want 2", report.ToolEntriesRendered)
	}
}

func TestConvertCodexRollout_EventMsgDeduplication(t *testing.T) {
	// Test that event_msg entries are deduplicated when response_item
	// entries exist for the same turn and role.
	data := []byte(`{"timestamp":"2026-05-09T04:43:46Z","type":"session_meta","payload":{"id":"dedup-test"}}
{"timestamp":"2026-05-09T04:43:48Z","type":"event_msg","payload":{"type":"user_message","message":"hello from event"}}
{"timestamp":"2026-05-09T04:43:49Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello from response"}]}}
{"timestamp":"2026-05-09T04:43:50Z","type":"event_msg","payload":{"type":"agent_message","message":"hi from event"}}
{"timestamp":"2026-05-09T04:43:51Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi from response"}]}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}

	// response_item should win over event_msg.
	if len(msgs) != 2 {
		t.Fatalf("msgs len=%d, want 2", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "hello from response" {
		t.Fatalf("msg0=%+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "hi from response" {
		t.Fatalf("msg1=%+v", msgs[1])
	}
	if report.SkippedDuplicates != 2 {
		t.Fatalf("SkippedDuplicates=%d, want 2", report.SkippedDuplicates)
	}
}

func TestConvertCodexRollout_TurnBoundaries(t *testing.T) {
	// Test that turn_context entries properly separate turns.
	data := []byte(`{"timestamp":"2026-05-09T04:43:46Z","type":"session_meta","payload":{"id":"turn-test"}}
{"timestamp":"2026-05-09T04:43:47Z","type":"turn_context","payload":{"turn_id":"turn-1","cwd":"/tmp","model":"gpt-5"}}
{"timestamp":"2026-05-09T04:43:48Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"first question"}]}}
{"timestamp":"2026-05-09T04:43:49Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first answer"}]}}
{"timestamp":"2026-05-09T04:43:50Z","type":"turn_context","payload":{"turn_id":"turn-2","cwd":"/tmp","model":"gpt-5"}}
{"timestamp":"2026-05-09T04:43:51Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"second question"}]}}
{"timestamp":"2026-05-09T04:43:52Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"second answer"}]}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}

	// Expected: 4 messages across 2 turns.
	if len(msgs) != 4 {
		t.Fatalf("msgs len=%d, want 4", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "first question" {
		t.Fatalf("msg0=%+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "first answer" {
		t.Fatalf("msg1=%+v", msgs[1])
	}
	if msgs[2].Role != "user" || msgs[2].Content != "second question" {
		t.Fatalf("msg2=%+v", msgs[2])
	}
	if msgs[3].Role != "assistant" || msgs[3].Content != "second answer" {
		t.Fatalf("msg3=%+v", msgs[3])
	}
}

func TestImport_Codex_WritesRecoverableSession(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CHORD_STATE_DIR", stateDir)
	t.Setenv("CHORD_SESSIONS_DIR", "")

	projectRoot := t.TempDir()
	input := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(input, []byte(`{"timestamp":"2026-01-01T00:00:00Z","type":"session_meta","payload":{"id":"sess-1"}}
{"timestamp":"2026-01-01T00:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}
{"timestamp":"2026-01-01T00:00:02Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}}
`), 0o600); err != nil {
		t.Fatalf("write input: %v", err)
	}

	res, err := Import(context.Background(), ImportOptions{Source: "codex", InputPath: input, ProjectRoot: projectRoot})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	rm := recovery.NewRecoveryManager(res.SessionDir)
	msgs, err := rm.LoadMessages("main")
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("msgs len=%d, want 2", len(msgs))
	}

	meta, err := recovery.LoadSessionMeta(res.SessionDir)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if meta == nil || meta.ImportedFrom == nil || meta.ImportedFrom.Source != "codex" {
		t.Fatalf("ImportedFrom missing: %+v", meta)
	}
	if meta.ImportedFrom.SourceSessionID != "sess-1" {
		t.Fatalf("SourceSessionID=%q, want sess-1", meta.ImportedFrom.SourceSessionID)
	}
}
