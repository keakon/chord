package agent

import (
	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
)

// minCompactionGracePeriodBatches is how many main requests the usage-driven
// compaction waits after the threshold is first crossed before it starts.
// During the grace period the model receives the context-pressure reminder
// (and the externalization warning once the auto-compact request is armed)
// and can actively reset via compact_context or write its state to files; the
// compaction only starts once the grace period expires and the threshold is
// still crossed. Two requests match the community-observed end-of-turn
// compaction window (Claude Code rapid-refill breaker uses three turns) and
// give a wrapping-up model one or two rounds to finish.
const minCompactionGracePeriodBatches = 2

// modelCompactionConfig returns the per-model compaction config for modelRef,
// or nil when absent. The config lives on the model definition itself
// (ModelConfig.Compaction, settable via model_templates with <<:).
// a.globalConfig is the effective config (global merged with project, see
// cmd/chord/common.go), so a single lookup covers both layers.
func (a *MainAgent) modelCompactionConfig(modelRef string) *config.ModelCompactionConfig {
	if a == nil || a.globalConfig == nil || modelRef == "" {
		return nil
	}
	providerName, modelID := analytics.SplitModelRef(modelRef)
	if providerName == "" {
		return nil
	}
	if prov, ok := a.globalConfig.Providers[providerName]; ok {
		if mc, ok := prov.Models[modelID]; ok && mc.Compaction != nil {
			return mc.Compaction
		}
	}
	return nil
}

// explicitReminderPct returns the configured reminder line for modelRef
// (per-model first, then the global reminder), or 0 when none is configured.
func (a *MainAgent) explicitReminderPct(modelRef string) float64 {
	if a == nil {
		return 0
	}
	if entry := a.modelCompactionConfig(modelRef); entry != nil && entry.Reminder != nil {
		return *entry.Reminder
	}
	if a.globalConfig != nil {
		return a.globalConfig.Context.Compaction.Reminder
	}
	return 0
}

// effectiveCompactionThreshold resolves the auto-compaction threshold for a
// model reference: the model definition's own compaction.threshold when
// present (including an explicit zero that disables auto-compaction for that
// model), otherwise the global threshold. The reminder line never moves this
// line: a configured reminder higher than the threshold simply never fires on
// its own (the threshold crossing itself triggers the reminder through the
// min() in queueContextPressureReminder, and the grace period of
// minCompactionGracePeriodBatches defers the actual compaction by a couple of
// requests).
func (a *MainAgent) effectiveCompactionThreshold(modelRef string) float64 {
	if a == nil {
		return config.DefaultContextCompactUsage
	}
	if entry := a.modelCompactionConfig(modelRef); entry != nil && entry.Threshold != nil {
		return *entry.Threshold
	}
	if a.globalConfig != nil {
		return a.globalConfig.Context.Compaction.Threshold
	}
	return config.DefaultContextCompactUsage
}

// effectiveReminderPct resolves the configured context-pressure reminder line
// for the current model: per-model reminder → global reminder → derived
// default (min(0.60, threshold*0.90)). It may return a value at or above the
// threshold — the caller applies the "whichever line is reached first"
// semantics (min with the threshold) so the reminder still fires on the
// threshold crossing itself. Returns 0 when the threshold disables automatic
// compaction (threshold<=0), meaning no reminder is ever injected.
func (a *MainAgent) effectiveReminderPct(threshold float64) float64 {
	modelRef := a.runningModelRef
	if modelRef == "" {
		modelRef = a.providerModelRef
	}
	return a.effectiveReminderPctForModelRef(modelRef, threshold)
}

// effectiveReminderPctForModelRef is effectiveReminderPct resolved against an
// explicit model reference instead of the current running model, so callers
// can preview the reminder line a model would manage its context with (the TUI
// re-colors the context display for the model that is next up after a switch).
func (a *MainAgent) effectiveReminderPctForModelRef(modelRef string, threshold float64) float64 {
	if threshold <= 0 {
		return 0
	}
	if reminder := a.explicitReminderPct(modelRef); reminder > 0 {
		return reminder
	}
	derived := contextPressureReminderRatioCap
	if head := threshold * contextPressureReminderThresholdRatio; head < derived {
		derived = head
	}
	return derived
}

// applyModelCompactionConfig applies the per-model compaction threshold for the
// current model reference to ctxmgr. Called at request boundaries after
// pending model-pool switches are applied; a model change also clears the
// compaction grace period (the new model re-evaluates usage against its own
// threshold) and bumps the budget epoch through SetThreshold, which re-arms
// the one-shot overlay claims for the new window.
func (a *MainAgent) applyModelCompactionConfig() {
	if a == nil || a.ctxMgr == nil {
		return
	}
	modelRef := a.runningModelRef
	if modelRef == "" {
		modelRef = a.providerModelRef
	}
	modelChanged := false
	if modelRef != a.appliedCompactionModelRef {
		// A real model change (not the first application after construction,
		// where appliedCompactionModelRef is still the zero value) starts a
		// fresh compaction window: re-derive the threshold and clear the
		// grace period and the exhausted flag so the new model gets its own
		// active-reset window.
		if a.appliedCompactionModelRef != "" {
			modelChanged = true
			a.gracePeriodStartBatch = 0
			a.gracePeriodExhausted = false
		}
		a.appliedCompactionModelRef = modelRef
	}
	a.ctxMgr.SetThreshold(a.effectiveCompactionThreshold(modelRef))
	// A usage-driven request armed under the previous model's threshold may
	// not be justified by the new model's line (for example a fallback from a
	// small-window model with a low threshold to a large-window one with a
	// high threshold). Re-evaluate the armed request against the freshly
	// applied threshold: if the post-response usage no longer crosses it,
	// clear the stale request so the new window is not force-compacted by an
	// old crossing. A request that still crosses the new threshold stays
	// armed and opens a fresh grace period through the regular gate.
	if modelChanged && a.autoCompactRequested.Load() && !a.ctxMgr.AutoCompactDecision().ShouldCompact {
		a.clearUsageDrivenAutoCompactRequest()
	}
}

// deferCompactionForGracePeriod defers the usage-driven compaction after the
// automatic-compaction threshold is crossed: when the crossing happened within
// the last minCompactionGracePeriodBatches main requests, the compaction is
// deferred one more request so the model can actively reset via
// compact_context or externalize its state.
// It records the anchor batch on first crossing, defers while the gap is below
// the limit, and clears the anchor (returning false) once the grace period
// expires so the caller starts the compaction. batch comes from
// currentRequestBatch, which counts dispatched main requests; a session or
// model switch resets the anchor to zero and the next crossing re-anchors.
func (a *MainAgent) deferCompactionForGracePeriod(batch uint64) bool {
	if a == nil {
		return false
	}
	// The model already spent its active-reset chance in this window (a
	// model-driven request settled as skipped/failed/cancelled); the
	// usage-driven safety net must take over now, so no fresh grace period.
	if a.gracePeriodExhausted {
		return false
	}
	if a.gracePeriodStartBatch == 0 {
		a.gracePeriodStartBatch = batch
		a.recordGracePolicyEvent("grace_opened")
		return true
	}
	if batch-a.gracePeriodStartBatch < minCompactionGracePeriodBatches {
		return true
	}
	// Grace period expired: the window's active-reset chance is spent, so the
	// next crossing in this window does not re-open it either.
	a.gracePeriodStartBatch = 0
	a.gracePeriodExhausted = true
	a.recordGracePolicyEvent("grace_expired")
	return false
}

// recordGracePolicyEvent emits a grace-period policy telemetry event. Guarded
// on the usage ledger: the grace helpers are also exercised on bare agents in
// tests, whose field accessors are not all nil-safe.
func (a *MainAgent) recordGracePolicyEvent(detail string) {
	if a == nil || a.usageLedger == nil {
		return
	}
	a.recordCompactionPolicyAnalyticsEvent(detail)
}
