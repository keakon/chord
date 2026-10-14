package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
)

func TestModelDrivenSkipSettlesWithoutClearingUsageState(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn("")
	a.autoCompactRequested.Store(true)
	a.ctxMgr.RestoreStats(message.TokenUsage{InputTokens: 1000})
	a.modelDrivenSkipNotice = ""
	a.settleModelDrivenSkip(&compactionDraft{Skip: true, InfoMessage: "projected savings too small"})
	if a.modelDrivenProposal.status != string(CompactionStatusSkipped) {
		t.Fatalf("checkpoint status after skip = %q", a.modelDrivenProposal.status)
	}
	if !a.autoCompactRequested.Load() {
		t.Fatal("model-driven skip must NOT clear autoCompactRequested")
	}
	if got := a.ctxMgr.GetStats().InputTokens; got != 1000 {
		t.Fatalf("model-driven skip must NOT clear LastTokenUsage, got %d", got)
	}
	if a.modelDrivenSkipNotice == "" {
		t.Fatal("skip notice should be stored for the continuation")
	}
	a.appendModelDrivenContinuationNotice()
	if a.modelDrivenSkipNotice != "" {
		t.Fatal("skip notice should be consumed after surfacing")
	}
	if a.pendingModelDrivenNotice == "" {
		t.Fatal("continuation notice should be queued as a transient overlay")
	}
}

func TestModelDrivenSettleRecordsLifecycleAndTerminalTrigger(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn("")
	a.beginCompactionState(7, compactionTarget{turnID: 1, turnEpoch: 1, sessionEpoch: a.sessionEpoch}, compactionTriggerModelDriven, continuationPlan{kind: compactionResumeModelDriven, turnID: 1}, 4, nil)

	preflight := &modelDrivenPreflightStats{CurrentTokens: 8000, ProjectedTokens: 5000, SavedTokens: 3000, SavedRatioPct: 37, ContinuationTokens: 90}
	a.settleModelDrivenSkip(&compactionDraft{Skip: true, InfoMessage: "Context checkpoint skipped: projected savings too small", ModelDrivenPreflight: preflight})

	// Lifecycle analytics: one skipped/model_driven event with the preflight
	// fields (P2-2 pilot metrics).
	stats := a.usageTracker.SessionStats()
	if stats.CompactionLifecycle["skipped/model_driven"] != 1 {
		t.Fatalf("skipped/model_driven count = %d, want 1; got %+v", stats.CompactionLifecycle["skipped/model_driven"], stats.CompactionLifecycle)
	}

	// Terminal event trigger comes from the active compaction state (P1-8).
	evt := a.compactionStatusEvent(CompactionStatusFailed, "boom")
	if evt.Trigger != string(compactionTriggerModelDriven) || evt.Reason != "boom" {
		t.Fatalf("terminal event = %+v, want trigger model_driven + reason boom", evt)
	}

	// Failure and cancel settlements also settle exactly once with the trigger.
	a.settleModelDrivenFailure(errCompactionWatchdog)
	if a.modelDrivenProposal.status != string(CompactionStatusFailed) {
		t.Fatalf("checkpoint status after failure = %q", a.modelDrivenProposal.status)
	}
	stats = a.usageTracker.SessionStats()
	if stats.CompactionLifecycle["failed/model_driven"] != 1 {
		t.Fatalf("failed/model_driven count = %d, want 1", stats.CompactionLifecycle["failed/model_driven"])
	}
	a.settleModelDrivenCancelled("cancelled by the user")
	if a.modelDrivenProposal.status != string(CompactionStatusCancelled) {
		t.Fatalf("checkpoint status after cancellation = %q", a.modelDrivenProposal.status)
	}
	stats = a.usageTracker.SessionStats()
	if stats.CompactionLifecycle["cancelled/model_driven"] != 1 {
		t.Fatalf("cancelled/model_driven count = %d, want 1", stats.CompactionLifecycle["cancelled/model_driven"])
	}
}

func TestModelDrivenSkipKeepsUsageDrivenSafetyNetArmed(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr.SetMaxTokens(1024)
	a.newTurn("")
	turnID := a.turn.ID
	a.autoCompactRequested.Store(true)
	target := compactionTarget{turnID: turnID, turnEpoch: a.turn.Epoch, sessionEpoch: a.sessionEpoch}
	a.startCompactionState(3, target, compactionTriggerModelDriven, continuationPlan{kind: compactionResumeModelDriven, turnID: turnID, turnEpoch: a.turn.Epoch, agentErrSourceID: "main"})
	a.modelDrivenSkipNotice = ""
	draft := &compactionDraft{
		Skip:        true,
		InfoMessage: "Context checkpoint skipped: projected savings too small",
		PlanID:      3,
		Target:      target,
	}
	prepareMainRequestDirectoryFixture(t, a)
	a.handleCompactionReady(Event{Type: EventCompactionReady, TurnID: turnID, Payload: draft})

	// The skip settles without clearing the usage-driven request...
	if !a.autoCompactRequested.Load() {
		t.Fatal("model-driven skip must keep autoCompactRequested armed")
	}
	if a.pendingModelDrivenNotice == "" {
		t.Fatal("skip resume must queue a transient continuation notice")
	}
	// ...and the resume re-enters beginMainLLMAfterPreparation, so the
	// usage-driven gate runs on the old context and starts a real
	// usage-driven compaction instead of spawning the next request directly.
	if !a.IsCompactionRunning() {
		t.Fatal("usage-driven compaction should have started after the skip resume")
	}
	if a.compactionState.trigger != compactionTriggerUsageDriven {
		t.Fatalf("trigger after skip resume = %q, want usage_driven", a.compactionState.trigger)
	}
}

func TestModelDrivenApplyFailureSurfacesRealReason(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr.SetMaxTokens(1024)
	a.ctxMgr.Append(message.Message{Role: "user", Content: "head message being compacted"})
	a.newTurn("")
	turnID := a.turn.ID
	target := compactionTarget{turnID: turnID, turnEpoch: a.turn.Epoch, sessionEpoch: a.sessionEpoch}
	a.startCompactionState(5, target, compactionTriggerModelDriven, continuationPlan{kind: compactionResumeModelDriven, turnID: turnID, turnEpoch: a.turn.Epoch, agentErrSourceID: "main"})
	a.modelDrivenSkipNotice = ""

	// A draft whose apply must fail the provenance gate: the source refs claim
	// the head is a tool message, but it is a user message.
	draft := &compactionDraft{
		NewMessages:        []message.Message{{Role: "user", Content: "[Context Summary]\ncheckpoint"}},
		Index:              1,
		HeadSplit:          1,
		SourceRefs:         []checkpointSourceRef{{Ordinal: 0, Role: "tool"}},
		SourceFingerprint:  "forged",
		AbsHistoryPath:     filepath.Join(projectRoot, "history-1.md"),
		AbsHistoryMetaPath: filepath.Join(projectRoot, "history-1.md.meta"),
		SummaryMode:        compactionSummaryModeModelDriven,
		ModelRef:           "model_declared",
		PlanID:             5,
		Target:             target,
	}
	a.handleCompactionReady(Event{Type: EventCompactionReady, TurnID: turnID, Payload: draft})

	// The failure reason is consumed by the resume path into the transient
	// continuation notice; it must not be the low-gain default.
	notice := a.pendingModelDrivenNotice
	if a.modelDrivenSkipNotice != "" {
		notice = a.modelDrivenSkipNotice
	}
	if notice == "" {
		t.Fatal("apply failure must surface a model-driven reason")
	}
	if strings.Contains(notice, "projected savings") {
		t.Fatalf("apply failure misreported as low-gain skip: %q", notice)
	}
	if !strings.Contains(notice, "provenance") {
		t.Fatalf("apply failure reason = %q, want provenance failure wording", notice)
	}
}

func TestCancelCompactionForTurnCancellationDiscardsModelDrivenWorker(t *testing.T) {
	t.Parallel()
	// P1-1: cancelling the requesting turn must stop an in-flight model-driven
	// checkpoint so a late ready draft can never rewrite history for abandoned
	// work. The worker context is parented on parentCtx, not the turn context,
	// so without explicit cancellation it would keep running past ESC.
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn("")
	turnID := a.turn.ID
	workerCtx, cancel := context.WithCancel(context.Background())
	a.beginCompactionState(
		5,
		compactionTarget{turnID: turnID, turnEpoch: 1, sessionEpoch: a.sessionEpoch},
		compactionTriggerModelDriven,
		continuationPlan{kind: compactionResumeModelDriven, turnID: turnID, turnEpoch: 1},
		2,
		cancel,
	)

	a.cancelCompactionForTurnCancellation(turnID)

	if workerCtx.Err() != context.Canceled {
		t.Fatalf("model-driven worker context was not cancelled: %v", workerCtx.Err())
	}
	if !a.compactionState.discard {
		t.Fatal("compaction state must be marked discard so a late ready draft cannot apply")
	}
	// The running worker settles on its own cancellation failure event (the
	// discard flag makes finishCompactionState drop the pending continuation);
	// simulate that event now to verify the state settles.
	a.handleCompactionFailed(Event{
		Type: EventCompactionFailed,
		Payload: &compactionFailure{
			planID: 5,
			target: compactionTarget{turnID: turnID, turnEpoch: 1, sessionEpoch: a.sessionEpoch},
			err:    context.Canceled,
		},
	})
	if a.IsCompactionRunning() {
		t.Fatal("compaction must no longer be running after the worker failure settles")
	}

	// Cancelling a different turn's checkpoint (e.g. a usage-driven compaction
	// from an earlier turn) must be a no-op.
	a2 := newTestMainAgent(t, t.TempDir())
	a2.newTurn("")
	otherCtx, otherCancel := context.WithCancel(context.Background())
	a2.beginCompactionState(
		6,
		compactionTarget{turnID: turnID, turnEpoch: 1, sessionEpoch: a2.sessionEpoch},
		compactionTriggerModelDriven,
		continuationPlan{kind: compactionResumeModelDriven, turnID: turnID, turnEpoch: 1},
		2,
		otherCancel,
	)
	a2.cancelCompactionForTurnCancellation(999)
	if otherCtx.Err() != nil {
		t.Fatal("cancelling an unrelated turn must not cancel the compaction worker")
	}
	if a2.compactionState.discard {
		t.Fatal("cancelling an unrelated turn must not mark the compaction discard")
	}
}

func TestCancelCompactionForTurnCancellationSettlesParkedDraftWithLiveTrigger(t *testing.T) {
	t.Parallel()
	// A model-driven draft already parked at the continuation barrier belongs
	// to the cancelled turn. Cancelling the turn must settle it while the
	// compaction state is still live — the terminal status event carries the
	// model_driven trigger and plan id only when it is built before the
	// reset — then clean its orphan history files and reset the state.
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn("")
	turnID := a.turn.ID
	history := filepath.Join(projectRoot, "history-7.md")
	if err := os.WriteFile(history, []byte("parked archive body"), 0o644); err != nil {
		t.Fatalf("write parked history archive: %v", err)
	}
	a.beginCompactionState(
		7,
		compactionTarget{turnID: turnID, turnEpoch: 1, sessionEpoch: a.sessionEpoch},
		compactionTriggerModelDriven,
		continuationPlan{kind: compactionResumeModelDriven, turnID: turnID, turnEpoch: 1},
		2,
		nil,
	)
	// Park the ready draft at the barrier like the model-driven apply path in
	// handleCompactionReady does once the worker reaches its terminal event.
	a.compactionState.readyDraft = &compactionDraft{PlanID: 7, AbsHistoryPath: history}

	a.cancelCompactionForTurnCancellation(turnID)

	if a.IsCompactionRunning() {
		t.Fatal("compaction must no longer be running after the parked draft settles")
	}
	if _, err := os.Stat(history); !os.IsNotExist(err) {
		t.Fatalf("parked draft history archive must be cleaned up, stat err=%v", err)
	}
	if a.modelDrivenSkipNotice == "" {
		t.Fatal("cancelling the parked draft must surface the continuation notice")
	}
	// Drain events until the terminal status event arrives (newTurn and the
	// settle path may interleave unrelated bookkeeping events first).
	deadline := time.After(2 * time.Second)
	for {
		select {
		case evt := <-a.outputCh:
			status, ok := evt.(CompactionStatusEvent)
			if !ok {
				continue
			}
			if status.Status != CompactionStatusCancelled {
				t.Fatalf("status = %v, want %v", status.Status, CompactionStatusCancelled)
			}
			if status.Trigger != string(compactionTriggerModelDriven) {
				t.Fatalf("trigger = %q, want %q (built from the live compaction state before reset)", status.Trigger, compactionTriggerModelDriven)
			}
			if status.PlanID != "7" {
				t.Fatalf("plan_id = %q, want \"7\"", status.PlanID)
			}
			return
		case <-deadline:
			t.Fatal("no compaction status event was emitted for the cancelled parked draft")
		}
	}
}

func TestModelDrivenProposalTransitionUpdatesMetadata(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	before := time.Now()
	a.transitionModelDrivenProposal(modelDrivenProposalAccepted, " accepted ")
	if a.modelDrivenProposal.status != modelDrivenProposalAccepted || a.modelDrivenProposal.reason != "accepted" {
		t.Fatalf("proposal metadata = status %q reason %q", a.modelDrivenProposal.status, a.modelDrivenProposal.reason)
	}
	if a.modelDrivenProposal.updatedAt.Before(before) {
		t.Fatalf("proposal timestamp = %v, before %v", a.modelDrivenProposal.updatedAt, before)
	}
	a.transitionModelDrivenProposal(CompactionStatusFailed, " failed ")
	if a.modelDrivenProposal.status != CompactionStatusFailed || a.modelDrivenProposal.reason != "failed" {
		t.Fatalf("terminal metadata = status %q reason %q", a.modelDrivenProposal.status, a.modelDrivenProposal.reason)
	}
}
