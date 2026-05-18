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

func assertCodexUnsupportedToolArgs(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatalf("unmarshal unsupported args: %v", err)
	}
	reason, _ := args["reason"].(string)
	if args["unsupported"] != true || args["source"] != "codex" || strings.TrimSpace(reason) == "" {
		t.Fatalf("unsupported metadata missing: %#v", args)
	}
	if _, ok := args["arguments"]; !ok {
		t.Fatalf("original arguments missing: %#v", args)
	}
}

func TestConvertCodexRollout_ParsesBasicJSONL(t *testing.T) {
	data := []byte(`{"timestamp":"2026-01-01T00:00:00Z","item":{"type":"EventMsg","session_id":"sess-1","role":"user","content":"hi"}}
{"timestamp":"2026-01-01T00:00:01Z","item":{"type":"ResponseItem","role":"assistant","content":"hello"}}
{"timestamp":"2026-01-01T00:00:02Z","item":{"FunctionCall":{"name":"bash","arguments":{"command":"ls"}}}}
{"timestamp":"2026-01-01T00:00:03Z","item":{"FunctionCallOutput":{"output":"file1"}}}
{"timestamp":"2026-01-01T00:00:04Z","item":{"type":"Reasoning","text":"secret"}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ToolModeStructured, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}

	// Expected source order: user "hi", assistant "hello", unsupported bash tool card,
	// then the linked tool result. "bash" is not in the tool mapping, so the
	// original tool name is preserved with unsupported metadata.
	if len(msgs) != 4 {
		t.Fatalf("msgs len=%d, want 4", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "hi" {
		t.Fatalf("msg0=%+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "hello" {
		t.Fatalf("msg1=%+v", msgs[1])
	}
	if msgs[2].Role != "assistant" || len(msgs[2].ToolCalls) != 1 || msgs[2].ToolCalls[0].Name != "bash" {
		t.Fatalf("msg2=%+v", msgs[2])
	}
	assertCodexUnsupportedToolArgs(t, msgs[2].ToolCalls[0].Args)
	if msgs[3].Role != "assistant" || !strings.Contains(msgs[3].Content, "orphaned tool result") {
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
	msgs, err := convertCodexRollout(data, ToolModeStructured, ReasoningStrict, &report)
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
	if msgs[2].ToolCalls[0].Name != "Shell" {
		t.Fatalf("tool name=%q, want Shell", msgs[2].ToolCalls[0].Name)
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
	msgs, err := convertCodexRollout(data, ToolModeAuto, ReasoningStrict, &report)
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
	msgs, err := convertCodexRollout(data, ToolModeStructured, ReasoningStrict, &report)
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
	msgs, err := convertCodexRollout(data, ToolModeStructured, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}

	// Expected: user, unsupported original-name tool card, linked tool result
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Fatalf("msg0 role=%q, want user", msgs[0].Role)
	}
	if msgs[1].Role != "assistant" || len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].Name != "unknown_tool" {
		t.Fatalf("msg1 should be unknown_tool card: %+v", msgs[1])
	}
	assertCodexUnsupportedToolArgs(t, msgs[1].ToolCalls[0].Args)
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "call_un" || !strings.Contains(msgs[2].Content, "some result") {
		t.Fatalf("msg2 should be linked tool result: %+v", msgs[2])
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
	data := []byte(`{"timestamp":"2026-05-09T04:43:46Z","type":"session_meta","payload":{"id":"dedup-test"}}
{"timestamp":"2026-05-09T04:43:48Z","type":"event_msg","payload":{"type":"user_message","message":"hello from event"}}
{"timestamp":"2026-05-09T04:43:49Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello from response"}]}}
{"timestamp":"2026-05-09T04:43:50Z","type":"event_msg","payload":{"type":"agent_message","message":"hi from event"}}
{"timestamp":"2026-05-09T04:43:51Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi from response"}]}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ToolModeStructured, ReasoningStrict, &report)
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
	msgs, err := convertCodexRollout(data, ToolModeStructured, ReasoningStrict, &report)
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

func TestConvertCodexRollout_TextToolModeDowngradesMappedTools(t *testing.T) {
	data := []byte(`{"timestamp":"2026-05-09T04:43:49Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"run pwd"}]}}
{"timestamp":"2026-05-09T04:43:52Z","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":\"pwd\"}","call_id":"call_1"}}
{"timestamp":"2026-05-09T04:43:53Z","type":"response_item","payload":{"type":"function_call_output","output":"/tmp","call_id":"call_1"}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ToolModeText, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3", len(msgs))
	}
	if len(msgs[1].ToolCalls) != 0 || msgs[2].Role != "assistant" {
		t.Fatalf("msgs=%+v", msgs)
	}
	if report.StructuredToolCalls != 0 || report.UnsupportedToolCalls != 1 {
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
	msgs, err := convertCodexRollout(data, ToolModeStructured, ReasoningVisible, &report)
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
	msgs, err := convertCodexRollout(data, ToolModeStructured, ReasoningStrict, &report)
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
	msgs, err := convertCodexRollout(data, ToolModeStructured, ReasoningStrict, &report)
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

func TestNormalizeToolMode_DefaultsCodexToAuto(t *testing.T) {
	mode, err := normalizeToolMode("codex", "")
	if err != nil {
		t.Fatalf("normalizeToolMode: %v", err)
	}
	if mode != ToolModeAuto {
		t.Fatalf("mode=%q, want %q", mode, ToolModeAuto)
	}
}

func TestConvertCodexRollout_AutoModeStructuresMappedAndDowngradesUnknown(t *testing.T) {
	data := []byte(`{"timestamp":"2026-05-09T04:43:46Z","type":"turn_context","payload":{"turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:47Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"inspect files"}],"turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:48Z","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":\"pwd\"}","call_id":"call_shell","turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:49Z","type":"response_item","payload":{"type":"function_call_output","output":"/tmp/project","call_id":"call_shell","turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:50Z","type":"response_item","payload":{"type":"function_call","name":"dynamic_tool","arguments":"{\"foo\":\"bar\"}","call_id":"call_dynamic","turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:51Z","type":"response_item","payload":{"type":"function_call_output","output":"dynamic result","call_id":"call_dynamic","turn_id":"turn-1"}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ToolModeAuto, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	if len(msgs) != 5 {
		t.Fatalf("msgs len=%d, want 5: %+v", len(msgs), msgs)
	}
	if msgs[1].Role != "assistant" || len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].Name != "Shell" {
		t.Fatalf("mapped auto tool not structured: %+v", msgs[1])
	}
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "call_shell" {
		t.Fatalf("mapped auto tool result not linked: %+v", msgs[2])
	}
	if msgs[3].Role != "assistant" || len(msgs[3].ToolCalls) != 1 || msgs[3].ToolCalls[0].Name != "dynamic_tool" {
		t.Fatalf("unknown auto tool not restored as original-name card: %+v", msgs[3])
	}
	assertCodexUnsupportedToolArgs(t, msgs[3].ToolCalls[0].Args)
	if msgs[4].Role != "tool" || msgs[4].ToolCallID != "call_dynamic" || !strings.Contains(msgs[4].Content, "dynamic result") {
		t.Fatalf("unknown auto result not linked: %+v", msgs[4])
	}
	if report.StructuredToolCalls != 1 || report.StructuredToolResults != 1 || report.UnsupportedToolCalls != 1 || report.UnsupportedToolResults != 1 {
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
	msgs, err := convertCodexRollout(data, ToolModeAuto, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	if len(msgs) != 7 {
		t.Fatalf("msgs len=%d, want 7: %+v", len(msgs), msgs)
	}
	if report.SourceSessionID != "realistic-session" {
		t.Fatalf("SourceSessionID=%q", report.SourceSessionID)
	}
	if report.StructuredToolCalls != 1 || report.StructuredToolResults != 1 || report.UnsupportedToolCalls != 1 || report.UnsupportedToolResults != 1 {
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

func TestConvertCodexRollout_UnsupportedWriteToolRestoresAsUnsupportedToolCard(t *testing.T) {
	data := []byte(`{"timestamp":"2026-05-09T04:43:46Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"write the file"}]}}
{"timestamp":"2026-05-09T04:43:47Z","type":"response_item","payload":{"type":"function_call","name":"write_file","arguments":"{\"path\":\"notes.txt\",\"content\":\"hello\"}","call_id":"call_w"}}
{"timestamp":"2026-05-09T04:43:48Z","type":"response_item","payload":{"type":"function_call_output","output":"ok","call_id":"call_w"}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ToolModeStructured, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3", len(msgs))
	}
	if msgs[1].Role != "assistant" || len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].Name != "write_file" {
		t.Fatalf("msg1=%+v", msgs[1])
	}
	assertCodexUnsupportedToolArgs(t, msgs[1].ToolCalls[0].Args)
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "call_w" || !strings.Contains(msgs[2].Content, "ok") {
		t.Fatalf("msg2=%+v", msgs[2])
	}
}

func TestConvertCodexRollout_UnsupportedEditToolRestoresAsUnsupportedToolCard(t *testing.T) {
	data := []byte(`{"timestamp":"2026-05-09T04:43:46Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"edit the file"}]}}
{"timestamp":"2026-05-09T04:43:47Z","type":"response_item","payload":{"type":"function_call","name":"edit_file","arguments":"{\"path\":\"notes.txt\",\"old_string\":\"hello\",\"new_string\":\"hi\",\"replace_all\":true}","call_id":"call_e"}}
{"timestamp":"2026-05-09T04:43:48Z","type":"response_item","payload":{"type":"function_call_output","output":"ok","call_id":"call_e"}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ToolModeStructured, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3", len(msgs))
	}
	if msgs[1].Role != "assistant" || len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].Name != "edit_file" {
		t.Fatalf("msg1=%+v", msgs[1])
	}
	assertCodexUnsupportedToolArgs(t, msgs[1].ToolCalls[0].Args)
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "call_e" || !strings.Contains(msgs[2].Content, "ok") {
		t.Fatalf("msg2=%+v", msgs[2])
	}
}

func TestConvertCodexRollout_ApplyPatchCustomToolRestoresAsUnsupportedToolCard(t *testing.T) {
	data := []byte(`{"timestamp":"2026-05-09T04:43:46Z","type":"turn_context","payload":{"turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:47Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"patch it"}],"turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:48Z","type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","input":"*** Begin Patch\n*** Update File: a.txt\n@@\n-old\n+new\n*** End Patch","call_id":"call_patch","turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:49Z","type":"response_item","payload":{"type":"custom_tool_call_output","output":"Success. Updated the following files:\nM a.txt","call_id":"call_patch","turn_id":"turn-1"}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ToolModeAuto, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3: %+v", len(msgs), msgs)
	}
	if msgs[1].Role != "assistant" || len(msgs[1].ToolCalls) != 1 || strings.TrimSpace(msgs[1].Content) != "" {
		t.Fatalf("apply_patch not restored as tool card: %+v", msgs[1])
	}
	call := msgs[1].ToolCalls[0]
	if call.Name != "apply_patch" || call.ID != "call_patch" {
		t.Fatalf("tool call=%+v, want apply_patch call_patch", call)
	}
	var args map[string]any
	if err := json.Unmarshal(call.Args, &args); err != nil {
		t.Fatalf("unmarshal args: %v", err)
	}
	if args["unsupported"] != true || args["source"] != "codex" || args["reason"] == "" {
		t.Fatalf("unsupported metadata missing: %#v", args)
	}
	arguments, ok := args["arguments"].(map[string]any)
	if !ok || !strings.Contains(arguments["input"].(string), "*** Begin Patch") {
		t.Fatalf("original arguments not preserved: %#v", args["arguments"])
	}
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "call_patch" || !strings.Contains(msgs[2].Content, "Success") {
		t.Fatalf("tool result not linked: %+v", msgs[2])
	}
	if report.StructuredToolCalls != 0 || report.StructuredToolResults != 0 || report.UnsupportedToolCalls != 1 || report.UnsupportedToolResults != 1 {
		t.Fatalf("report=%+v", report)
	}
}

func TestConvertCodexRollout_WebSearchEndRestoresAsUnsupportedToolCard(t *testing.T) {
	data := []byte(`{"timestamp":"2026-05-09T04:43:46Z","type":"turn_context","payload":{"turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:47Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"search it"}],"turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:48Z","type":"event_msg","payload":{"type":"web_search_begin","call_id":"ws_1","turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:49Z","type":"event_msg","payload":{"type":"web_search_end","call_id":"ws_1","query":"golang json rawmessage","action":{"type":"search","query":"golang json rawmessage"},"turn_id":"turn-1"}}
{"timestamp":"2026-05-09T04:43:50Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}],"turn_id":"turn-1"}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ToolModeAuto, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3: %+v", len(msgs), msgs)
	}
	if msgs[1].Role != "assistant" || len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].Name != "web_search" {
		t.Fatalf("web_search not restored as tool card: %+v", msgs[1])
	}
	assertCodexUnsupportedToolArgs(t, msgs[1].ToolCalls[0].Args)
	var args map[string]any
	if err := json.Unmarshal(msgs[1].ToolCalls[0].Args, &args); err != nil {
		t.Fatalf("unmarshal args: %v", err)
	}
	arguments, ok := args["arguments"].(map[string]any)
	if !ok || arguments["query"] != "golang json rawmessage" {
		t.Fatalf("web_search arguments not preserved: %#v", args)
	}
	if report.StructuredToolCalls != 0 || report.UnsupportedToolCalls != 1 || report.SkippedStatusEvents != 1 {
		t.Fatalf("report=%+v", report)
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
