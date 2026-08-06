package agent

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestCompleteRejectsBlankSummaryInIntercept(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	sub.handleLLMResponse(&llmResult{
		turnID: 1,
		resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
			mustJSONToolCall(t, "call-1", "complete", map[string]any{"summary": "   "}),
		})},
	})
	evt := <-parent.eventCh
	if evt.Type != EventAgentError {
		t.Fatalf("event.Type = %q, want %q", evt.Type, EventAgentError)
	}
}

func TestCompletionVerificationUsesLatestFinalizedExactCommand(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	sub.verificationLedger = []verificationLedgerEntry{{ToolCallID: "old", Command: "go test ./internal/a", Status: "failed"}, {ToolCallID: "new", Command: "go test ./internal/a", Status: "passed"}}
	env := &CompletionEnvelope{VerificationRun: []string{"go test ./internal/a"}}
	if err := sub.validateCompletionVerification(env); err != nil {
		t.Fatal(err)
	}
	if len(env.VerificationRecords) != 1 || env.VerificationRecords[0].ToolCallID != "new" || env.VerificationRecords[0].Status != "passed" {
		t.Fatalf("records = %#v", env.VerificationRecords)
	}
}

func TestCompletionVerificationRejectsUnmatchedCommand(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	if err := sub.validateCompletionVerification(&CompletionEnvelope{VerificationRun: []string{"go test ./missing"}}); err == nil {
		t.Fatal("expected unmatched verification command to be rejected")
	}
}

func TestCompletionVerificationRejectsPassedCommandBeforeLaterMutation(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	passed := &toolResult{CallID: "verify", Name: "shell", ArgsJSON: `{"command":"go test ./internal/a"}`}
	sub.recordTaskToolChanges(passed, false)
	sub.recordVerificationToolResult(passed, "ok", false)

	sub.recordTaskToolChanges(&toolResult{
		CallID: "edit", Name: tools.NameWrite, ArgsJSON: `{"path":"internal/a.go"}`,
		FileState: &message.ToolFileState{Writes: []message.TrackedFileState{{Path: "internal/a.go", Exists: true}}},
	}, false)

	if err := sub.validateCompletionVerification(&CompletionEnvelope{VerificationRun: []string{"go test ./internal/a"}}); err == nil || !strings.Contains(err.Error(), "mutation epoch") {
		t.Fatalf("validation error = %v, want stale verification rejection", err)
	}
}

func TestCompletionVerificationAcceptsConsecutiveDeclaredCommands(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	for i, command := range []string{"go test ./internal/a", "go test ./internal/b"} {
		result := &toolResult{CallID: fmt.Sprintf("verify-%d", i), Name: tools.NameShell, ArgsJSON: fmt.Sprintf(`{"command":%q}`, command)}
		sub.recordTaskToolChanges(result, false)
		sub.recordVerificationToolResult(result, "ok", false)
	}
	env := &CompletionEnvelope{VerificationRun: []string{"go test ./internal/a", "go test ./internal/b"}}
	if err := sub.validateCompletionVerification(env); err != nil {
		t.Fatal(err)
	}
	if len(env.VerificationRecords) != 2 {
		t.Fatalf("verification records = %#v, want two records", env.VerificationRecords)
	}
}
func TestDeferredCompletionRetainsStructuredEnvelope(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	parent.subs.mu.Lock()
	parent.subs.taskRecords["child-1"] = &DurableTaskRecord{
		TaskID:           "child-1",
		OwnerTaskID:      sub.taskID,
		JoinToOwner:      true,
		State:            string(SubAgentStateRunning),
		LatestInstanceID: "worker-child",
	}
	parent.subs.mu.Unlock()

	sub.handleLLMResponse(&llmResult{
		turnID: 1,
		resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
			mustJSONToolCall(t, "call-1", "complete", map[string]any{
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
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[sub.instanceID] = sub
	a.subs.taskRecords[sub.taskID] = &DurableTaskRecord{
		TaskID:           sub.taskID,
		LatestInstanceID: sub.instanceID,
		State:            string(SubAgentStateWaitingDescendant),
	}
	a.subs.mu.Unlock()
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
