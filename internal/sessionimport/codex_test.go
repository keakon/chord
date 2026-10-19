package sessionimport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func TestCodexTruncateImportedText(t *testing.T) {
	if got := codexTruncateImportedText("abc", codexImportedNoteMaxBytes); got != "abc" {
		t.Fatalf("short text changed: %q", got)
	}
	long := strings.Repeat("a", codexImportedNoteMaxBytes) + "\u4e2d\u6587tail"
	got := codexTruncateImportedText(long, codexImportedNoteMaxBytes)
	if !strings.HasSuffix(got, "\n...[truncated]") {
		t.Fatalf("truncation marker missing: %q", got[len(got)-40:])
	}
	if len(got) != codexImportedNoteMaxBytes+len("\n...[truncated]") {
		t.Fatalf("truncated len=%d, want %d", len(got), codexImportedNoteMaxBytes+len("\n...[truncated]"))
	}
	if !utf8.ValidString(got) {
		t.Fatal("truncation split a UTF-8 rune")
	}
	// A multibyte rune straddling the limit must not be split either.
	straddle := strings.Repeat("\u4e2d", codexImportedNoteMaxBytes)
	if got := codexTruncateImportedText(straddle, codexImportedNoteMaxBytes); !utf8.ValidString(got) {
		t.Fatal("truncation split a straddling rune")
	}
}

// assertCodexImportedToolCall asserts that a Codex tool call with no Chord
// mapping stayed a tool call under the source name with the source arguments.
// The call really ran in the source session, so it belongs in the trajectory
// as-is: rewriting it into text would drop the action, and wrapping the
// arguments in importer metadata would show bookkeeping instead of the call.
func assertCodexImportedToolCall(t *testing.T, msg message.Message, wantName string, wantArgs string) {
	t.Helper()
	if msg.Role != message.RoleAssistant {
		t.Fatalf("role=%q, want assistant: %+v", msg.Role, msg)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("tool calls=%+v, want the source call preserved", msg.ToolCalls)
	}
	if msg.ToolCalls[0].Name != wantName {
		t.Fatalf("tool name=%q, want %q", msg.ToolCalls[0].Name, wantName)
	}
	if got := strings.TrimSpace(string(msg.ToolCalls[0].Args)); got != wantArgs {
		t.Fatalf("tool args=%s, want the source arguments %s", got, wantArgs)
	}
}

func TestConvertCodexRollout_IgnoresItemJSONL(t *testing.T) {
	data := []byte(`{"timestamp":"2026-01-01T00:00:00Z","item":{"type":"EventMsg","session_id":"sess-1","role":"user","content":"hi"}}
{"timestamp":"2026-01-01T00:00:01Z","item":{"type":"ResponseItem","role":"assistant","content":"hello"}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("msgs len=%d, want 0", len(msgs))
	}
	if report.SourceSessionID != "" {
		t.Fatalf("SourceSessionID=%q, want empty", report.SourceSessionID)
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
	// Expected source order: user "hello", assistant "hi there", assistant tool call (Shell), tool result.
	if len(msgs) != 4 {
		t.Fatalf("msgs len=%d, want 4", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "hello" {
		t.Fatalf("msg0=%+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "hi there" {
		t.Fatalf("msg1=%+v", msgs[1])
	}
	if msgs[2].Role != "assistant" || len(msgs[2].ToolCalls) != 1 {
		t.Fatalf("msg2=%+v", msgs[2])
	}
	if msgs[2].ToolCalls[0].Name != "shell" {
		t.Fatalf("tool name=%q, want shell", msgs[2].ToolCalls[0].Name)
	}
	if msgs[3].Role != "tool" || msgs[3].ToolCallID != "call_1" {
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

func TestConvertCodexRollout_SkipsStartupInstructionUserMessage(t *testing.T) {
	data := []byte(`{"timestamp":"2026-05-09T04:43:46Z","type":"session_meta","payload":{"id":"startup-session"}}
{"timestamp":"2026-05-09T04:43:47Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"internal guidance"}]}}
{"timestamp":"2026-05-09T04:43:48Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions for /Users/me/project\n\n<INSTRUCTIONS>\nUse Chinese.\n</INSTRUCTIONS>"}]}}
{"timestamp":"2026-05-09T04:43:49Z","type":"turn_context","payload":{"turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:50Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"审查未push的commits是否正确合理"}]}}
{"timestamp":"2026-05-09T04:43:51Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"可以推送"}]}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("msgs len=%d, want 2: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != "user" || strings.Contains(msgs[0].Content, "AGENTS.md") || !strings.Contains(msgs[0].Content, "审查未push") {
		t.Fatalf("startup instructions should be skipped and real user message preserved: %+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "可以推送" {
		t.Fatalf("msg1=%+v", msgs[1])
	}
	if report.SkippedEntries < 2 {
		t.Fatalf("SkippedEntries=%d, want startup developer/user instructions skipped", report.SkippedEntries)
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
	if tc.Name != "shell" {
		t.Fatalf("tool name=%q, want shell", tc.Name)
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

func TestConvertCodexRollout_UnsupportedToolKeepsSourceCall(t *testing.T) {
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

	// Expected: user, the source tool call, its result.
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Fatalf("msg0 role=%q, want user", msgs[0].Role)
	}
	assertCodexImportedToolCall(t, msgs[1], "unknown_tool", `{"foo":"bar"}`)
	if msgs[2].Role != message.RoleTool || msgs[2].ToolCallID != "call_un" || msgs[2].Content != "some result" {
		t.Fatalf("paired tool result lost: %+v", msgs[2])
	}
	if report.KeptSourceToolCalls != 1 {
		t.Fatalf("KeptSourceToolCalls=%d, want 1", report.KeptSourceToolCalls)
	}
	if report.ToolEntriesRendered != 2 {
		t.Fatalf("ToolEntriesRendered=%d, want 2", report.ToolEntriesRendered)
	}
}

func TestConvertCodexRollout_MalformedArgumentsWrapToValidJSON(t *testing.T) {
	// A truncated or malformed arguments string in the rollout must not fail
	// the whole import: the call is wrapped into a legal JSON object that
	// preserves the original text under "input".
	data := []byte(`{"timestamp":"2026-05-09T04:43:46Z","type":"session_meta","payload":{"id":"test-session"}}
{"timestamp":"2026-05-09T04:43:49Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"do something"}]}}
{"timestamp":"2026-05-09T04:43:52Z","type":"response_item","payload":{"type":"function_call","name":"unknown_tool","arguments":"{\"foo\":\"bar","call_id":"call_malformed"}}
{"timestamp":"2026-05-09T04:43:53Z","type":"response_item","payload":{"type":"function_call_output","output":"some result","call_id":"call_malformed"}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3", len(msgs))
	}
	if msgs[1].Role != message.RoleAssistant || len(msgs[1].ToolCalls) != 1 {
		t.Fatalf("assistant tool call lost: %+v", msgs[1])
	}
	tc := msgs[1].ToolCalls[0]
	if tc.Name != "unknown_tool" {
		t.Fatalf("tool name=%q, want unknown_tool", tc.Name)
	}
	// The wrapped arguments must be valid JSON and contain the original text.
	var wrapped map[string]string
	if err := json.Unmarshal(tc.Args, &wrapped); err != nil {
		t.Fatalf("wrapped args are not valid JSON: %v, raw=%s", err, tc.Args)
	}
	if wrapped["input"] != `{"foo":"bar` {
		t.Fatalf("wrapped input=%q, want original malformed text", wrapped["input"])
	}
	if msgs[2].Role != message.RoleTool || msgs[2].ToolCallID != "call_malformed" || msgs[2].Content != "some result" {
		t.Fatalf("paired tool result lost: %+v", msgs[2])
	}
	if report.KeptSourceToolCalls != 1 {
		t.Fatalf("KeptSourceToolCalls=%d, want 1", report.KeptSourceToolCalls)
	}
}

func TestConvertCodexRollout_EventMsgDeduplication(t *testing.T) {
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
	if len(msgs) != 2 {
		t.Fatalf("msgs len=%d, want 2", len(msgs))
	}
	if msgs[0].Content != "hello from response" || msgs[1].Content != "hi from response" {
		t.Fatalf("msgs=%+v", msgs)
	}
	if report.DuplicateSourceConflicts != 2 {
		t.Fatalf("DuplicateSourceConflicts=%d, want 2", report.DuplicateSourceConflicts)
	}
}

func TestConvertCodexRollout_FallbackTurnBoundaryByUserMessages(t *testing.T) {
	data := []byte(`{"timestamp":"2026-05-09T04:43:46Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"first question"}]}}
{"timestamp":"2026-05-09T04:43:47Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first answer"}]}}
{"timestamp":"2026-05-09T04:43:48Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"second question"}]}}
{"timestamp":"2026-05-09T04:43:49Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"second answer"}]}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("msgs len=%d, want 4", len(msgs))
	}
	if msgs[0].Content != "first question" || msgs[1].Content != "first answer" || msgs[2].Content != "second question" || msgs[3].Content != "second answer" {
		t.Fatalf("msgs=%+v", msgs)
	}
}

func TestConvertCodexRollout_KeepsMappedToolsStructured(t *testing.T) {
	data := []byte(`{"timestamp":"2026-05-09T04:43:49Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"run pwd"}]}}
{"timestamp":"2026-05-09T04:43:52Z","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":\"pwd\"}","call_id":"call_1"}}
{"timestamp":"2026-05-09T04:43:53Z","type":"response_item","payload":{"type":"function_call_output","output":"/tmp","call_id":"call_1"}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3", len(msgs))
	}
	if len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].Name != "shell" || msgs[2].Role != message.RoleTool {
		t.Fatalf("msgs=%+v", msgs)
	}
	if report.StructuredToolCalls != 1 || report.StructuredToolResults != 1 || report.KeptSourceToolCalls != 0 {
		t.Fatalf("report=%+v", report)
	}
}

func TestConvertCodexRollout_AttachesReasoningAndUsage(t *testing.T) {
	data := []byte(`{"timestamp":"2026-05-09T04:43:46Z","type":"turn_context","payload":{"turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:47Z","type":"response_item","payload":{"type":"reasoning","content":[{"type":"reasoning_text","text":"thinking"}]}}
{"timestamp":"2026-05-09T04:43:48Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}}
{"timestamp":"2026-05-09T04:43:49Z","type":"event_msg","payload":{"type":"token_count","turn_id":"turn-1","info":{"last_token_usage":{"input_tokens":10,"cached_input_tokens":2,"output_tokens":5,"reasoning_output_tokens":3}}}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ReasoningVisible, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("msgs len=%d, want 1", len(msgs))
	}
	if msgs[0].ReasoningContent != "thinking" {
		t.Fatalf("ReasoningContent=%q", msgs[0].ReasoningContent)
	}
	if msgs[0].Usage == nil || msgs[0].Usage.InputTokens != 10 || msgs[0].Usage.OutputTokens != 5 || msgs[0].Usage.CacheReadTokens != 2 || msgs[0].Usage.ReasoningTokens != 3 {
		t.Fatalf("Usage=%+v", msgs[0].Usage)
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

func TestConvertCodexRollout_PairsToolResultByCallOwnerAcrossTurnContextAdvance(t *testing.T) {
	data := []byte(`{"timestamp":"2026-05-09T04:43:46Z","type":"turn_context","payload":{"turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:47Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"run pwd"}],"turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:48Z","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":\"pwd\"}","call_id":"call_1","turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:49Z","type":"turn_context","payload":{"turn_id":"turn-2"}}
{"timestamp":"2026-05-09T04:43:50Z","type":"response_item","payload":{"type":"function_call_output","output":"/tmp","call_id":"call_1","turn_id":"turn-2"}}
{"timestamp":"2026-05-09T04:43:51Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}],"turn_id":"turn-2"}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("msgs len=%d, want 4", len(msgs))
	}
	if msgs[1].Role != "assistant" || len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].ID != "call_1" {
		t.Fatalf("msg1=%+v", msgs[1])
	}
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "call_1" || msgs[2].Content != "/tmp" {
		t.Fatalf("msg2=%+v", msgs[2])
	}
	if report.MissingToolDeclarations != 0 {
		t.Fatalf("MissingToolDeclarations=%d, want 0", report.MissingToolDeclarations)
	}
}

func TestConvertCodexRollout_MapsKnownToolAndKeepsUnknownSourceCall(t *testing.T) {
	data := []byte(`{"timestamp":"2026-05-09T04:43:46Z","type":"turn_context","payload":{"turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:47Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"inspect files"}],"turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:48Z","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":\"pwd\"}","call_id":"call_shell","turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:49Z","type":"response_item","payload":{"type":"function_call_output","output":"/tmp/project","call_id":"call_shell","turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:50Z","type":"response_item","payload":{"type":"function_call","name":"dynamic_tool","arguments":"{\"foo\":\"bar\"}","call_id":"call_dynamic","turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:51Z","type":"response_item","payload":{"type":"function_call_output","output":"dynamic result","call_id":"call_dynamic","turn_id":"turn-1"}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	if len(msgs) != 5 {
		t.Fatalf("msgs len=%d, want 5: %+v", len(msgs), msgs)
	}
	if msgs[1].Role != "assistant" || len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].Name != "shell" {
		t.Fatalf("mapped auto tool not structured: %+v", msgs[1])
	}
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "call_shell" {
		t.Fatalf("mapped auto tool result not linked: %+v", msgs[2])
	}
	assertCodexImportedToolCall(t, msgs[3], "dynamic_tool", `{"foo":"bar"}`)
	if msgs[4].Role != message.RoleTool || msgs[4].ToolCallID != "call_dynamic" || msgs[4].Content != "dynamic result" {
		t.Fatalf("unmapped tool result not linked: %+v", msgs[4])
	}
	if report.StructuredToolCalls != 1 || report.StructuredToolResults != 2 || report.KeptSourceToolCalls != 1 {
		t.Fatalf("report=%+v", report)
	}
}

func TestConvertCodexRollout_RealisticFixtureReportAndLineage(t *testing.T) {
	data := []byte(`{"timestamp":"2026-05-09T04:43:46Z","type":"session_meta","payload":{"id":"realistic-session"}}
{"timestamp":"2026-05-09T04:43:47Z","type":"turn_context","payload":{"turn_id":"turn-1","cwd":"/tmp/project","model":"gpt-5"}}
{"timestamp":"2026-05-09T04:43:48Z","type":"event_msg","payload":{"type":"user_message","message":"list go files","turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:49Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"list go files"}],"turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:50Z","type":"response_item","payload":{"type":"function_call","name":"glob","arguments":"{\"pattern\":\"**/*.go\",\"path\":\".\"}","call_id":"call_glob","turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:51Z","type":"event_msg","payload":{"type":"exec_command_begin","call_id":"call_glob","turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:52Z","type":"response_item","payload":{"type":"function_call_output","output":"[\"main.go\",\"internal/a.go\"]","call_id":"call_glob","turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:53Z","type":"event_msg","payload":{"type":"agent_message","message":"found files","turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:54Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"found files"}],"turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:55Z","type":"turn_context","payload":{"turn_id":"turn-2","cwd":"/tmp/project","model":"gpt-5"}}
{"timestamp":"2026-05-09T04:43:56Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"check dynamic"}],"turn_id":"turn-2"}}
{"timestamp":"2026-05-09T04:43:57Z","type":"response_item","payload":{"type":"function_call","name":"dynamic_tool","arguments":"{\"query\":\"x\"}","call_id":"call_dyn","turn_id":"turn-2"}}
{"timestamp":"2026-05-09T04:43:58Z","type":"response_item","payload":{"type":"function_call_output","output":{"ok":true},"call_id":"call_dyn","turn_id":"turn-2"}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	if len(msgs) != 7 {
		t.Fatalf("msgs len=%d, want 7: %+v", len(msgs), msgs)
	}
	if report.SourceSessionID != "realistic-session" {
		t.Fatalf("SourceSessionID=%q", report.SourceSessionID)
	}
	if report.StructuredToolCalls != 1 || report.StructuredToolResults != 2 || report.KeptSourceToolCalls != 1 {
		t.Fatalf("tool stats=%+v", report)
	}
	if report.SkippedStatusEvents != 1 || report.SkippedDuplicates != 2 {
		t.Fatalf("source/report stats=%+v", report)
	}
	declared := map[string]bool{}
	for i, msg := range msgs {
		for _, call := range msg.ToolCalls {
			declared[call.ID] = true
		}
		if msg.Role == "tool" && !declared[msg.ToolCallID] {
			t.Fatalf("tool result at %d has no earlier declaration: %+v", i, msg)
		}
	}
}

func TestConvertCodexRollout_WriteToolConvertsToWrite(t *testing.T) {
	data := []byte(`{"timestamp":"2026-05-09T04:43:46Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"write the file"}]}}
{"timestamp":"2026-05-09T04:43:47Z","type":"response_item","payload":{"type":"function_call","name":"write_file","arguments":"{\"path\":\"notes.txt\",\"content\":\"hello\"}","call_id":"call_w"}}
{"timestamp":"2026-05-09T04:43:48Z","type":"response_item","payload":{"type":"function_call_output","output":"ok","call_id":"call_w"}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3", len(msgs))
	}
	if msgs[1].Role != "assistant" || len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].Name != "write" {
		t.Fatalf("msg1=%+v", msgs[1])
	}
	var args map[string]any
	if err := json.Unmarshal(msgs[1].ToolCalls[0].Args, &args); err != nil {
		t.Fatalf("unmarshal args: %v", err)
	}
	if args["path"] != "notes.txt" || args["content"] != "hello" {
		t.Fatalf("args=%#v", args)
	}
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "call_w" || !strings.Contains(msgs[2].Content, "ok") {
		t.Fatalf("msg2=%+v", msgs[2])
	}
}

func TestConvertCodexRollout_EditToolConvertsToEdit(t *testing.T) {
	data := []byte(`{"timestamp":"2026-05-09T04:43:46Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"edit the file"}]}}
{"timestamp":"2026-05-09T04:43:47Z","type":"response_item","payload":{"type":"function_call","name":"edit_file","arguments":"{\"path\":\"notes.txt\",\"old_string\":\"hello\",\"new_string\":\"hi\",\"replace_all\":true}","call_id":"call_e"}}
{"timestamp":"2026-05-09T04:43:48Z","type":"response_item","payload":{"type":"function_call_output","output":"ok","call_id":"call_e"}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3", len(msgs))
	}
	if msgs[1].Role != "assistant" || len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].Name != tools.NameEdit {
		t.Fatalf("msg1=%+v", msgs[1])
	}
	var args map[string]any
	if err := json.Unmarshal(msgs[1].ToolCalls[0].Args, &args); err != nil {
		t.Fatalf("unmarshal args: %v", err)
	}
	// edit_file records a before/after pair, so the closest Chord tool is the
	// replacement: the pair carries over instead of being repackaged as a
	// patch envelope the source never issued.
	if args["path"] != "notes.txt" || args["old_string"] != "hello" || args["new_string"] != "hi" || args["replace_all"] != true {
		t.Fatalf("args=%#v", args)
	}
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "call_e" || !strings.Contains(msgs[2].Content, "ok") {
		t.Fatalf("msg2=%+v", msgs[2])
	}
}

func TestConvertCodexRollout_ApplyPatchCustomToolConvertsToEdit(t *testing.T) {
	data := []byte(`{"timestamp":"2026-05-09T04:43:46Z","type":"turn_context","payload":{"turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:47Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"patch it"}],"turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:48Z","type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","input":"*** Begin Patch\n*** Update File:a.txt\n@@\n-old\n+new\n*** End Patch","call_id":"call_patch","turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:49Z","type":"response_item","payload":{"type":"custom_tool_call_output","output":"Success. Updated the following files:\nM a.txt","call_id":"call_patch","turn_id":"turn-1"}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3: %+v", len(msgs), msgs)
	}
	if msgs[1].Role != "assistant" || len(msgs[1].ToolCalls) != 1 || strings.TrimSpace(msgs[1].Content) != "" {
		t.Fatalf("apply_patch not restored as Patch tool card: %+v", msgs[1])
	}
	call := msgs[1].ToolCalls[0]
	if call.Name != tools.NameApplyPatch || call.ID != "call_patch" {
		t.Fatalf("tool call=%+v, want apply_patch call_patch", call)
	}
	var args map[string]any
	if err := json.Unmarshal(call.Args, &args); err != nil {
		t.Fatalf("unmarshal args: %v", err)
	}
	patch, _ := args["patch"].(string)
	if _, exists := args["path"]; exists || !strings.Contains(patch, "*** Update File:a.txt") || !strings.Contains(patch, "-old") || !strings.Contains(patch, "+new") {
		t.Fatalf("patch not preserved: %#v", args)
	}
	if !strings.Contains(patch, "*** Begin Patch") || !strings.Contains(patch, "*** End Patch") {
		t.Fatalf("patch envelope not preserved: %q", patch)
	}
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "call_patch" || !strings.Contains(msgs[2].Content, "Success") {
		t.Fatalf("tool result not linked: %+v", msgs[2])
	}
	if report.StructuredToolCalls != 1 || report.StructuredToolResults != 1 || report.KeptSourceToolCalls != 0 {
		t.Fatalf("report=%+v", report)
	}
}

// codexExecRollout builds a one-turn rollout whose only tool call is a freeform
// `exec` with the given JavaScript input and result content items. The lines are
// marshalled rather than hand-written so the JavaScript (which is full of quotes
// and newlines) survives without escaping mistakes.
func codexExecRollout(t *testing.T, input string, outputItems ...string) []byte {
	t.Helper()
	items := make([]map[string]string, 0, len(outputItems))
	for _, text := range outputItems {
		items = append(items, map[string]string{"type": "input_text", "text": text})
	}
	lines := []any{
		map[string]any{"timestamp": "2026-05-09T04:43:46Z", "type": "turn_context", "payload": map[string]any{"turn_id": "turn-1"}},
		map[string]any{"timestamp": "2026-05-09T04:43:47Z", "type": "response_item", "payload": map[string]any{
			"type": "message", "role": "user", "turn_id": "turn-1",
			"content": []map[string]string{{"type": "input_text", "text": "go"}},
		}},
		map[string]any{"timestamp": "2026-05-09T04:43:48Z", "type": "response_item", "payload": map[string]any{
			"type": "custom_tool_call", "name": "exec", "input": input, "call_id": "call_exec", "turn_id": "turn-1",
		}},
		map[string]any{"timestamp": "2026-05-09T04:43:49Z", "type": "response_item", "payload": map[string]any{
			"type": "custom_tool_call_output", "output": items, "call_id": "call_exec", "turn_id": "turn-1",
		}},
	}
	var b strings.Builder
	for _, line := range lines {
		encoded, err := json.Marshal(line)
		if err != nil {
			t.Fatalf("marshal fixture line: %v", err)
		}
		b.Write(encoded)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func TestConvertCodexRollout_ExecCommandConvertsToShell(t *testing.T) {
	// A verbatim `exec` input from a Codex rollout: one nested exec_command call.
	input := "const r = await tools.exec_command({cmd:\"git status --short\",\"workdir\":\"/tmp/project\",\"yield_time_ms\":10000,\"max_output_tokens\":12000});\ntext(r.output);"
	data := codexExecRollout(t, input,
		"Script completed\nWall time 0.1 seconds\nOutput:\n",
		"## main\n",
	)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3: %+v", len(msgs), msgs)
	}
	if msgs[1].Role != message.RoleAssistant || len(msgs[1].ToolCalls) != 1 {
		t.Fatalf("exec not mapped to a tool call: %+v", msgs[1])
	}
	call := msgs[1].ToolCalls[0]
	if call.Name != tools.NameShell || call.ID != "call_exec" {
		t.Fatalf("tool call=%+v, want shell call_exec", call)
	}
	var args map[string]any
	if err := json.Unmarshal(call.Args, &args); err != nil {
		t.Fatalf("unmarshal args: %v", err)
	}
	if args["command"] != "git status --short" || args["workdir"] != "/tmp/project" {
		t.Fatalf("args=%#v", args)
	}
	// The mapped call must look like a live Chord call: no synthesized
	// description, so the card header shows the command's first line.
	if _, ok := args["description"]; ok {
		t.Fatalf("imported shell call carries a synthesized description: %#v", args)
	}
	if msgs[2].Role != message.RoleTool || msgs[2].ToolCallID != "call_exec" {
		t.Fatalf("tool result not linked: %+v", msgs[2])
	}
	if msgs[2].Content != "## main\n" {
		t.Fatalf("result envelope not stripped: %q", msgs[2].Content)
	}
	if msgs[2].ToolStatus != message.ToolStatusSuccess {
		t.Fatalf("tool status=%q, want success", msgs[2].ToolStatus)
	}
	if report.StructuredToolCalls != 1 || report.StructuredToolResults != 1 || report.KeptSourceToolCalls != 0 {
		t.Fatalf("report=%+v", report)
	}
	if report.CodexExecScripts != 1 {
		t.Fatalf("CodexExecScripts=%d, want 1", report.CodexExecScripts)
	}
}

func TestConvertCodexRollout_ExecFailureMarksResultError(t *testing.T) {
	input := "const r = await tools.exec_command({cmd:\"false\"});"
	data := codexExecRollout(t, input,
		"Script failed\nWall time 0.0 seconds\nOutput:\nScript error:\nError: exit status 1",
	)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3: %+v", len(msgs), msgs)
	}
	if msgs[2].ToolStatus != message.ToolStatusError {
		t.Fatalf("tool status=%q, want error", msgs[2].ToolStatus)
	}
	if !strings.HasPrefix(msgs[2].Content, "Script error:") {
		t.Fatalf("failure body=%q", msgs[2].Content)
	}
}

func TestConvertCodexRollout_ExecApplyPatchConvertsToApplyPatch(t *testing.T) {
	input := "const patch = \"*** Begin Patch\\n*** Update File: a.txt\\n@@\\n-old\\n+new\\n*** End Patch\";\ntext(await tools.apply_patch(patch));"
	data := codexExecRollout(t, input,
		"Script completed\nWall time 0.0 seconds\nOutput:\n",
		"Success. Updated the following files:\nM a.txt",
	)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3: %+v", len(msgs), msgs)
	}
	call := msgs[1].ToolCalls[0]
	if call.Name != tools.NameApplyPatch || call.ID != "call_exec" {
		t.Fatalf("tool call=%+v, want apply_patch", call)
	}
	var args map[string]any
	if err := json.Unmarshal(call.Args, &args); err != nil {
		t.Fatalf("unmarshal args: %v", err)
	}
	patch, _ := args["patch"].(string)
	if !strings.Contains(patch, "*** Update File: a.txt") || !strings.Contains(patch, "-old") || !strings.Contains(patch, "+new") {
		t.Fatalf("patch not preserved: %q", patch)
	}
	if !strings.Contains(msgs[2].Content, "Success") {
		t.Fatalf("tool result=%q", msgs[2].Content)
	}
}

func TestConvertCodexRollout_ExecUnmappableNestedToolKeepsSourceCall(t *testing.T) {
	// write_stdin drives an interactive session Chord does not keep, so the call
	// has no mapping — but it still ran, so the source `exec` call and its result
	// stay in the trajectory.
	input := "const r = await tools.write_stdin({session_id:481,yield_time_ms:2500,max_output_tokens:8000,chars:\"\"});\ntext(JSON.stringify(r));"
	data := codexExecRollout(t, input,
		"Script completed\nWall time 1.0 seconds\nOutput:\n",
		"{\"session_id\":481,\"output\":\"more\"}",
	)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3: %+v", len(msgs), msgs)
	}
	wantArgs, _ := json.Marshal(map[string]string{"input": input})
	assertCodexImportedToolCall(t, msgs[1], "exec", string(wantArgs))
	if msgs[2].Role != message.RoleTool || msgs[2].ToolCallID != "call_exec" || msgs[2].Content != "{\"session_id\":481,\"output\":\"more\"}" {
		t.Fatalf("paired tool result lost: %+v", msgs[2])
	}
	if report.StructuredToolCalls != 0 || report.StructuredToolResults != 1 || report.KeptSourceToolCalls != 1 {
		t.Fatalf("report=%+v", report)
	}
	if report.CodexExecScripts != 1 {
		t.Fatalf("CodexExecScripts=%d, want 1", report.CodexExecScripts)
	}
}

func TestConvertCodexRollout_ExecWithMultipleNestedCallsKeepsSourceCall(t *testing.T) {
	// The isolate may issue several calls; the mapping must refuse instead of
	// picking the first, and the unanalyzable script stays the source call.
	input := "const a = await tools.exec_command({cmd:\"pwd\"});\nconst b = await tools.exec_command({cmd:\"ls\"});\ntext(a.output + b.output);"
	data := codexExecRollout(t, input,
		"Script completed\nWall time 0.0 seconds\nOutput:\n",
		"/tmp\nfile.go\n",
	)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3: %+v", len(msgs), msgs)
	}
	wantArgs, _ := json.Marshal(map[string]string{"input": input})
	assertCodexImportedToolCall(t, msgs[1], "exec", string(wantArgs))
	if msgs[2].Role != message.RoleTool || msgs[2].Content != "/tmp\nfile.go\n" {
		t.Fatalf("paired tool result lost: %+v", msgs[2])
	}
	if report.StructuredToolCalls != 0 || report.KeptSourceToolCalls != 1 {
		t.Fatalf("report=%+v", report)
	}
	if report.CodexExecScripts != 0 {
		t.Fatalf("CodexExecScripts=%d, want 0", report.CodexExecScripts)
	}
}

func TestConvertCodexRollout_CollaborationToolKeepsSourceCall(t *testing.T) {
	data := []byte(`{"timestamp":"2026-05-09T04:43:46Z","type":"turn_context","payload":{"turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:47Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"review it"}],"turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:48Z","type":"response_item","payload":{"type":"function_call","name":"wait_agent","namespace":"collaboration","arguments":"{\"timeout_ms\":1000}","call_id":"call_agent","turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:49Z","type":"response_item","payload":{"type":"function_call_output","output":"agent finished","call_id":"call_agent","turn_id":"turn-1"}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3: %+v", len(msgs), msgs)
	}
	assertCodexImportedToolCall(t, msgs[1], "wait_agent", `{"timeout_ms":1000}`)
	if msgs[2].Role != message.RoleTool || msgs[2].ToolCallID != "call_agent" || msgs[2].Content != "agent finished" {
		t.Fatalf("paired tool result lost: %+v", msgs[2])
	}
	if report.StructuredToolCalls != 0 || report.StructuredToolResults != 1 || report.KeptSourceToolCalls != 1 {
		t.Fatalf("report=%+v", report)
	}
}

func TestConvertCodexRollout_WebSearchEndRestoresAsToolCall(t *testing.T) {
	data := []byte(`{"timestamp":"2026-05-09T04:43:46Z","type":"turn_context","payload":{"turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:47Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"search it"}],"turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:48Z","type":"event_msg","payload":{"type":"web_search_begin","call_id":"ws_1","turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:49Z","type":"event_msg","payload":{"type":"web_search_end","call_id":"ws_1","query":"golang json rawmessage","action":{"type":"search","query":"golang json rawmessage"},"turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:50Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}],"turn_id":"turn-1"}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3: %+v", len(msgs), msgs)
	}
	assertCodexImportedToolCall(t, msgs[1], "web_search",
		`{"action":{"query":"golang json rawmessage","type":"search"},"query":"golang json rawmessage"}`)
	if msgs[2].Role != "assistant" || msgs[2].Content != "done" {
		t.Fatalf("trailing assistant message lost: %+v", msgs[2])
	}
	if report.StructuredToolCalls != 0 || report.KeptSourceToolCalls != 1 || report.SkippedStatusEvents != 1 {
		t.Fatalf("report=%+v", report)
	}
}

func TestCodexNormalizeSearchArgsUseCurrentToolSchema(t *testing.T) {
	grepRaw := normalizeImportedGrepArgs(map[string]any{
		"pattern": "TODO",
		"path":    "internal",
		"glob":    "**/*.go",
	})
	var grepArgs map[string]any
	if err := json.Unmarshal(grepRaw, &grepArgs); err != nil {
		t.Fatalf("unmarshal grep args: %v", err)
	}
	if _, ok := grepArgs["path"]; ok {
		t.Fatalf("grep args use legacy path field: %#v", grepArgs)
	}
	if _, ok := grepArgs["glob"]; ok {
		t.Fatalf("grep args use legacy glob field: %#v", grepArgs)
	}
	paths, _ := grepArgs["paths"].([]any)
	includes, _ := grepArgs["includes"].([]any)
	if grepArgs["pattern"] != "TODO" || len(paths) != 1 || paths[0] != "internal" || len(includes) != 1 || includes[0] != "**/*.go" {
		t.Fatalf("grep args=%#v", grepArgs)
	}

	globRaw := normalizeImportedGlobArgs(map[string]any{
		"pattern": "**/*.go",
		"path":    ".",
	})
	var globArgs map[string]any
	if err := json.Unmarshal(globRaw, &globArgs); err != nil {
		t.Fatalf("unmarshal glob args: %v", err)
	}
	if _, ok := globArgs["pattern"]; ok {
		t.Fatalf("glob args use legacy pattern field: %#v", globArgs)
	}
	patterns, _ := globArgs["patterns"].([]any)
	if len(patterns) != 1 || patterns[0] != "**/*.go" || globArgs["path"] != "." {
		t.Fatalf("glob args=%#v", globArgs)
	}
}

// A mapped shell call must carry only what the source recorded: a description
// survives when the origin had one (OpenCode's bash tool), and is absent
// otherwise so the Chord card header shows the command's first line — the same
// view a live call produces, with no import marker for user or model.
func TestCodexNormalizeShellArgsOmitsSynthesizedDescription(t *testing.T) {
	raw := normalizeImportedShellArgs(map[string]any{"command": "ls -la"})
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatalf("unmarshal shell args: %v", err)
	}
	if _, ok := args["description"]; ok {
		t.Fatalf("synthesized a description the source never had: %#v", args)
	}
	if args["command"] != "ls -la" {
		t.Fatalf("command lost: %#v", args)
	}

	withDesc := normalizeImportedShellArgs(map[string]any{"command": "ls -la", "description": "list files"})
	var descArgs map[string]any
	if err := json.Unmarshal(withDesc, &descArgs); err != nil {
		t.Fatalf("unmarshal shell args: %v", err)
	}
	if descArgs["description"] != "list files" {
		t.Fatalf("source description dropped: %#v", descArgs)
	}
}

// A mapped delete call must satisfy Chord's schema even when the source
// recorded no reason: the stand-in names the gap rather than the import, so
// the card header reads as a structural fact and carries no provenance.
func TestCodexNormalizeDeleteArgsUsesNeutralReasonFallback(t *testing.T) {
	raw := normalizeImportedDeleteArgs(map[string]any{"paths": []any{"a.txt"}})
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatalf("unmarshal delete args: %v", err)
	}
	if args["reason"] != deleteReasonFallback {
		t.Fatalf("reason=%v, want the neutral stand-in %q", args["reason"], deleteReasonFallback)
	}
	if strings.Contains(args["reason"].(string), "Imported") {
		t.Fatalf("reason leaked import provenance: %#v", args)
	}

	withReason := normalizeImportedDeleteArgs(map[string]any{"paths": []any{"a.txt"}, "reason": "stale artifact"})
	var reasonArgs map[string]any
	if err := json.Unmarshal(withReason, &reasonArgs); err != nil {
		t.Fatalf("unmarshal delete args: %v", err)
	}
	if reasonArgs["reason"] != "stale artifact" {
		t.Fatalf("source reason dropped: %#v", reasonArgs)
	}
}

func TestCodexNormalizeExecCommandArgsOmitsSynthesizedDescription(t *testing.T) {
	raw := codexNormalizeExecCommandArgs(map[string]any{"cmd": "git status", "workdir": "/tmp/project", "yield_time_ms": 10000, "max_output_tokens": 12000})
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatalf("unmarshal exec_command args: %v", err)
	}
	if _, ok := args["description"]; ok {
		t.Fatalf("synthesized a description the source never had: %#v", args)
	}
	if args["command"] != "git status" || args["workdir"] != "/tmp/project" {
		t.Fatalf("command or workdir lost: %#v", args)
	}
	if _, ok := args["timeout_ms"]; ok {
		t.Fatalf("host-only knobs leaked into chord args: %#v", args)
	}
}

func TestCodexNormalizeShellArgsUseCurrentToolSchema(t *testing.T) {
	raw := normalizeImportedShellArgs(map[string]any{
		"cmd":     "go test ./...",
		"workdir": "/tmp",
		"timeout": 30,
	})
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatalf("unmarshal shell args: %v", err)
	}
	if _, ok := args["timeout"]; ok {
		t.Fatalf("shell args use legacy timeout field: %#v", args)
	}
	// Codex records the shell timeout in seconds; Chord's shell takes
	// timeout_ms, so the imported deadline must be converted, not dropped.
	if args["timeout_ms"] != float64(30000) {
		t.Fatalf("timeout_ms=%v, want 30000", args["timeout_ms"])
	}
	if args["command"] != "go test ./..." || args["workdir"] != "/tmp" {
		t.Fatalf("shell args=%#v", args)
	}

	fractional := normalizeImportedShellArgs(map[string]any{
		"command": "sleep 2",
		"timeout": 1.5,
	})
	var fractionalArgs map[string]any
	if err := json.Unmarshal(fractional, &fractionalArgs); err != nil {
		t.Fatalf("unmarshal fractional shell args: %v", err)
	}
	if fractionalArgs["timeout_ms"] != float64(1500) {
		t.Fatalf("timeout_ms=%v, want 1500", fractionalArgs["timeout_ms"])
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
