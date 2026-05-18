package sessionimport

import (
	"encoding/json"
	"strings"
	"testing"
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
        {"id":"p4","type":"reasoning","text":"private chain of thought","time":{"start":1}}
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
	if msgs[1].Role != "assistant" || !strings.Contains(msgs[1].Content, "I'll check.") || !strings.Contains(msgs[1].Content, "[Imported unsupported tool: unknown-tool]") || !strings.Contains(msgs[1].Content, "call-1") {
		t.Fatalf("assistant tool fallback missing, got %+v", msgs[1])
	}
	if strings.Contains(msgs[1].Content, "private chain of thought") {
		t.Fatalf("strict reasoning should be skipped, got %q", msgs[1].Content)
	}
	if report.ToolEntriesRendered != 1 {
		t.Fatalf("ToolEntriesRendered=%d, want 1", report.ToolEntriesRendered)
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
