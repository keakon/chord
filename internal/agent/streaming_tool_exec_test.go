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

func TestStreamingToolExecutorArgsDriftWaitsForCompletedRollback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	completed := make(chan struct{})
	rollbackStarted := make(chan struct{})
	releaseRollback := make(chan struct{})
	exec := NewStreamingToolExecutor(7, ctx, func(AgentEvent) {}, func(context.Context, message.ToolCall) (ToolExecutionResult, error) {
		return ToolExecutionResult{
			EffectiveArgsJSON: `{"path":"a"}`,
			Result:            "a",
			speculativeHooks: &speculativeToolHooks{rollback: func() error {
				close(rollbackStarted)
				<-releaseRollback
				return nil
			}},
		}, nil
	})
	exec.SetTraceCallbacks(nil, func(_, _ string, _ time.Time) { close(completed) }, nil)
	if !exec.Start(message.ToolCall{ID: "call-1", Name: "Edit", Args: json.RawMessage(`{"path":"a","old_string":"before","new_string":"after"}`)}) {
		t.Fatal("Start returned false")
	}
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("speculative execution did not complete")
	}

	promoteReturned := make(chan struct{})
	var payload *ToolResultPayload
	var ok, drift bool
	go func() {
		payload, ok, drift = exec.Promote(message.ToolCall{ID: "call-1", Name: "Edit", Args: json.RawMessage(`{"path":"a","old_string":"before","new_string":"final"}`)})
		close(promoteReturned)
	}()

	select {
	case <-rollbackStarted:
	case <-time.After(time.Second):
		t.Fatal("rollback did not start")
	}
	select {
	case <-promoteReturned:
		t.Fatal("Promote returned before completed speculative rollback finished")
	default:
	}
	close(releaseRollback)
	select {
	case <-promoteReturned:
	case <-time.After(time.Second):
		t.Fatal("Promote did not return after rollback finished")
	}
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
