package agent

import (
	"math"
	"testing"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
)

// modelCompTestAgent builds a MainAgent whose per-model compaction configs live
// on the model definitions (ModelConfig.Compaction), keyed "provider/model".
func modelCompTestAgent(globalComp config.CompactionConfig, modelComp map[string]*config.ModelCompactionConfig, runningModelRef string) *MainAgent {
	cfg := &config.Config{
		Context:   config.ContextConfig{Compaction: globalComp},
		Providers: map[string]config.ProviderConfig{},
	}
	byProvider := map[string]map[string]config.ModelConfig{}
	for ref, comp := range modelComp {
		providerName, modelID := analytics.SplitModelRef(ref)
		if byProvider[providerName] == nil {
			byProvider[providerName] = map[string]config.ModelConfig{}
		}
		mc := byProvider[providerName][modelID]
		mc.Compaction = comp
		byProvider[providerName][modelID] = mc
	}
	for providerName, models := range byProvider {
		cfg.Providers[providerName] = config.ProviderConfig{Models: models}
	}
	return &MainAgent{globalConfig: cfg, runningModelRef: runningModelRef}
}

func TestEffectiveCompactionThresholdPerModelOverride(t *testing.T) {
	perModel := 0.3
	a := modelCompTestAgent(
		config.CompactionConfig{Threshold: 0.65},
		map[string]*config.ModelCompactionConfig{"openai/gpt-5.6-luna": {Threshold: &perModel}},
		"",
	)
	if got := a.effectiveCompactionThreshold("openai/gpt-5.6-luna"); got != 0.3 {
		t.Fatalf("per-model threshold = %v, want 0.3", got)
	}
	if got := a.effectiveCompactionThreshold("openai/gpt-5.6-sol"); got != 0.65 {
		t.Fatalf("unmatched model threshold = %v, want global 0.65", got)
	}
}

func TestEffectiveCompactionThresholdPerModelZeroDisables(t *testing.T) {
	zero := 0.0
	a := modelCompTestAgent(
		config.CompactionConfig{Threshold: 0.65},
		map[string]*config.ModelCompactionConfig{"p/m": {Threshold: &zero}},
		"",
	)
	if got := a.effectiveCompactionThreshold("p/m"); got != 0 {
		t.Fatalf("per-model zero threshold = %v, want 0 (disabled)", got)
	}
}

func TestEffectiveCompactionThresholdNilConfigFallsBack(t *testing.T) {
	var a *MainAgent
	if got := a.effectiveCompactionThreshold("p/m"); got != config.DefaultContextCompactUsage {
		t.Fatalf("nil agent threshold = %v, want default %v", got, config.DefaultContextCompactUsage)
	}
}

func TestEffectiveReminderPctPerModelThenGlobalThenDerived(t *testing.T) {
	perModel := 0.15
	global := 0.25
	a := modelCompTestAgent(
		config.CompactionConfig{Threshold: 0.3, Reminder: global},
		map[string]*config.ModelCompactionConfig{"openai/gpt-5.6-luna": {Reminder: &perModel}},
		"openai/gpt-5.6-luna",
	)
	// Per-model reminder below the threshold is used directly.
	if got := a.effectiveReminderPct(0.3); got != perModel {
		t.Fatalf("per-model reminder = %v, want 0.15", got)
	}
	// No per-model entry: global reminder is used.
	a = modelCompTestAgent(
		config.CompactionConfig{Threshold: 0.3, Reminder: global},
		nil,
		"openai/gpt-5.6-luna",
	)
	if got := a.effectiveReminderPct(0.3); got != global {
		t.Fatalf("global reminder = %v, want 0.25", got)
	}
	// Derived default: min(0.60, threshold*0.90).
	a = modelCompTestAgent(config.CompactionConfig{Threshold: 0.8}, nil, "")
	if got := a.effectiveReminderPct(0.8); got != 0.60 {
		t.Fatalf("derived reminder (threshold 0.8) = %v, want cap 0.60", got)
	}
	if got := a.effectiveReminderPct(0.4); math.Abs(got-0.36) > 1e-9 {
		t.Fatalf("derived reminder (threshold 0.4) = %v, want 0.36", got)
	}
}

func TestEffectiveReminderPctReturnsExplicitAboveThreshold(t *testing.T) {
	high := 0.7
	a := modelCompTestAgent(
		config.CompactionConfig{Threshold: 0.65},
		map[string]*config.ModelCompactionConfig{"p/m": {Reminder: &high}},
		"p/m",
	)
	if got := a.effectiveReminderPct(0.65); got != 0.7 {
		t.Fatalf("explicit reminder above threshold = %v, want 0.7 (returned as-is)", got)
	}
}

func TestEffectiveCompactionThresholdReminderDoesNotRaiseLine(t *testing.T) {
	perModelReminder := 0.7
	a := modelCompTestAgent(
		config.CompactionConfig{Threshold: 0.65},
		map[string]*config.ModelCompactionConfig{"openai/gpt-5.6-sol": {Reminder: &perModelReminder}},
		"",
	)
	// The reminder never moves the compaction line: the threshold stays the
	// hard line and the grace period defers the actual compaction.
	if got := a.effectiveCompactionThreshold("openai/gpt-5.6-sol"); got != 0.65 {
		t.Fatalf("threshold with high reminder = %v, want 0.65", got)
	}
}

func TestEffectiveCompactionThresholdGlobalReminderDoesNotRaiseLine(t *testing.T) {
	a := modelCompTestAgent(config.CompactionConfig{Threshold: 0.65, Reminder: 0.7}, nil, "")
	if got := a.effectiveCompactionThreshold("p/m"); got != 0.65 {
		t.Fatalf("global reminder must not raise the global threshold, got %v", got)
	}
}

func TestEffectiveCompactionThresholdZeroDisablesReminderNeverRevives(t *testing.T) {
	zero := 0.0
	reminder := 0.7
	a := modelCompTestAgent(
		config.CompactionConfig{Threshold: 0.65},
		map[string]*config.ModelCompactionConfig{"p/m": {Threshold: &zero, Reminder: &reminder}},
		"",
	)
	if got := a.effectiveCompactionThreshold("p/m"); got != 0 {
		t.Fatalf("threshold 0 must stay disabled despite reminder 0.7, got %v", got)
	}
	if got := a.effectiveReminderPct(0); got != 0 {
		t.Fatalf("reminder must be suppressed when threshold disables compaction, got %v", got)
	}
}

func TestDeferCompactionForGracePeriod(t *testing.T) {
	a := &MainAgent{}
	// First crossing: anchor recorded, compaction deferred.
	if !a.deferCompactionForGracePeriod(5) {
		t.Fatal("first crossing must defer")
	}
	if a.gracePeriodStartBatch != 5 {
		t.Fatalf("anchor = %d, want 5", a.gracePeriodStartBatch)
	}
	// One request later: still within the 2-batch grace window.
	if !a.deferCompactionForGracePeriod(6) {
		t.Fatal("batch 6 must still defer (gap 1 < 2)")
	}
	// Two requests later: grace expired, compaction proceeds.
	if a.deferCompactionForGracePeriod(7) {
		t.Fatal("batch 7 must expire the grace period (gap 2)")
	}
	if a.gracePeriodStartBatch != 0 {
		t.Fatalf("anchor must clear on expiry, got %d", a.gracePeriodStartBatch)
	}
}

func TestDeferCompactionForGracePeriodExhaustedAfterExpiry(t *testing.T) {
	a := &MainAgent{}
	if !a.deferCompactionForGracePeriod(3) {
		t.Fatal("first crossing must defer")
	}
	if !a.deferCompactionForGracePeriod(4) {
		t.Fatal("still in grace")
	}
	if a.deferCompactionForGracePeriod(5) {
		t.Fatal("grace expired")
	}
	// Once the grace period expires, the window's active-reset chance is
	// spent: a later threshold crossing in the same window does not re-open it.
	if a.deferCompactionForGracePeriod(9) {
		t.Fatal("grace must not re-open after expiry in the same window")
	}
	if !a.gracePeriodExhausted {
		t.Fatal("expiry must mark the grace period exhausted")
	}
}

func TestDeferCompactionForGracePeriodNilAgent(t *testing.T) {
	var a *MainAgent
	if a.deferCompactionForGracePeriod(1) {
		t.Fatal("nil agent must not defer")
	}
}

func TestApplyModelCompactionConfigSetsThresholdAndClearsGrace(t *testing.T) {
	perModel := 0.3
	a := modelCompTestAgent(
		config.CompactionConfig{Threshold: 0.65},
		map[string]*config.ModelCompactionConfig{"openai/gpt-5.6-luna": {Threshold: &perModel}},
		"openai/gpt-5.6-luna",
	)
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(1000000, 1000000, 0, 0.65)
	a.gracePeriodStartBatch = 4
	a.appliedCompactionModelRef = "openai/gpt-5.6-sol"
	a.applyModelCompactionConfig()
	if got := a.ctxMgr.Threshold(); got != 0.3 {
		t.Fatalf("applied threshold = %v, want 0.3", got)
	}
	if a.gracePeriodStartBatch != 0 {
		t.Fatal("model change must clear the grace period")
	}
	if a.appliedCompactionModelRef != "openai/gpt-5.6-luna" {
		t.Fatalf("applied ref = %q, want openai/gpt-5.6-luna", a.appliedCompactionModelRef)
	}
}

func TestApplyModelCompactionConfigSameModelKeepsGrace(t *testing.T) {
	a := modelCompTestAgent(config.CompactionConfig{Threshold: 0.65}, nil, "p/m")
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(1000000, 1000000, 0, 0.65)
	a.gracePeriodStartBatch = 3
	a.appliedCompactionModelRef = "p/m"
	a.applyModelCompactionConfig()
	if a.gracePeriodStartBatch != 3 {
		t.Fatal("same-model re-apply must keep the grace period")
	}
	if got := a.ctxMgr.Threshold(); got != 0.65 {
		t.Fatalf("threshold = %v, want 0.65", got)
	}
}

func TestApplyModelCompactionConfigFirstApplicationKeepsGrace(t *testing.T) {
	a := modelCompTestAgent(config.CompactionConfig{Threshold: 0.65}, nil, "p/m")
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(1000000, 1000000, 0, 0.65)
	// appliedCompactionModelRef starts as the zero value ""; the first
	// application must not treat that as a model change (no grace clear, no
	// exhausted clear), because a mid-window model-driven skip set exhausted
	// and the first request boundary must not wipe it.
	a.gracePeriodStartBatch = 2
	a.gracePeriodExhausted = true
	a.applyModelCompactionConfig()
	if a.gracePeriodStartBatch != 2 || !a.gracePeriodExhausted {
		t.Fatal("first application must keep grace/exhausted state")
	}
}
