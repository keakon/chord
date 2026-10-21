package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
	"github.com/keakon/chord/internal/tools"
)

func TestHandleCompactionReadyRechecksGateAfterQueuedInput(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr.SetMaxTokens(1024)
	a.newTurn("")
	if a.turn == nil {
		t.Fatal("expected active turn")
	}
	turnID := a.turn.ID
	a.startCompactionState(1, compactionTarget{turnID: turnID, turnEpoch: a.turn.Epoch, sessionEpoch: a.sessionEpoch}, compactionTriggerUsageDriven, continuationPlan{kind: compactionResumeMainLLM, turnID: turnID, turnEpoch: a.turn.Epoch, agentErrSourceID: "main"})
	a.pendingUserMessages = []pendingUserMessage{{Content: strings.Repeat("queued user message ", 220)}}

	draft := &compactionDraft{
		NewMessages:    []message.Message{{Role: "user", Content: "[Context Summary]\nsummary"}},
		Index:          1,
		AbsHistoryPath: "/tmp/history-1.md",
		SummaryMode:    "truncate_only",
		ModelRef:       "fallback",
		Manual:         false,
		ArchivedCount:  2,
	}
	a.handleCompactionReady(Event{Type: EventCompactionReady, TurnID: turnID, Payload: draft})

	if !a.IsCompactionRunning() {
		t.Fatal("compaction state should be running after gate re-check")
	}
	pending := a.currentCompactionPendingCall()
	if pending == nil || pending.turnID != turnID {
		t.Fatalf("pending continuation = %+v, want turn %d", pending, turnID)
	}
	if got := len(a.pendingUserMessages); got != 1 {
		t.Fatalf("len(pendingUserMessages) = %d, want 1 queued user message until next resume", got)
	}
	msgs := a.ctxMgr.Snapshot()
	if got := len(msgs); got != 0 {
		t.Fatalf("len(snapshot) = %d, want 0 because stale draft should be ignored", got)
	}
}

func TestHandleCompactCommandSchedulesAsyncCompaction(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr.Append(message.Message{Role: "user", Content: "one"})
	a.ctxMgr.Append(message.Message{Role: "assistant", Content: "two"})
	a.ctxMgr.Append(message.Message{Role: "user", Content: "three"})
	a.ctxMgr.Append(message.Message{Role: "assistant", Content: "four"})

	a.handleCompactCommand()
	if !a.IsCompactionRunning() {
		t.Fatal("compaction state should be running after /compact scheduling")
	}
	pending := a.currentCompactionPendingCall()
	if pending == nil {
		t.Fatal("pending continuation should be set for async manual compaction")
	}
	if pending.turnID != 0 {
		t.Fatalf("pending.turnID = %d, want 0 for idle manual compaction", pending.turnID)
	}
	if pending.continuation != compactionResumeIdle {
		t.Fatalf("pending continuation = %q, want %q", pending.continuation, compactionResumeIdle)
	}
}

func TestHandleCompactCommandSchedulesAsyncCompactionWhileBusy(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr.Append(message.Message{Role: "user", Content: "one"})
	a.ctxMgr.Append(message.Message{Role: "assistant", Content: "two"})
	a.ctxMgr.Append(message.Message{Role: "user", Content: "three"})
	a.ctxMgr.Append(message.Message{Role: "assistant", Content: "four"})
	a.newTurn("")
	turnID := a.turn.ID
	turnEpoch := a.turn.Epoch

	a.handleCompactCommand()
	if !a.IsCompactionRunning() {
		t.Fatal("compaction state should be running after busy /compact scheduling")
	}
	if a.turn == nil || a.turn.ID != turnID {
		t.Fatal("busy /compact should not clear the active turn")
	}
	pending := a.currentCompactionPendingCall()
	if pending == nil {
		t.Fatal("pending continuation should be set for busy manual compaction")
	}
	if pending.turnID != 0 {
		t.Fatalf("pending.turnID = %d, want 0 for manual idle continuation", pending.turnID)
	}
	if pending.turnEpoch != turnEpoch {
		t.Fatalf("pending.turnEpoch = %d, want active epoch %d", pending.turnEpoch, turnEpoch)
	}
	if pending.continuation != compactionResumeIdle {
		t.Fatalf("pending continuation = %q, want %q", pending.continuation, compactionResumeIdle)
	}
}

func TestScheduleCompactionSkipsWhenAlreadyRunning(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.startCompactionState(7, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTriggerUsageDriven, continuationPlan{kind: compactionResumeIdle})

	if ok := a.scheduleCompaction(false); ok {
		t.Fatal("scheduleCompaction should return false while compaction is already running")
	}
	pending := a.currentCompactionPendingCall()
	if pending == nil {
		t.Fatal("expected existing compaction pending state to remain")
	}
	if pending.planID != 7 {
		t.Fatalf("pending.planID = %d, want 7", pending.planID)
	}
}

func TestHistoryMutationAllowedOutsideCompaction(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	if err := a.historyMutationAllowed(0); err != nil {
		t.Fatalf("historyMutationAllowed outside compaction returned error: %v", err)
	}
}

func TestHistoryMutationAllowedRejectsFrozenHead(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.startCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTriggerManual, continuationPlan{kind: compactionResumeIdle})
	a.compactionState.headSplit = 3
	if err := a.historyMutationAllowed(2); err == nil {
		t.Fatal("historyMutationAllowed on frozen head = nil, want error")
	}
}

func TestHistoryMutationAllowedAllowsTail(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.startCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTriggerManual, continuationPlan{kind: compactionResumeIdle})
	a.compactionState.headSplit = 3
	if err := a.historyMutationAllowed(3); err != nil {
		t.Fatalf("historyMutationAllowed on tail returned error: %v", err)
	}
}

func TestHandleCompactionReadyAsyncIdleAppliesImmediately(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr.Append(message.Message{Role: "user", Content: "one"})
	a.ctxMgr.Append(message.Message{Role: "assistant", Content: "two"})
	a.startCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTriggerManual, continuationPlan{kind: compactionResumeIdle})
	a.compactionState.headSplit = 2

	draft := &compactionDraft{
		NewMessages:    []message.Message{{Role: "user", Content: "[Context Summary]", IsCompactionSummary: true}},
		HeadSplit:      2,
		Index:          1,
		AbsHistoryPath: filepath.Join(a.sessionDir, "history-1.md"),
		SummaryMode:    "truncate_only",
		PlanID:         1,
		Target:         compactionTarget{sessionEpoch: a.sessionEpoch},
		Manual:         true,
	}

	a.handleCompactionReady(Event{Type: EventCompactionReady, Payload: draft})

	if a.compactionState.readyDraft != nil {
		t.Fatal("readyDraft should be nil after idle async compaction applies immediately")
	}
	if a.compactionState.running {
		t.Fatal("compactionState.running should be false after idle async compaction apply")
	}
	msgs := a.ctxMgr.Snapshot()
	if len(msgs) != 1 || !msgs[0].IsCompactionSummary {
		t.Fatalf("messages after apply = %#v, want single compaction summary", msgs)
	}
}

func TestApplyCompactionDraftInvalidatesLLMRouting(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"test-key"})
	provider := &countingCompactionProvider{response: &message.Response{Content: "ok", StopReason: "stop"}}
	a.llmClient = llm.NewClient(providerCfg, provider, "model", 1024, "")
	a.ctxMgr.Append(message.Message{Role: "user", Content: "one"})
	a.ctxMgr.Append(message.Message{Role: "assistant", Content: "two"})
	a.ctxMgr.Append(message.Message{Role: "user", Content: "three"})

	draft := &compactionDraft{
		NewMessages:    []message.Message{{Role: "user", Content: "[Context Summary]", IsCompactionSummary: true}},
		HeadSplit:      2,
		Index:          1,
		AbsHistoryPath: filepath.Join(a.sessionDir, "history-1.md"),
		SummaryMode:    "truncate_only",
		PlanID:         1,
		Target:         compactionTarget{sessionEpoch: a.sessionEpoch},
		Manual:         true,
	}

	if err := a.applyCompactionDraft(draft); err != nil {
		t.Fatalf("applyCompactionDraft: %v", err)
	}
	if len(provider.invalidations) != 1 || provider.invalidations[0] != "context_compacted" {
		t.Fatalf("invalidations = %#v, want [context_compacted]", provider.invalidations)
	}
}

func TestApplyCompactionDraftOnlyRequestsPortableReplayForNativePayload(t *testing.T) {
	t.Parallel()
	portableReplay := modelcompat.ReplayCompatSynthesized
	for _, tc := range []struct {
		name             string
		newMessages      []message.Message
		wantReplayCompat *int
	}{
		{
			name: "plain text stays native",
			newMessages: []message.Message{{
				Role:                message.RoleUser,
				Content:             "[Context Summary]",
				IsCompactionSummary: true,
			}},
		},
		{
			name: "native reasoning becomes portable",
			newMessages: []message.Message{
				{Role: message.RoleUser, Content: "[Context Summary]", IsCompactionSummary: true},
				{
					Role:             message.RoleAssistant,
					Content:          "answer",
					ReasoningContent: "provider-native reasoning",
					Provenance:       &message.MessageProvenance{ProviderID: "test", ModelID: "model", WireFamily: modelcompat.WireFamilyAnthropic},
				},
			},
			wantReplayCompat: &portableReplay,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projectRoot := t.TempDir()
			a := newTestMainAgent(t, projectRoot)
			providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
				Type: config.ProviderTypeMessages,
				Models: map[string]config.ModelConfig{
					"model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
				},
			}, []string{"test-key"})
			provider := &countingCompactionProvider{response: &message.Response{Content: "ok", StopReason: "stop"}}
			a.llmClient = llm.NewClient(providerCfg, provider, "model", 1024, "")
			a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "old user"})
			a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, Content: "old assistant"})

			draft := &compactionDraft{
				NewMessages:    tc.newMessages,
				HeadSplit:      2,
				Index:          1,
				AbsHistoryPath: filepath.Join(a.sessionDir, "history-1.md"),
				SummaryMode:    "truncate_only",
				PlanID:         1,
				Target:         compactionTarget{sessionEpoch: a.sessionEpoch},
				Manual:         true,
			}
			if err := a.applyCompactionDraft(draft); err != nil {
				t.Fatalf("applyCompactionDraft: %v", err)
			}
			if _, err := a.llmClient.CompleteStream(context.Background(), a.ctxMgr.Snapshot(), nil, nil); err != nil {
				t.Fatalf("CompleteStream: %v", err)
			}
			if len(provider.tunings) != 1 {
				t.Fatalf("tunings = %+v, want one request", provider.tunings)
			}
			got := provider.tunings[0].ReplayCompat
			if tc.wantReplayCompat == nil {
				if got != nil {
					t.Fatalf("ReplayCompat = %v, want native/unset", *got)
				}
				return
			}
			if got == nil || *got != *tc.wantReplayCompat {
				t.Fatalf("ReplayCompat = %v, want %d", got, *tc.wantReplayCompat)
			}
		})
	}
}

func TestApplyReadyDraftClearsRunningState(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.startCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTriggerManual, continuationPlan{kind: compactionResumeIdle})
	a.compactionState.headSplit = 2
	a.compactionState.readyDraft = &compactionDraft{
		NewMessages:    []message.Message{{Role: "user", Content: "[Context Summary]", IsCompactionSummary: true}},
		HeadSplit:      2,
		Index:          1,
		AbsHistoryPath: filepath.Join(a.sessionDir, "history-1.md"),
		SummaryMode:    "truncate_only",
		PlanID:         1,
		Target:         compactionTarget{sessionEpoch: a.sessionEpoch},
		Manual:         true,
	}

	if ok, _ := a.applyReadyDraft(); !ok {
		t.Fatal("applyReadyDraft() = false, want true")
	}
	if a.compactionState.running {
		t.Fatal("compactionState.running should be false after applyReadyDraft")
	}
	if a.IsCompactionRunning() {
		t.Fatal("IsCompactionRunning() should be false after applyReadyDraft")
	}
}

func TestHandleCompactionReadyEmitsIdleActivityOnCompletion(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.startCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTriggerManual, continuationPlan{kind: compactionResumeIdle})
	draft := &compactionDraft{Skip: true, PlanID: 1, Target: compactionTarget{sessionEpoch: a.sessionEpoch}}

	a.handleCompactionReady(Event{Type: EventCompactionReady, Payload: draft})

	events := drainAgentEvents(a.Events())
	foundIdle := false
	foundSkipped := false
	for _, evt := range events {
		act, ok := evt.(AgentActivityEvent)
		if ok && act.AgentID == "main" && act.Type == ActivityIdle {
			foundIdle = true
		}
		status, ok := evt.(CompactionStatusEvent)
		if ok && status.Status == CompactionStatusSkipped {
			foundSkipped = true
		}
	}
	if !foundIdle {
		t.Fatalf("expected ActivityIdle after compaction ready, got %#v", events)
	}
	if !foundSkipped {
		t.Fatalf("expected skipped compaction status after no-op compaction, got %#v", events)
	}
}

// A ready draft parked for the continuation barrier must tell the TUI pill:
// the summarize work is done, so the pill freezes its elapsed and shows the
// ready state instead of counting the foreground request's remaining wait as
// compaction time.
func TestHandleCompactionReadyParkedDraftEmitsReadyStatus(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.startCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTriggerManual, continuationPlan{kind: compactionResumeIdle})
	a.compactionState.headSplit = 2
	a.newTurn("")
	if a.turn == nil {
		t.Fatal("expected active turn")
	}
	draft := &compactionDraft{
		NewMessages: []message.Message{{Role: "user", Content: "[Context Summary]", IsCompactionSummary: true}},
		HeadSplit:   2,
		PlanID:      1,
		Target:      compactionTarget{sessionEpoch: a.sessionEpoch},
		Manual:      true,
	}

	a.handleCompactionReady(Event{Type: EventCompactionReady, Payload: draft})

	if a.compactionState.readyDraft == nil {
		t.Fatal("an active turn must park the ready draft for the continuation barrier")
	}
	events := drainAgentEvents(a.Events())
	foundReady := false
	for _, evt := range events {
		if status, ok := evt.(CompactionStatusEvent); ok && status.Status == CompactionStatusReady {
			foundReady = true
			break
		}
	}
	if !foundReady {
		t.Fatalf("a parked draft must emit a ready compaction status, got %#v", events)
	}
}

func TestHandleCompactionFailedEmitsIdleActivityOnCompletion(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.startCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTriggerManual, continuationPlan{kind: compactionResumeIdle})

	a.handleCompactionFailed(Event{Type: EventCompactionFailed, Payload: &compactionFailure{planID: 1, target: compactionTarget{sessionEpoch: a.sessionEpoch}, err: fmt.Errorf("temporary compaction failure")}})

	events := drainAgentEvents(a.Events())
	foundIdle := false
	for _, evt := range events {
		act, ok := evt.(AgentActivityEvent)
		if ok && act.AgentID == "main" && act.Type == ActivityIdle {
			foundIdle = true
			break
		}
	}
	if !foundIdle {
		t.Fatalf("expected ActivityIdle after compaction failed, got %#v", events)
	}
}

func TestAutoCompactionDrainsPendingUserMessages(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr.SetMaxTokens(1024)

	// Add pending user message before auto compaction
	a.pendingUserMessages = []pendingUserMessage{{Content: "continue the task"}}

	// Start auto compaction (compactionResumeIdle means it's not during an LLM call)
	a.startCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTriggerUsageDriven, continuationPlan{kind: compactionResumeIdle})

	// Create a valid compaction draft
	draft := &compactionDraft{
		NewMessages:    []message.Message{{Role: "user", Content: "[Context Summary]\n## Goal\n- continue\n\n[Context compressed]", IsCompactionSummary: true}},
		Index:          1,
		AbsHistoryPath: filepath.Join(a.sessionDir, "history-1.md"),
		SummaryMode:    "truncate_only",
		PlanID:         1,
		Target:         compactionTarget{sessionEpoch: a.sessionEpoch},
	}

	a.handleCompactionReady(Event{Type: EventCompactionReady, Payload: draft})

	// Verify pending user messages were drained (a new turn should have started)
	if len(a.pendingUserMessages) != 0 {
		t.Fatalf("expected pendingUserMessages to be drained, got %d", len(a.pendingUserMessages))
	}
	if a.turn == nil {
		t.Fatal("expected a new turn to be created from pending user message")
	}
}

// A watchdog failure that loses the race against a completed draft parked at
// the continuation barrier must not discard that draft: the worker sends one
// terminal event per plan, so a same-plan failure after Ready can only be the
// watchdog's synthetic timeout.
func TestHandleCompactionFailedKeepsParkedReadyDraft(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.beginCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTriggerManual, continuationPlan{kind: compactionResumeIdle}, 1, nil)
	a.compactionState.readyDraft = &compactionDraft{PlanID: 1, Target: compactionTarget{sessionEpoch: a.sessionEpoch}}

	a.handleCompactionFailed(Event{Type: EventCompactionFailed, Payload: &compactionFailure{
		planID: 1,
		target: compactionTarget{sessionEpoch: a.sessionEpoch},
		err:    errCompactionWatchdog,
	}})

	if a.compactionState.readyDraft == nil {
		t.Fatal("watchdog failure discarded the parked ready draft")
	}
	if !a.IsCompactionRunning() {
		t.Fatal("compaction state must stay armed until the barrier applies the draft")
	}
}

func TestJobFinishedHandledImmediatelyDuringCompaction(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.startCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTriggerManual, continuationPlan{kind: compactionResumeIdle})
	jobID := t.Name() + "-1"
	payload := &tools.JobFinishedPayload{BackgroundID: jobID, AgentID: a.instanceID, Status: "completed (exit code 0)", Message: "[Background job " + jobID + " finished]\n\nStatus: completed (exit code 0)"}
	a.dispatch(Event{Type: EventJobFinished, SourceID: "main", Payload: payload})

	// Events are no longer queued behind compaction.
	// With no active turn, job-finished starts a new turn immediately.
	if a.turn == nil {
		t.Fatal("expected job-finished to start a turn immediately during compaction")
	}
	if got := len(a.pendingUserMessages); got != 0 {
		t.Fatalf("len(pendingUserMessages) = %d, want 0", got)
	}
}

func TestApplyReadyDraftAutoContinueFailureEmitsSingleIdleEvent(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.startCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTriggerUsageDriven, continuationPlan{kind: compactionResumeAutoContinue})
	a.compactionState.headSplit = 1
	a.compactionState.readyDraft = &compactionDraft{
		NewMessages:    []message.Message{{Role: "user", Content: "summary", IsCompactionSummary: true}},
		HeadSplit:      1,
		Index:          1,
		AbsHistoryPath: filepath.Join(a.sessionDir, "history-1.md"),
		SummaryMode:    "truncate_only",
		PlanID:         1,
		Target:         compactionTarget{sessionEpoch: a.sessionEpoch},
	}
	// Force applyCompactionDraft to fail by making the backup path a directory,
	// so rewriteSessionAfterCompaction cannot rename main.jsonl over it.
	if err := a.recoveryManager().PersistMessage("main", message.Message{Role: "user", Content: "before compaction"}); err != nil {
		t.Fatalf("PersistMessage before compaction: %v", err)
	}
	backupPath := filepath.Join(a.sessionDir, "main.pre-compress-1.jsonl")
	if err := os.MkdirAll(backupPath, 0o755); err != nil {
		t.Fatalf("MkdirAll(backupPath): %v", err)
	}

	a.setIdleAndDrainPending()

	idleCount := 0
	for len(a.outputCh) > 0 {
		evt := <-a.outputCh
		if _, ok := evt.(IdleEvent); ok {
			idleCount++
		}
	}
	if idleCount != 1 {
		t.Fatalf("IdleEvent count = %d, want 1", idleCount)
	}
	if a.turn != nil {
		t.Fatal("expected no active turn after failed auto-continue barrier apply")
	}
}
