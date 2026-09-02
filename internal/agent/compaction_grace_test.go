package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// graceTestAgent returns an agent with compact_context visible whose usage
// sits at the given fraction of a 0.8-threshold budget.
func graceTestAgent(t *testing.T, usage float64) *MainAgent {
	t.Helper()
	a := newTestMainAgent(t, t.TempDir())
	const budget = 1000000
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(budget, budget, 0, 0.8)
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: int(budget * usage)})
	a.modelDrivenCompactionEnabled.Store(true)
	a.tools.Register(tools.NewCompactContextTool(tools.CompactContextValidator{ContinuationStateMaxTokens: 2048}))
	a.requestBatches.reserve(a.sessionEpoch, 0) // batch 1 = last completed request
	return a
}

func TestCompactionGraceDefersTwoBatchesThenExpires(t *testing.T) {
	a := graceTestAgent(t, 0.85)
	snapshot := a.ctxMgr.Snapshot()

	// First crossing: grace starts on the current batch and the imminent
	// notice is queued exactly once.
	if !a.usageDrivenCompactionGraceDefers(snapshot) {
		t.Fatal("first crossing with compact_context visible must defer the compaction start")
	}
	if a.compactionGraceStartBatch != 1 {
		t.Fatalf("grace start batch = %d, want 1", a.compactionGraceStartBatch)
	}
	notice := a.pendingCompactionImminent
	if notice == "" || !strings.Contains(notice, "compact_context") || !strings.Contains(notice, "after the next 2 requests") || strings.Contains(notice, "<") {
		t.Fatalf("imminent notice = %q, want bare text naming compact_context and the 2-request window", notice)
	}
	a.pendingCompactionImminent = ""

	// Same batch again (the request was cancelled before dispatch and is
	// being retried): still deferred, notice not re-queued (claim pending).
	if !a.usageDrivenCompactionGraceDefers(snapshot) {
		t.Fatal("same-batch re-gate must still defer")
	}

	// One batch later: still inside the grace.
	a.requestBatches.reserve(a.sessionEpoch, 1) // batch 2
	if !a.usageDrivenCompactionGraceDefers(snapshot) {
		t.Fatal("one batch into the grace must still defer")
	}

	// Two batches later: expired — compaction starts and the window is spent.
	a.requestBatches.reserve(a.sessionEpoch, 2) // batch 3
	if a.usageDrivenCompactionGraceDefers(snapshot) {
		t.Fatal("grace must expire after minCompactionGracePeriodBatches")
	}
	if !a.compactionGraceExhausted || a.compactionGraceStartBatch != 0 {
		t.Fatalf("expired grace must mark the window exhausted, got exhausted=%v start=%d", a.compactionGraceExhausted, a.compactionGraceStartBatch)
	}
	// Same window, another crossing: no second grace.
	if a.usageDrivenCompactionGraceDefers(snapshot) {
		t.Fatal("an exhausted window must not re-open the grace")
	}

	// A fresh window (durable apply / session switch / model change) re-arms.
	a.clearCompactionGrace()
	if !a.usageDrivenCompactionGraceDefers(snapshot) {
		t.Fatal("a cleared window must grant the grace again")
	}
}

func TestCompactionGraceOnlyWhileCompactContextVisible(t *testing.T) {
	a := graceTestAgent(t, 0.85)
	snapshot := a.ctxMgr.Snapshot()
	a.modelDrivenCompactionEnabled.Store(false)
	if a.usageDrivenCompactionGraceDefers(snapshot) {
		t.Fatal("the default path (tool invisible) must keep the immediate compaction start")
	}
	if a.compactionGraceStartBatch != 0 || a.compactionGraceExhausted || a.pendingCompactionImminent != "" {
		t.Fatal("an invisible tool must not touch the grace state")
	}
}

func TestCompactionGraceHardCeilingBypass(t *testing.T) {
	// Usage already at 0.96 of the usable budget on the first crossing: the
	// grace is skipped outright so the next request cannot ride into an
	// oversize rejection, and the window counts as spent.
	a := graceTestAgent(t, 0.96)
	snapshot := a.ctxMgr.Snapshot()
	if a.usageDrivenCompactionGraceDefers(snapshot) {
		t.Fatal("usage above the hard ceiling must bypass the grace")
	}
	if !a.compactionGraceExhausted || a.pendingCompactionImminent != "" {
		t.Fatal("hard-ceiling bypass must mark the window exhausted without queuing a notice")
	}

	// Abnormal growth inside an active grace cuts it short.
	a = graceTestAgent(t, 0.85)
	snapshot = a.ctxMgr.Snapshot()
	if !a.usageDrivenCompactionGraceDefers(snapshot) {
		t.Fatal("first crossing must defer")
	}
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 970000})
	if a.usageDrivenCompactionGraceDefers(snapshot) {
		t.Fatal("crossing the hard ceiling mid-grace must end the grace immediately")
	}
	if !a.compactionGraceExhausted || a.compactionGraceStartBatch != 0 {
		t.Fatal("hard-ceiling cut must exhaust the window")
	}
}

func TestCompactionGraceEndsWhenModelDrivenSettlesWithoutApply(t *testing.T) {
	a := graceTestAgent(t, 0.85)
	snapshot := a.ctxMgr.Snapshot()
	if !a.usageDrivenCompactionGraceDefers(snapshot) {
		t.Fatal("first crossing must defer")
	}
	// The model requested a checkpoint and the runtime skipped it: it took
	// its shot, so the safety net must not be deferred any further.
	a.settleModelDrivenOutcome(CompactionStatusSkipped, "projected savings too small", nil)
	if !a.compactionGraceExhausted || a.compactionGraceStartBatch != 0 {
		t.Fatalf("model-driven skip must end the grace, got exhausted=%v start=%d", a.compactionGraceExhausted, a.compactionGraceStartBatch)
	}
	if a.usageDrivenCompactionGraceDefers(snapshot) {
		t.Fatal("after a model-driven skip the compaction must start on the next gate")
	}
}

func TestCompactionGraceSurvivesModelDrivenSettleBelowThreshold(t *testing.T) {
	// A low-gain skip early in the window — usage below the line, no grace
	// active, no usage-driven request armed — is not a shot at the safety net:
	// the grace the window has not granted yet must survive, so the later
	// crossing still gets its deferral and its imminent notice.
	a := graceTestAgent(t, 0.5)
	a.settleModelDrivenOutcome(CompactionStatusSkipped, "projected savings too small", nil)
	if a.compactionGraceExhausted || a.compactionGraceStartBatch != 0 {
		t.Fatalf("a settle below the threshold must not touch the grace, got exhausted=%v start=%d", a.compactionGraceExhausted, a.compactionGraceStartBatch)
	}
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 850000})
	if !a.usageDrivenCompactionGraceDefers(a.ctxMgr.Snapshot()) {
		t.Fatal("the later crossing must still be granted the grace")
	}
	if a.pendingCompactionImminent == "" {
		t.Fatal("the later crossing must still queue the imminent notice")
	}
}

func TestCompactionGraceEndsWhenModelDrivenSettlesAfterArmedCrossing(t *testing.T) {
	// The crossing was observed on the response (usage-driven request armed)
	// but the gate has not started the grace yet when the model's checkpoint
	// settles without applying: that is a shot at the safety net, so the
	// window's grace is spent and the next gate starts compaction.
	a := graceTestAgent(t, 0.85)
	a.autoCompactRequested.Store(true)
	a.settleModelDrivenOutcome(CompactionStatusSkipped, "projected savings too small", nil)
	if !a.compactionGraceExhausted {
		t.Fatal("a settle after the armed crossing must exhaust the grace")
	}
	if a.usageDrivenCompactionGraceDefers(a.ctxMgr.Snapshot()) {
		t.Fatal("after the exhausted grace the compaction must start on the next gate")
	}
}

func TestCompactionGraceClearedOnModelChangeAndSessionSwitch(t *testing.T) {
	a := graceTestAgent(t, 0.85)
	a.compactionGraceStartBatch = 1
	a.compactionGraceExhausted = true
	a.pendingCompactionImminent = "stale"
	// A real model change (appliedCompactionModelRef already set to another
	// model) re-derives the threshold and drops the old window's grace.
	a.appliedCompactionModelRef = "other/model"
	a.applyModelCompactionConfig()
	if a.compactionGraceStartBatch != 0 || a.compactionGraceExhausted || a.pendingCompactionImminent != "" {
		t.Fatal("model change must clear the grace state")
	}

	a.compactionGraceStartBatch = 1
	a.compactionGraceExhausted = true
	a.installSessionTarget(a.sessionDir)
	if a.compactionGraceStartBatch != 0 || a.compactionGraceExhausted {
		t.Fatal("session switch must clear the grace state")
	}
}

func TestCompactionImminentClaimAndOverlay(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	if !a.tryClaimCompactionImminent(1, 0, 0) {
		t.Fatal("fresh window must grant the imminent claim")
	}
	a.pendingCompactionImminent = "imminent text"
	overlays := a.buildTurnOverlayMessages()
	if len(overlays) != 1 || overlays[0].Kind != message.KindTurnOverlay || !strings.Contains(overlays[0].Content, "<system-reminder>\nimminent text\n</system-reminder>") {
		t.Fatalf("imminent overlay not attached as a wrapped turn overlay: %#v", overlays)
	}
	if a.pendingCompactionImminent != "" {
		t.Fatal("attaching must consume the pending notice")
	}
	// Attached but not dispatched: still claimable.
	if !a.tryClaimCompactionImminent(1, 0, 0) {
		t.Fatal("attached-but-undelivered claim must stay reusable")
	}
	a.markOverlayClaimsDelivered()
	if a.tryClaimCompactionImminent(1, 0, 0) {
		t.Fatal("delivered notice must not be claimable again in the same window")
	}
	// The reminder claim is independent of the imminent claim.
	if !a.tryClaimContextPressureReminder(1, 0, 0) {
		t.Fatal("imminent delivery must not consume the reminder claim")
	}
	if !a.tryClaimCompactionImminent(1, 1, 0) {
		t.Fatal("a new compaction window must reset the imminent claim")
	}
}
