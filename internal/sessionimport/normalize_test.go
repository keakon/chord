package sessionimport

import (
	"testing"

	"github.com/keakon/chord/internal/message"
)

// The marker is the same one a live Chord call leaves behind, keyed by the
// paired call's tool name: an imported shell result with no output is
// indistinguishable from a live one, and neither side sees an import marker.
func TestNormalizeImportedMessages_FillsEmptyToolResults(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "run it"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call_1", Name: "shell"}}},
		{Role: message.RoleTool, ToolCallID: "call_1", Content: "", ToolStatus: message.ToolStatusSuccess},
		{Role: message.RoleTool, ToolCallID: "call_2", Content: "output", ToolStatus: message.ToolStatusSuccess},
		{Role: message.RoleTool, ToolCallID: "call_3", Content: "", Parts: []message.ContentPart{{Type: message.ContentPartImage, MimeType: "image/png", Data: []byte{1, 2, 3}}}},
		{Role: message.RoleAssistant, Content: "done"},
	}
	if filled := normalizeImportedMessages(msgs); filled != 1 {
		t.Fatalf("filled=%d, want 1", filled)
	}
	if want := "(shell completed with no output)"; msgs[2].Content != want {
		t.Fatalf("empty tool result=%q, want %q", msgs[2].Content, want)
	}
	if msgs[2].ToolCallID != "call_1" || msgs[2].ToolStatus != message.ToolStatusSuccess {
		t.Fatalf("empty tool result lost its call linkage: %+v", msgs[2])
	}
	if msgs[3].Content != "output" {
		t.Fatalf("non-empty tool result=%q, want it untouched", msgs[3].Content)
	}
	if msgs[4].Content != "" || len(msgs[4].Parts) != 1 {
		t.Fatalf("attachment-only tool result=%+v, want it untouched", msgs[4])
	}
}

// A failure or cancellation the source recorded must not read as a completed
// call: the stand-in is the same line the live path produces for that outcome
// (composeToolResultTexts' generic error fallback, and a cancelled call's
// "Cancelled").
func TestNormalizeImportedMessages_FillsEmptyResultsByOutcome(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call_err", Name: "shell"}}},
		{Role: message.RoleTool, ToolCallID: "call_err", Content: "", ToolStatus: message.ToolStatusError},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call_cancel", Name: "exec"}}},
		{Role: message.RoleTool, ToolCallID: "call_cancel", Content: "", ToolStatus: message.ToolStatusCancelled},
	}
	if filled := normalizeImportedMessages(msgs); filled != 2 {
		t.Fatalf("filled=%d, want 2", filled)
	}
	if want := "Error: tool execution failed"; msgs[1].Content != want {
		t.Fatalf("failed empty result=%q, want %q", msgs[1].Content, want)
	}
	if want := "Cancelled"; msgs[3].Content != want {
		t.Fatalf("cancelled empty result=%q, want %q", msgs[3].Content, want)
	}
}

func TestConvertCodexRollout_EmptyExecOutputIsReplayable(t *testing.T) {
	// A `write_stdin` poll that produced nothing: codex records the result
	// envelope plus an empty text part, so the stripped body is empty.
	input := "const r = await tools.write_stdin({session_id:1,chars:\"\",yield_time_ms:1000});\ntext(r.output);\n"
	data := codexExecRollout(t, input, "Script completed\nWall time 5.0 seconds\nOutput:\n", "")

	var report ImportReport
	msgs, err := convertCodexRollout(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	if filled := normalizeImportedMessages(msgs); filled != 1 {
		t.Fatalf("filled=%d, want 1: %+v", filled, msgs)
	}
	// write_stdin has no Chord equivalent, so the call keeps its source name
	// and the marker is keyed by it — the same string the model reads.
	if want := "(exec completed with no output)"; msgs[len(msgs)-1].Content != want {
		t.Fatalf("tool result=%q, want %q", msgs[len(msgs)-1].Content, want)
	}
	for _, msg := range msgs {
		if msg.Role == message.RoleTool && msg.Content == "" {
			t.Fatalf("tool result left empty: %+v", msg)
		}
	}
}

// A Claude tool_result carries null when the tool produced nothing, and
// rawContentString turns that into an empty result.
func TestConvertClaudeTranscript_EmptyToolResultIsReplayable(t *testing.T) {
	data := []byte(`{"uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}
{"uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"pwd"}}]}}
{"uuid":"u2","parentUuid":"a1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":null}]}}
`)
	var report ImportReport
	msgs, err := convertClaudeTranscript(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertClaudeTranscript: %v", err)
	}
	if msgs[len(msgs)-1].Content != "" {
		t.Fatalf("fixture no longer produces an empty result: %+v", msgs[len(msgs)-1])
	}
	if filled := normalizeImportedMessages(msgs); filled != 1 {
		t.Fatalf("filled=%d, want 1: %+v", filled, msgs)
	}
	// The source tool maps onto Chord's shell, so the marker reads like a live
	// shell call — identical text for the model and the user.
	if want := "(shell completed with no output)"; msgs[len(msgs)-1].Content != want {
		t.Fatalf("tool result=%q, want %q", msgs[len(msgs)-1].Content, want)
	}
}

// An OpenCode tool whose output field is present but empty is a finished call
// with no text, so openCodeToolOutputValue reports it as a result.
func TestConvertOpenCodeExport_EmptyToolOutputIsReplayable(t *testing.T) {
	data := []byte(`{
  "messages": [
    {"info":{"role":"assistant"},"parts":[
      {"id":"p1","type":"tool","id":"call-shell","name":"shell","state":{"status":"completed","input":{"command":"ls"},"output":""}}
    ]}
  ]
}`)
	var report ImportReport
	msgs, err := convertOpenCodeExport(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if msgs[len(msgs)-1].Content != "" {
		t.Fatalf("fixture no longer produces an empty result: %+v", msgs[len(msgs)-1])
	}
	if filled := normalizeImportedMessages(msgs); filled != 1 {
		t.Fatalf("filled=%d, want 1: %+v", filled, msgs)
	}
	// The source tool maps onto Chord's shell, so the marker reads like a live
	// shell call — identical text for the model and the user.
	if want := "(shell completed with no output)"; msgs[len(msgs)-1].Content != want {
		t.Fatalf("tool result=%q, want %q", msgs[len(msgs)-1].Content, want)
	}
}
