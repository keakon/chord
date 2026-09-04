package agent

import (
	"fmt"
	"strconv"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/message"
)

const (
	// minCompactionGracePeriodBatches is the number of main-model request
	// batches the usage-driven compaction start is deferred after the
	// threshold crossing while compact_context is visible: the model gets the
	// crossing request plus one more to wrap up the phase and request a
	// model-driven checkpoint (the high-quality path) or externalize state
	// before the summary-based compaction takes over.
	minCompactionGracePeriodBatches = 2
	// compactionGraceHardCeilingRatio is the abnormal-growth bypass: once the
	// post-response usage reaches this fraction of the usable input budget
	// the grace is skipped (or cut short) and compaction starts immediately,
	// so a single batch that pulled in large tool output cannot ride the
	// grace into a provider oversize rejection.
	compactionGraceHardCeilingRatio = 0.95
)

// compactionImminentText renders the grace-period "compaction imminent"
// notice for a request inside the deferral window. requests is the number of
// main-model request batches left before automatic compaction takes over: the
// crossing request reports the full minCompactionGracePeriodBatches window and
// every later deferred request reports its true remaining count, so the model
// always sees how much room is actually left. It is bare
// content; the turn-overlay injector wraps it in a <system-reminder> block.
func compactionImminentText(requests int) string {
	countdown := fmt.Sprintf("the next %d requests", requests)
	if requests == 1 {
		countdown = "the next request"
	}
	return fmt.Sprintf("The context has crossed the automatic-compaction threshold. Automatic compaction will start after %s unless a context checkpoint is applied first.\n", countdown) +
		"If the current phase is wrapped up and its working state is externalized, call compact_context alone on this turn to checkpoint it now.\n" +
		"Otherwise write important findings, decisions, and working state to a project file your role may write (for example a task-notes file under .chord/notes/, named with a YYYYMMDD date prefix) so they survive the compaction."
}

// usageDrivenCompactionGraceDefers decides, on the pre-request gate after the
// usage-driven trigger fired, whether the compaction start is deferred by the
// threshold grace period. It returns true when the caller must spawn the
// request without starting the compaction.
//
// The grace applies only while compact_context is visible (the model has a
// way to act on it); on the default path the gate keeps its immediate start.
// It runs once per compaction window: an expired grace, a model-driven
// request that settled without applying after the crossing, or the
// hard-ceiling bypass mark the window exhausted so the safety net cannot be
// deferred twice. A durable apply, a session switch, or a model change clears
// the state.
func (a *MainAgent) usageDrivenCompactionGraceDefers(snapshot []message.Message) bool {
	if a == nil || a.ctxMgr == nil || a.compactionGraceExhausted || !a.compactContextVisible() {
		return false
	}
	decision := a.ctxMgr.AutoCompactDecision()
	if decision.UsableInputBudget <= 0 {
		return false
	}
	current := a.currentRequestBatch(snapshot)
	if float64(decision.EffectiveInputTokens) >= float64(decision.UsableInputBudget)*compactionGraceHardCeilingRatio {
		a.endCompactionGrace("hard_ceiling", current)
		return false
	}
	if a.compactionGraceStartBatch == 0 {
		a.compactionGraceStartBatch = current
		a.queueCompactionImminentNotice(minCompactionGracePeriodBatches)
		a.recordCompactionGraceEvent("started", current)
		return true
	}
	// Grace in progress: every deferred request re-attaches the imminent
	// notice with the true remaining countdown, so a model
	// that missed the crossing request — or whose copy was attached to a
	// cancelled dispatch — still sees how much room is left.
	if current >= a.compactionGraceStartBatch && current-a.compactionGraceStartBatch < minCompactionGracePeriodBatches {
		remaining := minCompactionGracePeriodBatches - int(current-a.compactionGraceStartBatch)
		a.queueCompactionImminentNotice(remaining)
		return true
	}
	a.endCompactionGrace("expired", current)
	return false
}

// endCompactionGrace marks the current window's grace exhausted: the next
// threshold crossing in this window starts compaction immediately. The grace
// stage event is recorded for an ended active window and for the two decisions
// that exhaust a window whose grace never started — the hard-ceiling bypass
// and a model-driven settle after an armed crossing — so a window whose grace
// was cut off before it began still leaves a diagnostic trail.
func (a *MainAgent) endCompactionGrace(reason string, current uint64) {
	if a == nil {
		return
	}
	active := a.compactionGraceStartBatch != 0
	a.compactionGraceStartBatch = 0
	a.compactionGraceExhausted = true
	a.pendingCompactionImminent = ""
	if active || reason == "hard_ceiling" || reason == "model_driven_settled" {
		a.recordCompactionGraceEvent(reason, current)
	}
}

// exhaustCompactionGraceAfterModelDriven ends the grace when a model-driven
// request settled without applying (skip/failure/cancel) after the window
// crossed the threshold: the model already took its shot at the safety net,
// so usage-driven compaction takes over on the next gate. "Crossed" means the
// grace is active, or the usage-driven request is armed because the crossing
// was observed on the response while the gate has not started the grace yet.
// A request that settled while usage was still below the line — a low-gain
// skip early in the window — is not a shot at the safety net and must not
// forfeit the grace the window has not granted yet.
func (a *MainAgent) exhaustCompactionGraceAfterModelDriven() {
	// Idempotent: a settle in a window whose grace already ended must not
	// re-exhaust it and double-record the stage event.
	if a == nil || a.ctxMgr == nil || a.compactionGraceExhausted {
		return
	}
	if a.compactionGraceStartBatch == 0 && !a.autoCompactRequested.Load() {
		return
	}
	a.endCompactionGrace("model_driven_settled", a.currentRequestBatch(a.ctxMgr.Snapshot()))
}

// clearCompactionGrace resets the grace state for a fresh compaction window
// (durable apply, session switch, model change).
func (a *MainAgent) clearCompactionGrace() {
	if a == nil {
		return
	}
	a.compactionGraceStartBatch = 0
	a.compactionGraceExhausted = false
	a.pendingCompactionImminent = ""
}

func (a *MainAgent) recordCompactionGraceEvent(stage string, batch uint64) {
	a.recordContextDiagnosticEvent(analytics.UsagePurposeCompactionGrace, map[string]string{
		"stage":         stage,
		"request_batch": strconv.FormatUint(batch, 10),
	})
}

// queueCompactionImminentNotice arms the "compaction imminent" overlay for a
// request inside the threshold grace period. The notice is
// sticky for the whole grace: the queue never suppresses an attach, because
// the gate only defers while the grace is active and the notice is the only
// signal that automatic compaction is about to take over — if a request could
// attach it, it is by definition still inside the window. remaining is the
// number of main-model request batches left before compaction starts; the
// crossing request reports the full window and every later deferred request
// reports its true countdown. The claim shares the reminder's (session, window,
// model, budget) key but only records first/repeat delivery stages for telemetry.
func (a *MainAgent) queueCompactionImminentNotice(remaining int) {
	windowEpoch := a.sessionEpoch
	windowIndex := int(a.compactionWindowGeneration)
	budgetEpoch := a.ctxMgr.TokenBudgetsEpoch()
	a.syncOverlayWindowClaim(&a.overlayClaims.imminent, windowEpoch, windowIndex, budgetEpoch)
	a.pendingCompactionImminent = compactionImminentText(remaining)
}
