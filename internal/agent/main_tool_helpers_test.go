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
		{ID: "3", Name: tools.NameGrep, Args: json.RawMessage(`{"pattern":"TODO","paths":["."]}`)},
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
		{ID: "2", Name: tools.NameGrep, Args: json.RawMessage(`{"pattern":"TODO","paths":["docs"]}`)},
		{ID: "3", Name: tools.NameGlob, Args: json.RawMessage(`{"path":"src","patterns":["**/*.go"]}`)},
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
		{ID: "1", Name: tools.NameGrep, Args: json.RawMessage(`{"pattern":"TODO","paths":["."]}`)},
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

func TestFinalizeStreamingToolCardsSkipsValidCallsAndDiscardsDeferredInvalid(t *testing.T) {
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
	ev, ok := events[0].(ToolCallDiscardEvent)
	if !ok {
		t.Fatalf("event = %#v, want ToolCallDiscardEvent", events[0])
	}
	if ev.ID != "deferred" || ev.Name != tools.NameRead || ev.Reason != "deferred" {
		t.Fatalf("discard event = %#v", ev)
	}
}

func TestBuildToolExecutionBatchesMergesJobReadsWithReadOnlySiblings(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.JobOutputTool{})
	registry.Register(tools.JobListTool{})
	registry.Register(tools.ReadTool{})
	calls := []message.ToolCall{
		{ID: "1", Name: tools.NameJobOutput, Args: json.RawMessage(`{"job_id":"job-1"}`)},
		{ID: "2", Name: tools.NameRead, Args: json.RawMessage(`{"path":"README.md"}`)},
		{ID: "3", Name: tools.NameJobList, Args: json.RawMessage(`{}`)},
		{ID: "4", Name: tools.NameJobOutput, Args: json.RawMessage(`{"job_id":"job-2"}`)},
	}

	batches := buildToolExecutionBatches(registry, calls)
	if len(batches) != 1 {
		t.Fatalf("len(batches) = %d, want 1: a waiting job read must not hold back read-only siblings", len(batches))
	}
	if got := len(batches[0].Calls); got != len(calls) {
		t.Fatalf("batch calls = %d, want %d", got, len(calls))
	}
}

func TestBuildToolExecutionBatchesMergesSameJobReads(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.JobOutputTool{})
	calls := []message.ToolCall{
		{ID: "1", Name: tools.NameJobOutput, Args: json.RawMessage(`{"job_id":"job-1"}`)},
		{ID: "2", Name: tools.NameJobOutput, Args: json.RawMessage(`{"job_id":"job-1","wait":"exit"}`)},
	}

	// Each call claims its own window from the job's shared cursor, so two reads
	// of one job stay independent instead of serializing the turn.
	batches := buildToolExecutionBatches(registry, calls)
	if len(batches) != 1 || len(batches[0].Calls) != 2 {
		t.Fatalf("batches = %#v, want one batch of two same-job reads", batches)
	}
}

func TestBuildToolExecutionBatchesMergesReadOnlyShellCommand(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.NewShellTool("bash"))
	registry.Register(tools.ReadTool{})
	calls := []message.ToolCall{
		{ID: "1", Name: tools.NameRead, Args: json.RawMessage(`{"path":"README.md"}`)},
		{ID: "2", Name: tools.NameShell, Args: json.RawMessage(`{"command":"git status"}`)},
	}

	batches := buildToolExecutionBatches(registry, calls)
	if len(batches) != 1 || len(batches[0].Calls) != 2 {
		t.Fatalf("batches = %#v, want one batch for an allowlisted read-only command", batches)
	}
}

func TestBuildToolExecutionBatchesKeepsMutatingShellAsBoundary(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.NewShellTool("bash"))
	registry.Register(tools.ReadTool{})
	calls := []message.ToolCall{
		{ID: "1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"go test ./..."}`)},
		{ID: "2", Name: tools.NameRead, Args: json.RawMessage(`{"path":"README.md"}`)},
	}

	batches := buildToolExecutionBatches(registry, calls)
	if len(batches) != 2 {
		t.Fatalf("len(batches) = %d, want 2: a mutating shell may touch any path and stays a boundary", len(batches))
	}
}

func TestBuildToolExecutionBatchesMergesRgWithReadsWithoutSiblingAbort(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.NewShellTool("bash"))
	registry.Register(tools.ReadTool{})
	registry.Register(tools.GrepTool{})
	calls := []message.ToolCall{
		{ID: "1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"rg --no-config -n pat --glob '*.go'"}`)},
		{ID: "2", Name: tools.NameRead, Args: json.RawMessage(`{"path":"README.md"}`)},
		{ID: "3", Name: tools.NameGrep, Args: json.RawMessage(`{"pattern":"TODO","paths":["."]}`)},
	}

	batches := buildToolExecutionBatches(registry, calls)
	if len(batches) != 1 || len(batches[0].Calls) != 3 {
		t.Fatalf("batches = %#v, want one batch of rg + read + grep", batches)
	}
	if batches[0].AbortSiblingsOnError {
		t.Fatal("read-only batch must not abort siblings: rg exit 1 (no match) is still a read")
	}
	// No member may arm cancellation on its own either: the executor only
	// cancels siblings when the batch flag is set, and the flag is the OR of
	// these per-call policies.
	for _, call := range calls {
		if policy := tools.PolicyForTool(registry, call.Name, call.Args); policy.AbortSiblingsOnError {
			t.Fatalf("policy for %s = %#v, want no sibling abort so a failing read cannot cancel the batch", call.Name, policy)
		}
	}
}
