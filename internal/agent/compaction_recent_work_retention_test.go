package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func estimateRetainedTestTokens(messages []message.Message) int {
	total := 0
	for _, msg := range messages {
		total += len(msg.Content)
		for _, call := range msg.ToolCalls {
			total += len(call.Args)
		}
	}
	return total
}

func TestCheckpointRetainedRecordsKeepsRecentBatchesWhileWorkIsActive(t *testing.T) {
	head := []message.Message{{Role: message.RoleUser, Content: "request"}}
	head = appendAll(head,
		toolBatchMessages("status-call", tools.NameShell, message.ToolStatusSuccess, "clean worktree"),
		toolBatchMessages("read-call", tools.NameRead, message.ToolStatusSuccess, "package main"),
		toolBatchMessages("cc-call", tools.NameCompactContext, message.ToolStatusSuccess, "Context checkpoint request accepted"),
	)

	got := checkpointRetainedRecords(head, maxCheckpointRetainedRecentBatches, maxCheckpointRetainedRecentTokens, estimateRetainedTestTokens)
	if len(got) != 4 {
		t.Fatalf("retained %d records, want the newest two successful batches: %+v", len(got), got)
	}
	if got[0].ToolCalls[0].ID != "status-call" || got[2].ToolCalls[0].ID != "read-call" {
		t.Fatalf("retained batches = %q then %q, want transcript order", got[0].ToolCalls[0].ID, got[2].ToolCalls[0].ID)
	}
	for _, msg := range got {
		if len(msg.ToolCalls) == 1 && msg.ToolCalls[0].ID == "cc-call" {
			t.Fatalf("the accepted compact_context batch must not be re-attached under its own checkpoint: %+v", got)
		}
	}
	// Successful observations keep their bodies: they are the working state the
	// continuation resumes from.
	if got[1].Content != "clean worktree" || got[3].Content != "package main" {
		t.Fatalf("observation bodies were elided: %+v", got)
	}
}

func TestCheckpointRetainedRecordsFailureOnlyWithoutRecentWindow(t *testing.T) {
	head := []message.Message{{Role: message.RoleUser, Content: "request"}}
	head = appendAll(head,
		toolBatchMessages("ok-call", tools.NameRead, message.ToolStatusSuccess, "file contents"),
		toolBatchMessages("bad-call", tools.NameShell, message.ToolStatusError, "exit status 1"),
	)

	got := checkpointRetainedRecords(head, 0, 0, nil)
	if len(got) != 2 || got[0].ToolCalls[0].ID != "bad-call" {
		t.Fatalf("retained %+v, want only the failed batch without a recent window", got)
	}
}

func TestCheckpointRetainedRecordsMergesFailuresAndObservationsInOrder(t *testing.T) {
	head := []message.Message{{Role: message.RoleUser, Content: "request"}}
	head = appendAll(head,
		toolBatchMessages("fail-call", tools.NameShell, message.ToolStatusError, "exit status 1"),
		toolBatchMessages("ok-call", tools.NameRead, message.ToolStatusSuccess, "file contents"),
		toolBatchMessages("cc-call", tools.NameCompactContext, message.ToolStatusSuccess, "Context checkpoint request accepted"),
	)

	got := checkpointRetainedRecords(head, 2, 2048, estimateRetainedTestTokens)
	if len(got) != 4 {
		t.Fatalf("retained %d records, want the failed batch plus the successful observation: %+v", len(got), got)
	}
	if got[0].ToolCalls[0].ID != "fail-call" || got[2].ToolCalls[0].ID != "ok-call" {
		t.Fatalf("retained batches = %q then %q, want transcript order", got[0].ToolCalls[0].ID, got[2].ToolCalls[0].ID)
	}
}

func TestCheckpointRetainedRecordsCapsRecentBatches(t *testing.T) {
	head := []message.Message{{Role: message.RoleUser, Content: "request"}}
	for _, callID := range []string{"call-1", "call-2", "call-3"} {
		head = appendAll(head, toolBatchMessages(callID, tools.NameShell, message.ToolStatusSuccess, "ok"))
	}

	got := checkpointRetainedRecords(head, maxCheckpointRetainedRecentBatches, 1<<20, estimateRetainedTestTokens)
	if want := 2 * maxCheckpointRetainedRecentBatches; len(got) != want {
		t.Fatalf("retained %d records, want %d (newest %d batches)", len(got), want, maxCheckpointRetainedRecentBatches)
	}
	if got[0].ToolCalls[0].ID != "call-2" || got[2].ToolCalls[0].ID != "call-3" {
		t.Fatalf("retained batches = %q/%q, want the newest two in order", got[0].ToolCalls[0].ID, got[2].ToolCalls[0].ID)
	}
}

func TestCheckpointRetainedRecordsStopsWindowAtOversizedBatch(t *testing.T) {
	head := []message.Message{{Role: message.RoleUser, Content: "request"}}
	head = appendAll(head,
		toolBatchMessages("old-call", tools.NameRead, message.ToolStatusSuccess, "old observation"),
		toolBatchMessages("huge-call", tools.NameRead, message.ToolStatusSuccess, strings.Repeat("x", 200)),
	)
	// The newest batch cannot fit whole but its call arguments do; it enters
	// elided so the continuation still sees what just ran. The older batch is
	// then outside the contiguous window.
	got := checkpointRetainedRecords(head, 2, 100, estimateRetainedTestTokens)
	if len(got) != 2 || got[0].ToolCalls[0].ID != "huge-call" {
		t.Fatalf("retained %+v, want only the elided newest batch", got)
	}
	if want := message.FormatToolResultElided(len(strings.Repeat("x", 200))); got[1].Content != want {
		t.Fatalf("oversized observation content = %q, want %q", got[1].Content, want)
	}
}

func TestCheckpointRetainedRecordsSkipsCompactContextOnlyBatchesForWindow(t *testing.T) {
	head := []message.Message{{Role: message.RoleUser, Content: "request"}}
	head = appendAll(head,
		toolBatchMessages("read-call", tools.NameRead, message.ToolStatusSuccess, "file contents"),
		toolBatchMessages("cc-call", tools.NameCompactContext, message.ToolStatusSuccess, "Context checkpoint request accepted"),
	)

	got := checkpointRetainedRecords(head, 1, 2048, estimateRetainedTestTokens)
	if len(got) != 2 || got[0].ToolCalls[0].ID != "read-call" {
		t.Fatalf("retained %+v, want the observation behind the checkpoint request", got)
	}
}

func TestModelDrivenBundleRecentWorkActive(t *testing.T) {
	cases := []struct {
		name   string
		bundle modelDrivenBarrierSnapshot
		want   bool
	}{
		{name: "settled", bundle: modelDrivenBarrierSnapshot{}, want: false},
		{name: "open todo", bundle: modelDrivenBarrierSnapshot{todos: []tools.TodoItem{{Status: "in_progress"}}}, want: true},
		{name: "running subagent", bundle: modelDrivenBarrierSnapshot{subAgents: []SubAgentInfo{{State: string(SubAgentStateRunning)}}}, want: true},
		{name: "queued user input", bundle: modelDrivenBarrierSnapshot{queuedUserMessages: []message.Message{{Role: message.RoleUser, Content: "also do this"}}}, want: true},
	}
	for _, tc := range cases {
		if got := tc.bundle.recentWorkActive(); got != tc.want {
			t.Fatalf("%s: recentWorkActive() = %v, want %v", tc.name, got, tc.want)
		}
	}
	finished := []tools.TodoItem{{Status: "completed"}}
	if (modelDrivenBarrierSnapshot{todos: finished}).recentWorkActive() {
		t.Fatal("completed todos must not keep the observation window open")
	}
}
