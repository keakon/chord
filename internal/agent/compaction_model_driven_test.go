package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func testCompactContextCall() (string, string) {
	return "cc-1", `{
		"active_objective": "land the model-driven context reset",
		"completed": ["tool written", "runtime barrier wired"],
		"decisions": ["keep archival profile", "MainAgent-only"],
		"open_issues": ["gateway contract test"],
		"next_step": "run the agent tests",
		"state_files": ["internal/agent/compaction_model_driven.go"]
	}`
}

func testToolCall(id, name string) message.ToolCall {
	return message.ToolCall{ID: id, Name: name, Args: json.RawMessage(`{}`)}
}

func TestCompactContextSoleToolCallDetection(t *testing.T) {
	ccID, _ := testCompactContextCall()
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "do work"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall("sibling-1", "read")}},
		{Role: message.RoleTool, ToolCallID: "sibling-1", Content: "ok"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall(ccID, tools.NameCompactContext)}},
	}
	if !compactContextSoleToolCall(msgs, ccID) {
		t.Fatal("sole compact_context call should pass the sole-call check")
	}

	multi := []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{
			testToolCall(ccID, tools.NameCompactContext),
			testToolCall("other", "read"),
		}},
	}
	if compactContextSoleToolCall(multi, ccID) {
		t.Fatal("compact_context with a sibling tool call must fail the sole-call check")
	}
	if compactContextSoleToolCall([]message.Message{{Role: message.RoleUser, Content: "x"}}, ccID) {
		t.Fatal("missing declaring message must fail the sole-call check")
	}
}

func TestTryArmModelDrivenCheckpointAcceptsValidArgs(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	ccID, args := testCompactContextCall()
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall(ccID, tools.NameCompactContext)}})
	result, err := a.tryArmModelDrivenCheckpoint(ccID, args)
	if err != nil {
		t.Fatalf("tryArmModelDrivenCheckpoint: %v", err)
	}
	if !strings.Contains(result, "accepted") {
		t.Fatalf("result = %q, want accepted wording", result)
	}
	if a.pendingModelDriven == nil {
		t.Fatal("pendingModelDriven should be armed")
	}
	if a.pendingModelDriven.Args.ActiveObjective != "land the model-driven context reset" {
		t.Fatalf("armed objective = %q", a.pendingModelDriven.Args.ActiveObjective)
	}
	if len(a.pendingModelDriven.Args.StateFiles) != 1 {
		t.Fatalf("state_files = %#v", a.pendingModelDriven.Args.StateFiles)
	}
}

func TestTryArmModelDrivenCheckpointRejectsBadArgs(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	if _, err := a.tryArmModelDrivenCheckpoint("cc-1", `{"state_files":["/etc/passwd"]}`); err == nil {
		t.Fatal("expected rejection for absolute state path")
	}
	if a.pendingModelDriven != nil {
		t.Fatal("pendingModelDriven must not be armed on rejection")
	}
	if _, err := a.tryArmModelDrivenCheckpoint("cc-1", `{}`); err == nil {
		t.Fatal("expected rejection for missing required fields")
	}

	// A compaction already owning the slot does not reject the request: the
	// model's explicit checkpoint may override an in-flight automatic
	// compaction, and the model-driven barrier discards the running
	// compaction before starting the model's own worker. The request is
	// armed normally.
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall("cc-1", tools.NameCompactContext)}})
	a.beginCompactionState(9, compactionTarget{turnID: 1, turnEpoch: 1, sessionEpoch: a.sessionEpoch}, compactionTriggerUsageDriven, continuationPlan{kind: compactionResumeAutoContinue, turnID: 1}, 0, nil)
	defer a.resetCompactionState()
	if _, err := a.tryArmModelDrivenCheckpoint("cc-1", `{"active_objective":"a","next_step":"b"}`); err != nil {
		t.Fatalf("model checkpoint must be accepted while an automatic compaction runs: %v", err)
	}
	if a.pendingModelDriven == nil {
		t.Fatal("pendingModelDriven must be armed while a compaction is running (override path)")
	}
}

func TestMaybeStartModelDrivenBarrierSkipsWithoutPending(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	if a.maybeStartModelDrivenBarrier() {
		t.Fatal("barrier must not start with no pending request")
	}
}

func TestMaybeStartModelDrivenBarrierStartsWorker(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	for _, msg := range []message.Message{
		{Role: message.RoleUser, Content: "first request"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall("cc-1", tools.NameCompactContext)}},
	} {
		a.ctxMgr.Append(msg)
	}
	ccID, args := testCompactContextCall()
	if _, err := a.tryArmModelDrivenCheckpoint(ccID, args); err != nil {
		t.Fatalf("tryArm: %v", err)
	}
	if !a.maybeStartModelDrivenBarrier() {
		t.Fatal("barrier should start for a pending model-driven request")
	}
	if a.pendingModelDriven != nil {
		t.Fatal("pending request must be consumed by the barrier")
	}
	if !a.IsCompactionRunning() {
		t.Fatal("compaction should be running after the barrier")
	}
	if a.compactionState.trigger != compactionTriggerModelDriven {
		t.Fatalf("trigger = %q, want model_driven", a.compactionState.trigger)
	}
	if a.compactionState.continuation.kind != compactionResumeModelDriven {
		t.Fatalf("continuation kind = %q, want model_driven", a.compactionState.continuation.kind)
	}
	if a.compactionState.headSplit <= 0 {
		t.Fatalf("head split = %d, want > 0", a.compactionState.headSplit)
	}
	// The worker runs without an event loop in this test, so it never reaches
	// a terminal state here; teardown joins it via signalStopping + outputWg.
}

func TestMaybeStartModelDrivenBarrierOverridesRunningUsageCompaction(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	for _, msg := range []message.Message{
		{Role: message.RoleUser, Content: "first request"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall("cc-1", tools.NameCompactContext)}},
	} {
		a.ctxMgr.Append(msg)
	}
	// An automatic usage-driven compaction owns the slot (a threshold crossing
	// started its async worker). The model's checkpoint request is accepted
	// and the barrier discards the running compaction before the model-driven
	// worker starts: the model chose this boundary on purpose, so it wins over
	// the runtime's background summary.
	cancelCalled := false
	a.beginCompactionState(7, compactionTarget{turnID: 1, turnEpoch: 1, sessionEpoch: a.sessionEpoch}, compactionTriggerUsageDriven, continuationPlan{kind: compactionResumeAutoContinue, turnID: 1}, 5, func() { cancelCalled = true })
	ccID, args := testCompactContextCall()
	if _, err := a.tryArmModelDrivenCheckpoint(ccID, args); err != nil {
		t.Fatalf("tryArm must accept the override request: %v", err)
	}
	if a.pendingModelDriven == nil {
		t.Fatal("pending request must be armed")
	}
	if !a.maybeStartModelDrivenBarrier() {
		t.Fatal("barrier should start the model-driven worker")
	}
	if !cancelCalled {
		t.Fatal("the running usage compaction must be cancelled for the model override")
	}
	if a.compactionState.trigger != compactionTriggerModelDriven {
		t.Fatalf("trigger = %q, want model_driven (model wins over usage-driven)", a.compactionState.trigger)
	}
	if a.compactionState.planID == 7 {
		t.Fatal("compaction state must belong to the new model-driven plan")
	}
}

func TestMaybeStartModelDrivenBarrierDiscardsReadyUsageDraft(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	for _, msg := range []message.Message{
		{Role: message.RoleUser, Content: "first request"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall("cc-1", tools.NameCompactContext)}},
	} {
		a.ctxMgr.Append(msg)
	}
	// A usage-driven draft is ready and waiting for the continuation barrier
	// (Case C). The model's checkpoint arrives in the tool batch before that
	// barrier resolves: the barrier must discard the ready draft (removing its
	// orphan history files) and start the model-driven worker instead.
	readyHistory := filepath.Join(t.TempDir(), "history-1.md")
	if err := os.WriteFile(readyHistory, []byte("# archived"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.beginCompactionState(7, compactionTarget{turnID: 1, turnEpoch: 1, sessionEpoch: a.sessionEpoch}, compactionTriggerUsageDriven, continuationPlan{kind: compactionResumeAutoContinue, turnID: 1}, 5, nil)
	a.compactionState.readyDraft = &compactionDraft{PlanID: 7, AbsHistoryPath: readyHistory}
	ccID, args := testCompactContextCall()
	if _, err := a.tryArmModelDrivenCheckpoint(ccID, args); err != nil {
		t.Fatalf("tryArm must accept the override request: %v", err)
	}
	if !a.maybeStartModelDrivenBarrier() {
		t.Fatal("barrier should start the model-driven worker")
	}
	if a.compactionState.readyDraft != nil {
		t.Fatal("ready draft must be discarded on the model override")
	}
	if _, err := os.Stat(readyHistory); !os.IsNotExist(err) {
		t.Fatalf("orphan ready history must be removed, stat err = %v", err)
	}
	if a.compactionState.trigger != compactionTriggerModelDriven {
		t.Fatalf("trigger = %q, want model_driven", a.compactionState.trigger)
	}
}

func TestMaybeStartModelDrivenBarrierIntervalSkipKeepsRunningUsageCompaction(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	for _, msg := range []message.Message{
		{Role: message.RoleUser, Content: "first request"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall("cc-1", tools.NameCompactContext)}},
	} {
		a.ctxMgr.Append(msg)
	}
	// A usage-driven compaction owns the slot and a recent model-driven apply
	// keeps the batch counter inside the 3-batch interval. The policy verdict
	// rejects the checkpoint request before the slot is touched: the running
	// automatic compaction must survive, and the settle must be synchronous
	// (started + skipped on one plan id, no worker, no continuation).
	cancelCalled := false
	a.beginCompactionState(7, compactionTarget{turnID: 1, turnEpoch: 1, sessionEpoch: a.sessionEpoch}, compactionTriggerUsageDriven, continuationPlan{kind: compactionResumeAutoContinue, turnID: 1}, 5, func() { cancelCalled = true })
	a.lastModelDrivenApplyBatch = 4
	a.requestBatches.mu.Lock()
	a.requestBatches.sessionEpoch = a.sessionEpoch
	a.requestBatches.sequence = 5
	a.requestBatches.mu.Unlock()
	ccID, args := testCompactContextCall()
	if _, err := a.tryArmModelDrivenCheckpoint(ccID, args); err != nil {
		t.Fatalf("tryArm must accept the checkpoint request: %v", err)
	}
	if a.maybeStartModelDrivenBarrier() {
		t.Fatal("barrier must not start a worker for an interval-blocked request")
	}
	if cancelCalled {
		t.Fatal("the running usage compaction must NOT be cancelled for an interval-blocked request")
	}
	if !a.IsCompactionRunning() || a.compactionState.trigger != compactionTriggerUsageDriven || a.compactionState.planID != 7 {
		t.Fatalf("the running usage compaction must stay untouched, running=%v trigger=%q plan=%d", a.IsCompactionRunning(), a.compactionState.trigger, a.compactionState.planID)
	}
	if a.pendingModelDriven != nil {
		t.Fatal("pending request must be consumed by the barrier")
	}
	planIDs := map[string]string{}
	synthetic := false
	for len(planIDs) < 2 {
		select {
		case evt := <-a.outputCh:
			status, ok := evt.(CompactionStatusEvent)
			if !ok {
				continue
			}
			if status.Trigger != string(compactionTriggerModelDriven) {
				t.Fatalf("trigger = %q, want model_driven", status.Trigger)
			}
			if status.Status == CompactionStatusStarted && !status.Synthetic {
				t.Fatal("sync-skip started must be synthetic (the plan never occupies the slot)")
			}
			synthetic = synthetic || status.Synthetic
			planIDs[status.Status] = status.PlanID
		case <-time.After(2 * time.Second):
			t.Fatalf("no started+skipped status events, got %v", planIDs)
		}
	}
	if !synthetic {
		t.Fatal("sync-skip events must carry the synthetic flag for slot-aware consumers")
	}
	if planIDs[CompactionStatusStarted] == "" || planIDs[CompactionStatusStarted] != planIDs[CompactionStatusSkipped] {
		t.Fatalf("started and skipped must share one plan id, got %v", planIDs)
	}
	if a.lastModelDrivenSkipReason != "interval" {
		t.Fatalf("skip reason = %q, want interval", a.lastModelDrivenSkipReason)
	}
	if a.pendingModelDrivenNotice == "" {
		t.Fatal("sync skip must queue the continuation notice for the next request")
	}
}

func TestMaybeStartModelDrivenBarrierCooldownSkipIsSynchronous(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "first request"})
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall("cc-1", tools.NameCompactContext)}})
	// The previous request skipped for low_gain two batches ago, so the same
	// reason is still cooling down: the barrier must short-circuit without
	// starting a worker or re-running the preflight.
	a.lastModelDrivenSkipReason = "low_gain"
	a.lastModelDrivenSkipBatch = 3
	a.requestBatches.mu.Lock()
	a.requestBatches.sessionEpoch = a.sessionEpoch
	a.requestBatches.sequence = 4
	a.requestBatches.mu.Unlock()
	ccID, args := testCompactContextCall()
	if _, err := a.tryArmModelDrivenCheckpoint(ccID, args); err != nil {
		t.Fatalf("tryArm must accept the checkpoint request: %v", err)
	}
	if a.maybeStartModelDrivenBarrier() {
		t.Fatal("barrier must not start a worker for a cooldown-blocked request")
	}
	if a.IsCompactionRunning() {
		t.Fatal("no compaction may run for a cooldown skip")
	}
	// The cooldown hit must NOT re-stamp the skip batch: the window is
	// anchored on the original low_gain skip (batch 3) so it can actually
	// expire; refreshing it here would make a per-batch retry slide the window
	// forever and the preflight would never run again.
	if a.lastModelDrivenSkipReason != "low_gain" || a.lastModelDrivenSkipBatch != 3 {
		t.Fatalf("cooldown skip must keep the original low_gain anchor, got reason=%q batch=%d", a.lastModelDrivenSkipReason, a.lastModelDrivenSkipBatch)
	}
	if a.pendingModelDrivenNotice == "" {
		t.Fatal("sync cooldown skip must queue the continuation notice")
	}
	// started + skipped share one plan id and the started is synthetic (the
	// cooldown skip never occupies the compaction slot).
	planIDs := map[string]string{}
	syntheticStarted := false
	for len(planIDs) < 2 {
		select {
		case evt := <-a.outputCh:
			status, ok := evt.(CompactionStatusEvent)
			if !ok {
				continue
			}
			if status.Status == CompactionStatusStarted && status.Synthetic {
				syntheticStarted = true
			}
			planIDs[status.Status] = status.PlanID
		case <-time.After(2 * time.Second):
			t.Fatalf("no started+skipped status events, got %v", planIDs)
		}
	}
	if !syntheticStarted {
		t.Fatal("cooldown-skip started must carry the synthetic flag")
	}
	if planIDs[CompactionStatusStarted] != planIDs[CompactionStatusSkipped] || planIDs[CompactionStatusSkipped] == "" {
		t.Fatalf("started and skipped must share one plan id, got %v", planIDs)
	}
}

func TestModelDrivenLowGainPreflightRejectsTinyContext(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	for _, msg := range []message.Message{
		{Role: message.RoleUser, Content: "short request"},
		{Role: message.RoleAssistant, Content: "short reply"},
	} {
		a.ctxMgr.Append(msg)
	}
	req := &modelDrivenCheckpointRequest{
		ToolCallID: "cc-1",
		Args:       tools.CompactContextArgs{ActiveObjective: "x", NextStep: "y"},
	}
	snapshot := a.ctxMgr.Snapshot()
	bundle := modelDrivenBarrierSnapshot{
		snapshot:              snapshot,
		maxTokens:             a.ctxMgr.GetMaxTokens(),
		prepareReducedRequest: a.compactionReductionScratch().prepareMessagesForLLM,
	}
	reason, skip, _ := a.modelDrivenLowGainPreflight(bundle, len(snapshot), snapshot, a.newModelDrivenCheckpointBuilder(bundle, snapshot, len(snapshot), req))
	if !skip {
		t.Fatal("tiny context must be skipped by the low-gain gate")
	}
	if !strings.Contains(reason, "low-gain") {
		t.Fatalf("reason = %q, want low-gain mention", reason)
	}
}

func TestModelDrivenSkipSettlesWithoutClearingUsageState(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
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
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
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
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr.SetMaxTokens(1024)
	a.newTurn()
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
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr.SetMaxTokens(1024)
	a.ctxMgr.Append(message.Message{Role: "user", Content: "head message being compacted"})
	a.newTurn()
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

func TestModelDrivenCheckpointWrapperNamesItsMode(t *testing.T) {
	content := buildCompactionCheckpointMessage("## Current User Request\n- x", nil, compactionSummaryModeModelDriven, nil)
	if !strings.Contains(content, "model-driven context checkpoint") {
		t.Fatalf("checkpoint wrapper missing model-driven copy:\n%s", content)
	}
	if !strings.Contains(content, "no summarization model was called") {
		t.Fatalf("checkpoint wrapper must state no summarization call:\n%s", content)
	}
}

func TestModelDrivenAppliedDraftCarriesPreflightStats(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()

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
	req := &modelDrivenCheckpointRequest{
		ToolCallID: "cc-1",
		Args:       tools.CompactContextArgs{ActiveObjective: "keep going", NextStep: "continue"},
	}

	draft, err := a.produceModelDrivenDraftAsync(t.Context(), bundle, 1, compactionTarget{}, 4, req)
	if err != nil {
		t.Fatalf("produceModelDrivenDraftAsync: %v", err)
	}
	if draft.ModelDrivenPreflight == nil {
		t.Fatal("applied draft must carry model-driven preflight stats")
	}
	p := draft.ModelDrivenPreflight
	if p.SavedTokens == 0 || p.CurrentTokens == 0 || p.ProjectedTokens == 0 {
		t.Fatalf("applied draft preflight savings must be populated, got %+v", p)
	}
	if p.SavedRatioPct == 0 {
		// The projected side must stay smaller than the current side for the
		// draft to be produced at all; a zero ratio with non-zero savings on
		// a big context would mean the numbers were never computed.
		t.Fatalf("applied draft preflight saved ratio must be populated, got %+v", p)
	}
	if p.CurrentBytes == 0 || p.ProjectedBytes == 0 {
		t.Fatalf("applied draft preflight bytes must be populated, got %+v", p)
	}
	if p.CheckpointBytes == 0 || p.HistoryMapBytes == 0 || p.ContinuationTokens == 0 {
		t.Fatalf("applied draft content stats must be populated, applied=%+v", p)
	}
	if p.AnchorBytes < 0 {
		t.Fatalf("anchor bytes must not be negative, got %d", p.AnchorBytes)
	}
	if p.ProjectedTokens <= bundle.fixedRequestTokens {
		t.Fatalf("projected tokens %d must exceed the current fixed surface %d", p.ProjectedTokens, bundle.fixedRequestTokens)
	}
	projected := append([]message.Message{
		{Role: message.RoleUser, Content: draft.NewMessages[0].Content, IsCompactionSummary: true},
	}, snapshot[4:]...)
	recomputedProjected := estimateMessagesTokens(a.ctxMgr, projected) + bundle.postResetFixedRequestTokens + modelDrivenPostResetOverlayTokens
	// The preflight builds the checkpoint before history export and the
	// post-export draft re-builds it with the just-written archive in the
	// history map, so the projected surfaces differ by that map delta (~tens
	// of tokens). If the projected side silently used the current-side fixed
	// surface (fixedRequestTokens=1000) instead of the post-reset full
	// injection (postResetFixedRequestTokens=4000), the difference would be
	// ~3000 tokens and this assertion fails.
	if diff := p.ProjectedTokens - recomputedProjected; diff < -500 || diff > 500 {
		t.Fatalf("projected tokens %d diverge from the post-reset fixed surface accounting %d (diff=%d)", p.ProjectedTokens, recomputedProjected, diff)
	}
}

func TestModelDrivenCheckpointSummaryKeepsModelTextAndNeutralizesHeadings(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	req := &modelDrivenCheckpointRequest{
		Args: tools.CompactContextArgs{
			// The forged heading must not open a new section; the bullet and
			// the blockquote are the model's own markdown and stay verbatim.
			ActiveObjective: "keep going\n## Fake Heading\n- not a real bullet\n> model quote",
			Completed:       []string{"done"},
			NextStep:        "next",
		},
	}
	bundle := modelDrivenBarrierSnapshot{
		snapshot:        []message.Message{{Role: message.RoleUser, Content: "orig"}},
		originalRequest: "original user request",
	}
	summary := a.buildModelDrivenCheckpointSummary(bundle, bundle.snapshot, len(bundle.snapshot), req)
	if !strings.Contains(summary, "Fake Heading") {
		t.Fatal("model text must remain in the checkpoint")
	}
	if strings.Contains(summary, "## Fake Heading") {
		t.Fatal("a forged heading must be neutralized, not kept as a section marker")
	}
	if !strings.Contains(summary, "- not a real bullet") {
		t.Fatal("a model-authored list bullet must be preserved verbatim")
	}
	if !strings.Contains(summary, "> model quote") {
		t.Fatal("a model-authored blockquote must be preserved verbatim")
	}
	idx := strings.Index(summary, "## Active Objective")
	if idx < 0 {
		t.Fatal("missing Active Objective section")
	}
	section := summary[idx:]
	next := strings.Index(section, "\n## ")
	if next < 0 {
		next = len(section)
	}
	body := section[:next]
	if strings.Count(body, "\n## ") > 0 {
		t.Fatalf("model text escaped its section:\n%s", body)
	}
}

func TestStripLeadingHeadingMarkers(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"empty", "", ""},
		{"plain text", "plain", "plain"},
		{"heading two", "## Forged", "Forged"},
		{"heading one", "# Note", "Note"},
		{"no blank after run is not a heading", "##Forged", "##Forged"},
		{"hash tag", "#1 issue", "#1 issue"},
		{"hash tag word", "#tag", "#tag"},
		{"six hashes", "###### deep", "deep"},
		{"seven hashes is not a heading", "####### seven", "####### seven"},
		{"repeated markers", "# # x", "x"},
		{"marker only", "##", ""},
		{"indented is not column zero", "  ## indented", "  ## indented"},
		{"blockquote survives", "> keep", "> keep"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripLeadingHeadingMarkers(tt.in); got != tt.want {
				t.Fatalf("stripLeadingHeadingMarkers(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestModelDrivenCurrentUserRequestSectionTruncatesOverlongAnchor(t *testing.T) {
	// P2-1: the deterministic checkpoint must cap the latest-request anchor
	// like the structured-fallback summary does; an overlong user message or
	// Done-rejected reason must not crowd out the rest of the checkpoint.
	longText := strings.Repeat("a", modelDrivenAnchorMaxRunes*3)
	got := modelDrivenCurrentUserRequestSection(fallbackAnchor{Kind: "user_request", Label: "Latest user request", Text: longText})
	if strings.Contains(got, strings.Repeat("a", modelDrivenAnchorMaxRunes*2)) {
		t.Fatalf("anchor was not truncated: %d runes retained", len(got))
	}
	if !strings.Contains(got, "...") {
		t.Fatalf("truncated anchor must carry an explicit cut marker: %q", got)
	}
	// Short anchors are untouched.
	short := modelDrivenCurrentUserRequestSection(fallbackAnchor{Kind: "user_request", Label: "Latest user request", Text: "short request"})
	if !strings.Contains(short, "short request") {
		t.Fatalf("short anchor must be preserved verbatim: %q", short)
	}
}

func TestModelDrivenCurrentUserRequestSectionInheritedChain(t *testing.T) {
	// A checkpoint built with no new user request inherits the previous
	// checkpoint's anchor. When that checkpoint was itself inherited, the
	// inherited label must never accumulate across the chain: each generation
	// renders exactly one label, mirroring the previous generation.
	gen1 := modelDrivenCurrentUserRequestSection(fallbackAnchor{Kind: "user_request", Label: "Latest user request", Text: "fix the bug"})
	if gen1 != "- Latest user request: fix the bug" {
		t.Fatalf("gen1 = %q, want the plain latest-request bullet", gen1)
	}
	gen2 := modelDrivenCurrentUserRequestSection(fallbackAnchor{Kind: "inherited_checkpoint", Label: inheritedCheckpointLabel, Text: gen1})
	want2 := "- " + inheritedCheckpointLabel + ": Latest user request: fix the bug"
	if gen2 != want2 {
		t.Fatalf("gen2 = %q, want %q", gen2, want2)
	}
	if got := strings.Count(gen2, inheritedCheckpointLabel); got != 1 {
		t.Fatalf("gen2 carries the inherited label %d times, want exactly 1", got)
	}
	// gen3 inherits gen2's rendered section: the label must stay at one
	// occurrence instead of nesting "Inherited ... Inherited ...".
	gen3 := modelDrivenCurrentUserRequestSection(fallbackAnchor{Kind: "inherited_checkpoint", Label: inheritedCheckpointLabel, Text: gen2})
	if gen3 != want2 {
		t.Fatalf("gen3 = %q, want %q (label must not accumulate)", gen3, want2)
	}
	if got := strings.Count(gen3, inheritedCheckpointLabel); got != 1 {
		t.Fatalf("gen3 carries the inherited label %d times, want exactly 1", got)
	}
}

func TestCancelCompactionForTurnCancellationDiscardsModelDrivenWorker(t *testing.T) {
	// P1-1: cancelling the requesting turn must stop an in-flight model-driven
	// checkpoint so a late ready draft can never rewrite history for abandoned
	// work. The worker context is parented on parentCtx, not the turn context,
	// so without explicit cancellation it would keep running past ESC.
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
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
	a2.newTurn()
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
	// A model-driven draft already parked at the continuation barrier belongs
	// to the cancelled turn. Cancelling the turn must settle it while the
	// compaction state is still live — the terminal status event carries the
	// model_driven trigger and plan id only when it is built before the
	// reset — then clean its orphan history files and reset the state.
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
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

func TestModelDrivenDraftCommittedArchiveSurvives(t *testing.T) {
	// The deferred archive cleanup only fires for failures: a successful
	// draft must keep its history file and meta so the apply step can use
	// them.
	dir := t.TempDir()
	a := newTestMainAgent(t, dir)
	a.newTurn()

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
	a.newTurn()

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

func TestModelDrivenIntervalVerdictRequiresThreeBatches(t *testing.T) {
	a := &MainAgent{}
	// No previous apply: no interval gate.
	if _, _, skip := a.modelDrivenIntervalCooldownVerdict(modelDrivenBarrierSnapshot{currentRequestBatch: 1}); skip {
		t.Fatal("no previous apply must not interval-skip")
	}
	// Same batch as the last apply: interval skip.
	bundle := modelDrivenBarrierSnapshot{currentRequestBatch: 3, lastModelDrivenApplyBatch: 3}
	reason, skipReason, skip := a.modelDrivenIntervalCooldownVerdict(bundle)
	if !skip || skipReason != "interval" || !strings.Contains(reason, "interval") {
		t.Fatalf("same batch must interval-skip, reason=%q skipReason=%q skip=%v", reason, skipReason, skip)
	}
	// 2 batches after apply: still skipping.
	if _, _, skip := a.modelDrivenIntervalCooldownVerdict(modelDrivenBarrierSnapshot{currentRequestBatch: 5, lastModelDrivenApplyBatch: 3}); !skip {
		t.Fatal("2 batches after the last apply must interval-skip")
	}
	// 3 batches after apply: allowed.
	if _, _, skip := a.modelDrivenIntervalCooldownVerdict(modelDrivenBarrierSnapshot{currentRequestBatch: 6, lastModelDrivenApplyBatch: 3}); skip {
		t.Fatal("3 batches after the last apply must pass the interval")
	}
}

func TestModelDrivenIntervalVerdictStaleAnchorCountsAsSatisfied(t *testing.T) {
	a := &MainAgent{}
	// Restored session whose batch numbering restarted below the persisted
	// last-apply anchor: the anchor is stale. It must neither underflow into
	// "satisfied by a huge gap" nor throttle every model-driven request until
	// the counter catches up; the interval simply counts as satisfied.
	if _, _, skip := a.modelDrivenIntervalCooldownVerdict(modelDrivenBarrierSnapshot{currentRequestBatch: 2, lastModelDrivenApplyBatch: 5}); skip {
		t.Fatal("a stale (current < last) apply anchor must not block model-driven requests")
	}
	// current == last is a genuine zero-batch gap and stays throttled.
	reason, _, skip := a.modelDrivenIntervalCooldownVerdict(modelDrivenBarrierSnapshot{currentRequestBatch: 5, lastModelDrivenApplyBatch: 5})
	if !skip || !strings.Contains(reason, "interval") {
		t.Fatalf("same-batch retry must interval-skip, got skip=%v reason=%q", skip, reason)
	}
	// currentRequestBatch 0 (no batch reserved yet) with a persisted anchor is
	// the classic restore shape; it is stale as well.
	if _, _, skip := a.modelDrivenIntervalCooldownVerdict(modelDrivenBarrierSnapshot{currentRequestBatch: 0, lastModelDrivenApplyBatch: 5}); skip {
		t.Fatal("batch 0 against a persisted anchor must not throttle")
	}
}

func TestModelDrivenPreflightReusesLastPreparedSurface(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	snapshot := []message.Message{
		{Role: message.RoleUser, Content: "original user request"},
		{Role: message.RoleAssistant, Content: strings.Repeat("analysis ", 4000)},
	}
	// This turn actually sent the above request, so the remembered reduced
	// surface is the provider-truth baseline.
	a.rememberPreparedLLMRequest(a.currentTurnID(), snapshot, snapshot, nil, nil, countToolResults(snapshot), a.contextReductionPolicy())
	snapshot = append(snapshot,
		message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall("cc-1", tools.NameCompactContext)}},
		message.Message{Role: message.RoleTool, ToolCallID: "cc-1", Content: requestAcceptedToolResult},
	)
	req := &modelDrivenCheckpointRequest{
		ToolCallID: "cc-1",
		Args:       tools.CompactContextArgs{ActiveObjective: "keep going", NextStep: "continue"},
	}
	bundle := a.captureModelDrivenBarrierSnapshot(snapshot)
	reason, skip, preflight := a.modelDrivenLowGainPreflight(bundle, len(snapshot), snapshot, a.newModelDrivenCheckpointBuilder(bundle, snapshot, len(snapshot), req))
	if skip {
		t.Fatalf("gate must pass on the reused baseline, got skip reason %q", reason)
	}
	if preflight.CurrentSource != "last_prepared" {
		t.Fatalf("current source = %q, want last_prepared", preflight.CurrentSource)
	}
}

func TestModelDrivenPreflightFallsBackToScratchWhenHeadChanged(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	snapshot := []message.Message{
		{Role: message.RoleUser, Content: "original user request"},
		{Role: message.RoleAssistant, Content: strings.Repeat("analysis ", 4000)},
	}
	a.rememberPreparedLLMRequest(a.currentTurnID(), snapshot, snapshot, nil, nil, countToolResults(snapshot), a.contextReductionPolicy())
	// A mid-head change (the user edited the original request) invalidates the
	// remembered prefix: the preflight must fall back to the scratch
	// reduction instead of reusing a stale baseline.
	snapshot[0] = message.Message{Role: message.RoleUser, Content: "edited user request"}
	snapshot = append(snapshot,
		message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall("cc-1", tools.NameCompactContext)}},
		message.Message{Role: message.RoleTool, ToolCallID: "cc-1", Content: requestAcceptedToolResult},
	)
	req := &modelDrivenCheckpointRequest{
		ToolCallID: "cc-1",
		Args:       tools.CompactContextArgs{ActiveObjective: "keep going", NextStep: "continue"},
	}
	bundle := a.captureModelDrivenBarrierSnapshot(snapshot)
	_, skip, preflight := a.modelDrivenLowGainPreflight(bundle, len(snapshot), snapshot, a.newModelDrivenCheckpointBuilder(bundle, snapshot, len(snapshot), req))
	if skip {
		t.Fatal("fixture must pass the gate")
	}
	if preflight.CurrentSource != "scratch" {
		t.Fatalf("current source = %q, want scratch for a changed head", preflight.CurrentSource)
	}
	// With no remembered request at all the same fallback applies.
	bundleNoPrep := modelDrivenBarrierSnapshot{
		snapshot:                    snapshot,
		maxTokens:                   a.ctxMgr.GetMaxTokens(),
		sessionDir:                  a.sessionDir,
		prepareReducedRequest:       a.compactionReductionScratch().prepareMessagesForLLM,
		fixedRequestTokens:          1000,
		postResetFixedRequestTokens: 4000,
		archiveMeta:                 a.captureCompactionArchiveMeta(),
	}
	_, skip2, preflight2 := a.modelDrivenLowGainPreflight(bundleNoPrep, len(snapshot), snapshot, a.newModelDrivenCheckpointBuilder(bundleNoPrep, snapshot, len(snapshot), req))
	if skip2 {
		t.Fatal("fixture must pass the gate without a prepared surface")
	}
	if preflight2.CurrentSource != "scratch" {
		t.Fatalf("current source = %q, want scratch without a prepared surface", preflight2.CurrentSource)
	}
}

func TestModelDrivenCacheRebuildCostAmortizesProjectedPrefix(t *testing.T) {
	// The rebuild charge is the write-read delta (1.15x) of the NEW checkpoint
	// prefix, amortized over the minimum apply interval — never the archived
	// head, which is dropped rather than rewritten.
	if got := modelDrivenCacheRebuildCost(0); got != 0 {
		t.Fatalf("zero projected prefix cost = %d, want 0", got)
	}
	projected := 30000
	want := projected * modelDrivenCacheRebuildDeltaNumer / modelDrivenCacheRebuildDeltaDenom / minModelDrivenApplyIntervalBatches
	if got := modelDrivenCacheRebuildCost(projected); got != want {
		t.Fatalf("rebuild cost = %d, want %d", got, want)
	}
	if got := modelDrivenCacheRebuildCost(projected); got >= projected {
		t.Fatalf("amortized rebuild cost %d must stay well below the projected prefix %d", got, projected)
	}
}

func TestModelDrivenPreflightRecordsCacheRebuildCostWithoutSubtracting(t *testing.T) {
	// The prompt-cache rebuild charge is telemetry: a cacheable session's
	// preflight records the amortized cost on the stats while the gate keeps
	// deciding on raw surface savings, so a charge that would have flipped the
	// verdict under the old "net = saved − cost" rule no longer denies the
	// reset — the cost telemetry answers whether cache rebuilds eat the
	// gains instead of pre-deciding it inside the gate.
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	snapshot := []message.Message{
		{Role: message.RoleUser, Content: "original user request"},
		{Role: message.RoleAssistant, Content: strings.Repeat("analysis ", 4000)},
		{Role: message.RoleUser, Content: "follow up"},
		{Role: message.RoleAssistant, Content: strings.Repeat("more analysis ", 4000)},
		{Role: message.RoleUser, Content: "tail request"},
	}
	bundle := modelDrivenBarrierSnapshot{
		snapshot:              snapshot,
		maxTokens:             a.ctxMgr.GetMaxTokens(),
		sessionDir:            a.sessionDir,
		prepareReducedRequest: a.compactionReductionScratch().prepareMessagesForLLM,
		promptCacheCapable:    true,
	}
	req := &modelDrivenCheckpointRequest{
		ToolCallID: "cc-1",
		Args:       tools.CompactContextArgs{ActiveObjective: "keep going", NextStep: "continue"},
	}
	reason, skip, preflight := a.modelDrivenLowGainPreflight(bundle, 4, snapshot, a.newModelDrivenCheckpointBuilder(bundle, snapshot, 4, req))
	if skip {
		t.Fatalf("gate must pass on raw savings, got skip reason %q", reason)
	}
	if preflight.CacheRebuildCost <= 0 {
		t.Fatalf("cache-capable preflight must record the rebuild cost, got %d", preflight.CacheRebuildCost)
	}
	if preflight.SavedTokens < modelDrivenLowGainMinTokens {
		t.Fatalf("fixture must produce savings above the floor, got %d", preflight.SavedTokens)
	}
}

func TestModelDrivenSkipCooldownShortCircuitsLowGain(t *testing.T) {
	a := &MainAgent{}
	// Previous low-gain skip at batch 10; retry one batch later.
	bundle := modelDrivenBarrierSnapshot{currentRequestBatch: 11, lastModelDrivenSkipReason: "low_gain", lastModelDrivenSkipBatch: 10}
	reason, skipReason, skip := a.modelDrivenIntervalCooldownVerdict(bundle)
	if !skip {
		t.Fatal("same-reason low-gain retry within the cooldown must short-circuit")
	}
	if skipReason != "low_gain" {
		t.Fatalf("cooldown bound reason = %q, want low_gain", skipReason)
	}
	if !strings.Contains(reason, "cooling down") {
		t.Fatalf("cooldown reason = %q, want cooling-down wording", reason)
	}
	// Two batches later the cooldown expires and the interval gate decides.
	if _, _, skip := a.modelDrivenIntervalCooldownVerdict(modelDrivenBarrierSnapshot{currentRequestBatch: 12, lastModelDrivenSkipReason: "low_gain", lastModelDrivenSkipBatch: 10}); skip {
		t.Fatal("low-gain cooldown must expire after 2 batches")
	}
}

func TestModelDrivenIntervalRejectionNotCooldownBlocked(t *testing.T) {
	a := &MainAgent{}
	// An interval skip was recorded at batch 10 (last apply at 9). The model
	// retries at batch 11: the interval still has not elapsed (11-9=2 < 3), so
	// the retry is interval-skipped again — gated by the deterministic
	// interval, never cooled down by the previous interval record.
	bundle := modelDrivenBarrierSnapshot{currentRequestBatch: 11, lastModelDrivenApplyBatch: 9, lastModelDrivenSkipReason: "interval", lastModelDrivenSkipBatch: 10}
	_, skipReason, skip := a.modelDrivenIntervalCooldownVerdict(bundle)
	if !skip {
		t.Fatal("retry while the interval has not elapsed must skip")
	}
	if skipReason != "interval" {
		t.Fatalf("interval-gated retry must keep the interval reason, got %q (must not be cooldown-blocked)", skipReason)
	}
	// Once the interval elapses (3 batches since the apply), a retry proceeds
	// to preflight: it is free of any cooldown.
	if _, _, skip := a.modelDrivenIntervalCooldownVerdict(modelDrivenBarrierSnapshot{currentRequestBatch: 12, lastModelDrivenApplyBatch: 9, lastModelDrivenSkipReason: "interval", lastModelDrivenSkipBatch: 10}); skip {
		t.Fatal("interval-satisfied retry must not be blocked by the interval skip record")
	}
}

func TestModelDrivenLowGainCheckComparesRawSavings(t *testing.T) {
	// The gates compare raw surface savings; the cache rebuild charge is a
	// per-provider billing weight recorded for telemetry, never subtracted
	// here, so a cacheable session is not pre-denied inside the gate.
	if reason, skip := modelDrivenLowGainCheck(10000, 5000); skip {
		t.Fatalf("raw savings above the gate must pass, got reason %q", reason)
	}
	// Savings that would have fallen below the floor only after subtracting a
	// 4000-token rebuild charge still pass: the cost no longer gates.
	if reason, skip := modelDrivenLowGainCheck(10000, 5000); skip {
		t.Fatalf("savings unaffected by the (telemetry-only) rebuild cost must pass, got reason %q", reason)
	}
	// The absolute floor still applies to raw savings.
	if reason, skip := modelDrivenLowGainCheck(10000, 1000); !skip {
		t.Fatal("raw savings below the absolute floor must skip")
	} else if !strings.Contains(reason, "below the low-gain gate") {
		t.Fatalf("floor skip reason must mention the gate, got %q", reason)
	}
	// The 10% relative gate applies to the raw savings against the current
	// prepared surface.
	if reason, skip := modelDrivenLowGainCheck(100000, 4000); !skip {
		t.Fatalf("raw savings below 10%% of the prepared surface must skip, got %q", reason)
	} else if !strings.Contains(reason, "below the low-gain gate") {
		t.Fatalf("relative-gate skip reason must mention the gate, got %q", reason)
	}
}

func TestModelDrivenApplyRecordsLastApplyBatch(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	a.ctxMgr.Append(message.Message{Role: "user", Content: "one"})
	a.ctxMgr.Append(message.Message{Role: "assistant", Content: "two"})
	a.ctxMgr.Append(message.Message{Role: "user", Content: "three"})
	// Reserve one request batch so currentRequestBatch returns a known value.
	a.requestBatches.reserve(a.sessionEpoch, 0)

	draft := &compactionDraft{
		NewMessages:    []message.Message{{Role: "user", Content: "[Context Summary]", IsCompactionSummary: true}},
		HeadSplit:      2,
		Index:          1,
		AbsHistoryPath: filepath.Join(a.sessionDir, "history-1.md"),
		SummaryMode:    compactionSummaryModeModelDriven,
		PlanID:         1,
		Target:         compactionTarget{sessionEpoch: a.sessionEpoch},
	}
	if err := a.applyCompactionDraft(draft); err != nil {
		t.Fatalf("applyCompactionDraft: %v", err)
	}
	if a.lastModelDrivenApplyBatch != 1 {
		t.Fatalf("lastModelDrivenApplyBatch = %d, want 1", a.lastModelDrivenApplyBatch)
	}
	if a.lastModelDrivenSkipBatch != 0 || a.lastModelDrivenSkipReason != "" {
		t.Fatalf("apply must clear the skip-cooldown state, got batch=%d reason=%q", a.lastModelDrivenSkipBatch, a.lastModelDrivenSkipReason)
	}
	// The checkpoint message itself carries the apply batch so a restart whose
	// transcript is a lone checkpoint restores the sequence continuously
	// instead of restarting at 0.
	checkpoint := a.ctxMgr.Snapshot()
	if len(checkpoint) == 0 || checkpoint[0].RequestBatch != 1 {
		var got uint64
		if len(checkpoint) > 0 {
			got = checkpoint[0].RequestBatch
		}
		t.Fatalf("checkpoint message RequestBatch = %d, want the apply batch 1", got)
	}
	// A durable apply advances the in-memory overlay window key: the reminder
	// claim keys on it, so the next window re-arms delivered and ccCalled.
	if a.compactionWindowGeneration != 1 {
		t.Fatalf("compactionWindowGeneration = %d, want 1 after the apply", a.compactionWindowGeneration)
	}
}

func TestUsageDrivenApplyClearsSkipCooldownAndGrace(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	a.ctxMgr.Append(message.Message{Role: "user", Content: "one"})
	a.ctxMgr.Append(message.Message{Role: "assistant", Content: "two"})
	a.ctxMgr.Append(message.Message{Role: "user", Content: "three"})
	a.requestBatches.reserve(a.sessionEpoch, 0)
	a.lastModelDrivenSkipBatch = 1
	a.lastModelDrivenSkipReason = "low_gain"
	a.compactionGraceStartBatch = 1
	a.compactionGraceExhausted = true
	a.pendingCompactionImminent = "stale"

	draft := &compactionDraft{
		NewMessages:    []message.Message{{Role: "user", Content: "[Context Summary]", IsCompactionSummary: true}},
		HeadSplit:      2,
		Index:          1,
		AbsHistoryPath: filepath.Join(a.sessionDir, "history-1.md"),
		PlanID:         1,
		Target:         compactionTarget{sessionEpoch: a.sessionEpoch},
	}
	if err := a.applyCompactionDraft(draft); err != nil {
		t.Fatalf("applyCompactionDraft: %v", err)
	}
	// A non-model-driven apply does not move the interval anchor…
	if a.lastModelDrivenApplyBatch != 0 {
		t.Fatalf("usage-driven apply must not record a model-driven apply batch, got %d", a.lastModelDrivenApplyBatch)
	}
	// …but the prepared surface the last low-gain verdict was computed on is
	// gone, so the skip cooldown and the grace window must reset.
	if a.lastModelDrivenSkipBatch != 0 || a.lastModelDrivenSkipReason != "" {
		t.Fatalf("usage-driven apply must clear the skip-cooldown state, got batch=%d reason=%q", a.lastModelDrivenSkipBatch, a.lastModelDrivenSkipReason)
	}
	if a.compactionGraceStartBatch != 0 || a.compactionGraceExhausted || a.pendingCompactionImminent != "" {
		t.Fatalf("durable apply must clear the grace state, got start=%d exhausted=%v pending=%q", a.compactionGraceStartBatch, a.compactionGraceExhausted, a.pendingCompactionImminent)
	}
}

func TestModelDrivenSettleRecordsSkipCooldownState(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	a.settleModelDrivenSkip(&compactionDraft{Skip: true, InfoMessage: "Context checkpoint skipped: the minimum 3-request-batch interval since the last applied context checkpoint has not elapsed", ModelDrivenSkipReason: "interval", ModelDrivenSkipBatch: 7})
	if a.lastModelDrivenSkipReason != "interval" || a.lastModelDrivenSkipBatch != 7 {
		t.Fatalf("interval skip must record the cooldown state, got reason=%q batch=%d", a.lastModelDrivenSkipReason, a.lastModelDrivenSkipBatch)
	}
	// Structural skips carry no verdict and must not touch the cooldown state.
	a.settleModelDrivenSkip(&compactionDraft{Skip: true, InfoMessage: "Not enough history to compact."})
	if a.lastModelDrivenSkipReason != "interval" || a.lastModelDrivenSkipBatch != 7 {
		t.Fatalf("structural skip must not touch the cooldown state, got reason=%q batch=%d", a.lastModelDrivenSkipReason, a.lastModelDrivenSkipBatch)
	}
}

func TestModelDrivenSkipDraftCarriesVerdict(t *testing.T) {
	draft := modelDrivenSkipDraft(3, compactionTarget{}, "reason text", "low_gain", 9, nil)
	if !draft.Skip || draft.InfoMessage != "Context checkpoint skipped: reason text" {
		t.Fatalf("skip draft = %+v, want skip with reason text", draft)
	}
	if draft.ModelDrivenSkipReason != "low_gain" || draft.ModelDrivenSkipBatch != 9 {
		t.Fatalf("skip draft verdict = reason=%q batch=%d, want low_gain/9", draft.ModelDrivenSkipReason, draft.ModelDrivenSkipBatch)
	}
}

func TestEstimatePostResetFixedRequestTokensAtLeastCurrentFixed(t *testing.T) {
	// P1-2: the projected side of the low-gain gate must account for the
	// post-reset full-injection tool surface. forceFullMCPToolInjection drops
	// cache-friendly mounts after apply, so the projected fixed surface can
	// never be smaller than the current one.
	a := newTestMainAgent(t, t.TempDir())
	current := a.estimateFixedRequestTokens()
	postReset := a.estimatePostResetFixedRequestTokens()
	if postReset < current {
		t.Fatalf("post-reset fixed tokens %d < current fixed tokens %d; full-injection surface cannot shrink", postReset, current)
	}
	if postReset <= 0 {
		t.Fatal("post-reset fixed surface must be positive")
	}
}

// TestMaybeStartModelDrivenBarrierReleasesStaleForegroundActivity pins the
// status-bar side of the barrier: the tool batch that just ended holds the
// shared main activity slot on "executing", and the barrier freezes the turn
// behind the checkpoint worker. The slot must be handed over, or the status bar
// keeps showing the stale executing state for the whole worker run.
func TestMaybeStartModelDrivenBarrierReleasesStaleForegroundActivity(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	for _, msg := range []message.Message{
		{Role: message.RoleUser, Content: "first request"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall("cc-1", tools.NameCompactContext)}},
	} {
		a.ctxMgr.Append(msg)
	}
	// The tool phase that just ended holds the foreground slot.
	a.emitActivity("main", ActivityExecuting, "1 tools")
	drainAgentEvents(a.outputCh)
	if !a.mainSlotForeground.Load() {
		t.Fatal("precondition: ActivityExecuting must hold the foreground slot")
	}

	ccID, args := testCompactContextCall()
	if _, err := a.tryArmModelDrivenCheckpoint(ccID, args); err != nil {
		t.Fatalf("tryArm: %v", err)
	}
	if !a.maybeStartModelDrivenBarrier() {
		t.Fatal("barrier should start for a pending model-driven request")
	}

	sawCompacting := false
	for _, ev := range drainAgentEvents(a.outputCh) {
		act, ok := ev.(AgentActivityEvent)
		if !ok {
			continue
		}
		if act.AgentID == "main" && act.Type == ActivityCompacting && act.Detail == compactionActivityDetail {
			sawCompacting = true
		}
		if act.AgentID == "main" && act.Type == ActivityExecuting {
			t.Fatalf("stale executing activity must not be re-emitted at the barrier: %+v", act)
		}
	}
	if !sawCompacting {
		t.Fatal("the barrier must hand the activity slot to compaction immediately")
	}
	if a.mainSlotForeground.Load() {
		t.Fatal("mainSlotForeground must be released while the frozen turn waits for the checkpoint")
	}
}

// TestModelDrivenCooldownExpiresDespitePerBatchRetries pins that the same-reason
// cooldown is a fixed window, not a sliding one: a model that retries on every
// batch must still reach a fresh preflight on the third batch. Refreshing the
// skip anchor on a cooldown hit would keep current-last pinned at 1 forever and
// the low-gain preflight would never run again.
func TestModelDrivenCooldownExpiresDespitePerBatchRetries(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	// A genuine low-gain skip settled at batch 10.
	a.settleModelDrivenSkip(modelDrivenSkipDraft(1, compactionTarget{}, "projected savings too small", modelDrivenSkipReasonLowGain, 10, nil))
	if a.lastModelDrivenSkipBatch != 10 {
		t.Fatalf("low-gain skip must anchor the cooldown at its own batch, got %d", a.lastModelDrivenSkipBatch)
	}

	// Batch 11: the model retries immediately and is cooled down.
	bundle := modelDrivenBarrierSnapshot{
		currentRequestBatch:       11,
		lastModelDrivenSkipReason: a.lastModelDrivenSkipReason,
		lastModelDrivenSkipBatch:  a.lastModelDrivenSkipBatch,
	}
	reason, skipReason, skip := a.modelDrivenIntervalCooldownVerdict(bundle)
	if !skip || skipReason != modelDrivenSkipReasonLowGain {
		t.Fatalf("retry one batch after a low-gain skip must be cooled down, got skip=%v reason=%q", skip, skipReason)
	}
	a.settleModelDrivenSkip(modelDrivenSkipDraft(2, compactionTarget{}, reason, skipReason, modelDrivenPolicySkipRecordBatch(skipReason, bundle.currentRequestBatch), nil))
	if a.lastModelDrivenSkipBatch != 10 {
		t.Fatalf("a cooldown hit must not slide the window, anchor batch = %d, want 10", a.lastModelDrivenSkipBatch)
	}

	// Batch 12: the cooldown has expired, so the request reaches the preflight
	// again instead of being short-circuited forever.
	if _, _, skip := a.modelDrivenIntervalCooldownVerdict(modelDrivenBarrierSnapshot{
		currentRequestBatch:       12,
		lastModelDrivenSkipReason: a.lastModelDrivenSkipReason,
		lastModelDrivenSkipBatch:  a.lastModelDrivenSkipBatch,
	}); skip {
		t.Fatal("the cooldown must expire two batches after the original low-gain skip even when the model retried every batch")
	}
}

// TestModelDrivenDuplicateSkipKeepsLowGainCooldownAnchor pins that a duplicate
// verdict leaves the cooldown state alone. No gate reads "duplicate" — the
// cooldown binds to low_gain and the duplicate verdict re-derives itself from
// the checkpoint fingerprint — so recording it would only overwrite a live
// low-gain anchor with a reason nothing consults, cutting that window short.
func TestModelDrivenDuplicateSkipKeepsLowGainCooldownAnchor(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	a.settleModelDrivenSkip(modelDrivenSkipDraft(1, compactionTarget{}, "projected savings too small", modelDrivenSkipReasonLowGain, 10, nil))

	// A duplicate request settles at a later batch: the low-gain anchor and its
	// reason must survive so the cooldown window still expires on its own clock.
	skipReason := modelDrivenSkipReasonDuplicate
	a.settleModelDrivenSkip(modelDrivenSkipDraft(2, compactionTarget{}, "no new work since the last applied checkpoint", skipReason, modelDrivenPolicySkipRecordBatch(skipReason, 11), nil))
	if a.lastModelDrivenSkipBatch != 10 || a.lastModelDrivenSkipReason != modelDrivenSkipReasonLowGain {
		t.Fatalf("duplicate skip must not touch the cooldown state, got batch=%d reason=%q, want 10/%q", a.lastModelDrivenSkipBatch, a.lastModelDrivenSkipReason, modelDrivenSkipReasonLowGain)
	}
	if _, _, skip := a.modelDrivenIntervalCooldownVerdict(modelDrivenBarrierSnapshot{
		currentRequestBatch:       11,
		lastModelDrivenSkipReason: a.lastModelDrivenSkipReason,
		lastModelDrivenSkipBatch:  a.lastModelDrivenSkipBatch,
	}); !skip {
		t.Fatal("the original low-gain cooldown must still hold at batch 11 after a duplicate skip")
	}
}

// TestModelDrivenCheckpointCarriesPriorCheckpointBody pins that consecutive
// model-driven resets do not erase the previous checkpoint's body: the prior
// checkpoint inside the archived head is carried forward verbatim, exactly as
// the usage-driven runner does.
func TestModelDrivenCheckpointDoesNotCarryPriorNaturalLanguageBody(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	// A prior checkpoint whose body is natural-language Markdown (a
	// usage-driven summary or a pre-typed model-driven checkpoint) is not
	// carried forward verbatim: recursion must not re-append whole previous
	// bodies. Machine-carryable state travels only through the typed state
	// block, and everything the new submission did not restate stays in the
	// archived history files.
	priorBody := "## Key Decisions\n- keep the archival profile for model-driven resets"
	prior := buildCompactionCheckpointMessage(priorBody, []string{"~/history-1.md"}, compactionSummaryModeModelDriven, nil)
	snapshot := []message.Message{
		{Role: message.RoleUser, Content: prior, IsCompactionSummary: true},
		{Role: message.RoleUser, Content: "next request"},
	}
	req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{ActiveObjective: "keep going", NextStep: "continue"}}
	bundle := modelDrivenBarrierSnapshot{snapshot: snapshot}

	summary := a.buildModelDrivenCheckpointSummary(bundle, snapshot, len(snapshot), req)
	if strings.Contains(summary, priorCheckpointSectionHeading) {
		t.Fatalf("model-driven checkpoint must not carry a natural-language previous-checkpoint section:\n%s", summary)
	}
	if strings.Contains(summary, "keep the archival profile for model-driven resets") {
		t.Fatalf("the previous natural-language body must not survive the reset:\n%s", summary)
	}
}

func TestModelDrivenCheckpointCarriesTypedStateAcrossGenerations(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	// The prior checkpoint's typed state block (decisions/open issues/evidence
	// references/stage) is the only machine channel across generations: the
	// merge keeps the fresh submission's items first and the carried ones
	// after, and the new checkpoint re-renders both the readable sections and
	// the typed block from the merged state.
	prior := buildCompactionCheckpointMessage("## Typed Checkpoint State\n- {\"evidence_refs\":[\"ev-old\"],\"stage_status\":\"completed\"}", nil, compactionSummaryModeModelDriven, nil)
	snapshot := []message.Message{{Role: message.RoleUser, Content: prior, IsCompactionSummary: true}, {Role: message.RoleUser, Content: "new request"}}
	req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{ActiveObjective: "continue", NextStep: "verify", EvidenceRefs: []string{"ev-new"}, StageStatus: "candidate", CheckpointKind: "provisional"}}
	summary := a.buildModelDrivenCheckpointSummary(modelDrivenBarrierSnapshot{snapshot: snapshot}, snapshot, len(snapshot), req)
	if !strings.Contains(summary, "evidence_refs") || !strings.Contains(summary, "ev-old") {
		t.Fatalf("typed checkpoint state was not carried: %s", summary)
	}
	// The fresh submission's items precede the carried ones and its stage
	// metadata overrides the carried one.
	state, ok := typedStateForTest(compactionSummaryBody(summary))
	if !ok {
		t.Fatalf("typed state missing from the new checkpoint: %s", summary)
	}
	if len(state.EvidenceRefs) != 2 || state.EvidenceRefs[0] != "ev-new" || state.EvidenceRefs[1] != "ev-old" {
		t.Fatalf("evidence refs = %v, want [ev-new ev-old]", state.EvidenceRefs)
	}
	if state.StageStatus != "candidate" || state.Kind != "provisional" {
		t.Fatalf("stage = status %q kind %q, want candidate/provisional", state.StageStatus, state.Kind)
	}
}

// TestModelDrivenCheckpointRenderReusesBuilderAndAddsExportedArchive pins the
// two-render contract: the shared builder renders identical summary/evidence
// content, and only the post-export render lists the freshly written archive —
// exactly once, with its topics.
func TestModelDrivenCheckpointRenderReusesBuilderAndAddsExportedArchive(t *testing.T) {
	sessionDir := t.TempDir()
	a := newTestMainAgent(t, sessionDir)
	a.sessionDir = sessionDir
	snapshot := []message.Message{
		{Role: message.RoleUser, Content: "first request"},
		{Role: message.RoleAssistant, Content: "working"},
		{Role: message.RoleUser, Content: "second request"},
	}
	req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{ActiveObjective: "keep going", NextStep: "continue"}}
	evidence := []evidenceItem{{Kind: evidenceToolError, Title: "Go build failure in internal/agent", Excerpt: "undefined: foo", Priority: 95, Sequence: 1}}
	bundle := modelDrivenBarrierSnapshot{snapshot: snapshot, sessionDir: sessionDir, evidenceItems: evidence}
	builder := a.newModelDrivenCheckpointBuilder(bundle, snapshot, 2, req)

	preflightContent, preflightStats := builder.render("")
	exported, _, _, err := a.exportCompactionHistory(snapshot[:2], 1, evidenceItemTopics(filterCompactionEvidenceForArchival(evidence)), a.captureCompactionArchiveMeta())
	if err != nil {
		t.Fatal(err)
	}
	postContent, postStats := builder.render(exported)

	if strings.Contains(preflightContent, filepath.Base(exported)) {
		t.Fatal("the pre-export render must not reference an archive that does not exist yet")
	}
	if got := strings.Count(postContent, filepath.Base(exported)); got != 1 {
		t.Fatalf("post-export render lists the archive %d times, want exactly 1:\n%s", got, postContent)
	}
	if !strings.Contains(postContent, "Go build failure in internal/agent") {
		t.Fatalf("the exported archive must be listed with its topics:\n%s", postContent)
	}
	if postStats.HistoryMapBytes <= preflightStats.HistoryMapBytes {
		t.Fatalf("history map bytes must grow with the exported archive: %d -> %d", preflightStats.HistoryMapBytes, postStats.HistoryMapBytes)
	}
	if preflightStats.ContinuationTokens != postStats.ContinuationTokens {
		t.Fatalf("continuation tokens must be identical across renders, got %d and %d", preflightStats.ContinuationTokens, postStats.ContinuationTokens)
	}
	// The two renders differ only by the history map line.
	if strings.Count(preflightContent, "## Active Objective") != 1 || strings.Count(postContent, "## Active Objective") != 1 {
		t.Fatal("both renders must contain the deterministic summary exactly once")
	}
}

// TestAppendDeferredModelDrivenToolResultCarriesFullMessageShape pins that the
// deferred emission path writes the same durable tool-message fields as the
// normal batch path: it used to hand-assemble a thin message and silently drop
// the payload/notes split, diff counters, audit, LSP reviews and provenance.
func TestAppendDeferredModelDrivenToolResultCarriesFullMessageShape(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	a.ctxMgr.Append(message.Message{
		Role:       message.RoleAssistant,
		ToolCalls:  []message.ToolCall{testToolCall("cc-1", tools.NameCompactContext)},
		Provenance: &message.MessageProvenance{ModelRef: "test/provider"},
	})
	payload := &ToolResultPayload{
		CallID:      "cc-1",
		Name:        tools.NameCompactContext,
		ArgsJSON:    "{}",
		Result:      "accepted",
		Payload:     "tool output",
		Notes:       []string{"a note"},
		Diff:        "--- a\n+++ b",
		DiffAdded:   3,
		DiffRemoved: 1,
		Duration:    1500 * time.Millisecond,
		Audit:       &message.ToolArgsAudit{EditSummary: "user tightened the args"},
		LSPReviews:  []message.LSPReview{{Path: "internal/agent/main.go"}},
		FileState:   &message.ToolFileState{Reads: []message.TrackedFileState{{Path: "internal/agent/main.go"}}},
	}
	a.appendDeferredModelDrivenToolResult(payload, "accepted", nil, false)

	snapshot := a.ctxMgr.Snapshot()
	last := snapshot[len(snapshot)-1]
	if last.Role != message.RoleTool || last.ToolCallID != "cc-1" {
		t.Fatalf("deferred append must write the tool message, got %+v", last)
	}
	if last.ToolPayload != "tool output" || len(last.ToolNotes) != 1 {
		t.Fatalf("payload/notes split lost: payload=%q notes=%v", last.ToolPayload, last.ToolNotes)
	}
	if last.ToolDiffAdded != 3 || last.ToolDiffRemoved != 1 {
		t.Fatalf("diff counters lost: +%d -%d", last.ToolDiffAdded, last.ToolDiffRemoved)
	}
	if last.Audit == nil || last.Audit.EditSummary != "user tightened the args" {
		t.Fatalf("tool args audit lost: %+v", last.Audit)
	}
	if len(last.LSPReviews) != 1 {
		t.Fatalf("LSP reviews lost: %v", last.LSPReviews)
	}
	if last.Provenance == nil || last.Provenance.ModelRef != "test/provider" {
		t.Fatalf("provenance lost: %+v", last.Provenance)
	}
	if last.ToolDurationMs != 1500 || last.FileState == nil || len(last.FileState.Reads) != 1 {
		t.Fatalf("duration/file state lost: %d %+v", last.ToolDurationMs, last.FileState)
	}
}

// TestModelDrivenResumeMergesQueuedUserMessageIntoContinuation pins the
// boundary between a model-driven checkpoint and real user messages that
// arrived while the checkpoint was pending: the queued messages are the
// current request, so the continuation runs on the compacted context with
// them merged into its single first request in arrival order (context summary
// first, then each fresh user message). The model acts on the messages
// immediately instead of finishing the checkpoint's archived objective first,
// and no "continue the current task" instruction is appended on top of the
// fresh input.
func TestModelDrivenResumeMergesQueuedUserMessageIntoContinuation(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	turnID := a.turn.ID
	target := compactionTarget{turnID: turnID, turnEpoch: a.turn.Epoch, sessionEpoch: a.sessionEpoch}
	a.startCompactionState(11, target, compactionTriggerModelDriven, continuationPlan{kind: compactionResumeModelDriven, turnID: turnID, turnEpoch: a.turn.Epoch, agentErrSourceID: "main"})
	pending := a.currentCompactionPendingCall()
	if pending == nil {
		t.Fatal("startCompactionState must arm a pending model-driven call")
	}
	// The apply landed: the slot is free and the compacted context is live,
	// with the checkpoint summary as its first message.
	a.resetCompactionState()
	a.ctxMgr.Append(message.Message{
		Role:                  message.RoleUser,
		Content:               "checkpoint summary",
		IsCompactionSummary:   true,
		CompactionSummaryMode: compactionSummaryModeModelDriven,
	})

	// Two real user messages arrived while the checkpoint was pending, in
	// arrival order.
	a.pendingUserMessages = []pendingUserMessage{
		{Content: "commit the pending changes", FromUser: true},
		{Content: "then update the changelog draft", FromUser: true},
	}

	if !a.resumePendingMainLLMAfterCompaction(pending, true) {
		t.Fatal("a successful model-driven apply must handle its own resume barrier")
	}
	if a.turn == nil || a.turn.ID != turnID {
		t.Fatal("the continuation must keep running in the checkpoint's own turn")
	}
	if len(a.pendingUserMessages) != 0 {
		t.Fatalf("queued user messages must all merge into the continuation request, got %d queued", len(a.pendingUserMessages))
	}
	if notice := a.pendingModelDrivenNotice; notice != "" {
		t.Fatalf("merged user input leads the continuation, so no 'continue the current task' notice may be appended, got %q", notice)
	}
	snapshot := a.ctxMgr.Snapshot()
	if len(snapshot) != 3 {
		t.Fatalf("continuation context must read [summary, first, second], got %d messages", len(snapshot))
	}
	if !snapshot[0].IsCompactionSummary {
		t.Fatalf("compaction summary must stay the first message of the continuation context, got %+v", snapshot[0])
	}
	if snapshot[1].Role != message.RoleUser || !strings.Contains(snapshot[1].Content, "commit the pending changes") {
		t.Fatalf("first queued user message must directly follow the summary, got %+v", snapshot[1])
	}
	if snapshot[2].Role != message.RoleUser || !strings.Contains(snapshot[2].Content, "then update the changelog draft") {
		t.Fatalf("second queued user message must follow the first in arrival order, got %+v", snapshot[2])
	}
}

// TestModelDrivenResumeWithEmptyQueueAppendsContinueInstruction pins the
// apply-success resume with no queued input: the compacted transcript ends in
// the summary message itself, so the continuation must append the "continue
// the current task" instruction — otherwise the model could read the summary
// as a finished reply and stop without continuing the task.
func TestModelDrivenResumeWithEmptyQueueAppendsContinueInstruction(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	turnID := a.turn.ID
	target := compactionTarget{turnID: turnID, turnEpoch: a.turn.Epoch, sessionEpoch: a.sessionEpoch}
	a.startCompactionState(11, target, compactionTriggerModelDriven, continuationPlan{kind: compactionResumeModelDriven, turnID: turnID, turnEpoch: a.turn.Epoch, agentErrSourceID: "main"})
	pending := a.currentCompactionPendingCall()
	if pending == nil {
		t.Fatal("startCompactionState must arm a pending model-driven call")
	}
	a.resetCompactionState()
	a.ctxMgr.Append(message.Message{
		Role:                  message.RoleUser,
		Content:               "checkpoint summary",
		IsCompactionSummary:   true,
		CompactionSummaryMode: compactionSummaryModeModelDriven,
	})

	if !a.resumePendingMainLLMAfterCompaction(pending, true) {
		t.Fatal("a successful model-driven apply must handle its own resume barrier")
	}
	if notice := a.pendingModelDrivenNotice; notice == "" || !strings.Contains(notice, "continue the current task") {
		t.Fatalf("an empty queue must not suppress the 'continue the current task' instruction, got %q", notice)
	}
	snapshot := a.ctxMgr.Snapshot()
	if len(snapshot) != 1 || !snapshot[0].IsCompactionSummary {
		t.Fatalf("an empty-queue resume must leave the compacted context unchanged, got %d messages", len(snapshot))
	}
}

func TestModelDrivenTerminalCandidateContinuesAfterApply(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	turnID := a.turn.ID
	target := compactionTarget{turnID: turnID, turnEpoch: a.turn.Epoch, sessionEpoch: a.sessionEpoch}
	a.startCompactionState(11, target, compactionTriggerModelDriven, continuationPlan{
		kind:      compactionResumeModelDriven,
		turnID:    turnID,
		turnEpoch: a.turn.Epoch,
	})
	pending := a.currentCompactionPendingCall()
	if pending == nil {
		t.Fatal("checkpoint must preserve its continuation")
	}
	a.resetCompactionState()
	a.ctxMgr.Append(message.Message{
		Role:                  message.RoleUser,
		Content:               "checkpoint summary",
		IsCompactionSummary:   true,
		CompactionSummaryMode: compactionSummaryModeModelDriven,
	})

	if !a.resumePendingMainLLMAfterCompaction(pending, true) {
		t.Fatal("terminal checkpoint apply must own the idle barrier")
	}
	if a.turn == nil || a.turn.ID != turnID {
		t.Fatal("checkpoint must keep the turn alive for remaining work or the final response")
	}
	if notice := a.pendingModelDrivenNotice; !strings.Contains(notice, "continue the current task") {
		t.Fatalf("checkpoint must schedule a continuation notice, got %q", notice)
	}
}

func TestModelDrivenTerminalCandidateContinuesForFreshUserInput(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	turnID := a.turn.ID
	target := compactionTarget{turnID: turnID, turnEpoch: a.turn.Epoch, sessionEpoch: a.sessionEpoch}
	a.startCompactionState(11, target, compactionTriggerModelDriven, continuationPlan{
		kind:      compactionResumeModelDriven,
		turnID:    turnID,
		turnEpoch: a.turn.Epoch,
	})
	pending := a.currentCompactionPendingCall()
	a.resetCompactionState()
	a.ctxMgr.Append(message.Message{
		Role:                  message.RoleUser,
		Content:               "checkpoint summary",
		IsCompactionSummary:   true,
		CompactionSummaryMode: compactionSummaryModeModelDriven,
	})
	a.pendingUserMessages = []pendingUserMessage{{Content: "new request", FromUser: true}}

	if !a.resumePendingMainLLMAfterCompaction(pending, true) {
		t.Fatal("terminal checkpoint with fresh input must resume the turn")
	}
	if a.turn == nil || a.turn.ID != turnID {
		t.Fatal("fresh user input must keep the checkpoint turn alive")
	}
	if len(a.pendingUserMessages) != 0 {
		t.Fatalf("fresh user input must merge into the continuation, got %d queued", len(a.pendingUserMessages))
	}
	snapshot := a.ctxMgr.Snapshot()
	if len(snapshot) != 2 || !strings.Contains(snapshot[1].Content, "new request") {
		t.Fatalf("fresh user input must be visible in continuation context, got %+v", snapshot)
	}
}

func TestModelDrivenCheckpointDuplicateUsesArgsAndRuntimeFingerprint(t *testing.T) {
	runtime := modelDrivenRuntimeInputFingerprint(modelDrivenRuntimeInput{
		todos: []tools.TodoItem{{ID: "task-1", Content: "finish", Status: "completed"}},
	})
	args := tools.CompactContextArgs{ActiveObjective: "finish", NextStep: "wait"}
	req := &modelDrivenCheckpointRequest{
		Args:            args,
		ArgsFingerprint: modelDrivenArgsFingerprint(args),
	}
	bundle := modelDrivenBarrierSnapshot{
		snapshot:                             []message.Message{{IsCompactionSummary: true, CompactionSummaryMode: compactionSummaryModeModelDriven}},
		runtimeStateFingerprint:              runtime,
		lastModelDrivenCheckpointFingerprint: modelDrivenCheckpointFingerprint(req.ArgsFingerprint, runtime),
	}
	if reason, skipReason, skip := (&MainAgent{}).modelDrivenCheckpointSkipVerdict(bundle, req); !skip || skipReason != modelDrivenSkipReasonDuplicate || !strings.Contains(reason, "unchanged") {
		t.Fatalf("unchanged checkpoint must be skipped as duplicate: reason=%q skip_reason=%q skip=%v", reason, skipReason, skip)
	}
	bundle.runtimeStateFingerprint = modelDrivenRuntimeInputFingerprint(modelDrivenRuntimeInput{
		todos: []tools.TodoItem{{ID: "task-1", Content: "finish", Status: "in_progress"}},
	})
	if _, skipReason, skip := (&MainAgent{}).modelDrivenCheckpointSkipVerdict(bundle, req); skip || skipReason == modelDrivenSkipReasonDuplicate {
		t.Fatalf("changed runtime state must not be treated as duplicate: skip_reason=%q skip=%v", skipReason, skip)
	}
}

// TestModelDrivenApplyRecordsFingerprintForDuplicateSkip pins the record side
// of the duplicate chain end to end: an applied checkpoint must write the
// fingerprint the skip verdict reads, so re-arming the same args with the
// same runtime state skips as duplicate. A test that only hand-sets the
// fingerprint asserts the verdict in isolation; this one fails if the apply
// stops recording it or records a different input.
func TestModelDrivenApplyRecordsFingerprintForDuplicateSkip(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	a.requestBatches.reserve(a.sessionEpoch, 0)

	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "verify parser"})
	callID := "checkpoint-1"
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{testToolCall(callID, tools.NameCompactContext)}})
	argsJSON := `{"active_objective":"verify parser","next_step":"run tests"}`
	if _, err := a.tryArmModelDrivenCheckpoint(callID, argsJSON); err != nil {
		t.Fatalf("tryArmModelDrivenCheckpoint: %v", err)
	}
	pending := a.pendingModelDriven
	if pending == nil {
		t.Fatal("arming must leave a pending model-driven call")
	}
	snapshot := a.ctxMgr.Snapshot()
	bundle := a.captureModelDrivenBarrierSnapshot(snapshot)
	draft := &compactionDraft{
		SummaryMode:                compactionSummaryModeModelDriven,
		RuntimeGeneration:          bundle.currentRequestBatch,
		RuntimeStateFingerprint:    bundle.runtimeStateFingerprint,
		ModelDrivenArgsFingerprint: pending.ArgsFingerprint,
		HeadSplit:                  len(snapshot),
		Index:                      1,
		PlanID:                     1,
		Target:                     compactionTarget{sessionEpoch: a.sessionEpoch},
		NewMessages: []message.Message{{
			Role:                  message.RoleUser,
			Content:               "checkpoint summary",
			IsCompactionSummary:   true,
			CompactionSummaryMode: compactionSummaryModeModelDriven,
		}},
	}
	if err := a.applyCompactionDraft(draft); err != nil {
		t.Fatalf("applyCompactionDraft: %v", err)
	}
	if a.lastModelDrivenCheckpointFingerprint == "" {
		t.Fatal("apply must record the checkpoint fingerprint")
	}

	// Re-arming the same args against the same runtime state must now skip
	// as a duplicate; the fingerprint the verdict compares against has to be
	// the one the apply just wrote, not a hand-set fixture.
	after := a.captureModelDrivenBarrierSnapshot(a.ctxMgr.Snapshot())
	retryCallID := "checkpoint-2"
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, RequestBatch: after.currentRequestBatch, ToolCalls: []message.ToolCall{testToolCall(retryCallID, tools.NameCompactContext)}})
	if _, err := a.tryArmModelDrivenCheckpoint(retryCallID, argsJSON); err != nil {
		t.Fatalf("re-arming identical args: %v", err)
	}
	retry := a.pendingModelDriven
	if retry == nil {
		t.Fatal("re-arming must leave a pending model-driven call")
	}
	if reason, skipReason, skip := a.modelDrivenCheckpointSkipVerdict(after, retry); !skip || skipReason != modelDrivenSkipReasonDuplicate {
		t.Fatalf("re-armed identical checkpoint must skip as duplicate: reason=%q skip_reason=%q skip=%v", reason, skipReason, skip)
	}
}

// TestModelDrivenResumeBackgroundCompletionDoesNotSuppressContinueInstruction
// pins the mergedUserInput criterion on its least obvious false positive: a
// queued background-task completion is FromUser=false and drains into the
// continuation as context, but it is not a fresh user request, so it must not
// suppress the "continue the current task" instruction (the queue-length
// heuristic this guards against used to treat any drained entry as merged
// user input).
func TestModelDrivenResumeBackgroundCompletionDoesNotSuppressContinueInstruction(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	turnID := a.turn.ID
	target := compactionTarget{turnID: turnID, turnEpoch: a.turn.Epoch, sessionEpoch: a.sessionEpoch}
	a.startCompactionState(11, target, compactionTriggerModelDriven, continuationPlan{kind: compactionResumeModelDriven, turnID: turnID, turnEpoch: a.turn.Epoch, agentErrSourceID: "main"})
	pending := a.currentCompactionPendingCall()
	if pending == nil {
		t.Fatal("startCompactionState must arm a pending model-driven call")
	}
	a.resetCompactionState()
	a.ctxMgr.Append(message.Message{
		Role:                  message.RoleUser,
		Content:               "checkpoint summary",
		IsCompactionSummary:   true,
		CompactionSummaryMode: compactionSummaryModeModelDriven,
	})
	a.pendingUserMessages = []pendingUserMessage{
		{Content: "Background task bg-1 finished (success).", Kind: message.KindBackgroundResult},
	}

	if !a.resumePendingMainLLMAfterCompaction(pending, true) {
		t.Fatal("a successful model-driven apply must handle its own resume barrier")
	}
	if len(a.pendingUserMessages) != 0 {
		t.Fatalf("the background completion must drain into the continuation, got %d queued", len(a.pendingUserMessages))
	}
	if notice := a.pendingModelDrivenNotice; notice == "" || !strings.Contains(notice, "continue the current task") {
		t.Fatalf("a system-generated entry must not suppress the 'continue the current task' instruction, got %q", notice)
	}
	snapshot := a.ctxMgr.Snapshot()
	if len(snapshot) != 2 || !strings.Contains(snapshot[1].Content, "Background task bg-1") {
		t.Fatalf("background completion must append as context after the summary, got %d messages", len(snapshot))
	}
}

// TestModelDrivenResumeKeepsIdleOnlySlashQueuedForIdleDrain pins that an
// idle-only slash command (/new, /resume*, /loop*, /mcp*) queued while the
// checkpoint was pending is not merged into the continuation: the drain
// re-queues it for the next idle drain (it is only valid while idle), nothing
// is appended, and the "continue the current task" instruction stays so the
// continuation request is not left to end on the bare summary.
func TestModelDrivenResumeKeepsIdleOnlySlashQueuedForIdleDrain(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	turnID := a.turn.ID
	target := compactionTarget{turnID: turnID, turnEpoch: a.turn.Epoch, sessionEpoch: a.sessionEpoch}
	a.startCompactionState(11, target, compactionTriggerModelDriven, continuationPlan{kind: compactionResumeModelDriven, turnID: turnID, turnEpoch: a.turn.Epoch, agentErrSourceID: "main"})
	pending := a.currentCompactionPendingCall()
	if pending == nil {
		t.Fatal("startCompactionState must arm a pending model-driven call")
	}
	a.resetCompactionState()
	a.ctxMgr.Append(message.Message{
		Role:                  message.RoleUser,
		Content:               "checkpoint summary",
		IsCompactionSummary:   true,
		CompactionSummaryMode: compactionSummaryModeModelDriven,
	})
	a.pendingUserMessages = []pendingUserMessage{{Content: "/new", FromUser: true}}

	if !a.resumePendingMainLLMAfterCompaction(pending, true) {
		t.Fatal("a successful model-driven apply must handle its own resume barrier")
	}
	if len(a.pendingUserMessages) != 1 || a.pendingUserMessages[0].Content != "/new" {
		t.Fatalf("an idle-only slash command must stay queued for the idle drain, got %v", a.pendingUserMessages)
	}
	if notice := a.pendingModelDrivenNotice; notice == "" || !strings.Contains(notice, "continue the current task") {
		t.Fatalf("an idle-only slash command must not suppress the 'continue the current task' instruction, got %q", notice)
	}
	snapshot := a.ctxMgr.Snapshot()
	if len(snapshot) != 1 || !snapshot[0].IsCompactionSummary {
		t.Fatalf("an idle-only slash command must not be appended to the continuation context, got %d messages", len(snapshot))
	}
}

// TestModelDrivenResumeAfterFailedApplyMergesQueuedUserInput pins the
// recheckGate=false resume (model-driven skip/failure): the checkpoint did not
// apply, but the same turn still continues automatically on the old context,
// so queued user messages merge into its first request exactly as on the
// applied path — they are the current request and must not trail the
// continuation's reply. The notice explaining why the checkpoint was not
// applied is still appended on top.
func TestModelDrivenResumeAfterFailedApplyMergesQueuedUserInput(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	turnID := a.turn.ID
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "original request"})
	target := compactionTarget{turnID: turnID, turnEpoch: a.turn.Epoch, sessionEpoch: a.sessionEpoch}
	a.startCompactionState(11, target, compactionTriggerModelDriven, continuationPlan{kind: compactionResumeModelDriven, turnID: turnID, turnEpoch: a.turn.Epoch, agentErrSourceID: "main"})
	pending := a.currentCompactionPendingCall()
	if pending == nil {
		t.Fatal("startCompactionState must arm a pending model-driven call")
	}
	a.resetCompactionState()
	a.pendingUserMessages = []pendingUserMessage{
		{Content: "commit the pending changes", FromUser: true},
		{Content: "then update the changelog draft", FromUser: true},
	}

	if !a.resumePendingMainLLMAfterCompaction(pending, false) {
		t.Fatal("a failed model-driven apply must still resume its own turn")
	}
	if len(a.pendingUserMessages) != 0 {
		t.Fatalf("queued user messages must merge into the failed-apply continuation, got %d queued", len(a.pendingUserMessages))
	}
	if notice := a.pendingModelDrivenNotice; notice == "" || !strings.Contains(notice, "Context checkpoint not applied") {
		t.Fatalf("the failed-apply continuation must carry the non-apply notice, got %q", notice)
	}
	snapshot := a.ctxMgr.Snapshot()
	if len(snapshot) != 3 {
		t.Fatalf("failed-apply continuation context must read [request, first, second], got %d messages", len(snapshot))
	}
	if snapshot[1].Role != message.RoleUser || !strings.Contains(snapshot[1].Content, "commit the pending changes") {
		t.Fatalf("first queued user message must follow the request, got %+v", snapshot[1])
	}
	if snapshot[2].Role != message.RoleUser || !strings.Contains(snapshot[2].Content, "then update the changelog draft") {
		t.Fatalf("second queued user message must follow the first in arrival order, got %+v", snapshot[2])
	}
}

// TestModelDrivenResumeAfterDiscardPreservesQueuedUserInput locks the
// discard/cancel resume asymmetry on purpose: a discarded checkpoint is a user
// abort, so the restarted request must NOT merge queued user messages — they
// wait for the next explicit round instead of auto-running behind a
// continuation the user just stopped. This is the mirror image of the
// failed-apply resume, which merges because nothing user-initiated was
// aborted there.
func TestModelDrivenResumeAfterDiscardPreservesQueuedUserInput(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	turnID := a.turn.ID
	a.ctxMgr.Append(message.Message{
		Role:                  message.RoleUser,
		Content:               "checkpoint summary",
		IsCompactionSummary:   true,
		CompactionSummaryMode: compactionSummaryModeModelDriven,
	})
	a.pendingUserMessages = []pendingUserMessage{{Content: "user follow-up after cancel", FromUser: true}}
	plan := continuationPlan{kind: compactionResumeModelDriven, turnID: turnID, turnEpoch: a.turn.Epoch, agentErrSourceID: "main"}

	if !a.resumeModelDrivenTurnAfterDiscard(plan) {
		t.Fatal("a discarded model-driven checkpoint must resume the deferred turn")
	}
	if len(a.pendingUserMessages) != 1 || a.pendingUserMessages[0].Content != "user follow-up after cancel" {
		t.Fatalf("queued user input must stay queued after a discard, got %v", a.pendingUserMessages)
	}
	if notice := a.pendingModelDrivenNotice; notice == "" || !strings.Contains(notice, "Context checkpoint not applied") {
		t.Fatalf("the discard resume must still carry the non-apply notice, got %q", notice)
	}
	snapshot := a.ctxMgr.Snapshot()
	if len(snapshot) != 1 || !snapshot[0].IsCompactionSummary {
		t.Fatalf("the discard resume must not append queued user input to the context, got %d messages", len(snapshot))
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
	a.newTurn()
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

func TestValidateModelDrivenCheckpointKindEvidenceRequirements(t *testing.T) {
	// Committed checkpoints are authoritative and must anchor to evidence.
	for _, args := range []tools.CompactContextArgs{
		{CheckpointKind: "committed"},
		{CheckpointKind: "committed", StageStatus: "completed"},
	} {
		err := validateModelDrivenCheckpointKind(args)
		if err == nil {
			t.Fatalf("expected evidence requirement for %#v", args)
		}
		// Rejection must point at the provisional escape hatch so the model
		// does not burn retries fabricating evidence the session never had.
		if !strings.Contains(err.Error(), "retry with checkpoint_kind=provisional") {
			t.Fatalf("rejection %q lacks the provisional downgrade guidance for %#v", err, args)
		}
	}
	// A provisional completed stage carries no claim-classification semantics
	// that require evidence; only observed claims (checked in the observed
	// validators) and committed checkpoints must anchor to evidence IDs,
	// which the model may not even have in view before the first checkpoint.
	if err := validateModelDrivenCheckpointKind(tools.CompactContextArgs{StageStatus: "completed"}); err != nil {
		t.Fatalf("provisional completed stage without evidence should be accepted: %v", err)
	}
	if err := validateModelDrivenCheckpointKind(tools.CompactContextArgs{CheckpointKind: "committed", StageStatus: "completed", EvidenceRefs: []string{"e-1"}}); err != nil {
		t.Fatalf("valid committed checkpoint rejected: %v", err)
	}
	if err := validateModelDrivenCheckpointKind(tools.CompactContextArgs{CheckpointKind: "committed", EvidenceRefs: []string{"e-1"}}); err == nil {
		t.Fatal("committed checkpoint without completed stage should be rejected")
	}
}

func TestValidateModelDrivenEvidenceRefsUsesFullTracker(t *testing.T) {
	a := &MainAgent{}
	a.evidence.add(evidenceItem{Kind: evidenceToolDiff, Key: "full-evidence", Excerpt: "important"})
	if err := a.validateModelDrivenEvidenceRefs([]string{evidenceItemID(a.evidence.snapshot()[0])}); err != nil {
		t.Fatalf("full tracker evidence was rejected: %v", err)
	}
}

func TestValidateModelDrivenClaimKindsObservedRequiresEvidence(t *testing.T) {
	args := tools.CompactContextArgs{
		Completed:  []string{"tests pass"},
		ClaimKinds: map[string]string{"tests pass": "observed"},
	}
	if err := validateModelDrivenCheckpointKind(args); err == nil {
		t.Fatal("observed claim without evidence should be rejected")
	}
}

// A rejection whose remedy depends on runtime state must carry that state: the
// observed-without-evidence error names the evidence IDs this context actually
// resolves, so the model can cite one or reclassify the claim instead of
// guessing what "observed" required.
func TestExplainCheckpointRejectionHintsResolvableEvidence(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.evidence.add(evidenceItem{Kind: evidenceToolDiff, Key: "resolvable-diff", Excerpt: "diff"})
	id := evidenceItemID(a.evidence.snapshot()[0])

	err := a.explainCheckpointRejection(checkpointClaimNeedsEvidenceError{claim: "tests pass"})
	if err == nil {
		t.Fatal("observed claim without evidence must stay rejected")
	}
	for _, want := range []string{"tests pass", id, "claim_evidence", "top-level evidence_refs", "derived/assumed/proposed"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("rejection hint %q must contain %q", err, want)
		}
	}
}

// Only the evidence check needs runtime state appended; every other violation
// text is already complete, so wrapping it would bury the reason.
func TestExplainCheckpointRejectionLeavesOtherErrorsUnchanged(t *testing.T) {
	a := &MainAgent{}
	original := errors.New("committed compact_context requires stage_status=completed")
	if got := a.explainCheckpointRejection(original); got != original {
		t.Fatalf("non-evidence rejection must pass through unchanged, got %v", got)
	}
}

func TestResolvableEvidenceHintOmitsNegativeAndInvalidated(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.evidence.add(evidenceItem{Kind: evidenceToolDiff, Key: "positive", Excerpt: "diff"})
	a.evidence.add(evidenceItem{Kind: evidenceToolError, Key: "negative", Excerpt: "failed"})
	a.evidence.add(evidenceItem{Kind: evidenceToolDiff, Key: "stale", Excerpt: "old", Validity: evidenceValidityInvalidated})
	byKey := make(map[string]string)
	for _, item := range a.evidence.snapshot() {
		byKey[item.Key] = evidenceItemID(item)
	}

	hint := a.resolvableEvidenceHint()
	if !strings.Contains(hint, byKey["positive"]) {
		t.Fatalf("hint must list the positive evidence ID, got %q", hint)
	}
	if strings.Contains(hint, byKey["negative"]) || strings.Contains(hint, byKey["stale"]) {
		t.Fatalf("hint must omit negative and invalidated evidence, got %q", hint)
	}
}

// A checkpoint evidence pack is the only place an archived item's ID remains
// citable, and its rendered classification and validity must filter the hint
// the same way live records do, or following the hint would trade one
// rejection for another.
func TestResolvableEvidenceHintOmitsNegativeAndInvalidatedPackIDs(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry(), ctxMgr: ctxmgr.NewManager(10000, 1000)}
	positive := buildEvidenceItem(evidenceToolDiff, "positive", "needed", "tool", "diff")
	negative := buildEvidenceItem(evidenceToolError, "negative", "needed", "tool", "failed")
	invalidated := buildEvidenceItem(evidenceToolDiff, "invalidated", "needed", "tool", "stale")
	invalidated.Validity = evidenceValidityInvalidated
	checkpoint := buildCompactionCheckpointMessage("## Current User Request\n- continue", nil, compactionSummaryModeModelDriven, []evidenceItem{positive, negative, invalidated})
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: checkpoint, IsCompactionSummary: true})

	hint := a.resolvableEvidenceHint()
	if !strings.Contains(hint, evidenceItemID(positive)) {
		t.Fatalf("hint must list the positive pack ID, got %q", hint)
	}
	for _, item := range []evidenceItem{negative, invalidated} {
		if strings.Contains(hint, evidenceItemID(item)) {
			t.Fatalf("hint must omit pack evidence %s %q, got %q", item.Kind, evidenceItemID(item), hint)
		}
	}
}

func TestResolvableEvidenceHintWithoutEvidence(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	hint := a.resolvableEvidenceHint()
	if !strings.Contains(hint, "no evidence ID is resolvable in this context") {
		t.Fatalf("hint without evidence must say none is resolvable, got %q", hint)
	}
}

// The unknown-ID exit must be as self-correcting as the observed path: a
// rejection without the resolvable menu left the model to regenerate an
// ev-<12hex> shape from memory (the reported session's invented-ID failures).
func TestUnknownEvidenceRefHintListsResolvableEvidence(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.evidence.add(evidenceItem{Kind: evidenceToolDiff, Key: "unknown-ref", Excerpt: "diff"})
	id := evidenceItemID(a.evidence.snapshot()[0])
	err := a.validateModelDrivenEvidenceRefs([]string{"ev-000000000000"})
	if err == nil {
		t.Fatal("invented evidence ID must stay rejected")
	}
	for _, want := range []string{"ev-000000000000", id, "evidence IDs resolvable in this context"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("unknown-ID rejection %q must carry the resolvable menu entry %q", err, want)
		}
	}
	if err := a.validateModelDrivenEvidenceRefs([]string{id}); err != nil {
		t.Fatalf("live tracker ID must stay resolvable: %v", err)
	}
}

func TestUnknownEvidenceRefHintListsPackIDs(t *testing.T) {
	positive := buildEvidenceItem(evidenceToolDiff, "unknown-pack", "needed", "tool", "diff")
	checkpoint := buildCompactionCheckpointMessage("## Current User Request\n- continue", nil, compactionSummaryModeModelDriven, []evidenceItem{positive})
	a := &MainAgent{tools: tools.NewRegistry(), ctxMgr: ctxmgr.NewManager(10000, 1000)}
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: checkpoint, IsCompactionSummary: true})
	err := a.validateModelDrivenEvidenceRefs([]string{"ev-000000000000"})
	if err == nil {
		t.Fatal("invented ID must stay rejected with a pack in context")
	}
	if !strings.Contains(err.Error(), evidenceItemID(positive)) {
		t.Fatalf("unknown-ID rejection %q must list the pack-resolvable ID %q", err, evidenceItemID(positive))
	}
}

func TestUnknownEvidenceRefHintWithoutEvidence(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	hint := a.unknownEvidenceRefHint()
	if !strings.Contains(hint, "no evidence ID is resolvable in this context") {
		t.Fatalf("hint without evidence must say none is resolvable, got %q", hint)
	}
	if !strings.Contains(hint, "derived/assumed/proposed") {
		t.Fatalf("empty menu must keep the downgrade path actionable, got %q", hint)
	}
	if strings.Contains(hint, "resolvable in this context: ") {
		t.Fatalf("hint without evidence must not render an ID list, got %q", hint)
	}
}

// End-to-end through the arm path: the rejection the model sees must be the
// hinted one, and a rejected request must not arm a pending checkpoint.
func TestTryArmModelDrivenCheckpointHintsResolvableEvidence(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	a.evidence.add(evidenceItem{Kind: evidenceToolDiff, Key: "arm-hint-diff", Excerpt: "diff"})
	id := evidenceItemID(a.evidence.snapshot()[0])
	ccID := "cc-observed"
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall(ccID, tools.NameCompactContext)}})

	args := `{"active_objective":"a","next_step":"b","completed":["tests pass"],"claim_kinds":{"tests pass":"observed"}}`
	_, err := a.tryArmModelDrivenCheckpoint(ccID, args)
	if err == nil {
		t.Fatal("observed claim without claim_evidence must be rejected")
	}
	if !strings.Contains(err.Error(), id) {
		t.Fatalf("rejection %q must name the resolvable evidence ID %q", err, id)
	}
	if a.pendingModelDriven != nil {
		t.Fatal("a rejected request must not arm a pending checkpoint")
	}
}

// Same guarantee for the unknown-ID exit: a rejected evidence_refs entry must
// carry the resolvable menu, and a rejected request must never arm a pending
// checkpoint.
func TestTryArmModelDrivenCheckpointUnknownEvidenceRefHintsResolvable(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	a.evidence.add(evidenceItem{Kind: evidenceToolDiff, Key: "unknown-arm", Excerpt: "diff"})
	id := evidenceItemID(a.evidence.snapshot()[0])
	ccID := "cc-unknown"
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall(ccID, tools.NameCompactContext)}})

	args := `{"active_objective":"a","next_step":"b","evidence_refs":["ev-000000000000"]}`
	_, err := a.tryArmModelDrivenCheckpoint(ccID, args)
	if err == nil {
		t.Fatal("unknown evidence ID must be rejected at arm time")
	}
	if !strings.Contains(err.Error(), id) {
		t.Fatalf("rejection %q must name the resolvable evidence ID %q", err, id)
	}
	if a.pendingModelDriven != nil {
		t.Fatal("a rejected request must not arm a pending checkpoint")
	}
}

func TestValidateObservedClaimEvidenceMustBeListed(t *testing.T) {
	args := tools.CompactContextArgs{
		Completed: []string{"tests pass"}, EvidenceRefs: []string{"ev-1"},
		ClaimKinds:    map[string]string{"tests pass": "observed"},
		ClaimEvidence: map[string][]string{"tests pass": {"ev-2"}},
	}
	if err := validateModelDrivenCheckpointKind(args); err == nil {
		t.Fatal("observed claim evidence outside evidence_refs should be rejected")
	}
}

func TestValidateObservedClaimEvidenceRejectsErrorEvidence(t *testing.T) {
	a := &MainAgent{}
	a.evidence.add(evidenceItem{Kind: evidenceToolError, Key: "err", Excerpt: "failed"})
	id := evidenceItemID(a.evidence.snapshot()[0])
	args := tools.CompactContextArgs{ClaimKinds: map[string]string{"tests pass": "observed"}, ClaimEvidence: map[string][]string{"tests pass": {id}}}
	if err := a.validateObservedClaimEvidence(args); err == nil {
		t.Fatal("error evidence should not support observed completion")
	}
}

func TestValidateCommittedEvidenceRejectsErrorEvidence(t *testing.T) {
	a := &MainAgent{}
	a.evidence.add(evidenceItem{Kind: evidenceToolError, Key: "err", Excerpt: "failed"})
	id := evidenceItemID(a.evidence.snapshot()[0])
	if err := a.validateCommittedEvidence(tools.CompactContextArgs{CheckpointKind: "committed", EvidenceRefs: []string{id}}); err == nil {
		t.Fatal("committed checkpoint should reject error evidence")
	}
}

func TestModelDrivenProposalTransitionUpdatesMetadata(t *testing.T) {
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

// assertNoFakeTopLevelSection fails when rendered contains a line that opens a
// top-level checkpoint section at column zero ("## ".."###### "), the exact
// structure the next generation's typed/heading parsers key on.
func assertNoFakeTopLevelSection(t *testing.T, label, rendered string) {
	t.Helper()
	for line := range strings.SplitSeq(rendered, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "### ") ||
			strings.HasPrefix(trimmed, "#### ") || strings.HasPrefix(trimmed, "##### ") ||
			strings.HasPrefix(trimmed, "###### ") {
			t.Fatalf("%s leaked a top-level heading: %q\nrendered:\n%s", label, line, rendered)
		}
	}
}

func TestCheckpointInlineValueFlattensMultilineHeadingValues(t *testing.T) {
	if got := checkpointInlineValue("plain text"); got != "plain text" {
		t.Fatalf("plain value changed: %q", got)
	}
	if got := checkpointInlineValue("#tag"); got != "#tag" {
		t.Fatalf("hash without trailing space must survive: %q", got)
	}
	if got := checkpointInlineValue("## heading stripped"); got != "heading stripped" {
		t.Fatalf("single-line heading marker must be stripped: %q", got)
	}
	multi := "## Injected Section\nstill a line"
	got := checkpointInlineValue(multi)
	if strings.Contains(got, "\n") || strings.HasPrefix(got, "## ") {
		t.Fatalf("multiline value must flatten and strip headings: %q", got)
	}
	assertNoFakeTopLevelSection(t, "checkpointInlineValue", got)
}

func TestModelDrivenClaimAndStageRenderersEscapeMultilineHeadingValues(t *testing.T) {
	// Model-authored claim names, kinds, stage metadata and planned file paths
	// may embed newlines plus "## " at column zero. The renderers must strip
	// the heading markers and flatten the value, so no value can open a fake
	// top-level section that the next generation's parsers would read as a
	// real heading.
	evidence := renderClaimEvidenceSection(map[string][]string{
		"## Claim One": {"ev-1", "## Fake Evidence"},
		"plain":        {"ev-2"},
	})
	assertNoFakeTopLevelSection(t, "claim evidence", evidence)
	if !strings.Contains(evidence, "Claim One") {
		t.Fatalf("flattened claim name must still read:\n%s", evidence)
	}

	kinds := renderClaimKindsSection(map[string]string{
		"## Fake Claim": "## observed",
	})
	assertNoFakeTopLevelSection(t, "claim kinds", kinds)
	if !strings.Contains(kinds, "Fake Claim | kind: observed") {
		t.Fatalf("flattened kind line = %q", kinds)
	}

	stage := renderModelDrivenStageSection("## stage-id", "## completed\nstill there", "committed")
	assertNoFakeTopLevelSection(t, "stage", stage)
	if !strings.Contains(stage, "Stage ID: stage-id") || !strings.Contains(stage, "Stage status: completed still there") {
		t.Fatalf("flattened stage line = %q", stage)
	}

	planned := renderPlannedStateFilesSection([]string{"docs/plan.md", "## Fake Path\nsecond line"})
	assertNoFakeTopLevelSection(t, "planned state files", planned)
	if !strings.Contains(planned, "Fake Path second line") {
		t.Fatalf("flattened planned path = %q", planned)
	}

	stateFiles := renderStateFilesSection([]string{"src/main.go", "## Fake\nsrc/other.go"})
	assertNoFakeTopLevelSection(t, "state files", stateFiles)
}

func TestRenderFallbackSummarySectionsDropsEmptyCheckpointStage(t *testing.T) {
	rendered := renderFallbackSummarySections([]fallbackSummarySection{
		{"## Current User Request", "- keep going"},
		{"## Checkpoint Stage", renderModelDrivenStageSection("", "", "")},
	}, nil)
	if strings.Contains(rendered, "## Checkpoint Stage") {
		t.Fatalf("empty stage section must be omitted, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "No stage metadata reported by the model") {
		t.Fatalf("empty stage body must not survive, got:\n%s", rendered)
	}

	kept := renderFallbackSummarySections([]fallbackSummarySection{
		{"## Current User Request", "- keep going"},
		{"## Checkpoint Stage", renderModelDrivenStageSection("stage-1", "completed", "committed")},
	}, nil)
	if !strings.Contains(kept, "## Checkpoint Stage") || !strings.Contains(kept, "Stage ID: stage-1") {
		t.Fatalf("reported stage metadata must be kept, got:\n%s", kept)
	}
}

func TestModelDrivenRuntimeStateFingerprintIgnoresSubAgentOrder(t *testing.T) {
	base := modelDrivenBarrierSnapshot{
		todos: []tools.TodoItem{{ID: "t1", Content: "first"}},
		subAgents: []SubAgentInfo{
			{InstanceID: "inst-b", TaskID: "task-b", State: "running"},
			{InstanceID: "inst-a", TaskID: "task-a", State: "running"},
		},
		backgroundObjects:  []recovery.BackgroundObjectState{{ID: "bg-1"}},
		queuedUserMessages: []message.Message{{Role: message.RoleUser, Content: "queued"}},
		evidenceItems:      []evidenceItem{{Kind: evidenceToolDiff, Key: "ev", Excerpt: "diff"}},
	}
	shuffled := base
	shuffled.subAgents = []SubAgentInfo{
		{InstanceID: "inst-a", TaskID: "task-a", State: "running"},
		{InstanceID: "inst-b", TaskID: "task-b", State: "running"},
	}
	if got, want := modelDrivenRuntimeStateFingerprint(base), modelDrivenRuntimeStateFingerprint(shuffled); got != want {
		t.Fatalf("fingerprint changed with unchanged live SubAgent order: %s != %s", got, want)
	}

	// A genuine state change must still be detected.
	changed := base
	changed.subAgents = []SubAgentInfo{
		{InstanceID: "inst-a", TaskID: "task-a", State: "completed"},
		{InstanceID: "inst-b", TaskID: "task-b", State: "running"},
	}
	if got, want := modelDrivenRuntimeStateFingerprint(base), modelDrivenRuntimeStateFingerprint(changed); got == want {
		t.Fatal("fingerprint must change when a live SubAgent state changes")
	}
}

// TestMaybeStartModelDrivenBarrierTurnNilSettlesProposal pins the barrier
// no-turn settle: a pending request whose turn died before the tool-batch
// barrier must not leave the proposal record stuck in preparing/accepted — the
// barrier settles it through the normal terminal (cancelled) path so the
// persisted record and a later restore never report intent that can no longer
// be applied.
func TestMaybeStartModelDrivenBarrierTurnNilSettlesProposal(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.modelDrivenProposal = modelDrivenProposalState{
		requestID: "cc-1",
		status:    modelDrivenProposalAccepted,
		reason:    "accepted by runtime validation",
		argsJSON:  `{"active_objective":"a","next_step":"b"}`,
	}
	a.pendingModelDriven = &modelDrivenCheckpointRequest{
		ToolCallID: "cc-1",
		Args:       tools.CompactContextArgs{ActiveObjective: "a", NextStep: "b"},
	}
	if a.turn != nil {
		t.Fatal("fixture requires a turn-less agent")
	}
	if a.maybeStartModelDrivenBarrier() {
		t.Fatal("barrier must not start a worker when the turn is gone")
	}
	if a.pendingModelDriven != nil {
		t.Fatal("pending request must be consumed by the barrier")
	}
	if a.modelDrivenProposal.status != CompactionStatusCancelled {
		t.Fatalf("proposal status = %q, want cancelled (terminal)", a.modelDrivenProposal.status)
	}
	if a.modelDrivenProposal.argsJSON != "" {
		t.Fatalf("terminal settle must clear the audit args copy, got %q", a.modelDrivenProposal.argsJSON)
	}
}

// TestValidateCommittedEvidenceRejectsEscalateEvidence pins the committed
// evidence consistency rule: escalate is a negative outcome record (an open
// intervention request) exactly like tool_error and done_rejected, so
// committed checkpoints reject it uniformly instead of letting an
// escalated-but-unfinished stage read as completed.
func TestValidateCommittedEvidenceRejectsEscalateEvidence(t *testing.T) {
	a := &MainAgent{}
	a.evidence.add(evidenceItem{Kind: evidenceEscalate, Key: "esc", Excerpt: "subagent requests intervention"})
	id := evidenceItemID(a.evidence.snapshot()[0])
	if err := a.validateCommittedEvidence(tools.CompactContextArgs{CheckpointKind: "committed", EvidenceRefs: []string{id}}); err == nil {
		t.Fatal("committed checkpoint should reject escalate evidence")
	}
}

// TestValidateCommittedEvidenceJudgesArchivedPackEvidenceByRenderedKind pins
// the archived half of the cross-generation evidence chain: after an apply
// archives the negative record's source messages, its ID survives only inside
// the checkpoint evidence pack. The pack now renders the machine category, so
// committed acceptance judges carried references by the same rules as live
// ones — an archived escalate/tool_error cannot upgrade a stage to committed —
// while a record that only proves ID existence (an older pack format without a
// classification) cannot support the committed upgrade either.
func TestValidateCommittedEvidenceJudgesArchivedPackEvidenceByRenderedKind(t *testing.T) {
	newAgentWithCheckpoint := func(item evidenceItem, content string) (*MainAgent, string) {
		t.Helper()
		checkpoint := buildCompactionCheckpointMessage("## Current User Request\n- continue", nil, compactionSummaryModeModelDriven, []evidenceItem{item})
		a := &MainAgent{tools: tools.NewRegistry(), ctxMgr: ctxmgr.NewManager(10000, 1000)}
		a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: checkpoint, IsCompactionSummary: true})
		return a, checkpoint
	}

	// Archived negative records stay rejected for committed and for observed
	// claims alike.
	escalate := buildEvidenceItem(evidenceEscalate, "subagent intervention open", "needed", "tool", "requests intervention")
	a, checkpoint := newAgentWithCheckpoint(escalate, "")
	if !strings.Contains(checkpoint, "Evidence Kind: escalate") {
		t.Fatalf("checkpoint pack must render the machine category:\n%s", checkpoint)
	}
	escalateID := evidenceItemID(escalate)
	if err := a.validateCommittedEvidence(tools.CompactContextArgs{CheckpointKind: "committed", EvidenceRefs: []string{escalateID}}); err == nil {
		t.Fatal("committed checkpoint over an archived escalate record must be rejected")
	}
	if err := a.validateObservedClaimEvidence(tools.CompactContextArgs{
		ClaimKinds:    map[string]string{"tests pass": "observed"},
		ClaimEvidence: map[string][]string{"tests pass": {escalateID}},
	}); err == nil {
		t.Fatal("observed claim over an archived escalate record must be rejected")
	}

	toolError := buildEvidenceItem(evidenceToolError, "Go build failure", "blocker", "tool", "undefined: foo")
	b, _ := newAgentWithCheckpoint(toolError, "")
	toolErrorID := evidenceItemID(toolError)
	if err := b.validateCommittedEvidence(tools.CompactContextArgs{CheckpointKind: "committed", EvidenceRefs: []string{toolErrorID}}); err == nil {
		t.Fatal("committed checkpoint over an archived tool_error record must be rejected")
	}

	// A positive archived record with a rendered kind still supports the
	// committed upgrade: that is the primary cross-generation use case.
	diff := buildEvidenceItem(evidenceToolDiff, "Recent code diff", "needed", "tool", "diff content")
	c, _ := newAgentWithCheckpoint(diff, "")
	diffID := evidenceItemID(diff)
	if err := c.validateCommittedEvidence(tools.CompactContextArgs{CheckpointKind: "committed", EvidenceRefs: []string{diffID}}); err != nil {
		t.Fatalf("committed checkpoint over an archived positive record must validate: %v", err)
	}

	// Presence-only proof (an older pack format without the kind line) must
	// not upgrade to committed, but still resolves for evidence_refs and stays
	// allowed for observed claims the pack's full record can back.
	_, diffCheckpoint := newAgentWithCheckpoint(diff, "")
	var legacyLines []string
	for line := range strings.SplitSeq(diffCheckpoint, "\n") {
		if !strings.HasPrefix(line, "Evidence Kind: ") {
			legacyLines = append(legacyLines, line)
		}
	}
	legacyContent := strings.Join(legacyLines, "\n")
	legacyAgent := &MainAgent{tools: tools.NewRegistry(), ctxMgr: ctxmgr.NewManager(10000, 1000)}
	legacyAgent.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: legacyContent, IsCompactionSummary: true})
	if err := legacyAgent.validateModelDrivenEvidenceRefs([]string{diffID}); err != nil {
		t.Fatalf("presence-only pack ID must still resolve as an evidence reference: %v", err)
	}
	if err := legacyAgent.validateObservedClaimEvidence(tools.CompactContextArgs{
		ClaimKinds:    map[string]string{"tests pass": "observed"},
		ClaimEvidence: map[string][]string{"tests pass": {diffID}},
	}); err != nil {
		t.Fatalf("presence-only pack ID must stay allowed for observed claims: %v", err)
	}
	if err := legacyAgent.validateCommittedEvidence(tools.CompactContextArgs{CheckpointKind: "committed", EvidenceRefs: []string{diffID}}); err == nil {
		t.Fatal("committed checkpoint must not upgrade over ID-existence-only proof")
	}
}

// TestValidateModelDrivenEvidenceRefsResolvesCheckpointPackID pins the
// previous-generation resolution half of the cross-generation evidence chain:
// after an apply rebuilds the runtime tracker from the surviving messages, an
// evidence ID that only survives inside the rendered evidence pack of a
// checkpoint still in the context must validate — the checkpoint message is
// the only place the archived item's ID remains visible. Invented IDs stay
// rejected.
func TestValidateModelDrivenEvidenceRefsResolvesCheckpointPackID(t *testing.T) {
	item := buildEvidenceItem(evidenceToolDiff, "Recent code diff", "needed", "tool", "diff content")
	checkpoint := buildCompactionCheckpointMessage(
		"## Current User Request\n- continue",
		nil, compactionSummaryModeModelDriven, []evidenceItem{item})
	a := &MainAgent{tools: tools.NewRegistry(), ctxMgr: ctxmgr.NewManager(10000, 1000)}
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: checkpoint, IsCompactionSummary: true})
	// The tracker is empty: the item's source messages were archived.
	id := evidenceItemID(item)
	if err := a.validateModelDrivenEvidenceRefs([]string{id}); err != nil {
		t.Fatalf("checkpoint-pack evidence ID must validate after its source was archived: %v", err)
	}
	if err := a.validateObservedClaimEvidence(tools.CompactContextArgs{
		ClaimKinds:    map[string]string{"tests pass": "observed"},
		ClaimEvidence: map[string][]string{"tests pass": {id}},
	}); err != nil {
		t.Fatalf("observed claim over checkpoint-pack evidence must validate: %v", err)
	}
	if err := a.validateModelDrivenEvidenceRefs([]string{"ev-000000000000"}); err == nil {
		t.Fatal("invented evidence ID must still be rejected")
	}
}

// TestE2EModelDrivenEvidenceIDSurvivesApplyIntoNextGeneration drives the full
// reported chain: a real durable apply archives the runtime evidence, rebuilds
// the tracker empty, and leaves the checkpoint's evidence pack as the only
// place its Evidence IDs remain visible. The next generation's arm validation
// must accept those IDs (resolution against the checkpoint in context) instead
// of starting a rejection chain.
func TestE2EModelDrivenEvidenceIDSurvivesApplyIntoNextGeneration(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	a.requestBatches.reserve(a.sessionEpoch, 0)

	// Round 1: real context whose tool diff becomes runtime evidence, then an
	// archival model-driven apply renders the evidence pack inside the
	// checkpoint message.
	callID := "call-diff-1"
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "refactor the loader"})
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: callID, Name: tools.NameEdit, Args: mustJSONRaw(t, map[string]any{"path": "loader.go"})}}})
	a.ctxMgr.Append(message.Message{Role: message.RoleTool, ToolCallID: callID, ToolStatus: "success", Content: "applied", ToolDiff: "*** Update File: loader.go"})
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "keep the public API stable"})
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, Content: "checking"})

	// Rebuild the evidence candidate list the way the event loop does at the
	// barrier (captureModelDrivenBarrierSnapshot), then render a model-driven
	// checkpoint that carries the archival evidence pack.
	a.resetRuntimeEvidenceFromMessages(a.ctxMgr.Snapshot())
	evidenceItems := filterCompactionEvidenceForArchival(a.evidenceItemsForCompaction(a.ctxMgr.GetMaxTokens()))
	if len(evidenceItems) == 0 {
		t.Fatal("fixture must produce archival evidence items")
	}
	var packID string
	for _, item := range evidenceItems {
		packID = evidenceItemID(item)
	}
	snapshot := a.ctxMgr.Snapshot()
	req := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{ActiveObjective: "refactor the loader", NextStep: "continue", EvidenceRefs: []string{packID}}}
	summary := a.buildModelDrivenCheckpointSummary(modelDrivenBarrierSnapshot{snapshot: snapshot}, snapshot, len(snapshot), req)
	content := buildCompactionCheckpointMessage(summary, nil, compactionSummaryModeModelDriven, evidenceItems)
	// The archival model-driven apply archives the whole head; after it only
	// the checkpoint survives, so the archived evidence cannot re-enter the
	// runtime tracker.
	draft := &compactionDraft{
		NewMessages:    []message.Message{{Role: message.RoleUser, Content: content, IsCompactionSummary: true, CompactionSummaryMode: compactionSummaryModeModelDriven}},
		HeadSplit:      len(snapshot),
		Index:          1,
		AbsHistoryPath: filepath.Join(a.sessionDir, "history-1.md"),
		SummaryMode:    compactionSummaryModeModelDriven,
		PlanID:         1,
		Target:         compactionTarget{sessionEpoch: a.sessionEpoch},
	}
	if err := a.applyCompactionDraft(draft); err != nil {
		t.Fatalf("apply round-1 checkpoint: %v", err)
	}

	// The apply rebuilt the runtime evidence from the surviving messages; the
	// checkpoint itself is skipped, so the pack's item is no longer a runtime
	// candidate.
	for _, item := range a.evidence.snapshot() {
		if evidenceItemID(item) == packID {
			t.Fatal("archived evidence must not survive as a runtime candidate after the apply")
		}
	}

	// Next generation's arm validation references the previous pack's ID. It
	// must resolve against the checkpoint still in the context (the reported
	// chain rejected it as unknown).
	live := a.ctxMgr.Snapshot()
	if len(live) == 0 || !strings.Contains(live[0].Content, "Evidence ID: "+packID) {
		t.Fatalf("checkpoint must render the evidence pack with the ID:\n%s", live[0].Content)
	}
	if err := a.validateModelDrivenEvidenceRefs([]string{packID}); err != nil {
		t.Fatalf("previous-generation evidence pack ID rejected after apply: %v", err)
	}
}
