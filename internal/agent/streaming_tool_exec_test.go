package agent

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
)

func TestStreamingToolExecutorPromotesCompletedResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	exec := NewStreamingToolExecutor(7, ctx, nil, func(context.Context, message.ToolCall) (ToolExecutionResult, error) {
		calls.Add(1)
		return ToolExecutionResult{EffectiveArgsJSON: `{"path":"README.md"}`, Result: "ok"}, nil
	})
	call := message.ToolCall{ID: "call-1", Name: "Read", Args: json.RawMessage(`{"path":"README.md"}`)}
	if !exec.Start(call) {
		t.Fatal("Start returned false")
	}
	payload, ok, drift := exec.Promote(call)
	if drift {
		t.Fatal("Promote reported drift")
	}
	if !ok || payload == nil {
		t.Fatal("Promote did not return cached payload")
	}
	if calls.Load() != 1 {
		t.Fatalf("execute calls = %d, want 1", calls.Load())
	}
	if payload.Result != "ok" || payload.TurnID != 7 {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestStreamingToolExecutorArgsDriftInvalidates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exec := NewStreamingToolExecutor(7, ctx, nil, func(context.Context, message.ToolCall) (ToolExecutionResult, error) {
		return ToolExecutionResult{EffectiveArgsJSON: `{"path":"a"}`, Result: "a"}, nil
	})
	if !exec.Start(message.ToolCall{ID: "call-1", Name: "Read", Args: json.RawMessage(`{"path":"a"}`)}) {
		t.Fatal("Start returned false")
	}
	payload, ok, drift := exec.Promote(message.ToolCall{ID: "call-1", Name: "Read", Args: json.RawMessage(`{"path":"b"}`)})
	if !drift {
		t.Fatal("Promote did not report drift")
	}
	if ok || payload != nil {
		t.Fatalf("payload=%#v ok=%v, want no cached result", payload, ok)
	}
}

func TestStreamingToolExecutorDiscardSuppressesVisibleResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	events := make(chan AgentEvent, 4)
	exec := NewStreamingToolExecutor(7, ctx, func(evt AgentEvent) { events <- evt }, func(context.Context, message.ToolCall) (ToolExecutionResult, error) {
		<-release
		return ToolExecutionResult{EffectiveArgsJSON: `{"path":"README.md"}`, Result: "ok"}, nil
	})
	call := message.ToolCall{ID: "call-1", Name: "Read", Args: json.RawMessage(`{"path":"README.md"}`)}
	if !exec.Start(call) {
		t.Fatal("Start returned false")
	}
	discarded := exec.DiscardAll("filtered")
	if len(discarded) != 1 {
		t.Fatalf("discarded len = %d, want 1", len(discarded))
	}
	close(release)
	time.Sleep(50 * time.Millisecond)
	for {
		select {
		case evt := <-events:
			if _, ok := evt.(ToolResultEvent); ok {
				t.Fatalf("unexpected ToolResultEvent after discard: %#v", evt)
			}
		default:
			return
		}
	}
}
