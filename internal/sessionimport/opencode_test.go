package sessionimport

import (
	"encoding/json"
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
