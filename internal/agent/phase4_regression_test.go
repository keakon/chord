package agent

import (
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
)

func TestCompleteRejectsBlankSummaryInIntercept(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	sub.handleLLMResponse(&llmResult{
		turnID: 1,
		resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
			mustJSONToolCall(t, "call-1", "Complete", map[string]any{"summary": "   "}),
		})},
	})
	evt := <-parent.eventCh
	if evt.Type != EventAgentError {
		t.Fatalf("event.Type = %q, want %q", evt.Type, EventAgentError)
	}
}

func TestDeferredCompletionRetainsStructuredEnvelope(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	parent.mu.Lock()
	parent.taskRecords["child-1"] = &DurableTaskRecord{
		TaskID:           "child-1",
		OwnerTaskID:      sub.taskID,
		JoinToOwner:      true,
		State:            string(SubAgentStateRunning),
		LatestInstanceID: "worker-child",
	}
	parent.mu.Unlock()

	sub.handleLLMResponse(&llmResult{
		turnID: 1,
		resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
			mustJSONToolCall(t, "call-1", "Complete", map[string]any{
				"summary":               "final summary",
				"files_changed":         []string{"internal/a.go"},
				"verification_run":      []string{"go test ./internal/a"},
				"known_risks":           []string{"manual QA"},
				"follow_up_recommended": []string{"review"},
			}),
		})},
	})

	pending := sub.PendingCompleteIntent()
	if pending == nil || pending.Envelope == nil {
		t.Fatalf("PendingCompleteIntent() = %#v, want structured envelope", pending)
	}
	if got := pending.Envelope.FilesChanged; len(got) != 1 || got[0] != "internal/a.go" {
		t.Fatalf("pending files_changed = %#v", got)
	}
	if got := pending.Envelope.VerificationRun; len(got) != 1 || got[0] != "go test ./internal/a" {
		t.Fatalf("pending verification_run = %#v", got)
	}
}

func TestCoordinationSnapshotDoesNotDeadlockOnWaitingDescendant(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "task-parent")
	sub.instanceID = "worker-parent"
	sub.setState(SubAgentStateWaitingDescendant, "waiting for child")
	sub.runtimeState.stateChangedAt = time.Now().Add(-coordinationSnapshotStallAfter - time.Minute)
	a.mu.Lock()
	delete(a.subAgents, "worker-1")
	a.subAgents[sub.instanceID] = sub
	a.taskRecords[sub.taskID] = &DurableTaskRecord{
		TaskID:           sub.taskID,
		LatestInstanceID: sub.instanceID,
		State:            string(SubAgentStateWaitingDescendant),
	}
	a.mu.Unlock()
	done := make(chan string, 1)
	go func() {
		done <- a.buildCoordinationSnapshotOverlay()
	}()
	select {
	case out := <-done:
		if out == "" {
			t.Fatal("snapshot unexpectedly empty")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("buildCoordinationSnapshotOverlay appears deadlocked")
	}
}
