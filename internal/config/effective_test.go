package config

import "testing"

func TestConfigEffectiveHelpers(t *testing.T) {
	truePtr := true
	falsePtr := false

	if !EffectiveResponsesWebsocket(" CODEX ", nil) {
		t.Fatal("codex preset should default websocket on")
	}
	if EffectiveResponsesWebsocket("codex", &falsePtr) {
		t.Fatal("provider override false should disable websocket")
	}
	if !EffectiveResponsesWebsocket("other", &truePtr) {
		t.Fatal("provider override true should enable websocket")
	}
	if EffectiveResponsesWebsocket(ProviderPresetAzure, nil) {
		t.Fatal("azure preset should default websocket off")
	}

	if !EffectiveStore(&falsePtr, &truePtr) {
		t.Fatal("model Store should override provider Store")
	}
	if !EffectiveStore(&truePtr, nil) {
		t.Fatal("provider Store should be used when model Store nil")
	}
	if EffectiveStore(nil, nil) {
		t.Fatal("nil Store values should default false")
	}

	compat := &ThinkingToolcallCompatConfig{Enabled: &truePtr}
	if !compat.EnabledValue() {
		t.Fatal("EnabledValue should read true pointer")
	}
	if (&ThinkingToolcallCompatConfig{Enabled: &falsePtr}).EnabledValue() {
		t.Fatal("EnabledValue should read false pointer")
	}
	if (*ThinkingToolcallCompatConfig)(nil).EnabledValue() {
		t.Fatal("nil compat should default false")
	}

	cache := &PromptCacheConfig{CacheTools: &truePtr}
	if !cache.CacheToolsEnabled() {
		t.Fatal("CacheToolsEnabled should read true pointer")
	}
	if (*PromptCacheConfig)(nil).CacheToolsEnabled() {
		t.Fatal("nil prompt cache should default false")
	}
}

func TestModelConfigEffectiveAccessors(t *testing.T) {
	m := ModelConfig{
		Modalities:  &ModelModalities{Input: []string{"text", "pdf"}},
		Text:        &TextConfig{Verbosity: "high"},
		Thinking:    &ThinkingConfig{Type: "adaptive", Effort: "medium", Display: "summary"},
		PromptCache: &PromptCacheConfig{Mode: "auto"},
	}
	if !m.SupportsInput("pdf") || m.SupportsInput("image") {
		t.Fatalf("SupportsInput with explicit modalities unexpected")
	}
	defaultModalities := ModelConfig{}
	if !defaultModalities.SupportsInput("text") || !defaultModalities.SupportsInput("image") || defaultModalities.SupportsInput("pdf") {
		t.Fatalf("SupportsInput defaults unexpected")
	}
	if m.EffectiveTextVerbosity() != "high" || m.EffectiveThinkingType() != "adaptive" || m.EffectiveThinkingEffort() != "medium" || m.EffectiveThinkingDisplay() != "summary" || m.EffectivePromptCacheMode() != "auto" {
		t.Fatalf("unexpected model effective accessors: %+v", m)
	}
	if (ModelConfig{}).EffectivePromptCacheMode() != "explicit" {
		t.Fatal("prompt cache mode should default explicit")
	}
	if tiers := (&ModelConfig{}).SupportedServiceTierSet(ProviderPresetCodex, nil); !tiers[ServiceTierFast] || !tiers[ServiceTierSlow] {
		t.Fatal("preset: codex should default to fast and slow service tiers")
	}
	if len((&ModelConfig{}).SupportedServiceTierSet("", nil)) != 0 {
		t.Fatal("non-codex preset should default service tiers to false")
	}
	tiers := (&ModelConfig{SupportedServiceTiers: []ServiceTier{ServiceTierSlow}}).SupportedServiceTierSet(ProviderPresetCodex, nil)
	if tiers[ServiceTierFast] || !tiers[ServiceTierSlow] {
		t.Fatalf("supported_service_tiers should override defaults, got %#v", tiers)
	}

	providerTiers := []ServiceTier{ServiceTierFast, ServiceTierSlow}
	if tiers := (&ModelConfig{}).SupportedServiceTierSet("", providerTiers); !tiers[ServiceTierSlow] {
		t.Fatal("provider supported_service_tiers should act as model default")
	}
	modelOverride := (&ModelConfig{SupportedServiceTiers: []ServiceTier{ServiceTierFast}}).SupportedServiceTierSet("", providerTiers)
	if !modelOverride[ServiceTierFast] || modelOverride[ServiceTierSlow] {
		t.Fatalf("model supported_service_tiers should override provider default, got %#v", modelOverride)
	}
	if NormalizeServiceTier(" FAST ") != ServiceTierFast {
		t.Fatal("NormalizeServiceTier should fold fast input")
	}
	if NormalizeServiceTier("") != ServiceTierStandard {
		t.Fatal("empty service tier should default to standard")
	}
	pricing := ModelCost{
		Input:                  1,
		Output:                 2,
		CacheRead:              0.1,
		ServiceTierMultipliers: &ServiceTierMultipliers{Fast: 2, Slow: 0.5},
		InputTiers:             []ModelCostInputTier{{AboveInputTokens: 100, Input: 3, Output: 4, CacheRead: 0.3, CacheWrite1h: 5}},
	}
	resolved := pricing.ResolvePricing(50, ServiceTierStandard)
	if resolved.Input != 1 || resolved.Output != 2 || resolved.CacheWrite != 1 || resolved.CacheWrite1h != 1 || resolved.InputTierAboveTokens != -1 || resolved.ServiceTierMultiplier != 1 {
		t.Fatalf("unexpected base pricing resolution: %+v", resolved)
	}
	resolved = pricing.ResolvePricing(101, ServiceTierFast)
	if resolved.Input != 6 || resolved.Output != 8 || resolved.CacheRead != 0.6 || resolved.CacheWrite != 6 || resolved.CacheWrite1h != 10 {
		t.Fatalf("unexpected tier pricing resolution: %+v", resolved)
	}
	if resolved.InputTierAboveTokens != 100 || resolved.ServiceTier != ServiceTierFast || resolved.ServiceTierMultiplier != 2 {
		t.Fatalf("unexpected resolved metadata: %+v", resolved)
	}
	v := ModelVariant{Text: &TextConfig{Verbosity: "low"}, Thinking: &ThinkingConfig{Type: "enabled"}}
	if v.EffectiveTextVerbosity() != "low" || v.EffectiveThinkingType() != "enabled" {
		t.Fatalf("unexpected variant effective accessors: %+v", v)
	}
}
