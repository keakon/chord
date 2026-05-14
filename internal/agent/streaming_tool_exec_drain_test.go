package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
)

func TestStreamingToolExecutor_DrainCompletedResults(t *testing.T) {
	ctx := context.Background()
	turnID := uint64(1)
	emitCh := make(chan AgentEvent, 10)
	emit := func(evt AgentEvent) { emitCh <- evt }

	// Mock execute function that succeeds
	execute := func(ctx context.Context, tc message.ToolCall) (ToolExecutionResult, error) {
		time.Sleep(10 * time.Millisecond) // Simulate work
		return ToolExecutionResult{
			Result:            "test result for " + tc.Name,
			EffectiveArgsJSON: string(tc.Args),
		}, nil
	}

	exec := NewStreamingToolExecutor(turnID, ctx, emit, execute)

	// Start speculative execution for two tools
	call1 := message.ToolCall{
		ID:   "call_1",
		Name: "Read",
		Args: json.RawMessage(`{"path":"file1.txt"}`),
	}
	call2 := message.ToolCall{
		ID:   "call_2",
		Name: "Shell",
		Args: json.RawMessage(`{"command":"echo test"}`),
	}

	started1 := exec.Start(call1)
	started2 := exec.Start(call2)

	if !started1 || !started2 {
		t.Fatal("Failed to start speculative execution")
	}

	// Wait for both to complete
	time.Sleep(50 * time.Millisecond)

	// Drain completed results
	results := exec.DrainCompletedResults()

	if len(results) != 2 {
		t.Errorf("Expected 2 completed results, got %d", len(results))
	}

	if payload, ok := results["call_1"]; !ok {
		t.Error("Missing result for call_1")
	} else {
		if payload.Name != "Read" {
			t.Errorf("Expected Name=Read, got %s", payload.Name)
		}
		if payload.Result != "test result for Read" {
			t.Errorf("Expected result 'test result for Read', got %s", payload.Result)
		}
		if payload.Error != nil {
			t.Errorf("Expected no error, got %v", payload.Error)
		}
	}

	if payload, ok := results["call_2"]; !ok {
		t.Error("Missing result for call_2")
	} else {
		if payload.Name != "Shell" {
			t.Errorf("Expected Name=Shell, got %s", payload.Name)
		}
	}
}

func TestStreamingToolExecutor_DrainCompletedResults_IgnoresIncomplete(t *testing.T) {
	ctx := context.Background()
	turnID := uint64(1)
	emitCh := make(chan AgentEvent, 10)
	emit := func(evt AgentEvent) { emitCh <- evt }

	// Mock execute function that takes a long time
	execute := func(ctx context.Context, tc message.ToolCall) (ToolExecutionResult, error) {
		time.Sleep(200 * time.Millisecond) // Long running
		return ToolExecutionResult{
			Result:            "test result",
			EffectiveArgsJSON: string(tc.Args),
		}, nil
	}

	exec := NewStreamingToolExecutor(turnID, ctx, emit, execute)

	// Start speculative execution
	call := message.ToolCall{
		ID:   "call_slow",
		Name: "Read",
		Args: json.RawMessage(`{"path":"file.txt"}`),
	}

	started := exec.Start(call)
	if !started {
		t.Fatal("Failed to start speculative execution")
	}

	// Drain immediately without waiting for completion
	time.Sleep(10 * time.Millisecond)
	results := exec.DrainCompletedResults()

	// Should not include the still-running tool
	if len(results) != 0 {
		t.Errorf("Expected 0 completed results (tool still running), got %d", len(results))
	}
}
