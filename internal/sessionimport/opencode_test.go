package sessionimport

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/imageutil"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestConvertOpenCodeExport_ParsesBasicMessages(t *testing.T) {
	data := []byte(`{
  "info": {"id": "sess-1"},
  "messages": [
    {"role": "user", "content": "hi"},
    {"role": "assistant", "content": "hello"}
  ]
}`)
	var report ImportReport
	msgs, err := convertOpenCodeExport(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("msgs len=%d, want 2", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "hi" {
		t.Fatalf("msg0=%+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "hello" {
		t.Fatalf("msg1=%+v", msgs[1])
	}
	if report.SourceSessionID != "sess-1" {
		t.Fatalf("SourceSessionID=%q, want sess-1", report.SourceSessionID)
	}
}

func TestConvertOpenCodeExport_WarnsForUnknownRole(t *testing.T) {
	data := []byte(`{
  "messages": [
    {"role": "developer", "content": "internal guidance"}
  ]
}`)
	var report ImportReport
	msgs, err := convertOpenCodeExport(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("msgs len=%d, want 1", len(msgs))
	}
	if msgs[0].Role != message.RoleAssistant {
		t.Fatalf("role=%q, want assistant", msgs[0].Role)
	}
	wantWarning := `unknown role "developer"; imported as assistant text`
	found := false
	for _, warning := range report.Warnings {
		if strings.Contains(warning, wantWarning) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("warnings=%q, want %q", report.Warnings, wantWarning)
	}
}

func TestConvertOpenCodeExport_ToolFallbackWarnsOnlyActualCause(t *testing.T) {
	data := []byte(`{
  "messages": [
    {"role": "assistant", "parts": [
      {"type": "tool", "tool": "bash", "state": {"input": {"command": "ls"}, "status": "completed", "output": "ok"}}
    ]}
  ]
}`)
	var report ImportReport
	if _, err := convertOpenCodeExport(data, ReasoningStrict, &report); err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	var toolWarns []string
	for _, warning := range report.Warnings {
		if strings.Contains(warning, "imported as text") {
			toolWarns = append(toolWarns, warning)
		}
	}
	if len(toolWarns) != 1 || !strings.Contains(toolWarns[0], "missing call id") {
		t.Fatalf("warnings=%q, want exactly one missing-call-id warning", report.Warnings)
	}
}

func TestConvertOpenCodeExport_KeepsUnmappedToolCallsVerbatim(t *testing.T) {
	data := []byte(`{
  "info": {"id": "sess-2"},
  "messages": [
    {
      "info": {"id": "msg-user", "role": "user"},
      "parts": [{"id":"p1","type":"text","text":"please inspect"}]
    },
    {
      "info": {"id": "msg-assistant", "role": "assistant"},
      "parts": [
        {"id":"p2","type":"text","text":"I'll check."},
        {"id":"p3","type":"tool","callID":"call-1","tool":"unknown-tool","state":{"status":"completed","input":{"path":"README.md"},"output":"done","title":"unknown","metadata":{},"time":{"start":1,"end":2}}},
        {"id":"p4","type":"text","text":"Then I checked another file."},
        {"id":"p5","type":"tool-invocation","callID":"call-2","tool":"second-tool","state":{"status":"completed","input":{"path":"main.go"},"output":"ok"}},
        {"id":"p6","type":"reasoning","text":"private chain of thought","time":{"start":1}}
      ]
    }
  ]
}`)
	var report ImportReport
	msgs, err := convertOpenCodeExport(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	// user + text + call + result + text + call + result
	if len(msgs) != 7 {
		t.Fatalf("msgs len=%d, want 7: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != "user" || msgs[0].Content != "please inspect" {
		t.Fatalf("msg0=%+v", msgs[0])
	}
	if msgs[1].Role != message.RoleAssistant || msgs[1].Content != "I'll check." {
		t.Fatalf("leading text not preserved: %+v", msgs[1])
	}
	assertOpenCodeToolCall(t, msgs[2], "call-1", "unknown-tool", `{"path":"README.md"}`)
	assertOpenCodeToolResult(t, msgs[3], "call-1", "done", message.ToolStatusSuccess)
	if msgs[4].Role != message.RoleAssistant || msgs[4].Content != "Then I checked another file." {
		t.Fatalf("text between tool calls not preserved: %+v", msgs[4])
	}
	assertOpenCodeToolCall(t, msgs[5], "call-2", "second-tool", `{"path":"main.go"}`)
	assertOpenCodeToolResult(t, msgs[6], "call-2", "ok", message.ToolStatusSuccess)
	if strings.Contains(msgs[1].Content, "private chain of thought") {
		t.Fatalf("strict reasoning should be skipped, got %q", msgs[1].Content)
	}
	if report.ToolEntriesRendered != 2 {
		t.Fatalf("ToolEntriesRendered=%d, want 2", report.ToolEntriesRendered)
	}
	if report.StructuredToolCalls != 0 || report.StructuredToolResults != 2 {
		t.Fatalf("structured tool counts = %d/%d, want 0/2", report.StructuredToolCalls, report.StructuredToolResults)
	}
	if report.UnsupportedToolCalls != 2 {
		t.Fatalf("UnsupportedToolCalls=%d, want 2", report.UnsupportedToolCalls)
	}
	if report.ReasoningBlocksSkipped != 1 {
		t.Fatalf("ReasoningBlocksSkipped=%d, want 1", report.ReasoningBlocksSkipped)
	}
	for _, warning := range report.Warnings {
		if strings.Contains(warning, "imported as text") {
			t.Fatalf("a kept tool call should not be reported as text: %q", warning)
		}
	}
}

// Current exports put the tool name in "name", the call id in "id", and the
// result in a state.content array, with a failed tool carrying state.error.
// Tools Chord does not map (webfetch, patch) still arrive as calls with their
// results.
func TestConvertOpenCodeExport_KeepsCurrentSchemaToolCalls(t *testing.T) {
	data := []byte(`{
  "messages": [
    {"info":{"role":"assistant"},"parts":[
      {"id":"p1","type":"tool","id":"call-shell","name":"shell","state":{"status":"completed","input":{"command":"ls -la"},"content":[{"type":"text","text":"file list"}]}},
      {"id":"p2","type":"tool","id":"call-fetch","name":"webfetch","state":{"status":"completed","input":{"url":"https://example.com","format":"markdown"},"content":[{"type":"text","text":"# Example"},{"type":"file","uri":"file:///tmp/a.png","name":"a.png"}]}},
      {"id":"p3","type":"tool","id":"call-patch","name":"patch","state":{"status":"error","input":{"patch":"*** Begin Patch\n*** End Patch"},"error":{"type":"ToolError","message":"context mismatch"}}}
    ]}
  ]
}`)
	var report ImportReport
	msgs, err := convertOpenCodeExport(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 6 {
		t.Fatalf("msgs len=%d, want 6: %+v", len(msgs), msgs)
	}
	// shell maps onto a Chord tool; webfetch and patch do not, so their calls
	// keep the source name and arguments. The mapped call carries only what the
	// source recorded — no synthesized description, so the card shows the
	// command's first line exactly like a live call.
	wantCalls := map[string]struct{ name, args string }{
		"call-shell": {tools.NameShell, `{"command":"ls -la"}`},
		"call-fetch": {"webfetch", `{"format":"markdown","url":"https://example.com"}`},
		"call-patch": {"patch", `{"patch":"*** Begin Patch\n*** End Patch"}`},
	}
	wantResults := map[string]struct{ content, status string }{
		"call-shell": {"file list", message.ToolStatusSuccess},
		"call-fetch": {"# Example\na.png (file:///tmp/a.png)", message.ToolStatusSuccess},
		"call-patch": {"context mismatch", message.ToolStatusError},
	}
	seenCalls := map[string]bool{}
	seenResults := map[string]bool{}
	for _, msg := range msgs {
		switch {
		case msg.Role == message.RoleAssistant && len(msg.ToolCalls) == 1:
			call := msg.ToolCalls[0]
			want, ok := wantCalls[call.ID]
			if !ok {
				t.Fatalf("unexpected tool call %s", call.ID)
			}
			if call.Name != want.name || !sameJSON(t, call.Args, want.args) {
				t.Fatalf("call %s = %s/%s, want %s/%s", call.ID, call.Name, call.Args, want.name, want.args)
			}
			seenCalls[call.ID] = true
		case msg.Role == message.RoleTool:
			want, ok := wantResults[msg.ToolCallID]
			if !ok {
				t.Fatalf("unexpected tool result for %s", msg.ToolCallID)
			}
			if msg.Content != want.content || msg.ToolStatus != want.status {
				t.Fatalf("result %s = %q/%q, want %q/%q", msg.ToolCallID, msg.Content, msg.ToolStatus, want.content, want.status)
			}
			seenResults[msg.ToolCallID] = true
		default:
			t.Fatalf("unexpected message: %+v", msg)
		}
	}
	if len(seenCalls) != 3 || len(seenResults) != 3 {
		t.Fatalf("saw %d calls and %d results, want 3/3", len(seenCalls), len(seenResults))
	}
	if report.ToolEntriesRendered != 3 {
		t.Fatalf("ToolEntriesRendered=%d, want 3", report.ToolEntriesRendered)
	}
	if report.StructuredToolCalls != 1 || report.StructuredToolResults != 3 {
		t.Fatalf("structured tool counts = %d/%d, want 1/3", report.StructuredToolCalls, report.StructuredToolResults)
	}
	if report.UnsupportedToolCalls != 2 {
		t.Fatalf("UnsupportedToolCalls=%d, want 2", report.UnsupportedToolCalls)
	}
}

// A mapped tool whose arguments do not fit the Chord shape keeps the source name
// and arguments: the mapping cannot be applied, and the call still ran.
func TestConvertOpenCodeExport_MappedToolWithForeignArgsKeepsSourceCall(t *testing.T) {
	data := []byte(`{
  "messages": [
    {"info":{"role":"assistant"},"parts":[
      {"id":"p1","type":"tool","id":"call-read","name":"read","state":{"status":"completed","input":{"filename":"README.md"},"content":[{"type":"text","text":"contents"}]}}
    ]}
  ]
}`)
	var report ImportReport
	msgs, err := convertOpenCodeExport(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("msgs len=%d, want 2: %+v", len(msgs), msgs)
	}
	assertOpenCodeToolCall(t, msgs[0], "call-read", "read", `{"filename":"README.md"}`)
	assertOpenCodeToolResult(t, msgs[1], "call-read", "contents", message.ToolStatusSuccess)
	if report.StructuredToolCalls != 0 || report.StructuredToolResults != 1 || report.UnsupportedToolCalls != 1 {
		t.Fatalf("counts = %d/%d/%d, want 0/1/1", report.StructuredToolCalls, report.StructuredToolResults, report.UnsupportedToolCalls)
	}
}

func TestConvertOpenCodeExport_ToolPartMissingNameStaysText(t *testing.T) {
	data := []byte(`{
  "messages": [
    {"info":{"role":"assistant"},"parts":[
      {"id":"p1","type":"tool","callID":"call-1","state":{"status":"completed","input":{"path":"README.md"},"output":"ok"}}
    ]}
  ]
}`)
	var report ImportReport
	msgs, err := convertOpenCodeExport(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("msgs len=%d, want 1: %+v", len(msgs), msgs)
	}
	if len(msgs[0].ToolCalls) != 0 || !strings.Contains(msgs[0].Content, "[Imported unsupported tool") {
		t.Fatalf("a part without a name cannot name a call: %+v", msgs[0])
	}
	if !hasWarning(report.Warnings, "tool part missing name; imported as text") {
		t.Fatalf("warnings=%q, want a missing-name warning", report.Warnings)
	}
	if report.ToolEntriesRendered != 1 || report.UnsupportedToolCalls != 1 {
		t.Fatalf("counts = %d/%d, want 1/1", report.ToolEntriesRendered, report.UnsupportedToolCalls)
	}
}

// A call the source never finished has no result to pair with it: it stays a
// call, and no result is invented for it.
func TestConvertOpenCodeExport_UnfinishedToolCallKeepsCallWithoutResult(t *testing.T) {
	data := []byte(`{
  "messages": [
    {"info":{"role":"assistant"},"parts":[
      {"id":"p1","type":"tool","id":"call-1","name":"webfetch","state":{"status":"streaming","input":"{\"url\":"}},
      {"id":"p2","type":"tool","id":"call-2","name":"read","state":{"status":"running","input":{"path":"a.md"},"metadata":{}}}
    ]}
  ]
}`)
	var report ImportReport
	msgs, err := convertOpenCodeExport(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("msgs len=%d, want 2: %+v", len(msgs), msgs)
	}
	assertOpenCodeToolCall(t, msgs[0], "call-1", "webfetch", `{"input":"{\"url\":"}`)
	assertOpenCodeToolCall(t, msgs[1], "call-2", "read", `{"path":"a.md"}`)
	if report.StructuredToolResults != 0 {
		t.Fatalf("StructuredToolResults=%d, want 0", report.StructuredToolResults)
	}
	if report.StructuredToolCalls != 1 || report.UnsupportedToolCalls != 1 {
		t.Fatalf("call counts = %d/%d, want 1/1", report.StructuredToolCalls, report.UnsupportedToolCalls)
	}
}

func TestConvertOpenCodeExport_ConvertsKnownToolParts(t *testing.T) {
	data := []byte(`{
  "messages": [
    {
      "info": {"role": "assistant"},
      "parts": [
        {"type":"tool","callID":"call-read","tool":"read_file","state":{"status":"completed","input":{"path":"README.md"},"output":"contents"}},
        {"type":"tool","callID":"call-patch","tool":"edit","state":{"status":"completed","input":{"patch":"*** Begin Patch\n*** Update File: a.txt\n@@\n-old\n+new\n*** End Patch"},"output":"ok"}},
        {"type":"tool","callID":"call-unknown","tool":"unknown-tool","state":{"status":"completed","input":{"path":"main.go"},"output":"ok"}}
      ]
    }
  ]
}`)
	var report ImportReport
	msgs, err := convertOpenCodeExport(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 6 {
		t.Fatalf("msgs len=%d, want 6: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != message.RoleAssistant || len(msgs[0].ToolCalls) != 1 || msgs[0].ToolCalls[0].Name != "read" || msgs[0].ToolCalls[0].ID != "call-read" {
		t.Fatalf("read tool call not structured: %+v", msgs[0])
	}
	if msgs[1].Role != message.RoleTool || msgs[1].ToolCallID != "call-read" || msgs[1].Content != "contents" || msgs[1].ToolStatus != message.ToolStatusSuccess {
		t.Fatalf("read tool result not structured: %+v", msgs[1])
	}
	if msgs[2].Role != message.RoleAssistant || len(msgs[2].ToolCalls) != 1 || msgs[2].ToolCalls[0].Name != tools.NameApplyPatch || msgs[2].ToolCalls[0].ID != "call-patch" {
		t.Fatalf("edit tool call not structured as the patch it was: %+v", msgs[2])
	}
	var patchArgs map[string]any
	if err := json.Unmarshal(msgs[2].ToolCalls[0].Args, &patchArgs); err != nil {
		t.Fatalf("unmarshal patch args: %v", err)
	}
	// This edit recorded a real envelope, so it imports as the patch tool and
	// keeps the envelope verbatim rather than a replacement taking it.
	envelope, _ := patchArgs["patch"].(string)
	if _, exists := patchArgs["path"]; exists || !strings.Contains(envelope, "*** Update File: a.txt") || !strings.Contains(envelope, "-old") || !strings.Contains(envelope, "+new") {
		t.Fatalf("patch args=%#v", patchArgs)
	}
	if msgs[3].Role != message.RoleTool || msgs[3].ToolCallID != "call-patch" || msgs[3].Content != "ok" || msgs[3].ToolStatus != message.ToolStatusSuccess {
		t.Fatalf("edit tool result not structured: %+v", msgs[3])
	}
	assertOpenCodeToolCall(t, msgs[4], "call-unknown", "unknown-tool", `{"path":"main.go"}`)
	assertOpenCodeToolResult(t, msgs[5], "call-unknown", "ok", message.ToolStatusSuccess)
	if report.ToolEntriesRendered != 3 {
		t.Fatalf("ToolEntriesRendered=%d, want 3", report.ToolEntriesRendered)
	}
	if report.StructuredToolCalls != 2 || report.StructuredToolResults != 3 {
		t.Fatalf("structured tool counts = %d/%d, want 2/3", report.StructuredToolCalls, report.StructuredToolResults)
	}
	if report.UnsupportedToolCalls != 1 {
		t.Fatalf("UnsupportedToolCalls=%d, want 1", report.UnsupportedToolCalls)
	}
}

// assertOpenCodeToolCall checks that a message is one assistant message with a
// single tool call, whose name and arguments are the source's — not a renamed
// call, not text, and not an importer metadata wrapper.
func assertOpenCodeToolCall(t *testing.T, msg message.Message, wantID, wantName, wantArgs string) {
	t.Helper()
	if msg.Role != message.RoleAssistant || len(msg.ToolCalls) != 1 {
		t.Fatalf("not a single tool call: %+v", msg)
	}
	call := msg.ToolCalls[0]
	if call.ID != wantID || call.Name != wantName {
		t.Fatalf("call = %s/%s, want %s/%s", call.ID, call.Name, wantID, wantName)
	}
	if !sameJSON(t, call.Args, wantArgs) {
		t.Fatalf("call %s args = %s, want %s", wantName, call.Args, wantArgs)
	}
}

func assertOpenCodeToolResult(t *testing.T, msg message.Message, wantCallID, wantContent, wantStatus string) {
	t.Helper()
	if msg.Role != message.RoleTool || msg.ToolCallID != wantCallID {
		t.Fatalf("not the result of %s: %+v", wantCallID, msg)
	}
	if msg.Content != wantContent || msg.ToolStatus != wantStatus {
		t.Fatalf("result of %s = %q/%q, want %q/%q", wantCallID, msg.Content, msg.ToolStatus, wantContent, wantStatus)
	}
}

func sameJSON(t *testing.T, got json.RawMessage, want string) bool {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("args are not JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("want is not JSON: %v", err)
	}
	gotNorm, _ := json.Marshal(gotValue)
	wantNorm, _ := json.Marshal(wantValue)
	return string(gotNorm) == string(wantNorm)
}

func hasWarning(warnings []string, want string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, want) {
			return true
		}
	}
	return false
}

func TestConvertOpenCodeExport_CountsSkippedReasoningOnlyParts(t *testing.T) {
	data := []byte(`{
  "messages": [
    {"info":{"role":"assistant"},"parts":[{"type":"reasoning","text":"private chain of thought"}]}
  ]
}`)
	var report ImportReport
	msgs, err := convertOpenCodeExport(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("msgs len=%d, want 0", len(msgs))
	}
	if report.SkippedEntries != 1 {
		t.Fatalf("SkippedEntries=%d, want 1", report.SkippedEntries)
	}
	if report.ReasoningBlocksSkipped != 1 {
		t.Fatalf("ReasoningBlocksSkipped=%d, want 1", report.ReasoningBlocksSkipped)
	}
}

func TestConvertOpenCodeExport_ToleratesArrayRoot(t *testing.T) {
	arr := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":"a"}`),
		json.RawMessage(`{"role":"assistant","content":"b"}`),
	}
	data, _ := json.Marshal(arr)
	var report ImportReport
	msgs, err := convertOpenCodeExport(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("msgs len=%d, want 2", len(msgs))
	}
}

func TestConvertOpenCodeExport_StructuresContentArrayToolBlocks(t *testing.T) {
	data := []byte(`{
  "messages": [
    {"role":"assistant","content":[
      {"type":"text","text":"checking"},
      {"type":"tool","callID":"call-c","tool":"shell","input":{"command":"pwd"},"output":"/tmp","status":"completed"}
    ]}
  ]
}`)
	var report ImportReport
	msgs, err := convertOpenCodeExport(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != message.RoleAssistant || msgs[0].Content != "checking" {
		t.Fatalf("leading text not preserved: %+v", msgs[0])
	}
	if msgs[1].Role != message.RoleAssistant || len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].Name != "shell" || msgs[1].ToolCalls[0].ID != "call-c" {
		t.Fatalf("content-array tool call not structured: %+v", msgs[1])
	}
	if msgs[2].Role != message.RoleTool || msgs[2].ToolCallID != "call-c" || msgs[2].Content != "/tmp" {
		t.Fatalf("content-array tool result not structured: %+v", msgs[2])
	}
	if report.StructuredToolCalls != 1 || report.StructuredToolResults != 1 {
		t.Fatalf("structured tool counts = %d/%d, want 1/1", report.StructuredToolCalls, report.StructuredToolResults)
	}
}

func TestConvertOpenCodeExport_InlineImageFilePartBecomesImagePart(t *testing.T) {
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC"
	data := []byte(`{
  "messages": [
    {"role": "user", "parts": [
      {"type": "text", "text": "what is this?"},
      {"type": "file", "mime": "image/png", "filename": "clipboard", "url": "data:image/png;base64,` + png + `", "source": {"type": "file", "path": "clipboard"}}
    ]}
  ]
}`)
	var report ImportReport
	msgs, err := convertOpenCodeExport(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("msgs len=%d, want 2: %+v", len(msgs), msgs)
	}
	if msgs[0].Content != "what is this?" {
		t.Fatalf("leading text not preserved: %+v", msgs[0])
	}
	img := msgs[1]
	if len(img.Parts) != 1 || img.Parts[0].Type != message.ContentPartImage {
		t.Fatalf("file part not imported as an image part: %+v", img)
	}
	if img.Parts[0].MimeType != "image/png" || img.Parts[0].FileName != "clipboard.png" {
		t.Fatalf("image part metadata=%+v", img.Parts[0])
	}
	if len(img.Parts[0].Data) == 0 {
		t.Fatalf("image part carries no bytes: %+v", img.Parts[0])
	}
	if strings.Contains(img.Content, png) || strings.Contains(fmt.Sprint(img.Parts), png) {
		t.Fatalf("base64 payload leaked into the message: %+v", img)
	}
	if hasWarning(report.Warnings, "unsupported part type file; imported as text") {
		t.Fatalf("file part still reported as unsupported: %q", report.Warnings)
	}
}

func TestConvertOpenCodeExport_FilePartWithoutInlinePayloadKeepsReference(t *testing.T) {
	data := []byte(`{
  "messages": [
    {"role": "user", "parts": [
      {"type": "file", "mime": "text/plain", "filename": "notes.txt", "url": "/tmp/notes.txt"}
    ]}
  ]
}`)
	var report ImportReport
	msgs, err := convertOpenCodeExport(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("msgs len=%d, want 1: %+v", len(msgs), msgs)
	}
	if len(msgs[0].Parts) != 0 {
		t.Fatalf("path-only file should not become an attachment: %+v", msgs[0])
	}
	for _, want := range []string{"[attachment]", "notes.txt", "/tmp/notes.txt"} {
		if !strings.Contains(msgs[0].Content, want) {
			t.Fatalf("content=%q, want it to contain %q", msgs[0].Content, want)
		}
	}
}

func TestConvertOpenCodeExport_PatchPartIsSkipped(t *testing.T) {
	data := []byte(`{
  "messages": [
    {"role": "assistant", "parts": [
      {"type": "patch", "hash": "82b6344", "files": ["/repo/a.go"]}
    ]}
  ]
}`)
	var report ImportReport
	msgs, err := convertOpenCodeExport(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("patch metadata should not become a message: %+v", msgs)
	}
	if hasWarning(report.Warnings, "unsupported part type patch; imported as text") {
		t.Fatalf("patch part still reported as unsupported: %q", report.Warnings)
	}
}

func TestConvertOpenCodeExport_CamelCaseEditArgsMapToChordEdit(t *testing.T) {
	data := []byte(`{
  "messages": [
    {"role": "assistant", "parts": [
      {"type": "tool", "tool": "edit", "callID": "call-1", "state": {
        "status": "completed",
        "input": {"filePath": "/repo/a.go", "oldString": "alpha", "newString": "beta"},
        "output": "updated"
      }}
    ]}
  ]
}`)
	var report ImportReport
	msgs, err := convertOpenCodeExport(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("msgs len=%d, want 2: %+v", len(msgs), msgs)
	}
	call := msgs[0].ToolCalls
	if len(call) != 1 || call[0].Name != tools.NameEdit {
		t.Fatalf("edit not mapped to the chord edit tool: %+v", msgs[0])
	}
	var args map[string]any
	if err := json.Unmarshal(call[0].Args, &args); err != nil {
		t.Fatalf("args not JSON: %v", err)
	}
	// The pair the agent issued imports under Chord's names, so the call stays
	// the replacement it was instead of being rewritten into another tool's
	// patch envelope.
	if args["path"] != "/repo/a.go" || args["old_string"] != "alpha" || args["new_string"] != "beta" {
		t.Fatalf("edit args=%#v", args)
	}
	if _, exists := args["patch"]; exists {
		t.Fatalf("replacement rewritten as a patch: %#v", args)
	}
	if report.StructuredToolCalls != 1 {
		t.Fatalf("StructuredToolCalls=%d, want 1", report.StructuredToolCalls)
	}
	if report.UnsupportedToolCalls != 0 {
		t.Fatalf("UnsupportedToolCalls=%d, want 0", report.UnsupportedToolCalls)
	}
}

// TestConvertOpenCodeExport_EditWithoutLocationStaysSourceCall covers the
// degrade path: a record that names neither the text it replaced nor a patch
// has no edit location to show, so importing it as a Chord edit would invent
// one. It keeps the name and arguments the source issued instead.
func TestConvertOpenCodeExport_EditWithoutLocationStaysSourceCall(t *testing.T) {
	data := []byte(`{
  "messages": [
    {"role": "assistant", "parts": [
      {"type": "tool", "tool": "edit", "callID": "call-1", "state": {
        "status": "completed",
        "input": {"filePath": "/repo/a.go"},
        "output": "ok"
      }}
    ]}
  ]
}`)
	var report ImportReport
	msgs, err := convertOpenCodeExport(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("msgs len=%d, want call + result: %+v", len(msgs), msgs)
	}
	assertOpenCodeToolCall(t, msgs[0], "call-1", "edit", `{"filePath":"/repo/a.go"}`)
	assertOpenCodeToolResult(t, msgs[1], "call-1", "ok", message.ToolStatusSuccess)
	if report.StructuredToolCalls != 0 || report.UnsupportedToolCalls != 1 {
		t.Fatalf("call counts = %d/%d, want 0/1", report.StructuredToolCalls, report.UnsupportedToolCalls)
	}
}

func TestConvertOpenCodeExport_CamelCaseReadArgsMapToChordRead(t *testing.T) {
	data := []byte(`{
  "messages": [
    {"role": "assistant", "parts": [
      {"type": "tool", "tool": "read", "callID": "call-2", "state": {
        "status": "completed",
        "input": {"filePath": "/repo/a.go", "offset": 10, "limit": 20},
        "output": "content"
      }}
    ]}
  ]
}`)
	var report ImportReport
	msgs, err := convertOpenCodeExport(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 2 || len(msgs[0].ToolCalls) != 1 || msgs[0].ToolCalls[0].Name != tools.NameRead {
		t.Fatalf("read not mapped to the chord read tool: %+v", msgs)
	}
	var args map[string]any
	if err := json.Unmarshal(msgs[0].ToolCalls[0].Args, &args); err != nil {
		t.Fatalf("args not JSON: %v", err)
	}
	if args["path"] != "/repo/a.go" {
		t.Fatalf("read args=%+v, want path=/repo/a.go", args)
	}
	if args["offset"] != float64(10) || args["limit"] != float64(20) {
		t.Fatalf("read range not preserved: %+v", args)
	}
}

func TestConvertOpenCodeExport_ShellTimeoutMillisecondsKeepTheirDeadline(t *testing.T) {
	data := []byte(`{
  "messages": [
    {"role": "assistant", "parts": [
      {"type": "tool", "tool": "bash", "callID": "call-3", "state": {
        "status": "completed",
        "input": {"command": "go test ./...", "timeout": 900000},
        "output": "ok"
      }}
    ]}
  ]
}`)
	var report ImportReport
	msgs, err := convertOpenCodeExport(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 2 || len(msgs[0].ToolCalls) != 1 {
		t.Fatalf("msgs=%+v", msgs)
	}
	var args map[string]any
	if err := json.Unmarshal(msgs[0].ToolCalls[0].Args, &args); err != nil {
		t.Fatalf("args not JSON: %v", err)
	}
	// 900000ms is 15 minutes; the chord tool takes timeout_ms.
	if args["timeout_ms"] != float64(900000) {
		t.Fatalf("timeout_ms=%v, want 900000", args["timeout_ms"])
	}
}

// An inline payload is bounded by the limits the clipboard reader enforces, so
// an attachment the preview path would refuse to read back is dropped with the
// reason named instead of being written into the transcript.
func TestOpenCodeOversizeInlineAttachmentIsNotCarried(t *testing.T) {
	oversize := base64.StdEncoding.EncodeToString(make([]byte, imageutil.MaxImageSourceBytes+1))
	part := map[string]any{
		"type":     "file",
		"mime":     "image/png",
		"filename": "clipboard",
		"url":      "data:image/png;base64," + oversize,
	}
	attach, marker := openCodeFilePartAttachment(part)
	if attach != nil {
		t.Fatalf("attach = %#v, want none for an oversize payload", attach)
	}
	if !strings.Contains(marker, "import limit") || !strings.Contains(marker, "clipboard") {
		t.Fatalf("marker = %q, want the size reason and the file name", marker)
	}
}
