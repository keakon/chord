package sessionimport

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
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

func TestConvertOpenCodeExport_ParsesCurrentExportPartsAndToolFallback(t *testing.T) {
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
	if len(msgs) != 2 {
		t.Fatalf("msgs len=%d, want 2", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "please inspect" {
		t.Fatalf("msg0=%+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || !strings.Contains(msgs[1].Content, "I'll check.") || len(msgs[1].ToolCalls) != 0 {
		t.Fatalf("assistant tool fallback should be text-only, got %+v", msgs[1])
	}
	firstToolIdx := strings.Index(msgs[1].Content, "[Imported unsupported tool: unknown-tool]")
	middleTextIdx := strings.Index(msgs[1].Content, "Then I checked another file.")
	secondToolIdx := strings.Index(msgs[1].Content, "[Imported unsupported tool: second-tool]")
	if firstToolIdx < 0 || middleTextIdx < 0 || secondToolIdx < 0 || !(firstToolIdx < middleTextIdx && middleTextIdx < secondToolIdx) {
		t.Fatalf("assistant tool fallback order not preserved: %q", msgs[1].Content)
	}
	for _, want := range []string{"[Imported unsupported tool: unknown-tool]", "call-1", "README.md", "done", "[Imported unsupported tool: second-tool]", "call-2", "main.go", "ok"} {
		if !strings.Contains(msgs[1].Content, want) {
			t.Fatalf("assistant tool fallback content = %q, want fragment %q", msgs[1].Content, want)
		}
	}
	if strings.Contains(msgs[1].Content, "private chain of thought") {
		t.Fatalf("strict reasoning should be skipped, got %q", msgs[1].Content)
	}
	if report.ToolEntriesRendered != 2 {
		t.Fatalf("ToolEntriesRendered=%d, want 2", report.ToolEntriesRendered)
	}
	if report.UnsupportedToolCalls != 2 {
		t.Fatalf("UnsupportedToolCalls=%d, want 2", report.UnsupportedToolCalls)
	}
	if report.ReasoningBlocksSkipped != 1 {
		t.Fatalf("ReasoningBlocksSkipped=%d, want 1", report.ReasoningBlocksSkipped)
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
	if len(msgs) != 5 {
		t.Fatalf("msgs len=%d, want 5", len(msgs))
	}
	if msgs[0].Role != message.RoleAssistant || len(msgs[0].ToolCalls) != 1 || msgs[0].ToolCalls[0].Name != "read" || msgs[0].ToolCalls[0].ID != "call-read" {
		t.Fatalf("read tool call not structured: %+v", msgs[0])
	}
	if msgs[1].Role != message.RoleTool || msgs[1].ToolCallID != "call-read" || msgs[1].Content != "contents" || msgs[1].ToolStatus != message.ToolStatusSuccess {
		t.Fatalf("read tool result not structured: %+v", msgs[1])
	}
	if msgs[2].Role != message.RoleAssistant || len(msgs[2].ToolCalls) != 1 || msgs[2].ToolCalls[0].Name != "edit" || msgs[2].ToolCalls[0].ID != "call-patch" {
		t.Fatalf("edit tool call not structured: %+v", msgs[2])
	}
	if msgs[3].Role != message.RoleTool || msgs[3].ToolCallID != "call-patch" || msgs[3].Content != "ok" || msgs[3].ToolStatus != message.ToolStatusSuccess {
		t.Fatalf("edit tool result not structured: %+v", msgs[3])
	}
	if !strings.Contains(msgs[4].Content, "[Imported unsupported tool: unknown-tool]") {
		t.Fatalf("unknown tool should remain fallback text: %+v", msgs[4])
	}
	if report.ToolEntriesRendered != 3 {
		t.Fatalf("ToolEntriesRendered=%d, want 3", report.ToolEntriesRendered)
	}
	if report.StructuredToolCalls != 2 || report.StructuredToolResults != 2 {
		t.Fatalf("structured tool counts = %d/%d, want 2/2", report.StructuredToolCalls, report.StructuredToolResults)
	}
	if report.UnsupportedToolCalls != 1 {
		t.Fatalf("UnsupportedToolCalls=%d, want 1", report.UnsupportedToolCalls)
	}
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
