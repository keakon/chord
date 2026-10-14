package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func TestMaybeStartModelDrivenBarrierSkipsWithoutPending(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn("")
	if a.maybeStartModelDrivenBarrier() {
		t.Fatal("barrier must not start with no pending request")
	}
}

func TestMaybeStartModelDrivenBarrierStartsWorker(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn("")
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
	a.newTurn("")
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
	a.newTurn("")
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
	a.newTurn("")
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
	a.newTurn("")
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

// TestMaybeStartModelDrivenBarrierReleasesStaleForegroundActivity pins the
// status-bar side of the barrier: the tool batch that just ended holds the
// shared main activity slot on "executing", and the barrier freezes the turn
// behind the checkpoint worker. The slot must be handed over, or the status bar
// keeps showing the stale executing state for the whole worker run.
func TestMaybeStartModelDrivenBarrierReleasesStaleForegroundActivity(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn("")
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
