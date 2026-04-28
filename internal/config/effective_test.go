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
	v := ModelVariant{Text: &TextConfig{Verbosity: "low"}, Thinking: &ThinkingConfig{Type: "enabled"}}
	if v.EffectiveTextVerbosity() != "low" || v.EffectiveThinkingType() != "enabled" {
		t.Fatalf("unexpected variant effective accessors: %+v", v)
	}
}
