package agent

import (
	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
)

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
// (per-model first, then the global reminder): 0 when none is configured,
// negative (config.CompactionReminderDisabled) when explicitly disabled.
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
// line: a configured reminder at or above the threshold simply never injects
// on its own — the crossing request itself carries the grace imminent notice
// or the usage-driven externalization warning (see queueContextPressureReminder).
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
// default (min(0.60, threshold*0.90)). The resolved line may sit at or above
// the threshold — queueContextPressureReminder applies the "whichever line is
// reached first" semantics via min with the threshold and never injects when
// the line is at/above it (the crossing and grace-deferred requests carry the
// imminent notice or the externalization warning). Returns
// 0 when the threshold disables automatic compaction (threshold<=0) or the
// reminder is explicitly disabled, meaning no reminder is ever injected.
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
// The line may sit at or above the threshold; queueContextPressureReminder
// then never injects a separate reminder.
func (a *MainAgent) effectiveReminderPctForModelRef(modelRef string, threshold float64) float64 {
	if threshold <= 0 {
		return 0
	}
	reminder := a.explicitReminderPct(modelRef)
	if reminder < 0 {
		// Explicitly disabled (config.CompactionReminderDisabled): no
		// reminder line even though automatic compaction stays on.
		return 0
	}
	if reminder > 0 {
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
// pending model-pool switches are applied; a model change changes the reminder
// claim's model identity, which resets the reminder-class overlay claims for
// the new window (full reminder text becomes available again; the warning claim
// resets with the request generation). A model change also retires the size
// observation when it was measured against the previous window: the trigger
// frame starts from unknown — any request armed before the switch is cleared,
// and nothing arms until a new response supplies usage or a frozen estimate
// against the new window's line. The gauge keeps the retired reading as stale
// until that response arrives. An observation the new model itself produced
// (a fallback response reporting usage for the model that answered) is kept and judged
// against the new line instead. The round itself is never deferred behind a
// compaction: only a hard context-length rejection suspends a round.
func (a *MainAgent) applyModelCompactionConfig() {
	if a == nil || a.ctxMgr == nil {
		return
	}
	// The event loop owns the threshold, grace, and notice updates, including
	// fallback boundaries. Serialize this application with request callbacks
	// that can change the running model and its budgets while the loop runs.
	a.modelUpdateMu.Lock()
	defer a.modelUpdateMu.Unlock()
	a.llmMu.Lock()
	modelRef := a.mainModelRefLocked()
	previousModelRef := a.appliedCompactionModelRef
	// A real model change (not the first application after construction, where
	// appliedCompactionModelRef is still the zero value) marks the switch so the
	// armed usage-driven request is re-evaluated below.
	modelChanged := previousModelRef != "" && previousModelRef != modelRef
	// Only an observation owned by another window is stale. The ownership is
	// cleared together with the observation it describes, in the same critical
	// section that decides it, so a concurrent sample cannot be misattributed.
	observationMatchesNewModel := modelRef != "" && a.usageObservationModelRef == modelRef
	if modelChanged && !observationMatchesNewModel {
		a.usageObservationModelRef = ""
	}
	a.appliedCompactionModelRef = modelRef
	a.llmMu.Unlock()
	if modelChanged {
		// The new model re-evaluates usage against its own threshold, so the
		// previous window's grace state does not carry over. The retired
		// reading stays on the gauge as stale; only the cross-model calibration
		// ratio feeds the next frozen estimate.
		a.clearCompactionGrace()
		if !observationMatchesNewModel {
			a.ctxMgr.InvalidateSizeObservation()
		}
	}
	previousThreshold := a.ctxMgr.Threshold()
	newThreshold := a.effectiveCompactionThreshold(modelRef)
	a.ctxMgr.SetThreshold(newThreshold)
	// A model switch that moves the compaction line or the effective reminder
	// line invalidates every context-pressure notice measured against the
	// previous lines. Arms a cleanup for the next idle boundary instead of
	// rewriting history mid-request. The reminder line is resolved per model
	// (per-model reminder, then global, then the derived default) with the same
	// 0/-1 semantics the queue path uses, so its change is compared as the
	// resolved value rather than the raw config.
	if modelChanged {
		previousReminder := a.effectiveReminderPctForModelRef(previousModelRef, previousThreshold)
		newReminder := a.effectiveReminderPctForModelRef(modelRef, newThreshold)
		if newThreshold != previousThreshold || newReminder != previousReminder {
			a.armContextNoticeCleanup()
		}
	}
	// Re-evaluate the armed request against the freshly applied threshold. The
	// invalidation above leaves the decision unknown, so a request armed under
	// the previous window is cleared here: the new window must not be
	// force-compacted by an old crossing, and it arms again only once a
	// response supplies usage or a frozen estimate against its own line.
	if modelChanged {
		if a.ctxMgr.AutoCompactDecision().ShouldCompact {
			a.armUsageDrivenAutoCompactRequest()
		} else if a.autoCompactRequested.Load() {
			a.clearUsageDrivenAutoCompactRequest()
		}
	}
}
