package agent

import (
	"context"
	"encoding/json"
	"testing"
	"testing/synctest"

	"github.com/keakon/chord/internal/message"
)

func TestStreamingToolExecutor_DrainCompletedResults(t *testing.T) {
	ctx := context.Background()
	turnID := uint64(1)
	emitCh := make(chan AgentEvent, 10)
	emit := func(evt AgentEvent) { emitCh <- evt }

	// Mock execute function that succeeds
	execute := func(ctx context.Context, tc message.ToolCall) (ToolExecutionResult, error) {
		return ToolExecutionResult{
			Result:            "test result for " + tc.Name,
			EffectiveArgsJSON: string(tc.Args),
		}, nil
	}

	exec := NewStreamingToolExecutor(turnID, ctx, emit, execute)

	// Start speculative execution for two tools
	call1 := message.ToolCall{
		ID:   "call_1",
		Name: "read",
		Args: json.RawMessage(`{"path":"file1.txt"}`),
	}
	call2 := message.ToolCall{
		ID:   "call_2",
		Name: "shell",
		Args: json.RawMessage(`{"command":"echo test"}`),
	}

	started1 := exec.Start(call1)
	started2 := exec.Start(call2)

	if !started1 || !started2 {
		t.Fatal("Failed to start speculative execution")
	}

	// Wait for both speculative executions to finish instead of guessing a
	// fixed window that flakes under load.
	waitForStreamingToolDone(t, exec, call1.ID)
	waitForStreamingToolDone(t, exec, call2.ID)

	// Drain completed results
	results := exec.DrainCompletedResults()

	if len(results) != 2 {
		t.Errorf("Expected 2 completed results, got %d", len(results))
	}

	if payload, ok := results["call_1"]; !ok {
		t.Error("Missing result for call_1")
	} else {
		if payload.Name != "read" {
			t.Errorf("Expected Name=read, got %s", payload.Name)
		}
		if payload.Result != "test result for read" {
			t.Errorf("Expected result 'test result for read', got %s", payload.Result)
		}
		if payload.Error != nil {
			t.Errorf("Expected no error, got %v", payload.Error)
		}
	}

	if payload, ok := results["call_2"]; !ok {
		t.Error("Missing result for call_2")
	} else {
		if payload.Name != "shell" {
			t.Errorf("Expected Name=shell, got %s", payload.Name)
		}
	}
}

func TestStreamingToolExecutor_DrainCompletedResults_IgnoresIncomplete(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		defer func() {
			close(release)
			// Join execution and result publication even when an assertion fails.
			synctest.Wait()
		}()
		execute := func(ctx context.Context, tc message.ToolCall) (ToolExecutionResult, error) {
			close(started)
			<-release
			return ToolExecutionResult{
				Result:            "test result",
				EffectiveArgsJSON: string(tc.Args),
			}, nil
		}
		exec := NewStreamingToolExecutor(1, t.Context(), nil, execute)
		call := message.ToolCall{
			ID:   "call_slow",
			Name: "read",
			Args: json.RawMessage(`{"path":"file.txt"}`),
		}
		if !exec.Start(call) {
			t.Fatal("failed to start speculative execution")
		}
		<-started
		synctest.Wait()
		if results := exec.DrainCompletedResults(); len(results) != 0 {
			t.Errorf("expected no completed results while execution is blocked, got %d", len(results))
		}
	})
}
