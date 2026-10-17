package sessionimport

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// assertClaudeUnsupportedToolCall asserts that a Claude tool call with no Chord
// mapping stayed a tool call under the source name, with the source arguments
// verbatim and no importer metadata folded in.
func assertClaudeUnsupportedToolCall(t *testing.T, msg message.Message, wantName string, wantArgs string) {
	t.Helper()
	if msg.Role != message.RoleAssistant {
		t.Fatalf("role=%q, want assistant", msg.Role)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("tool calls=%+v, want the source call preserved", msg.ToolCalls)
	}
	if msg.ToolCalls[0].Name != wantName {
		t.Fatalf("tool name=%q, want %q", msg.ToolCalls[0].Name, wantName)
	}
	if got := strings.TrimSpace(string(msg.ToolCalls[0].Args)); got != wantArgs {
		t.Fatalf("tool args=%s, want the source arguments verbatim %s", got, wantArgs)
	}
}

func TestConvertClaudeTranscript_AcceptsStringContentMessages(t *testing.T) {
	data := []byte(`{"uuid":"u1","message":{"role":"user","content":"plain question"}}
{"uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"text","text":"answer"}]}}
`)
	var report ImportReport
	msgs, err := convertClaudeTranscript(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertClaudeTranscript: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("msgs len=%d, want 2: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != "user" || msgs[0].Content != "plain question" {
		t.Fatalf("string user content not imported: %+v", msgs[0])
	}
	if report.Claude == nil || report.Claude.NonSidechainMessages != 2 || report.Claude.SelectedSpanLength != 2 {
		t.Fatalf("claude report=%+v", report.Claude)
	}
}

func TestConvertClaudeTranscript_StructuredThinkingAndTools(t *testing.T) {
	data := []byte(`{"uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}
{"uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"sig"},{"type":"text","text":"hello"},{"type":"tool_use","id":"toolu_1","name":"Shell","input":{"command":"pwd"}}]}}
{"uuid":"u2","parentUuid":"a1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"/tmp"}]}}
`)
	var report ImportReport
	msgs, err := convertClaudeTranscript(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertClaudeTranscript: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3", len(msgs))
	}
	if msgs[1].Role != "assistant" || len(msgs[1].ThinkingBlocks) != 1 || len(msgs[1].ToolCalls) != 1 {
		t.Fatalf("assistant msg=%+v", msgs[1])
	}
	if msgs[1].ToolCalls[0].Name != "shell" {
		t.Fatalf("imported tool name=%q, want shell", msgs[1].ToolCalls[0].Name)
	}
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "toolu_1" || msgs[2].Content != "/tmp" {
		t.Fatalf("tool msg=%+v", msgs[2])
	}
	if report.Claude == nil || report.Claude.StructuredToolCalls != 1 || report.Claude.StructuredToolResults != 1 {
		t.Fatalf("claude report=%+v", report.Claude)
	}
}

func TestConvertClaudeTranscript_DoesNotRestoreEarlierRetryFailure(t *testing.T) {
	data := []byte(`{"uuid":"u0","message":{"role":"user","content":"same prompt"}}
{"uuid":"a0","parentUuid":"u0","message":{"role":"assistant","content":[{"type":"text","text":"API Error: unavailable"}]}}
{"uuid":"s0","parentUuid":"a0","type":"system"}
{"uuid":"u1","message":{"role":"user","content":"same prompt"}}
{"uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"pwd"}}]}}
{"uuid":"r1","parentUuid":"a1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"/tmp"}]}}
{"uuid":"a2","parentUuid":"r1","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}
`)
	var report ImportReport
	msgs, err := convertClaudeTranscript(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertClaudeTranscript: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("msgs len=%d, want 4: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != "user" || msgs[0].Content != "same prompt" || len(msgs[1].ToolCalls) != 1 || msgs[3].Content != "done" {
		t.Fatalf("main retry chain not preserved: %+v", msgs)
	}
	for _, msg := range msgs {
		if strings.Contains(msg.Content, "API Error") {
			t.Fatalf("failed retry branch should not be restored: %+v", msgs)
		}
	}
	if report.Claude == nil || report.Claude.SelectedSpanLength != 4 || report.Claude.TerminalCandidates != 2 {
		t.Fatalf("claude report=%+v", report.Claude)
	}
}

func TestConvertClaudeTranscript_MapsBashAndReadTools(t *testing.T) {
	data := []byte(`{"uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"inspect"}]}}
{"uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"pwd","description":"print cwd"}}]}}
{"uuid":"u2","parentUuid":"a1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"/tmp"}]}}
{"uuid":"a2","parentUuid":"u2","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_2","name":"Read","input":{"file_path":"/tmp/a.txt","offset":2,"limit":5}}]}}
`)
	var report ImportReport
	msgs, err := convertClaudeTranscript(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertClaudeTranscript: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("msgs len=%d, want 4: %+v", len(msgs), msgs)
	}
	if len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].Name != "shell" {
		t.Fatalf("Bash not mapped to shell: %+v", msgs[1])
	}
	if len(msgs[3].ToolCalls) != 1 || msgs[3].ToolCalls[0].Name != "read" {
		t.Fatalf("Read not restored as tool card: %+v", msgs[3])
	}
	if report.StructuredToolCalls != 2 || report.Claude == nil || report.Claude.StructuredToolCalls != 2 || report.UnsupportedToolCalls != 0 {
		t.Fatalf("report=%+v", report)
	}
}

// A mapped Bash call must look like a live Chord shell call: the source
// description survives, and a call without one stays without one so the card
// header shows the command's first line instead of an import marker.
func TestNormalizeClaudeBashArgsOmitsSynthesizedDescription(t *testing.T) {
	raw := normalizeClaudeBashArgs(json.RawMessage(`{"command":"pwd"}`))
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatalf("unmarshal bash args: %v", err)
	}
	if _, ok := args["description"]; ok {
		t.Fatalf("synthesized a description the source never had: %#v", args)
	}
	if args["command"] != "pwd" {
		t.Fatalf("command lost: %#v", args)
	}

	withDesc := normalizeClaudeBashArgs(json.RawMessage(`{"command":"pwd","description":"print cwd"}`))
	var descArgs map[string]any
	if err := json.Unmarshal(withDesc, &descArgs); err != nil {
		t.Fatalf("unmarshal bash args: %v", err)
	}
	if descArgs["description"] != "print cwd" {
		t.Fatalf("source description dropped: %#v", descArgs)
	}
}

// A mapped delete call must satisfy Chord's schema even when the source
// recorded no reason: the stand-in names the gap rather than the import, so
// the card header reads as a structural fact and carries no provenance.
func TestNormalizeClaudeDeleteArgsUsesNeutralReasonFallback(t *testing.T) {
	raw := normalizeClaudeDeleteArgs(json.RawMessage(`{"path":"a.txt"}`))
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

	withReason := normalizeClaudeDeleteArgs(json.RawMessage(`{"path":"a.txt","reason":"stale artifact"}`))
	var reasonArgs map[string]any
	if err := json.Unmarshal(withReason, &reasonArgs); err != nil {
		t.Fatalf("unmarshal delete args: %v", err)
	}
	if reasonArgs["reason"] != "stale artifact" {
		t.Fatalf("source reason dropped: %#v", reasonArgs)
	}
}

// TestConvertClaudeTranscript_MapsAskUserQuestionToQuestion pins that Claude's
// question imports as Chord's own question: the recorded text, header, flag and
// choices carry over, and an option is named by the label the user was shown
// rather than by an ID the importer invented.
func TestConvertClaudeTranscript_MapsAskUserQuestionToQuestion(t *testing.T) {
	data := []byte(`{"uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"pick"}]}}
{"uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"AskUserQuestion","input":{"questions":[{"question":"Which branch?","header":"Branch","multiSelect":true,"options":[{"label":"main","description":"trunk"},{"label":"develop","description":"integration"}]}]}}]}}
{"uuid":"u2","parentUuid":"a1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"User answered: main"}]}}
`)
	var report ImportReport
	msgs, err := convertClaudeTranscript(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertClaudeTranscript: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs=%+v, want user + question card + result", msgs)
	}
	if len(msgs[1].ToolCalls) != 1 {
		t.Fatalf("tool calls=%+v, want the question", msgs[1].ToolCalls)
	}
	call := msgs[1].ToolCalls[0]
	if call.Name != tools.NameQuestion {
		t.Fatalf("tool name=%q, want %q", call.Name, tools.NameQuestion)
	}
	args, err := tools.DecodeQuestionArgs(call.Args)
	if err != nil {
		t.Fatalf("imported question args are not a valid question call: %v (%s)", err, call.Args)
	}
	if len(args.Questions) != 1 {
		t.Fatalf("questions=%+v, want one", args.Questions)
	}
	want := tools.QuestionItem{
		Question: "Which branch?",
		Header:   "Branch",
		Multiple: true,
		Options: []tools.QuestionOption{
			{ID: "main", Label: "main", Description: "trunk"},
			{ID: "develop", Label: "develop", Description: "integration"},
		},
	}
	if !reflect.DeepEqual(args.Questions[0], want) {
		t.Fatalf("question=%+v, want %+v", args.Questions[0], want)
	}
	if report.UnsupportedToolCalls != 0 || report.StructuredToolCalls != 1 {
		t.Fatalf("report=%+v", report)
	}
}

// TestConvertClaudeTranscript_KeepsAskUserQuestionWhenLabelsCannotIdentifyOptions
// pins the fallback of that mapping: choices Chord cannot tell apart under their
// labels have no faithful reading, so the call stays the source call with the
// source arguments instead of being given IDs the source never recorded.
func TestConvertClaudeTranscript_KeepsAskUserQuestionWhenLabelsCannotIdentifyOptions(t *testing.T) {
	input := `{"questions":[{"question":"Which one?","header":"Pick","options":[{"label":"same"},{"label":"same"}]}]}`
	data := []byte(`{"uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"pick"}]}}
{"uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"AskUserQuestion","input":` + input + `}]}}
{"uuid":"u2","parentUuid":"a1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"User answered: same"}]}}
`)
	var report ImportReport
	msgs, err := convertClaudeTranscript(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertClaudeTranscript: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs=%+v, want user + tool card + result", msgs)
	}
	assertClaudeUnsupportedToolCall(t, msgs[1], "AskUserQuestion", input)
	if report.UnsupportedToolCalls != 1 || report.StructuredToolCalls != 0 {
		t.Fatalf("report=%+v", report)
	}
}

// TestConvertClaudeTranscript_UnsupportedToolKeepsSourceCall pins that a Claude
// tool with no Chord mapping stays a tool call under its own name with its own
// arguments: the action ran in the source session, so it is history the model
// should read as-is rather than a rewritten or dropped record.
func TestConvertClaudeTranscript_UnsupportedToolKeepsSourceCall(t *testing.T) {
	data := []byte(`{"uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"search"}]}}
{"uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_unknown","name":"WebSearch","input":{"query":"golang"}}]}}
{"uuid":"u2","parentUuid":"a1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_unknown","content":"12 hits"}]}}
`)
	var report ImportReport
	msgs, err := convertClaudeTranscript(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertClaudeTranscript: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs=%+v, want user + tool card + result", msgs)
	}
	assertClaudeUnsupportedToolCall(t, msgs[1], "WebSearch", `{"query":"golang"}`)
	if msgs[2].Role != message.RoleTool || msgs[2].ToolCallID != "toolu_unknown" || msgs[2].Content != "12 hits" {
		t.Fatalf("paired tool result lost: %+v", msgs[2])
	}
	if report.UnsupportedToolCalls != 1 || report.StructuredToolCalls != 0 {
		t.Fatalf("report=%+v", report)
	}
}

// TestConvertClaudeTranscript_UnsupportedToolWithoutInputKeepsCall makes sure a
// source call that carried no arguments is still replayed as a call rather than
// dropped.
func TestConvertClaudeTranscript_UnsupportedToolWithoutInputKeepsCall(t *testing.T) {
	data := []byte(`{"uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"plan"}]}}
{"uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_todo","name":"TodoWrite"}]}}
`)
	var report ImportReport
	msgs, err := convertClaudeTranscript(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertClaudeTranscript: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("msgs=%+v, want user + tool card", msgs)
	}
	assertClaudeUnsupportedToolCall(t, msgs[1], "TodoWrite", "{}")
	if report.UnsupportedToolCalls != 1 || report.StructuredToolCalls != 0 {
		t.Fatalf("report=%+v", report)
	}
}

func TestConvertClaudeTranscript_FileMutationToolsConvertWhenPossible(t *testing.T) {
	data := []byte(`{"uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"modify files"}]}}
{"uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_edit","name":"edit","input":{"file_path":"a.txt","old_string":"old","new_string":"new"}},{"type":"tool_use","id":"toolu_multi","name":"MultiEdit","input":{"file_path":"b.txt","edits":[{"old_string":"x","new_string":"y"}]}},{"type":"tool_use","id":"toolu_write","name":"Write","input":{"file_path":"c.txt","content":"hello"}},{"type":"tool_use","id":"toolu_update","name":"Update","input":{"file_path":"d.txt","patch":"*** Begin Patch\n*** Update File:d.txt\n@@\n-old\n+new\n*** End Patch"}}]}}
{"uuid":"u2","parentUuid":"a1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_edit","content":"ok"},{"type":"tool_result","tool_use_id":"toolu_multi","content":"ok"},{"type":"tool_result","tool_use_id":"toolu_write","content":"ok"},{"type":"tool_result","tool_use_id":"toolu_update","content":"ok"}]}}
`)
	var report ImportReport
	msgs, err := convertClaudeTranscript(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertClaudeTranscript: %v", err)
	}
	if len(msgs) != 6 {
		t.Fatalf("msgs len=%d, want 6: %+v", len(msgs), msgs)
	}
	if len(msgs[1].ToolCalls) != 4 {
		t.Fatalf("tool calls=%+v, want 4", msgs[1].ToolCalls)
	}
	wantNames := []string{"edit", "edit", "write", "apply_patch"}
	for i, call := range msgs[1].ToolCalls {
		if call.Name != wantNames[i] {
			t.Fatalf("tool call %d name=%q, want %q", i, call.Name, wantNames[i])
		}
	}
	var editArgs map[string]any
	if err := json.Unmarshal(msgs[1].ToolCalls[0].Args, &editArgs); err != nil {
		t.Fatalf("unmarshal edit args: %v", err)
	}
	// A before/after pair imports as the replacement it was: the same values
	// under Chord's names, not a rewritten patch body.
	if editArgs["path"] != "a.txt" || editArgs["old_string"] != "old" || editArgs["new_string"] != "new" {
		t.Fatalf("edit args=%#v", editArgs)
	}
	var multiArgs struct {
		Path  string `json:"path"`
		Edits []struct {
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		} `json:"edits"`
	}
	if err := json.Unmarshal(msgs[1].ToolCalls[1].Args, &multiArgs); err != nil {
		t.Fatalf("unmarshal MultiEdit args: %v", err)
	}
	if multiArgs.Path != "b.txt" || len(multiArgs.Edits) != 1 || multiArgs.Edits[0].OldString != "x" || multiArgs.Edits[0].NewString != "y" {
		t.Fatalf("MultiEdit args=%#v", multiArgs)
	}
	var writeArgs map[string]any
	if err := json.Unmarshal(msgs[1].ToolCalls[2].Args, &writeArgs); err != nil {
		t.Fatalf("unmarshal write args: %v", err)
	}
	if writeArgs["path"] != "c.txt" || writeArgs["content"] != "hello" {
		t.Fatalf("write args=%#v", writeArgs)
	}
	var updateArgs map[string]any
	if err := json.Unmarshal(msgs[1].ToolCalls[3].Args, &updateArgs); err != nil {
		t.Fatalf("unmarshal update args: %v", err)
	}
	// An Update that really spoke in patches becomes apply_patch and keeps the
	// envelope it was issued with, rather than a replacement tool taking patch
	// arguments.
	updatePatch, _ := updateArgs["patch"].(string)
	if _, exists := updateArgs["path"]; exists || !strings.Contains(updatePatch, "*** Update File:d.txt") || !strings.Contains(updatePatch, "-old") || !strings.Contains(updatePatch, "+new") {
		t.Fatalf("update args=%#v", updateArgs)
	}
	if report.StructuredToolCalls != 4 || report.UnsupportedToolCalls != 0 || report.Claude == nil || report.Claude.StructuredToolCalls != 4 {
		t.Fatalf("report=%+v", report)
	}
}

func TestConvertClaudeTranscript_MultiEditEmptyReplacementConvertsToDeletionBatch(t *testing.T) {
	data := []byte(`{"uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"delete text"}]}}
{"uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_multi","name":"MultiEdit","input":{"file_path":"b.txt","edits":[{"old_string":"remove me\n","new_string":""}]}}]}}
`)
	var report ImportReport
	msgs, err := convertClaudeTranscript(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertClaudeTranscript: %v", err)
	}
	if len(msgs) != 2 || len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].Name != "edit" {
		t.Fatalf("msgs=%+v, want edit tool card", msgs)
	}
	var args struct {
		Path  string `json:"path"`
		Edits []struct {
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		} `json:"edits"`
	}
	if err := json.Unmarshal(msgs[1].ToolCalls[0].Args, &args); err != nil {
		t.Fatalf("unmarshal args: %v", err)
	}
	// The batch keeps the empty replacement: dropping it would turn an edit
	// that deleted text into one that changed nothing.
	if args.Path != "b.txt" || len(args.Edits) != 1 || !strings.Contains(args.Edits[0].OldString, "remove me") || args.Edits[0].NewString != "" {
		t.Fatalf("args=%#v", args)
	}
	if report.StructuredToolCalls != 1 || report.UnsupportedToolCalls != 0 {
		t.Fatalf("report=%+v", report)
	}
}

func TestConvertClaudeTranscript_AutoKeepsUnsignedToolTurnAsToolCard(t *testing.T) {
	data := []byte(`{"uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}
{"uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"thinking","thinking":"reason"},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"pwd","description":"print working dir"}}]}}
`)
	var report ImportReport
	msgs, err := convertClaudeTranscript(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertClaudeTranscript: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("msgs len=%d, want 2", len(msgs))
	}
	if len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].Name != "shell" {
		t.Fatalf("expected Bash mapped to shell tool card, got %+v", msgs[1].ToolCalls)
	}
	if msgs[1].Content != "" {
		t.Fatalf("did not expect imported tool marker content: %q", msgs[1].Content)
	}
	if report.ReasoningBlocksSkipped == 0 {
		t.Fatalf("ReasoningBlocksSkipped=%d, want >0", report.ReasoningBlocksSkipped)
	}
	if report.Claude == nil || report.Claude.StructuredToolCalls != 1 || report.Claude.DowngradedVisibleEntries != 0 {
		t.Fatalf("claude report=%+v", report.Claude)
	}
}

func TestConvertClaudeTranscript_SelectsLongestCompleteNonSidechainChain(t *testing.T) {
	data := []byte(`{"uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"start"}]}}
{"uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"text","text":"full-1"}]}}
{"uuid":"u2","parentUuid":"a1","message":{"role":"user","content":[{"type":"text","text":"full-2"}]}}
{"uuid":"short","message":{"role":"assistant","content":[{"type":"text","text":"short-tail"}]}}
`)
	var report ImportReport
	msgs, err := convertClaudeTranscript(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertClaudeTranscript: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3", len(msgs))
	}
	if msgs[2].Content != "full-2" {
		t.Fatalf("final content=%q, want full-2", msgs[2].Content)
	}
	if report.Claude == nil || report.Claude.TerminalCandidates != 2 || report.Claude.SelectedSpanLength != 3 {
		t.Fatalf("claude report=%+v", report.Claude)
	}
	if len(report.Warnings) == 0 {
		t.Fatalf("expected warning for multiple leaves")
	}
}

func TestConvertClaudeTranscript_CompactBoundaryPreservedSegmentBridge(t *testing.T) {
	data := []byte(`{"uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"before compact"}]}}
{"uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"text","text":"kept"}]}}
{"uuid":"cb1","type":"system","subtype":"compact_boundary","parentUuid":"a1","logicalParentUuid":"a1","content":{"preservedSegment":{"tailUuid":"a1"}}}
{"uuid":"u2","parentUuid":"cb1","message":{"role":"user","content":[{"type":"text","text":"after compact"}]}}
`)
	var report ImportReport
	msgs, err := convertClaudeTranscript(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertClaudeTranscript: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3", len(msgs))
	}
	if msgs[0].Content != "before compact" || msgs[1].Content != "kept" || msgs[2].Content != "after compact" {
		t.Fatalf("unexpected messages: %+v", msgs)
	}
	if report.Claude == nil || report.Claude.CompactBoundaries != 1 {
		t.Fatalf("claude report=%+v", report.Claude)
	}
}

func TestConvertClaudeTranscript_SelectsAssistantEndedChainOnTie(t *testing.T) {
	data := []byte(`{"uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"start"}]}}
{"uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"text","text":"main-1"}]}}
{"uuid":"u2","parentUuid":"a1","message":{"role":"user","content":[{"type":"text","text":"main-2"}]}}
{"uuid":"u3","message":{"role":"user","content":[{"type":"text","text":"alt-1"}]}}
{"uuid":"a2","parentUuid":"u3","message":{"role":"assistant","content":[{"type":"text","text":"alt-2"}]}}
`)
	var report ImportReport
	msgs, err := convertClaudeTranscript(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertClaudeTranscript: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3", len(msgs))
	}
	if msgs[2].Content != "main-2" {
		t.Fatalf("final content=%q, want main-2", msgs[2].Content)
	}
}

func TestConvertClaudeTranscript_ToolLineageCountsAreAccurate(t *testing.T) {
	data := []byte(`{"uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}
{"uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Shell","input":{"command":"pwd"}}]}}
`)
	var report ImportReport
	msgs, err := convertClaudeTranscript(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertClaudeTranscript: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("msgs len=%d, want 2", len(msgs))
	}
	if report.Claude == nil {
		t.Fatalf("missing claude report")
	}
	if report.Claude.StructuredToolCalls != 1 {
		t.Fatalf("StructuredToolCalls=%d, want 1", report.Claude.StructuredToolCalls)
	}
	if report.Claude.StructuredToolResults != 0 {
		t.Fatalf("StructuredToolResults=%d, want 0", report.Claude.StructuredToolResults)
	}
	if report.Claude.DowngradedVisibleEntries != 0 {
		t.Fatalf("DowngradedVisibleEntries=%d, want 0", report.Claude.DowngradedVisibleEntries)
	}
}

func TestConvertClaudeTranscript_LogicalParentCountsAsLeafBridge(t *testing.T) {
	data := []byte(`{"uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"start"}]}}
{"uuid":"m1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"text","text":"middle"}]}}
{"uuid":"m2","logicalParentUuid":"m1","message":{"role":"user","content":[{"type":"text","text":"after-relink"}]}}
`)
	var report ImportReport
	msgs, err := convertClaudeTranscript(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertClaudeTranscript: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3", len(msgs))
	}
	if msgs[2].Content != "after-relink" {
		t.Fatalf("final content=%q, want after-relink", msgs[2].Content)
	}
}

func TestConvertClaudeTranscript_SkipsSidechainsFromMainImport(t *testing.T) {
	data := []byte(`{"uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"main"}]}}
{"uuid":"s1","isSidechain":true,"agentId":"simplify","message":{"role":"assistant","content":[{"type":"text","text":"side"}]}}
{"uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"text","text":"main-reply"}]}}
`)
	var report ImportReport
	msgs, err := convertClaudeTranscript(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertClaudeTranscript: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("msgs len=%d, want 2", len(msgs))
	}
	if report.Claude == nil || report.Claude.SidechainMessagesSkipped != 1 || report.Claude.NonSidechainMessages != 2 {
		t.Fatalf("claude report=%+v", report.Claude)
	}
	if len(report.Claude.SidechainAgentIDs) != 1 || report.Claude.SidechainAgentIDs[0] != "simplify" {
		t.Fatalf("sidechain agent ids=%v", report.Claude.SidechainAgentIDs)
	}
	if len(report.Claude.Diagnostics) == 0 {
		t.Fatalf("expected claude diagnostics")
	}
}

func TestConvertClaudeTranscript_UnsupportedVisibleRoleFallsBackToReadableMessage(t *testing.T) {
	data := []byte(`{"uuid":"sys1","type":"system","subtype":"tool_use_summary","message":{"role":"system","content":[{"type":"text","text":"tool summary"}]}}`)
	var report ImportReport
	msgs, err := convertClaudeTranscript(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertClaudeTranscript: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("msgs len=%d, want 1", len(msgs))
	}
	if msgs[0].Role != "assistant" || !strings.Contains(msgs[0].Content, "[system entry]") || !strings.Contains(msgs[0].Content, "Subtype: tool_use_summary") {
		t.Fatalf("unexpected fallback msg=%+v", msgs[0])
	}
	if strings.Contains(msgs[0].Content, "Reason:") {
		t.Fatalf("fallback keeps import bookkeeping: %+v", msgs[0])
	}
}

func TestConvertClaudeTranscript_CompactBoundaryWithoutPreservedTailCutsOffHistory(t *testing.T) {
	data := []byte(`{"uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"before compact"}]}}
{"uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"text","text":"old history"}]}}
{"uuid":"cb1","type":"system","subtype":"compact_boundary","parentUuid":"a1","logicalParentUuid":"a1","content":{"preservedSegment":{}}}
{"uuid":"u2","parentUuid":"cb1","message":{"role":"user","content":[{"type":"text","text":"after compact"}]}}
`)
	var report ImportReport
	msgs, err := convertClaudeTranscript(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertClaudeTranscript: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("msgs len=%d, want 1: %+v", len(msgs), msgs)
	}
	if msgs[0].Content != "after compact" {
		t.Fatalf("content=%q, want after compact", msgs[0].Content)
	}
}

func TestConvertClaudeTranscript_TombstoneExcludesTargetMessage(t *testing.T) {
	data := []byte(`{"uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"keep"}]}}
{"uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"text","text":"delete me"}]}}
{"uuid":"ts1","type":"tombstone","content":{"targetUuid":"a1"}}
{"uuid":"u2","logicalParentUuid":"u1","message":{"role":"user","content":[{"type":"text","text":"after delete"}]}}
`)
	var report ImportReport
	msgs, err := convertClaudeTranscript(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertClaudeTranscript: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("msgs len=%d, want 2: %+v", len(msgs), msgs)
	}
	if msgs[0].Content != "keep" || msgs[1].Content != "after delete" {
		t.Fatalf("unexpected messages: %+v", msgs)
	}
	if report.Claude == nil || report.Claude.Tombstones != 1 {
		t.Fatalf("claude report=%+v", report.Claude)
	}
}

func TestConvertClaudeTranscript_AllowsPendingToolUseAtLeafWhenSelectingChain(t *testing.T) {
	data := []byte(`{"uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"main"}]}}
{"uuid":"a0","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"text","text":"ack"}]}}
{"uuid":"u2","parentUuid":"a0","message":{"role":"user","content":[{"type":"text","text":"run tool"}]}}
{"uuid":"a1","parentUuid":"u2","message":{"role":"assistant","content":[{"type":"tool_use","id":"pending","name":"Shell","input":{"command":"pwd"}}]}}
{"uuid":"x1","message":{"role":"user","content":[{"type":"text","text":"short"}]}}
{"uuid":"x2","parentUuid":"x1","message":{"role":"assistant","content":[{"type":"text","text":"complete"}]}}
`)
	var report ImportReport
	msgs, err := convertClaudeTranscript(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertClaudeTranscript: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("msgs len=%d, want 4: %+v", len(msgs), msgs)
	}
	if msgs[0].Content != "main" || len(msgs[3].ToolCalls) != 1 {
		t.Fatalf("selected wrong chain: %+v", msgs)
	}
	if report.Claude == nil || !strings.Contains(report.Claude.SelectionReason, "tier1=complete") {
		t.Fatalf("selection reason=%+v", report.Claude)
	}
}

func TestConvertClaudeTranscript_MixedUnsupportedVisibleEntryFallsBackInChain(t *testing.T) {
	data := []byte(`{"uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"start"}]}}
{"uuid":"sys1","parentUuid":"u1","type":"system","subtype":"tool_use_summary","message":{"role":"system","content":[{"type":"text","text":"summary"}]}}
{"uuid":"a1","parentUuid":"sys1","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}
`)
	var report ImportReport
	msgs, err := convertClaudeTranscript(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertClaudeTranscript: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3: %+v", len(msgs), msgs)
	}
	if msgs[0].Content != "start" || !strings.Contains(msgs[1].Content, "[system entry]") || msgs[2].Content != "done" {
		t.Fatalf("unexpected messages: %+v", msgs)
	}
	if report.Claude == nil || report.Claude.DowngradedVisibleEntries != 1 {
		t.Fatalf("claude report=%+v", report.Claude)
	}
}

func TestConvertClaudeTranscript_PrefersCompleteToolLineageOverLongerBrokenChain(t *testing.T) {
	data := []byte(`{"uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"good-start"}]}}
{"uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Shell","input":{"command":"pwd"}}]}}
{"uuid":"u2","parentUuid":"a1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"/tmp"}]}}
{"uuid":"x1","message":{"role":"user","content":[{"type":"text","text":"broken-start"}]}}
{"uuid":"x2","parentUuid":"x1","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_2","name":"Shell","input":{"command":"ls"}}]}}
{"uuid":"x3","parentUuid":"x2","message":{"role":"user","content":[{"type":"text","text":"still longer"}]}}
`)
	var report ImportReport
	msgs, err := convertClaudeTranscript(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertClaudeTranscript: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3", len(msgs))
	}
	if msgs[0].Content != "good-start" {
		t.Fatalf("selected wrong chain: %+v", msgs)
	}
	if report.Claude == nil || !strings.Contains(report.Claude.SelectionReason, "tier1=complete") {
		t.Fatalf("selection reason=%+v", report.Claude)
	}
}
