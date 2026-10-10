package agent

import (
	"strconv"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/ctxmgr"
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
	// compactionGraceHardCeilingRatio is the observed-urgency bypass: once the
	// latest observation (or the single frozen estimate when usage missed)
	// reaches this fraction of the usable input budget the grace is skipped (or
	// cut short) and compaction starts immediately. It no longer guards against
	// a pre-request size collision; it only measures how urgent the last
	// observation already is. Unknown (no observation, no frozen estimate) never
	// reaches it and simply consumes its batches like any other deferred request.
	compactionGraceHardCeilingRatio = 0.95
)

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
	// Abrupt crossing: the observation reached the threshold without the
	// pressure reminder ever being delivered in this window — usage jumped
	// instead of climbing past the reminder line, so the model has had no
	// early warning to act on. The grace countdown has no one to serve: skip
	// it, start compaction immediately, and let the crossing request carry a
	// single externalization warning (the reminder never injects on a request
	// that already stages the warning — the ladder in stageContextNotice).
	// A disabled reminder line (reminder: -1/0, or a line at/above the
	// threshold) keeps this condition permanently true: a configuration that
	// opted out of early notices gets no countdown either.
	if a.contextPressureAbruptCrossing(decision) {
		a.endCompactionGrace("abrupt_crossing", current)
		return false
	}
	if float64(decision.EffectiveInputTokens) >= float64(decision.UsableInputBudget)*compactionGraceHardCeilingRatio {
		a.endCompactionGrace("hard_ceiling", current)
		return false
	}
	if !a.compactionGraceActive {
		a.compactionGraceActive = true
		a.compactionGraceStartBatch = current
		a.queueCompactionImminentNotice()
		a.recordCompactionGraceEvent("started", current)
		return true
	}
	// Grace remains active. Retry the notice only if no request delivered it;
	// later requests read the retained history without a changing countdown.
	if current >= a.compactionGraceStartBatch && current-a.compactionGraceStartBatch < minCompactionGracePeriodBatches {
		a.queueCompactionImminentNotice()
		return true
	}
	a.endCompactionGrace("expired", current)
	return false
}

// contextPressureAbruptCrossing reports whether the current observation
// reached the compaction threshold while the pressure reminder has never been
// delivered in this compaction window: neither the window claim carries a
// delivery nor does the transcript carry a durable reminder row. Rebinding the
// claim to the live window key first keeps a delivery from a previous window
// from masking an abrupt crossing of the fresh one.
func (a *MainAgent) contextPressureAbruptCrossing(decision ctxmgr.AutoCompactDecision) bool {
	if a == nil || a.ctxMgr == nil || !decision.ShouldCompact {
		return false
	}
	claim := a.syncOverlayWindowClaim(&a.overlayClaims.reminder, a.currentOverlayWindowKey())
	if claim.delivered {
		return false
	}
	return !a.hasDurablePressureNotice(contextNoticePressure)
}

// endCompactionGrace marks the current window's grace exhausted: the next
// threshold crossing in this window starts compaction immediately. The grace
// stage event is recorded for an ended active window and for the decisions
// that exhaust a window whose grace never started — the hard-ceiling bypass,
// an abrupt crossing, and a model-driven settle after an armed crossing — so a
// window whose grace was cut off before it began still leaves a diagnostic
// trail.
func (a *MainAgent) endCompactionGrace(reason string, current uint64) {
	if a == nil {
		return
	}
	active := a.compactionGraceActive
	a.compactionGraceActive = false
	a.compactionGraceStartBatch = 0
	a.compactionGraceExhausted = true
	a.setPendingContextNoticeText(contextNoticeImminent, "")
	// The cycle keeps its threshold-class pressure (the usage-driven request
	// is still armed and the next crossing starts compaction immediately);
	// only the grace stage is over.
	if a.ctxMgr != nil {
		a.notePressureStage(pressureStageArmed, a.currentOverlayWindowKey())
	}
	if active || reason != "expired" {
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
	if !a.compactionGraceActive && !a.autoCompactRequested.Load() {
		return
	}
	a.endCompactionGrace("model_driven_settled", a.currentRequestBatch(nil))
}

// clearCompactionGrace resets the grace state for a fresh compaction window
// (durable apply, session switch, model change).
func (a *MainAgent) clearCompactionGrace() {
	if a == nil {
		return
	}
	a.compactionGraceStartBatch = 0
	a.compactionGraceActive = false
	a.compactionGraceExhausted = false
	a.setPendingContextNoticeText(contextNoticeImminent, "")
	// The window the cycle belonged to is gone (session switch, restore,
	// model/budget change, or a durable apply that ended it first): close it so
	// the next pressure observation opens a fresh identity instead of
	// inheriting the reached stage.
	a.endPressureCycle(pressureEndWindowClosed)
}

func (a *MainAgent) recordCompactionGraceEvent(stage string, batch uint64) {
	a.recordContextDiagnosticEvent(analytics.UsagePurposeCompactionGrace, map[string]string{
		"stage":         stage,
		"request_batch": strconv.FormatUint(batch, 10),
	})
}

// queueCompactionImminentNotice stages the upper-threshold notice once.
// The durable message remains visible throughout grace and compaction startup.
func (a *MainAgent) queueCompactionImminentNotice() {
	a.notePressureStage(pressureStageGrace, a.currentOverlayWindowKey())
	claim := a.syncOverlayWindowClaim(&a.overlayClaims.imminent, a.currentOverlayWindowKey())
	if claim.delivered || a.hasDurablePressureNotice(contextNoticeImminent) {
		return
	}
	a.stageContextNotice(contextNoticeImminent, compactionThresholdNoticeText)
}
