package agent

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestSubAgentInvalidCompleteGetsRejectedToolResultAndBoundedFollowUp(t *testing.T) {
	tests := []struct {
		name       string
		args       any
		wantReason string
	}{
		{name: "json parse error", args: map[string]any{"summary": 123}, wantReason: "cannot unmarshal number"},
		{name: "blank summary", args: map[string]any{"summary": "   "}, wantReason: "summary is required"},
		{name: "artifact outside session", args: map[string]any{"summary": "done", "artifacts": []map[string]any{{"rel_path": "../outside.txt"}}}, wantReason: "artifact path escapes"},
		{name: "typed result without result_type", args: map[string]any{"summary": "done", "result": map[string]any{"value": 1}}, wantReason: "result or result_ref requires result_type"},
		{name: "result_type without result or result_ref", args: map[string]any{"summary": "done", "result_type": "type/test"}, wantReason: "result_type requires result or result_ref"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parent, sub := newMixedBatchTestSubAgent(t)
			providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
				Type:   config.ProviderTypeChatCompletions,
				Models: map[string]config.ModelConfig{"model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}}},
			}, []string{"key"})
			// The follow-up request is sent with a forced required-tool-choice
			// tuning, so the scripted response must return a tool call (mirroring
			// the terminal-recovery tests) or the client cannot finalize it.
			provider := &blockingStreamProvider{calls: []scriptedStreamCall{{resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{mustJSONToolCall(t, "retry-1", "complete", map[string]any{"summary": "done"})})}}}}
			sub.llmClient = llm.NewClient(providerCfg, provider, "model", 1024, "sys")

			sub.handleLLMResponse(&llmResult{turnID: 1, resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
				mustJSONToolCall(t, "call-1", "complete", tc.args),
			})}})

			// Must not be terminal: the worker gets one bounded follow-up request
			// instead of failing on the spot.
			select {
			case evt := <-parent.eventCh:
				t.Fatalf("invalid Complete terminated the task with %#v instead of a bounded follow-up", evt)
			default:
			}
			if result := waitForSubAgentLLMResult(t, sub, time.Second); result.err != nil {
				t.Fatalf("follow-up request failed: %v", result.err)
			}
			if got := sub.turn.SubAgentCompletionRecoveryCount; got != 1 {
				t.Fatalf("completion recovery count = %d, want 1", got)
			}
			if got := sub.turn.SubAgentTerminalRecoveryCount; got != 0 {
				t.Fatalf("terminal (pure-text) recovery count = %d, want 0 (rejected Complete must not consume the wrap-up nudge)", got)
			}

			// The rejected Complete call got a tool result so the transcript keeps
			// its tool-call pairing.
			msgs := sub.ctxMgr.Snapshot()
			foundRejected := false
			for _, msg := range msgs {
				if msg.Role != "tool" || msg.ToolCallID != "call-1" {
					continue
				}
				foundRejected = strings.HasPrefix(msg.Content, "Completion rejected:") && strings.Contains(msg.Content, tc.wantReason)
				break
			}
			if !foundRejected {
				t.Fatalf("missing rejected tool result containing %q in %#v", tc.wantReason, msgs)
			}

			// The follow-up request carried the fix-it nudge for the model.
			seen, _ := provider.snapshot()
			if len(seen) != 1 {
				t.Fatalf("provider request count = %d, want 1 bounded follow-up", len(seen))
			}
			last := seen[0][len(seen[0])-1]
			if last.Role != "user" || !strings.Contains(last.Content, "Completion was rejected:") {
				t.Fatalf("follow-up request tail = %#v, want rejection nudge", last)
			}
		})
	}
}

func TestSubAgentInvalidCompleteRetryHasOwnBudgetAfterPureTextRecovery(t *testing.T) {
	// The pure-text wrap-up nudge and the rejected-Complete follow-up used to
	// share one budget, so a text-only reply followed by a rejected Complete
	// failed with a "after retry" error that was actually the first attempt.
	// Each recovery class now gets its own single follow-up.
	parent, sub := newMixedBatchTestSubAgent(t)
	providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{"model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}}},
	}, []string{"key"})
	provider := &blockingStreamProvider{calls: []scriptedStreamCall{{resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{mustJSONToolCall(t, "retry-1", "complete", map[string]any{"summary": "done"})})}}}}
	sub.llmClient = llm.NewClient(providerCfg, provider, "model", 1024, "sys")
	// Simulate a text-only reply that already spent the wrap-up nudge.
	sub.turn.SubAgentTerminalRecoveryCount = 1

	sub.handleLLMResponse(&llmResult{turnID: 1, resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
		mustJSONToolCall(t, "call-1", "complete", map[string]any{"summary": "   "}),
	})}})

	select {
	case evt := <-parent.eventCh:
		t.Fatalf("rejected Complete terminated despite the pure-text budget already being spent: %#v", evt)
	default:
	}
	if result := waitForSubAgentLLMResult(t, sub, time.Second); result.err != nil {
		t.Fatalf("follow-up request failed: %v", result.err)
	}
	if got := sub.turn.SubAgentCompletionRecoveryCount; got != 1 {
		t.Fatalf("completion recovery count = %d, want 1 (the split budget must still be available)", got)
	}

	// The corrected follow-up call completes normally: the worker kept its
	// second chance after one pure-text reply and one rejected Complete.
	sub.handleLLMResponse(&llmResult{turnID: 1, resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
		mustJSONToolCall(t, "call-2", "complete", map[string]any{"summary": "done"}),
	})}})
	select {
	case evt := <-parent.eventCh:
		if evt.Type != EventAgentDone {
			t.Fatalf("event.Type = %q, want EventAgentDone", evt.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for corrected Complete")
	}
}

func TestSubAgentCoReturnedInvalidCompleteRejectedAfterSiblingsSettle(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{"model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}}},
	}, []string{"key"})
	provider := &blockingStreamProvider{calls: []scriptedStreamCall{{resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{mustJSONToolCall(t, "retry-1", "complete", map[string]any{"summary": "done"})})}}}}
	sub.llmClient = llm.NewClient(providerCfg, provider, "model", 1024, "sys")

	sub.handleLLMResponse(&llmResult{turnID: 1, resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
		mustJSONToolCall(t, "call-1", "complete", map[string]any{"summary": "done", "result": map[string]any{"value": 1}}),
		mustJSONToolCall(t, "call-2", "Dummy", map[string]any{"value": "x"}),
	})}})
	if sub.pendingComplete != nil || sub.pendingCompleteCallID != "" {
		t.Fatalf("invalid Complete must not become a pending completion: %#v", sub.pendingComplete)
	}
	if sub.pendingRejectedCompleteErr == nil || sub.pendingRejectedCompleteCallID != "call-1" {
		t.Fatalf("pending rejected completion = %q/%v, want call-1 with the typed-result error", sub.pendingRejectedCompleteCallID, sub.pendingRejectedCompleteErr)
	}

	// The sibling tool settles first; only then is the rejected Complete
	// appended and the bounded follow-up issued.
	sub.handleToolResult(&toolResult{CallID: "call-2", Name: "Dummy", ArgsJSON: `{"value":"x"}`, Result: "ok", TurnID: 1})
	select {
	case evt := <-parent.eventCh:
		t.Fatalf("invalid Complete terminated the task with %#v instead of a bounded follow-up", evt)
	default:
	}
	if result := waitForSubAgentLLMResult(t, sub, time.Second); result.err != nil {
		t.Fatalf("follow-up request failed: %v", result.err)
	}
	if got := sub.turn.SubAgentCompletionRecoveryCount; got != 1 {
		t.Fatalf("completion recovery count = %d, want 1", got)
	}
	if sub.pendingRejectedCompleteErr != nil || sub.pendingRejectedCompleteCallID != "" {
		t.Fatalf("pending rejected completion not cleared after rejection: %q/%v", sub.pendingRejectedCompleteCallID, sub.pendingRejectedCompleteErr)
	}
	msgs := sub.ctxMgr.Snapshot()
	foundRejected := false
	for _, msg := range msgs {
		if msg.Role != "tool" || msg.ToolCallID != "call-1" {
			continue
		}
		foundRejected = strings.HasPrefix(msg.Content, "Completion rejected:") && strings.Contains(msg.Content, "result or result_ref requires result_type")
		break
	}
	if !foundRejected {
		t.Fatalf("missing rejected tool result in %#v", msgs)
	}
}

func TestSubAgentRepeatedInvalidCompleteFailsAfterRecoveryBudgetSpent(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	sub.turn.SubAgentCompletionRecoveryCount = 1
	sub.handleLLMResponse(&llmResult{turnID: 1, resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
		mustJSONToolCall(t, "call-1", "complete", map[string]any{"summary": "   "}),
	})}})

	select {
	case evt := <-parent.eventCh:
		if evt.Type != EventAgentError {
			t.Fatalf("event.Type = %q, want %q", evt.Type, EventAgentError)
		}
		if err, ok := evt.Payload.(error); !ok || !strings.Contains(err.Error(), "summary is required") {
			t.Fatalf("error payload = %#v, want rejected-after-retry error with the arg cause", evt.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for bounded recovery failure")
	}
}

func TestCompletionVerificationRecordsRoundTrip(t *testing.T) {
	original := &CompletionEnvelope{VerificationRun: []string{"go test ./..."}, VerificationRecords: []VerificationRecord{{ToolCallID: "call-1", Command: "go test ./...", Status: "failed", Summary: "exit 1"}}}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var restored CompletionEnvelope
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if len(restored.VerificationRecords) != 1 || restored.VerificationRecords[0] != original.VerificationRecords[0] {
		t.Fatalf("restored = %#v", restored.VerificationRecords)
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

func TestCompletionVerificationRejectsLatestFailureAfterEarlierPass(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	sub.verificationLedger = []verificationLedgerEntry{{ToolCallID: "old", Command: "go test ./internal/a", Status: "passed"}, {ToolCallID: "new", Command: "go test ./internal/a", Status: "failed"}}
	env := &CompletionEnvelope{VerificationRun: []string{"go test ./internal/a"}}
	err := sub.validateCompletionVerification(env)
	if err == nil || !strings.Contains(err.Error(), `status "failed"`) {
		t.Fatalf("validation error = %v, want latest failed status", err)
	}
	if len(env.VerificationRecords) != 0 {
		t.Fatalf("verification records = %#v, want none", env.VerificationRecords)
	}
}

func TestCompletionVerificationAcceptsOlderDeclarationWhenNewestCoversCurrentEpoch(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	sub.verificationLedger = []verificationLedgerEntry{
		{ToolCallID: "lint", Command: "golangci-lint run", Status: "passed", MutationEpoch: 1},
		{ToolCallID: "test", Command: "go test ./...", Status: "passed", MutationEpoch: 2},
	}
	sub.workspaceMutationEpoch = 2
	if err := sub.validateCompletionVerification(&CompletionEnvelope{VerificationRun: []string{"golangci-lint run", "go test ./..."}}); err != nil {
		t.Fatalf("multi-command verification rejected even though the newest command covers the current epoch: %v", err)
	}
}

func TestCompletionVerificationRejectsFailedCommand(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	sub.verificationLedger = []verificationLedgerEntry{{ToolCallID: "failed", Command: "go test ./internal/a", Status: "failed"}}
	err := sub.validateCompletionVerification(&CompletionEnvelope{VerificationRun: []string{"go test ./internal/a"}})
	if err == nil {
		t.Fatal("expected failed verification command to be rejected")
	}
	if !strings.Contains(err.Error(), `status "failed"`) {
		t.Fatalf("error = %q, want failed status", err)
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
	sub.tools.Register(tools.ShellTool{})
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

func TestCompletionVerificationAcceptsDeclaredCommandsWithUndeclaredEpochGap(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	sub.tools.Register(tools.ShellTool{})
	// modify → lint → build → test: build is a non-declared, non-read-only
	// command that advances the epoch between the two declared commands.
	// Declaring the honest superset [lint, test] must not fail on that gap.
	sub.recordTaskToolChanges(&toolResult{
		CallID: "edit", Name: tools.NameWrite, ArgsJSON: `{"path":"internal/a.go"}`,
		FileState: &message.ToolFileState{Writes: []message.TrackedFileState{{Path: "internal/a.go", Exists: true}}},
	}, false)
	for i, command := range []string{"golangci-lint run", "go build ./...", "go test ./..."} {
		result := &toolResult{CallID: fmt.Sprintf("cmd-%d", i), Name: tools.NameShell, ArgsJSON: fmt.Sprintf(`{"command":%q}`, command)}
		sub.recordTaskToolChanges(result, false)
		sub.recordVerificationToolResult(result, "ok", false)
	}
	env := &CompletionEnvelope{VerificationRun: []string{"golangci-lint run", "go test ./..."}}
	if err := sub.validateCompletionVerification(env); err != nil {
		t.Fatalf("honest declaration rejected on undeclared epoch gap: %v", err)
	}
	if len(env.VerificationRecords) != 2 {
		t.Fatalf("verification records = %#v, want two records", env.VerificationRecords)
	}
}

func TestCompletionVerificationIsolatedBetweenSubAgents(t *testing.T) {
	parent, first := newMixedBatchTestSubAgent(t)
	second := newControllableTestSubAgent(t, parent, "task-second")
	first.verificationLedger = []verificationLedgerEntry{{ToolCallID: "sibling-call", Command: "go test ./shared", Status: "passed"}}
	if err := second.validateCompletionVerification(&CompletionEnvelope{VerificationRun: []string{"go test ./shared"}}); err == nil {
		t.Fatal("expected sibling verification command to be rejected")
	}
}

func TestCompletionVerificationDoesNotReuseOldInstanceLedger(t *testing.T) {
	parent, old := newMixedBatchTestSubAgent(t)
	old.verificationLedger = []verificationLedgerEntry{{ToolCallID: "old-call", Command: "go test ./old", Status: "passed"}}
	newInstance := newControllableTestSubAgent(t, parent, old.taskID)
	if err := newInstance.validateCompletionVerification(&CompletionEnvelope{VerificationRun: []string{"go test ./old"}}); err == nil {
		t.Fatal("expected old instance verification command to be rejected")
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

// TestCompleteParametersSchemaEncodesResultPairing pins the structural form of
// the Complete argument schema: the result fields must form two anyOf groups —
// a summary-only completion that carries no result field, and a typed-result
// completion that pairs result_type with exactly one of result/result_ref — so
// the pairing constraint is visible to the model while it constructs
// arguments. The runtime validator in validateCompleteTypedResult stays as the
// fallback.
func TestCompleteParametersSchemaEncodesResultPairing(t *testing.T) {
	params := (tools.CompleteTool{}).Parameters()
	if got, ok := params["required"].([]string); !ok || !slices.Equal(got, []string{"summary"}) {
		t.Fatalf(`Parameters()["required"] = %#v, want ["summary"]`, params["required"])
	}
	groups, ok := params["anyOf"].([]map[string]any)
	if !ok || len(groups) != 2 {
		t.Fatalf(`Parameters()["anyOf"] = %#v, want the summary-only and typed-result groups`, params["anyOf"])
	}

	summaryGroup := groups[0]
	if got, ok := summaryGroup["required"].([]string); !ok || !slices.Equal(got, []string{"summary"}) {
		t.Fatalf("summary-only group required = %#v, want [summary]", summaryGroup["required"])
	}
	not, ok := summaryGroup["not"].(map[string]any)
	if !ok {
		t.Fatalf("summary-only group = %#v, want not to exclude every result field", summaryGroup)
	}
	forbidden, ok := not["anyOf"].([]map[string]any)
	if !ok || len(forbidden) != 3 {
		t.Fatalf(`summary-only group not["anyOf"] = %#v, want result_type/result/result_ref`, not["anyOf"])
	}
	for i, want := range [][]string{{"result_type"}, {"result"}, {"result_ref"}} {
		if got, ok := forbidden[i]["required"].([]string); !ok || !slices.Equal(got, want) {
			t.Fatalf("summary-only group not.anyOf[%d] required = %#v, want %v", i, forbidden[i]["required"], want)
		}
	}

	typedGroup := groups[1]
	if got, ok := typedGroup["required"].([]string); !ok || !slices.Equal(got, []string{"summary", "result_type"}) {
		t.Fatalf("typed-result group required = %#v, want [summary result_type]", typedGroup["required"])
	}
	pair, ok := typedGroup["anyOf"].([]map[string]any)
	if !ok || len(pair) != 2 {
		t.Fatalf(`typed-result group anyOf = %#v, want the result and result_ref alternatives`, typedGroup["anyOf"])
	}
	for i, want := range [][]string{{"result"}, {"result_ref"}} {
		if got, ok := pair[i]["required"].([]string); !ok || !slices.Equal(got, want) {
			t.Fatalf("typed-result group anyOf[%d] required = %#v, want %v", i, pair[i]["required"], want)
		}
	}
}
