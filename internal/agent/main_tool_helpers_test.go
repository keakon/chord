package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestBuildToolExecutionBatchesKeepsMutationsAsBoundaries(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.ReadTool{})
	registry.Register(tools.GrepTool{})
	registry.Register(tools.WriteTool{})
	calls := []message.ToolCall{
		{ID: "1", Name: tools.NameRead, Args: json.RawMessage(`{"path":"README.md"}`)},
		{ID: "2", Name: tools.NameWrite, Args: json.RawMessage(`{"path":"README.md","content":"x"}`)},
		{ID: "3", Name: tools.NameGrep, Args: json.RawMessage(`{"pattern":"TODO","path":"."}`)},
	}

	batches := buildToolExecutionBatches(registry, calls)
	if len(batches) != 3 {
		t.Fatalf("len(batches) = %d, want 3", len(batches))
	}
	for i, wantID := range []string{"1", "2", "3"} {
		if len(batches[i].Calls) != 1 || batches[i].Calls[0].ID != wantID {
			t.Fatalf("batch[%d] = %#v, want single call %s", i, batches[i].Calls, wantID)
		}
	}
}

func TestBuildToolExecutionBatchesGroupsOnlyConsecutiveReadOnlyCalls(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.ReadTool{})
	registry.Register(tools.GrepTool{})
	registry.Register(tools.GlobTool{})
	calls := []message.ToolCall{
		{ID: "1", Name: tools.NameRead, Args: json.RawMessage(`{"path":"README.md"}`)},
		{ID: "2", Name: tools.NameGrep, Args: json.RawMessage(`{"pattern":"TODO","path":"docs"}`)},
		{ID: "3", Name: tools.NameGlob, Args: json.RawMessage(`{"path":"src","pattern":"**/*.go"}`)},
	}

	batches := buildToolExecutionBatches(registry, calls)
	if len(batches) != 1 {
		t.Fatalf("len(batches) = %d, want 1", len(batches))
	}
	if len(batches[0].Calls) != 3 {
		t.Fatalf("len(batches[0].Calls) = %d, want 3", len(batches[0].Calls))
	}
}

func TestBuildToolExecutionBatchesSplitsDirectoryReadFromFileWrite(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.GrepTool{})
	registry.Register(tools.WriteTool{})
	calls := []message.ToolCall{
		{ID: "1", Name: tools.NameGrep, Args: json.RawMessage(`{"pattern":"TODO","path":"."}`)},
		{ID: "2", Name: tools.NameWrite, Args: json.RawMessage(`{"path":"internal/agent/main.go","content":"x"}`)},
	}

	batches := buildToolExecutionBatches(registry, calls)
	if len(batches) != 2 {
		t.Fatalf("len(batches) = %d, want 2", len(batches))
	}
}

func TestFinalizeStreamingToolCardsEmitsDiscardReasonForStartedSpeculativeCall(t *testing.T) {
	turn := &Turn{}
	turn.recordStreamingToolCall(PendingToolCall{
		CallID:   "call-1",
		Name:     tools.NameRead,
		ArgsJSON: `{"path":"README.md"}`,
		AgentID:  "agent-1",
	})
	var events []AgentEvent
	finalizeStreamingToolCards(func(evt AgentEvent) { events = append(events, evt) }, nil, map[string]StreamingToolDiscardInfo{
		"call-1": {Started: true, Reason: "filtered"},
	}, turn)
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1", len(events))
	}
	ev, ok := events[0].(ToolResultEvent)
	if !ok {
		t.Fatalf("event = %#v, want ToolResultEvent", events[0])
	}
	if ev.Status != ToolResultStatusError || ev.AgentID != "agent-1" {
		t.Fatalf("tool result event = %#v", ev)
	}
	if !strings.Contains(ev.Result, "Speculative tool execution was discarded") || !strings.Contains(ev.Result, "reason=filtered") {
		t.Fatalf("discard result = %q", ev.Result)
	}

	events = nil
	finalizeStreamingToolCards(func(evt AgentEvent) { events = append(events, evt) }, nil, nil, turn)
	if len(events) != 0 {
		t.Fatalf("second finalize emitted drained events: %#v", events)
	}
}

func TestFinalizeStreamingToolCardsSkipsValidCallsAndMarksDeferredInvalid(t *testing.T) {
	turn := &Turn{}
	turn.recordStreamingToolCall(PendingToolCall{CallID: "valid", Name: tools.NameRead, ArgsJSON: `{"path":"README.md"}`})
	turn.recordStreamingToolCall(PendingToolCall{CallID: "deferred", Name: tools.NameRead, ArgsJSON: `{"path":"docs"}`})
	turn.recordStreamingToolCall(PendingToolCall{Name: tools.NameRead, ArgsJSON: `{}`})

	var events []AgentEvent
	finalizeStreamingToolCards(func(evt AgentEvent) { events = append(events, evt) }, map[string]struct{}{"valid": {}}, map[string]StreamingToolDiscardInfo{
		"deferred": {Reason: "deferred"},
	}, turn)
	if len(events) != 1 {
		t.Fatalf("events len = %d, want only deferred invalid event", len(events))
	}
	ev, ok := events[0].(ToolResultEvent)
	if !ok {
		t.Fatalf("event = %#v, want ToolResultEvent", events[0])
	}
	if ev.CallID != "deferred" || ev.Status != ToolResultStatusError {
		t.Fatalf("tool result event = %#v", ev)
	}
	if !strings.Contains(ev.Result, "not executed") || !strings.Contains(ev.Result, "reason=deferred") {
		t.Fatalf("deferred result = %q", ev.Result)
	}
}
