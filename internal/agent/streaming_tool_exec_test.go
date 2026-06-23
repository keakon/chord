package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestSpeculativeExecutionRejectsInvisibleEditFamilyToolBeforeFileMutation(t *testing.T) {
	projectRoot := t.TempDir()
	targetPath := filepath.Join(projectRoot, "target.txt")
	if err := os.WriteFile(targetPath, []byte("old line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	registry.Register(tools.PatchTool{BaseDir: projectRoot})
	registry.Register(tools.EditTool{BaseDir: projectRoot})
	pipeline := toolExecutionPipeline{
		registry:    registry,
		projectRoot: projectRoot,
		visibleToolNames: func() map[string]struct{} {
			return map[string]struct{}{tools.NameEdit: {}}
		},
	}

	call := message.ToolCall{ID: "patch-1", Name: tools.NamePatch, Args: json.RawMessage(`{"path":"` + targetPath + `","patch":"@@\n-old line\n+new line\n"}`)}
	_, err := pipeline.executeSpeculative(t.Context(), call)
	if err == nil {
		t.Fatal("executeSpeculative patch on edit-only surface succeeded; want rejection")
	}
	if !strings.Contains(err.Error(), "not available for the current model") || !strings.Contains(err.Error(), tools.NameEdit) {
		t.Fatalf("err = %q, want hidden tool error mentioning edit alternative", err.Error())
	}
	got, readErr := os.ReadFile(targetPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "old line\n" {
		t.Fatalf("file changed after rejected speculative patch = %q", got)
	}
}
func TestStreamingToolExecutorPromotesCompletedResult(t *testing.T) {
	ctx := t.Context()
	var calls atomic.Int32
	exec := NewStreamingToolExecutor(7, ctx, nil, func(context.Context, message.ToolCall) (ToolExecutionResult, error) {
		calls.Add(1)
		return ToolExecutionResult{EffectiveArgsJSON: `{"path":"README.md"}`, Result: "ok"}, nil
	})
	call := message.ToolCall{ID: "call-1", Name: "read", Args: json.RawMessage(`{"path":"README.md"}`)}
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
	ctx := t.Context()
	exec := NewStreamingToolExecutor(7, ctx, nil, func(context.Context, message.ToolCall) (ToolExecutionResult, error) {
		return ToolExecutionResult{EffectiveArgsJSON: `{"path":"a"}`, Result: "a"}, nil
	})
	if !exec.Start(message.ToolCall{ID: "call-1", Name: "read", Args: json.RawMessage(`{"path":"a"}`)}) {
		t.Fatal("Start returned false")
	}
	payload, ok, drift := exec.Promote(message.ToolCall{ID: "call-1", Name: "read", Args: json.RawMessage(`{"path":"b"}`)})
	if !drift {
		t.Fatal("Promote did not report drift")
	}
	if ok || payload != nil {
		t.Fatalf("payload=%#v ok=%v, want no cached result", payload, ok)
	}
}

func TestSpeculativeConflictKeysNormalizesToolName(t *testing.T) {
	expectedPath, err := filepath.Abs("demo.txt")
	if err != nil {
		t.Fatal(err)
	}
	keys := speculativeConflictKeys(message.ToolCall{
		Name: " write ",
		Args: json.RawMessage(`{"path":"demo.txt","content":"x"}`),
	}, "")
	if len(keys) != 1 || keys[0] != "file:"+expectedPath {
		t.Fatalf("speculativeConflictKeys() = %v, want [file:%s]", keys, expectedPath)
	}
}

func TestStreamingToolExecutorArgsDriftWaitsForCompletedRollback(t *testing.T) {
	ctx := t.Context()
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
	if !exec.Start(message.ToolCall{ID: "call-1", Name: tools.NamePatch, Args: json.RawMessage(`{"path":"demo.txt","patch":"@@\n-before\n+after\n"}`)}) {
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
		payload, ok, drift = exec.Promote(message.ToolCall{ID: "call-1", Name: tools.NamePatch, Args: json.RawMessage(`{"path":"demo.txt","patch":"@@\n-before\n+final\n"}`)})
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
	ctx := t.Context()
	release := make(chan struct{})
	events := make(chan AgentEvent, 4)
	exec := NewStreamingToolExecutor(7, ctx, func(evt AgentEvent) { events <- evt }, func(context.Context, message.ToolCall) (ToolExecutionResult, error) {
		<-release
		return ToolExecutionResult{EffectiveArgsJSON: `{"path":"README.md"}`, Result: "ok"}, nil
	})
	call := message.ToolCall{ID: "call-1", Name: "read", Args: json.RawMessage(`{"path":"README.md"}`)}
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
