package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestModelDrivenCheckpointWrapperNamesItsMode(t *testing.T) {
	content := buildCompactionCheckpointMessage("## Current User Request\n- x", nil, compactionSummaryModeModelDriven, nil)
	if !strings.Contains(content, "model-driven context checkpoint") {
		t.Fatalf("checkpoint wrapper missing model-driven copy:\n%s", content)
	}
	if !strings.Contains(content, "no summarization model was called") {
		t.Fatalf("checkpoint wrapper must state no summarization call:\n%s", content)
	}
}

func TestModelDrivenDraftCommittedArchiveSurvives(t *testing.T) {
	// The deferred archive cleanup only fires for failures: a successful
	// draft must keep its history file and meta so the apply step can use
	// them.
	dir := t.TempDir()
	a := newTestMainAgent(t, dir)
	a.newTurn("")

	snapshot := []message.Message{
		{Role: message.RoleUser, Content: "original user request"},
		{Role: message.RoleAssistant, Content: strings.Repeat("analysis ", 4000)},
		{Role: message.RoleUser, Content: "follow up"},
		{Role: message.RoleAssistant, Content: strings.Repeat("more analysis ", 4000)},
		{Role: message.RoleUser, Content: "another follow up"},
		{Role: message.RoleAssistant, Content: strings.Repeat("final analysis ", 4000)},
	}
	bundle := modelDrivenBarrierSnapshot{
		snapshot:                    snapshot,
		maxTokens:                   a.ctxMgr.GetMaxTokens(),
		sessionDir:                  a.sessionDir,
		prepareReducedRequest:       a.compactionReductionScratch().prepareMessagesForLLM,
		fixedRequestTokens:          1000,
		postResetFixedRequestTokens: 4000,
		archiveMeta:                 a.captureCompactionArchiveMeta(),
	}
	req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{ActiveObjective: "keep going", NextStep: "continue"}}

	draft, err := a.produceModelDrivenDraftAsync(context.Background(), bundle, 1, compactionTarget{}, 4, req)
	if err != nil {
		t.Fatalf("produceModelDrivenDraftAsync: %v", err)
	}
	if draft == nil || draft.AbsHistoryPath == "" || draft.AbsHistoryMetaPath == "" {
		t.Fatalf("draft = %+v, want committed history/meta paths", draft)
	}
	if _, statErr := os.Stat(draft.AbsHistoryPath); statErr != nil {
		t.Fatalf("history file missing after a successful draft: %v", statErr)
	}
	if _, statErr := os.Stat(draft.AbsHistoryMetaPath); statErr != nil {
		t.Fatalf("meta file missing after a successful draft: %v", statErr)
	}
}

func TestModelDrivenDraftPreExportCancellationLeavesNoFiles(t *testing.T) {
	// Cancelling before the archive is written must return the cancellation
	// with nothing on disk: the worker's first ctx check fires before export,
	// so no history/meta files are ever created.
	dir := t.TempDir()
	a := newTestMainAgent(t, dir)
	a.newTurn("")

	snapshot := []message.Message{
		{Role: message.RoleUser, Content: "original user request"},
		{Role: message.RoleAssistant, Content: strings.Repeat("analysis ", 4000)},
		{Role: message.RoleUser, Content: "follow up"},
	}
	bundle := modelDrivenBarrierSnapshot{
		snapshot:                    snapshot,
		maxTokens:                   a.ctxMgr.GetMaxTokens(),
		sessionDir:                  a.sessionDir,
		prepareReducedRequest:       a.compactionReductionScratch().prepareMessagesForLLM,
		fixedRequestTokens:          1000,
		postResetFixedRequestTokens: 4000,
		archiveMeta:                 a.captureCompactionArchiveMeta(),
	}
	req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{ActiveObjective: "keep going", NextStep: "continue"}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	draft, err := a.produceModelDrivenDraftAsync(ctx, bundle, 1, compactionTarget{}, 4, req)
	if err == nil {
		t.Fatalf("cancelled worker must return an error, got draft %+v", draft)
	}
	entries, _ := os.ReadDir(a.sessionDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "history-") {
			t.Fatalf("history file left by a pre-export cancellation: %s", e.Name())
		}
	}
}

func TestModelDrivenCheckpointRequestIDUsesToolCallID(t *testing.T) {
	a := &MainAgent{}
	a.armModelDrivenProposal("call-42", tools.CompactContextArgs{ActiveObjective: "x"}, `{"active_objective":"x"}`, "accepted by runtime validation")
	if got := a.modelDrivenProposal.requestID; got != "call-42" {
		t.Fatalf("proposal request ID = %q, want call-42", got)
	}
	if a.modelDrivenProposal.status != modelDrivenProposalAccepted || a.modelDrivenProposal.reason != "accepted by runtime validation" {
		t.Fatalf("proposal metadata = status %q reason %q", a.modelDrivenProposal.status, a.modelDrivenProposal.reason)
	}
	// The identity survives past the terminal settle (a settle can arrive
	// after the armed request was consumed by the barrier), so diagnostics can
	// still correlate back to the request that produced the attempt.
	a.transitionModelDrivenProposal(CompactionStatusSkipped, "projected savings too small")
	if got := a.modelDrivenProposal.requestID; got != "call-42" {
		t.Fatalf("settled proposal request ID = %q, want call-42", got)
	}
}

func TestApplyModelDrivenDraftRejectsChangedRuntimeGeneration(t *testing.T) {
	a := &MainAgent{}
	a.ctxMgr = ctxmgr.NewManager(10000, 10000)
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "request", RequestBatch: 2})
	err := a.applyCompactionDraftAsync(&compactionDraft{
		SummaryMode:       compactionSummaryModeModelDriven,
		RuntimeGeneration: 1,
		HeadSplit:         1,
		NewMessages:       []message.Message{{Role: message.RoleUser, Content: "summary"}},
	})
	if err == nil || !strings.Contains(err.Error(), "runtime generation changed") {
		t.Fatalf("stale model-driven draft error = %v", err)
	}
	for _, want := range []string{"new input was received after the checkpoint was prepared", "process that input first, then retry compact_context"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("generation rejection %q must guide the retry with %q", err, want)
		}
	}
}

func TestApplyModelDrivenDraftKeepsQueuedUserInputCurrent(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn("")
	a.requestBatches.reserve(a.sessionEpoch, 0)

	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "implement the parser contract"})
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, Content: "parser implementation in progress"})
	// Barrier-time capture: the user has not typed yet.
	bundle := a.captureModelDrivenBarrierSnapshot(a.ctxMgr.Snapshot())
	// User input arrives while the checkpoint draft is pending. It is merged
	// into the continuation request after the apply, never into the checkpoint,
	// so it must not void the apply: rejecting the draft here would push the
	// model into an unbounded prepare/void retry loop on every typed interrupt.
	a.pendingUserMessages = []pendingUserMessage{{Content: "queued follow-up", FromUser: true}}

	draft := &compactionDraft{
		SummaryMode:             compactionSummaryModeModelDriven,
		RuntimeGeneration:       bundle.currentRequestBatch,
		RuntimeStateFingerprint: bundle.runtimeStateFingerprint,
		HeadSplit:               1,
		Index:                   1,
		AbsHistoryPath:          filepath.Join(a.sessionDir, "history-1.md"),
		NewMessages:             []message.Message{{Role: message.RoleUser, Content: "summary checkpoint", IsCompactionSummary: true}},
		PlanID:                  1,
		Target:                  compactionTarget{sessionEpoch: a.sessionEpoch},
	}
	if err := a.applyCompactionDraft(draft); err != nil {
		t.Fatalf("queued user input alone must not void the draft: %v", err)
	}
	snapshot := a.ctxMgr.Snapshot()
	if len(snapshot) == 0 || !snapshot[0].IsCompactionSummary {
		t.Fatalf("checkpoint must be the transcript head after the apply, got %d messages", len(snapshot))
	}
	if len(a.pendingUserMessages) != 1 || a.pendingUserMessages[0].Content != "queued follow-up" {
		t.Fatalf("queued user messages must stay queued after the apply, got %d", len(a.pendingUserMessages))
	}
}

func TestApplyModelDrivenDraftGuidanceNamesNewStateOnStale(t *testing.T) {
	a := &MainAgent{}
	a.ctxMgr = ctxmgr.NewManager(10000, 10000)
	a.sessionDir = t.TempDir()
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "request", RequestBatch: 1})
	bundle := a.captureModelDrivenBarrierSnapshot(a.ctxMgr.Snapshot())
	// A genuine runtime-state change between the barrier capture and the apply
	// (here: a new todo item) must still void the draft — but the rejection has
	// to tell the model what happened and how to proceed instead of silently
	// dropping the attempt.
	a.todoMu.Lock()
	a.todoItems = append(a.todoItems, tools.TodoItem{ID: "t1", Content: "new work"})
	a.todoMu.Unlock()
	err := a.applyCompactionDraftAsync(&compactionDraft{
		SummaryMode:             compactionSummaryModeModelDriven,
		RuntimeGeneration:       1,
		RuntimeStateFingerprint: bundle.runtimeStateFingerprint,
		HeadSplit:               1,
		NewMessages:             []message.Message{{Role: message.RoleUser, Content: "summary"}},
	})
	if err == nil {
		t.Fatal("changed runtime state must still void the draft")
	}
	for _, want := range []string{"runtime state fingerprint changed", "process the new state first, then retry compact_context"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("stale rejection %q must guide the retry with %q", err, want)
		}
	}
}
