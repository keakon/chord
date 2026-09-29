package agent

// A busy /compact with a visible compact_context starts the summarize worker
// immediately AND arms a manual compaction intent: the next main request
// carries a persistent imperative notice inviting the model to submit its own
// checkpoint first. Whichever side settles first wins — the worker applying at
// the continuation barrier, or a model checkpoint applying over it (the
// existing override rule). This file owns the intent's lifecycle, the gate
// injection, and the restart fallback that mirrors the usage-driven path's
// behavior when a model checkpoint settles without applying.

// manualOverlayClaim is the per-intent claim of the manual imperative notice.
// It binds to the armed intent's plan id, so one /compact intent writes at
// most one durable row; a request cancelled before dispatch leaves the claim
// spendable and the next gate re-stages the text.
type manualOverlayClaim struct {
	planID          uint64
	deliveryPending bool
	delivered       bool
}

// manualNoticeActive reports whether the manual /compact intent is armed. The
// flag is the ladder's suppression key for the usage-driven notices: it stays
// set even while the notice row is withdrawn, covering the window where the
// model's checkpoint settled without applying and the restarted worker runs
// silently.
func (a *MainAgent) manualNoticeActive() bool {
	return a != nil && a.manualCompactArmed.Load()
}

// armManualCompactionIntent arms the manual intent for the worker the command
// just started. A re-issued /compact while an intent is already armed keeps
// the original intent: the running (or about-to-restart) worker still owes the
// user a compaction, and one intent writes at most one notice row.
func (a *MainAgent) armManualCompactionIntent(planID uint64) {
	if a.manualCompactArmed.Load() {
		return
	}
	a.manualCompactArmedPlanID.Store(planID)
	a.manualCompactArmed.Store(true)
}

// clearManualCompactionIntent drops the armed intent without touching any
// notice row. Used where the row is already gone (any successful apply drops
// the durable rows with the rewritten history) or where the session that held
// it is being replaced.
func (a *MainAgent) clearManualCompactionIntent() {
	a.manualCompactArmed.Store(false)
}

// clearManualCompactionIntentAndWithdrawNotice drops the armed intent and
// withdraws the manual notice row with it: the request the row advertised has
// a terminal answer (worker skip/failed, user cancellation), so replaying the
// imperative would only invite a checkpoint nobody asked for anymore.
func (a *MainAgent) clearManualCompactionIntentAndWithdrawNotice() {
	a.manualCompactArmed.Store(false)
	a.withdrawManualCompactionNotice()
}

// withdrawManualCompactionNotice arms the idle-boundary removal of the manual
// notice rows. Request surfaces omit them immediately
// (omitStaleContextNoticesFromRequest); the durable rewrite waits for an idle
// boundary so live compaction indices stay stable. Any previously armed
// withdrawal scope is preserved.
func (a *MainAgent) withdrawManualCompactionNotice() {
	if a == nil || !a.contextNoticesPersisted.Load() {
		return
	}
	if scope := a.contextNoticeWithdrawalScope.Load(); scope != contextNoticeWithdrawAll {
		a.contextNoticeWithdrawalScope.Store(scope | contextNoticeWithdrawManual)
	}
	a.contextNoticesStale.Store(true)
}

// handleManualCompactionIntentAtGate runs on the pre-request gate after the
// ready-draft apply (an apply cleared the armed intent) and before the
// usage-driven trigger evaluation:
//
//   - The armed intent's own worker (same plan id, manual trigger) is still
//     running: stage the imperative notice for this request. One row per
//     intent; afterwards the durable row replays without new injections.
//   - The intent is armed but no worker is running (a model checkpoint took
//     over and settled without applying): restart the manual worker. The new
//     plan id never matches the armed record, so the notice is not injected
//     again — this is the no-second-injection mechanism, not a guard flag.
//   - Anything else (model worker running) is left alone.
func (a *MainAgent) handleManualCompactionIntentAtGate() {
	if !a.manualNoticeActive() {
		return
	}
	if a.compactionState.isRunning() {
		if a.compactionState.trigger == compactionTriggerManual &&
			a.compactionState.planID == a.manualCompactArmedPlanID.Load() {
			a.injectManualCompactionNoticeAtGate()
		}
		return
	}
	if a.scheduleCompaction(true) {
		a.recordCompactionPolicyAnalyticsEvent("manual_compaction_restarted")
	}
}

// injectManualCompactionNoticeAtGate stages the manual imperative notice for
// the request this gate is about to spawn. The pressure cycle is opened (or
// advanced) before staging because the durable-row reservation at dispatch
// requires an open cycle.
func (a *MainAgent) injectManualCompactionNoticeAtGate() {
	if !a.compactContextVisible() {
		return
	}
	planID := a.manualCompactArmedPlanID.Load()
	a.overlayClaims.mu.Lock()
	c := &a.overlayClaims.manual
	if c.planID != planID {
		*c = manualOverlayClaim{planID: planID}
	}
	delivered := c.delivered
	a.overlayClaims.mu.Unlock()
	if delivered {
		return
	}
	if a.hasDurablePressureNotice(contextNoticeManual) {
		return
	}
	a.notePressureStage(pressureStageArmed, a.currentOverlayWindowKey())
	a.stageContextNotice(contextNoticeManual, manualCompactionNoticeText)
}

// restartManualCompactionIfArmedIdle is the settle-point fallback for a turn
// that died while the manual intent was armed: the model-driven continuation
// that would have re-entered the gate is gone, so the worker restarts here as
// an idle compaction (compactionResumeIdle) and applies as soon as it is
// ready. ESC cleared the intent before any settle on that path, so a user
// abort never restarts anything.
func (a *MainAgent) restartManualCompactionIfArmedIdle() {
	if !a.manualNoticeActive() || a.compactionState.isRunning() || a.turn != nil {
		return
	}
	if a.scheduleCompaction(true) {
		a.recordCompactionPolicyAnalyticsEvent("manual_compaction_restarted")
	}
}
