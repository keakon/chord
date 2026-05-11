package agent

import (
	"strings"
	"testing"
)

func TestFinalizeStreamingToolCardsMarksIncompleteCallsNotExecuted(t *testing.T) {
	turn := &Turn{ID: 1}
	turn.recordStreamingToolCall(PendingToolCall{CallID: "call-1", Name: "Read", ArgsJSON: `{"path":`, AgentID: "main"})

	var events []AgentEvent
	finalizeStreamingToolCards(func(evt AgentEvent) { events = append(events, evt) }, nil, nil, turn)

	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1", len(events))
	}
	got, ok := events[0].(ToolResultEvent)
	if !ok {
		t.Fatalf("event type = %T, want ToolResultEvent", events[0])
	}
	if got.CallID != "call-1" || got.Status != ToolResultStatusError {
		t.Fatalf("tool result = %#v", got)
	}
	if want := "This tool call was not executed"; got.Result == "" || got.AgentID != "main" || got.ArgsJSON != `{"path":` || got.Status != ToolResultStatusError || got.CallID != "call-1" || got.Name != "Read" {
		t.Fatalf("tool result fields = %#v", got)
	} else if !strings.HasPrefix(got.Result, want) {
		t.Fatalf("tool result message = %q, want prefix %q", got.Result, want)
	}
}

func TestFinalizeStreamingToolCardsMarksDiscardedSpeculativeCalls(t *testing.T) {
	turn := &Turn{ID: 1}
	turn.recordStreamingToolCall(PendingToolCall{CallID: "call-2", Name: "Edit", ArgsJSON: `{"path":"a"}`, AgentID: "sub-1"})

	var events []AgentEvent
	finalizeStreamingToolCards(
		func(evt AgentEvent) { events = append(events, evt) },
		nil,
		map[string]StreamingToolDiscardInfo{
			"call-2": {Started: true, Reason: "args drift"},
		},
		turn,
	)

	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1", len(events))
	}
	got, ok := events[0].(ToolResultEvent)
	if !ok {
		t.Fatalf("event type = %T, want ToolResultEvent", events[0])
	}
	if got.Status != ToolResultStatusError {
		t.Fatalf("status = %v, want error", got.Status)
	}
	if want := "Speculative tool execution was discarded during finalize"; !strings.HasPrefix(got.Result, want) {
		t.Fatalf("result = %q, want prefix %q", got.Result, want)
	}
	if got.AgentID != "sub-1" {
		t.Fatalf("agent id = %q, want sub-1", got.AgentID)
	}
}

func TestFinalizeStreamingToolCardsSkipsValidatedCalls(t *testing.T) {
	turn := &Turn{ID: 1}
	turn.recordStreamingToolCall(PendingToolCall{CallID: "call-3", Name: "Read", ArgsJSON: `{"path":"README.md"}`})

	var events []AgentEvent
	finalizeStreamingToolCards(func(evt AgentEvent) { events = append(events, evt) }, map[string]struct{}{"call-3": {}}, nil, turn)

	if len(events) != 0 {
		t.Fatalf("events len = %d, want 0", len(events))
	}
	if remaining := turn.drainStreamingToolCalls(); len(remaining) != 0 {
		t.Fatalf("remaining streaming calls = %d, want 0 after drain", len(remaining))
	}
}
