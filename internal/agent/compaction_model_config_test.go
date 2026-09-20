package agent

import (
	"math"
	"testing"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
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

func TestEffectiveReminderPctExplicitDisable(t *testing.T) {
	disabled := float64(config.CompactionReminderDisabled)
	// Per-model -1 disables the reminder while automatic compaction stays on.
	a := modelCompTestAgent(
		config.CompactionConfig{Threshold: 0.8, Reminder: 0.5},
		map[string]*config.ModelCompactionConfig{"p/m": {Reminder: &disabled}},
		"p/m",
	)
	if got := a.effectiveReminderPct(0.8); got != 0 {
		t.Fatalf("per-model disabled reminder = %v, want 0 (no reminder)", got)
	}
	if got := a.effectiveCompactionThreshold("p/m"); got != 0.8 {
		t.Fatalf("disabling the reminder must not touch the threshold, got %v", got)
	}
	// Global -1 disables it for models without their own reminder.
	a = modelCompTestAgent(config.CompactionConfig{Threshold: 0.8, Reminder: disabled}, nil, "p/m")
	if got := a.effectiveReminderPct(0.8); got != 0 {
		t.Fatalf("global disabled reminder = %v, want 0", got)
	}
	// A per-model reminder overrides a global disable.
	perModel := 0.4
	a = modelCompTestAgent(
		config.CompactionConfig{Threshold: 0.8, Reminder: disabled},
		map[string]*config.ModelCompactionConfig{"p/m": {Reminder: &perModel}},
		"p/m",
	)
	if got := a.effectiveReminderPct(0.8); got != perModel {
		t.Fatalf("per-model reminder over a global disable = %v, want 0.4", got)
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

func TestApplyModelCompactionConfigSetsThresholdOnModelChange(t *testing.T) {
	perModel := 0.3
	a := modelCompTestAgent(
		config.CompactionConfig{Threshold: 0.65},
		map[string]*config.ModelCompactionConfig{"openai/gpt-5.6-luna": {Threshold: &perModel}},
		"openai/gpt-5.6-luna",
	)
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(1000000, 1000000, 0, 0.65)
	a.appliedCompactionModelRef = "openai/gpt-5.6-sol"
	a.applyModelCompactionConfig()
	if got := a.ctxMgr.Threshold(); got != 0.3 {
		t.Fatalf("applied threshold = %v, want 0.3", got)
	}
	if a.appliedCompactionModelRef != "openai/gpt-5.6-luna" {
		t.Fatalf("applied ref = %q, want openai/gpt-5.6-luna", a.appliedCompactionModelRef)
	}
}

func TestApplyModelCompactionConfigSameModelKeepsThreshold(t *testing.T) {
	a := modelCompTestAgent(config.CompactionConfig{Threshold: 0.65}, nil, "p/m")
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(1000000, 1000000, 0, 0.65)
	a.appliedCompactionModelRef = "p/m"
	a.applyModelCompactionConfig()
	if got := a.ctxMgr.Threshold(); got != 0.65 {
		t.Fatalf("threshold = %v, want 0.65", got)
	}
}

func TestApplyModelCompactionConfigModelChangeMarksNoticesStale(t *testing.T) {
	perModel := 0.3
	a := modelCompTestAgent(
		config.CompactionConfig{Threshold: 0.65},
		map[string]*config.ModelCompactionConfig{"openai/gpt-5.6-luna": {Threshold: &perModel}},
		"openai/gpt-5.6-luna",
	)
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(1000000, 1000000, 0, 0.65)
	a.appliedCompactionModelRef = "openai/gpt-5.6-sol"
	// No durable notice row: the switch has nothing to withdraw, so no audit
	// is armed.
	a.applyModelCompactionConfig()
	if a.contextNoticesStale.Load() {
		t.Fatal("a model change must not arm the audit when no durable notice exists")
	}
	// With a durable row present the moved threshold arms the audit.
	a.ctxMgr.SetThreshold(0.65)
	a.appliedCompactionModelRef = "openai/gpt-5.6-sol"
	a.contextNoticesPersisted.Store(true)
	a.applyModelCompactionConfig()
	if !a.contextNoticesStale.Load() {
		t.Fatal("a model change that moves the threshold must mark the context notices stale")
	}
}

func TestApplyModelCompactionConfigModelChangeSameThresholdKeepsNoticesFresh(t *testing.T) {
	perModel := 0.65
	a := modelCompTestAgent(
		config.CompactionConfig{Threshold: 0.65},
		map[string]*config.ModelCompactionConfig{"openai/gpt-5.6-luna": {Threshold: &perModel}},
		"openai/gpt-5.6-luna",
	)
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(1000000, 1000000, 0, 0.65)
	a.appliedCompactionModelRef = "openai/gpt-5.6-sol"
	a.contextNoticesPersisted.Store(true)
	a.applyModelCompactionConfig()
	if a.contextNoticesStale.Load() {
		t.Fatal("a model change that keeps the same line must keep the context notices fresh")
	}
}

func TestApplyModelCompactionConfigModelChangeReminderOnlyMarksNoticesStale(t *testing.T) {
	// Same threshold on both models, but the switch moves the effective
	// reminder line (derived 0.585 for the old model vs. an explicit 0.2 for
	// the new one): the durable notice described the old line, so it is stale.
	perModelReminder := 0.2
	a := modelCompTestAgent(
		config.CompactionConfig{Threshold: 0.65},
		map[string]*config.ModelCompactionConfig{"openai/gpt-5.6-luna": {Reminder: &perModelReminder}},
		"openai/gpt-5.6-luna",
	)
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(1000000, 1000000, 0, 0.65)
	a.appliedCompactionModelRef = "openai/gpt-5.6-sol"
	a.contextNoticesPersisted.Store(true)
	a.applyModelCompactionConfig()
	if got := a.ctxMgr.Threshold(); got != 0.65 {
		t.Fatalf("threshold = %v, want 0.65 (unchanged)", got)
	}
	if !a.contextNoticesStale.Load() {
		t.Fatal("a model change that moves only the reminder line must mark the context notices stale")
	}
}

func TestApplyModelCompactionConfigSameModelKeepsNoticesFresh(t *testing.T) {
	a := modelCompTestAgent(config.CompactionConfig{Threshold: 0.65}, nil, "p/m")
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(1000000, 1000000, 0, 0.65)
	a.appliedCompactionModelRef = "p/m"
	a.contextNoticesPersisted.Store(true)
	a.applyModelCompactionConfig()
	if a.contextNoticesStale.Load() {
		t.Fatal("a same-model re-apply must not mark the context notices stale")
	}
}

func TestApplyModelCompactionConfigModelChangeKeepsArmedWhenStillOverNewThreshold(t *testing.T) {
	// An armed usage-driven request that still crosses the new model's lower
	// threshold survives the switch: it opens a fresh grace period through the
	// regular gate instead of being force-compacted immediately.
	perModel := 0.3
	a := modelCompTestAgent(
		config.CompactionConfig{Threshold: 0.65},
		map[string]*config.ModelCompactionConfig{"openai/gpt-5.6-luna": {Threshold: &perModel}},
		"openai/gpt-5.6-luna",
	)
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(1000000, 1000000, 0, 0.65)
	a.appliedCompactionModelRef = "openai/gpt-5.6-sol"
	a.armUsageDrivenAutoCompactRequest()
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 500000}) // 0.5 of budget
	a.applyModelCompactionConfig()
	if !a.autoCompactRequested.Load() {
		t.Fatal("armed request must survive the switch when usage still crosses the new threshold")
	}
	if got := a.ctxMgr.Threshold(); got != 0.3 {
		t.Fatalf("applied threshold = %v, want 0.3", got)
	}
	if !a.ctxMgr.AutoCompactDecision().ShouldCompact {
		t.Fatal("usage 0.5 must still cross the new 0.3 threshold")
	}
}

func TestApplyModelCompactionConfigModelChangeClearsStaleArmedRequest(t *testing.T) {
	// A usage-driven request armed under the previous model's threshold must
	// not force-compact the new window when the post-response usage no longer
	// crosses the new model's threshold (e.g. fallback from a small-window
	// model to a large-window one: threshold 0.3 -> 0.7 with usage at 0.5).
	perModel := 0.7
	a := modelCompTestAgent(
		config.CompactionConfig{Threshold: 0.3},
		map[string]*config.ModelCompactionConfig{"openai/gpt-5.6-sol": {Threshold: &perModel}},
		"openai/gpt-5.6-sol",
	)
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(1000000, 1000000, 0, 0.3)
	a.appliedCompactionModelRef = "openai/gpt-5.6-luna"
	a.armUsageDrivenAutoCompactRequest()
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 500000}) // 0.5 of budget
	a.applyModelCompactionConfig()
	if a.autoCompactRequested.Load() {
		t.Fatal("stale armed request must be cleared when usage no longer crosses the new threshold")
	}
	if got := a.ctxMgr.Threshold(); got != 0.7 {
		t.Fatalf("applied threshold = %v, want 0.7", got)
	}
	if a.ctxMgr.AutoCompactDecision().ShouldCompact {
		t.Fatal("usage 0.5 must not cross the new 0.7 threshold")
	}
}

func TestIdleAutoCompactionReevaluatesStaleArmAgainstRealignedModel(t *testing.T) {
	// A non-narrowing fallback boundary commits nothing, so when its round ends
	// the armed request and the threshold can still describe the small-window
	// model the identity has realigned away from. maybeRunAutoCompaction must
	// re-apply the per-model config before deciding, or the idle path
	// force-compacts the wider window under the previous model's line.
	a := modelCompTestAgent(
		config.CompactionConfig{Threshold: 0.3},
		map[string]*config.ModelCompactionConfig{"openai/gpt-5.6-sol": {Threshold: new(0.7)}},
		"openai/gpt-5.6-luna",
	)
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(1000000, 1000000, 0, 0.3)
	a.appliedCompactionModelRef = "openai/gpt-5.6-luna"
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 500000}) // 0.5 of budget
	if !a.ctxMgr.AutoCompactDecision().ShouldCompact {
		t.Fatal("precondition: usage 0.5 must cross luna's 0.3 line")
	}
	// The round's own success path armed the request against luna's line.
	a.armUsageDrivenAutoCompactRequest()

	// The wider model took over the round and its first token realigned the
	// identity; the boundary committed nothing because it did not narrow.
	a.runningModelRef = "openai/gpt-5.6-sol"

	a.maybeRunAutoCompaction()
	if a.autoCompactRequested.Load() {
		t.Fatal("stale armed request must not survive the idle re-evaluation")
	}
	if got := a.ctxMgr.Threshold(); got != 0.7 {
		t.Fatalf("applied threshold = %v, want the realigned model's 0.7", got)
	}
}

func TestApplyModelCompactionConfigModelChangeArmsCrossedUsage(t *testing.T) {
	// A switch onto a model whose stricter threshold the current context already
	// crosses arms the usage-driven request, so the next pre-request gate starts
	// the compaction in parallel with the round instead of deferring that round
	// behind the compaction.
	perModel := 0.3
	a := modelCompTestAgent(
		config.CompactionConfig{Threshold: 0.65},
		map[string]*config.ModelCompactionConfig{"openai/gpt-5.6-luna": {Threshold: &perModel}},
		"openai/gpt-5.6-luna",
	)
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(1000000, 1000000, 0, 0.65)
	a.appliedCompactionModelRef = "openai/gpt-5.6-sol"
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 500000}) // 0.5 of budget
	if a.autoCompactRequested.Load() {
		t.Fatal("precondition: nothing arms the request before the switch")
	}
	a.applyModelCompactionConfig()
	if !a.autoCompactRequested.Load() {
		t.Fatal("a switch onto a crossed line must arm the usage-driven compaction")
	}
	if got := a.ctxMgr.Threshold(); got != 0.3 {
		t.Fatalf("applied threshold = %v, want 0.3", got)
	}
}

func TestApplyModelCompactionConfigSameModelKeepsArmedRequest(t *testing.T) {
	// No model change: the armed request is never re-evaluated or cleared.
	a := modelCompTestAgent(config.CompactionConfig{Threshold: 0.65}, nil, "p/m")
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(1000000, 1000000, 0, 0.65)
	a.appliedCompactionModelRef = "p/m"
	a.armUsageDrivenAutoCompactRequest()
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 200000}) // below the threshold
	a.applyModelCompactionConfig()
	if !a.autoCompactRequested.Load() {
		t.Fatal("same-model re-apply must not touch the armed request")
	}
}

func TestContextPressureLinesForModelRefUsesModelConfig(t *testing.T) {
	// Per-model threshold + inherited global reminder (no per-model reminder).
	perModel := 0.6
	a := modelCompTestAgent(
		config.CompactionConfig{Threshold: 0.7, Reminder: 0.55},
		map[string]*config.ModelCompactionConfig{"openai/gpt-x": {Threshold: &perModel}},
		"openai/gpt-x",
	)
	reminder, threshold := a.ContextPressureLinesForModelRef("openai/gpt-x")
	if threshold != 0.6 || reminder != 0.55 {
		t.Fatalf("lines = (%v, %v), want (0.55, 0.6)", reminder, threshold)
	}

	// A model without overrides inherits the global threshold and reminder.
	reminder, threshold = a.ContextPressureLinesForModelRef("other/model")
	if threshold != 0.7 || reminder != 0.55 {
		t.Fatalf("inherited lines = (%v, %v), want (0.55, 0.7)", reminder, threshold)
	}

	// Global threshold with derived reminder: min(0.60, 0.8*0.90).
	a = modelCompTestAgent(config.CompactionConfig{Threshold: 0.8}, nil, "p/m")
	reminder, threshold = a.ContextPressureLinesForModelRef("p/m")
	if threshold != 0.8 || reminder != 0.60 {
		t.Fatalf("derived lines = (%v, %v), want (0.60, 0.8)", reminder, threshold)
	}

	// Global threshold 0 disables auto-compaction and reminders entirely.
	a = modelCompTestAgent(config.CompactionConfig{}, nil, "p/m")
	reminder, threshold = a.ContextPressureLinesForModelRef("p/m")
	if reminder != 0 || threshold != 0 {
		t.Fatalf("disabled lines = (%v, %v), want (0, 0)", reminder, threshold)
	}

	// Empty/unknown refs report no lines so callers keep their fallback.
	if reminder, threshold := a.ContextPressureLinesForModelRef(""); reminder != 0 || threshold != 0 {
		t.Fatalf("empty-ref lines = (%v, %v), want (0, 0)", reminder, threshold)
	}
}

func TestContextPressureLinesForModelRefFollowsTheRefNotTheRunningModel(t *testing.T) {
	// The lines are a pure mapping from the queried ref: two models with
	// different thresholds return their own lines regardless of which one the
	// agent is currently running, which is what lets the TUI re-color the
	// context display as soon as a pending model switch is shown.
	weak := 0.3
	spacious := 0.7
	a := modelCompTestAgent(
		config.CompactionConfig{Threshold: 0.8},
		map[string]*config.ModelCompactionConfig{
			"openai/weak":     {Threshold: &weak},
			"openai/spacious": {Threshold: &spacious},
		},
		"openai/spacious", // running ref says the spacious model
	)
	reminder, threshold := a.ContextPressureLinesForModelRef("openai/weak")
	if threshold != 0.3 || reminder != 0.27 {
		t.Fatalf("weak lines = (%v, %v), want (0.27, 0.3)", reminder, threshold)
	}
	reminder, threshold = a.ContextPressureLinesForModelRef("openai/spacious")
	if threshold != 0.7 || reminder != 0.6 {
		t.Fatalf("spacious lines = (%v, %v), want (0.60, 0.7)", reminder, threshold)
	}
}
