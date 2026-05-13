package llm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
)

func TestPersistLLMTraceWritesToolLifecycleSummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), LLMTraceFileName())
	writer := NewTraceWriter(path)
	collector := newLLMTraceCollector("responses", "gpt-5.5", nil)
	collector.Callback(message.StreamDelta{Type: "status", Status: &message.StatusDelta{Type: "connecting"}})
	collector.Callback(message.StreamDelta{Progress: &message.StreamProgressDelta{Bytes: 42, Events: 2}})
	collector.Callback(message.StreamDelta{Type: "tool_use_start", ToolCall: &message.ToolCallDelta{ID: "call_1", Name: "Edit"}})
	collector.Callback(message.StreamDelta{Type: "tool_use_delta", ToolCall: &message.ToolCallDelta{ID: "call_1", Name: "Edit", Input: `{"path":"a`}})
	collector.Callback(message.StreamDelta{Type: "tool_use_delta", ToolCall: &message.ToolCallDelta{ID: "call_1", Name: "Edit", Input: `{"path":"abc"}`}})
	collector.Callback(message.StreamDelta{Type: "text", Text: "done"})
	startedAt := time.Now().Add(-25 * time.Millisecond)
	persistLLMTrace(writer, collector, 200, "http", startedAt, &message.Response{
		Content:    "done",
		StopReason: "tool_calls",
		ToolCalls: []message.ToolCall{{
			ID:   "call_1",
			Name: "Edit",
			Args: json.RawMessage(`{"path":"abc"}`),
		}},
	}, nil)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("line count = %d, want 1", len(lines))
	}
	var rec LLMTraceRecord
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if rec.Provider != "responses" || rec.Model != "gpt-5.5" {
		t.Fatalf("provider/model = %q/%q, want responses/gpt-5.5", rec.Provider, rec.Model)
	}
	if rec.HTTPStatus != 200 || rec.Transport != "http" {
		t.Fatalf("http_status/transport = %d/%q, want 200/http", rec.HTTPStatus, rec.Transport)
	}
	if rec.FinalToolCalls != 1 || rec.StopReason != "tool_calls" {
		t.Fatalf("final_tool_calls/stop_reason = %d/%q, want 1/tool_calls", rec.FinalToolCalls, rec.StopReason)
	}
	if rec.ProgressBytes != 42 || rec.ProgressEvents != 2 {
		t.Fatalf("progress = %d/%d, want 42/2", rec.ProgressBytes, rec.ProgressEvents)
	}
	if rec.DurationMS <= 0 {
		t.Fatalf("duration_ms = %d, want > 0", rec.DurationMS)
	}
	if len(rec.ToolCalls) != 1 {
		t.Fatalf("len(tool_calls) = %d, want 1", len(rec.ToolCalls))
	}
	tc := rec.ToolCalls[0]
	if tc.ID != "call_1" || tc.Name != "Edit" {
		t.Fatalf("tool id/name = %q/%q, want call_1/Edit", tc.ID, tc.Name)
	}
	if !tc.Started || !tc.Finalized || tc.DeltaCount != 2 || !tc.FinalJSONValid {
		t.Fatalf("tool trace = %+v, want started finalized delta_count=2 final_json_valid=true", tc)
	}
	if tc.ArgsBytes <= 0 {
		t.Fatalf("args_bytes = %d, want > 0", tc.ArgsBytes)
	}
}
