package agent

import (
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
)

// modelDownshiftTestAgent returns an agent whose ctxmgr crosses a 0.8
// auto-compaction threshold on the given input budget.
func modelDownshiftTestAgent(t *testing.T, budget int) *MainAgent {
	a := newTestMainAgent(t, t.TempDir())
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(budget, budget, 0, 0.8)
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: int(float64(budget) * 0.9)})
	return a
}

func TestModelDownshiftCrossingGates(t *testing.T) {
	a := modelDownshiftTestAgent(t, 1000000)
	if !a.modelDownshiftCrossing() {
		t.Fatal("usage 0.9 of budget must cross the 0.8 threshold")
	}

	// Below the line: no crossing.
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(1000000, 1000000, 0, 0.8)
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 500000})
	if a.modelDownshiftCrossing() {
		t.Fatal("usage 0.5 of budget must not cross the threshold")
	}

	// Breaker suppression disables the crossing.
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(1000000, 1000000, 0, 0.8)
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 900000})
	a.autoCompactFailureState.SuppressedUntilTurn = 1
	if a.modelDownshiftCrossing() {
		t.Fatal("suppressed usage-driven compaction must not start a downshift compaction")
	}
	a.autoCompactFailureState.SuppressedUntilTurn = 0

	// An already-running compaction disables the crossing.
	a.beginCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTriggerManual, continuationPlan{kind: compactionResumeIdle}, 0, nil)
	defer a.resetCompactionState()
	if a.modelDownshiftCrossing() {
		t.Fatal("a running compaction must not start a second downshift compaction")
	}
}

func TestMaybeRunModelDownshiftCompactionSkipsWhenTurnActive(t *testing.T) {
	a := modelDownshiftTestAgent(t, 1000000)
	a.newTurn()
	a.maybeRunModelDownshiftCompaction()
	if a.IsCompactionRunning() {
		t.Fatal("a model downshift compaction must not start while a turn is active")
	}
}

func TestDeferModelDownshiftCompactionDefersRoundUntilDraftApplies(t *testing.T) {
	a := modelDownshiftTestAgent(t, 1000000)
	if !a.deferModelDownshiftCompactionAtGate(7, "main", a.ctxMgr.Snapshot()) {
		t.Fatal("deferral must start when the crossing exists")
	}
	if !a.IsCompactionRunning() {
		t.Fatal("deferral must start the compaction worker")
	}
	if a.compactionState.trigger != compactionTriggerModelDownshift {
		t.Fatalf("trigger = %q, want %q", a.compactionState.trigger, CompactionTriggerModelDownshift)
	}
	if a.compactionState.continuation.kind != compactionResumeMainLLM {
		t.Fatalf("continuation kind = %q, want %q", a.compactionState.continuation.kind, compactionResumeMainLLM)
	}
	if !a.compactionState.downshiftSuspended {
		t.Fatal("the deferred round must be marked downshiftSuspended so the draft applies when ready")
	}
	if a.compactionState.continuation.turnID != 7 {
		t.Fatalf("continuation turn = %d, want 7", a.compactionState.continuation.turnID)
	}
}

// TestDeferModelDownshiftCompactionAtGateReleasesStaleForegroundActivity
// reproduces the stale-status-bar bug: the tool batch that just completed left
// the shared main activity slot on executing (mainSlotForeground held), and the
// gate defers the next LLM round behind a model-downshift compaction. The slot
// must be handed to compaction immediately so the status bar stops showing the
// stale executing state for the whole compaction.
func TestDeferModelDownshiftCompactionAtGateReleasesStaleForegroundActivity(t *testing.T) {
	a := modelDownshiftTestAgent(t, 1000000)
	t.Cleanup(func() {
		if a.IsCompactionRunning() {
			a.handleCompactionCancel()
		}
		a.compactionWg.Wait()
	})

	// The tool phase that just ended: main_handlers_llm.go emits executing
	// before dispatching the batch, which holds the foreground slot.
	a.emitActivity("main", ActivityExecuting, "1 tools")
	drainAgentEvents(a.outputCh)
	if !a.mainSlotForeground.Load() {
		t.Fatal("precondition: ActivityExecuting must hold the foreground slot")
	}

	if !a.deferModelDownshiftCompactionAtGate(7, "main", a.ctxMgr.Snapshot()) {
		t.Fatal("deferral must start when the crossing exists")
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
			t.Fatalf("stale executing activity must not be re-emitted after the gate: %+v", act)
		}
	}
	if !sawCompacting {
		t.Fatal("the deferred gate must hand the activity slot to compaction immediately")
	}
	if a.mainSlotForeground.Load() {
		t.Fatal("mainSlotForeground must stay released while the deferred round waits for compaction")
	}
}

func TestDeferModelDownshiftCompactionNoCrossing(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(1000000, 1000000, 0, 0.8)
	if a.deferModelDownshiftCompactionAtGate(7, "main", a.ctxMgr.Snapshot()) {
		t.Fatal("deferral must not start below the threshold")
	}
	if a.IsCompactionRunning() {
		t.Fatal("no compaction may start below the threshold")
	}
}

func TestApplyModelCompactionConfigReportsModelChange(t *testing.T) {
	a := modelCompTestAgent(config.CompactionConfig{Threshold: 0.65}, nil, "p/m")
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(1000000, 1000000, 0, 0.65)
	a.appliedCompactionModelRef = "p/old"
	if !a.applyModelCompactionConfig() {
		t.Fatal("a running-model change must report modelChanged")
	}
	if a.applyModelCompactionConfig() {
		t.Fatal("a same-model re-apply must not report modelChanged")
	}
}
