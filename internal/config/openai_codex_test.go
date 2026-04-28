package config

import "testing"

func TestNormalizeOpenAICodexProvider_PresetDefaults(t *testing.T) {
	cfg := ProviderConfig{
		Preset: ProviderPresetCodex,
	}

	got, meta, err := NormalizeOpenAICodexProvider(cfg, false)
	if err != nil {
		t.Fatalf("NormalizeOpenAICodexProvider returned error: %v", err)
	}
	if !meta.Enabled || !meta.Strict || meta.Source != OpenAICodexSourcePreset {
		t.Fatalf("unexpected meta: %#v", meta)
	}
	if got.APIURL != OpenAICodexResponsesURL {
		t.Fatalf("unexpected api_url: %q", got.APIURL)
	}
	if got.TokenURL != OpenAIOAuthTokenURL {
		t.Fatalf("unexpected token_url: %q", got.TokenURL)
	}
	if got.ClientID != OpenAIOAuthClientID {
		t.Fatalf("unexpected client_id: %q", got.ClientID)
	}
}

func TestNormalizeOpenAICodexProvider_PresetConflict(t *testing.T) {
	cfg := ProviderConfig{
		Type:   ProviderTypeResponses,
		Preset: ProviderPresetCodex,
		APIURL: "https://example.com/v1/responses",
	}

	_, _, err := NormalizeOpenAICodexProvider(cfg, false)
	if err == nil {
		t.Fatal("expected error for conflicting preset config")
	}
}

func TestEffectiveStore(t *testing.T) {
	true_ := true
	false_ := false

	if EffectiveStore(nil, nil) != false {
		t.Error("both nil: want false")
	}
	if EffectiveStore(&true_, nil) != true {
		t.Error("provider true, model nil: want true")
	}
	if EffectiveStore(nil, &true_) != true {
		t.Error("provider nil, model true: want true")
	}
	if EffectiveStore(&true_, &false_) != false {
		t.Error("provider true, model false: model wins, want false")
	}
	if EffectiveStore(&false_, &true_) != true {
		t.Error("provider false, model true: model wins, want true")
	}
}

func TestNormalizeOpenAICodexProvider_StoreDefault(t *testing.T) {
	// preset: codex does not default store to true (official OAuth API requires false on wire).
	cfg := ProviderConfig{Preset: ProviderPresetCodex}
	got, _, err := NormalizeOpenAICodexProvider(cfg, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Store != nil {
		t.Fatalf("preset codex: want Store unset (nil), got %v", got.Store)
	}

	// explicit true is preserved for config round-trip; ResponsesProvider overrides for OAuth keys.
	true_ := true
	cfg1 := ProviderConfig{Type: ProviderTypeResponses, Preset: ProviderPresetCodex, Store: &true_}
	got1, _, err := NormalizeOpenAICodexProvider(cfg1, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got1.Store == nil || *got1.Store != true {
		t.Fatalf("preset codex explicit true: want Store=true, got %v", got1.Store)
	}

	// explicit false must be preserved.
	false_ := false
	cfg2 := ProviderConfig{Type: ProviderTypeResponses, Preset: ProviderPresetCodex, Store: &false_}
	got2, _, err := NormalizeOpenAICodexProvider(cfg2, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got2.Store == nil || *got2.Store != false {
		t.Fatalf("preset codex explicit false: want Store=false, got %v", got2.Store)
	}
}

func TestNormalizeOpenAICodexProvider_PresetRejectsLegacyType(t *testing.T) {
	cfg := ProviderConfig{Type: ProviderTypeChatCompletions, Preset: ProviderPresetCodex}
	_, _, err := NormalizeOpenAICodexProvider(cfg, false)
	if err == nil {
		t.Fatal("expected preset codex to reject legacy type=openai")
	}
}

func TestNormalizeOpenAICodexProvider_WithoutPresetDisabled(t *testing.T) {
	cases := []struct {
		name string
		cfg  ProviderConfig
	}{
		{
			name: "plain openai",
			cfg:  ProviderConfig{Type: ProviderTypeChatCompletions},
		},
		{
			name: "legacy token url",
			cfg: ProviderConfig{
				Type:     ProviderTypeChatCompletions,
				TokenURL: OpenAIOAuthTokenURL,
				APIURL:   "https://example.com/openai/v1/responses",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, meta, err := NormalizeOpenAICodexProvider(tc.cfg, true)
			if err != nil {
				t.Fatalf("NormalizeOpenAICodexProvider returned error: %v", err)
			}
			if meta.Enabled || meta.Strict || meta.Source != "" {
				t.Fatalf("unexpected meta: %#v", meta)
			}
			if got.Type != tc.cfg.Type || got.APIURL != tc.cfg.APIURL || got.TokenURL != tc.cfg.TokenURL || got.ClientID != tc.cfg.ClientID || got.Preset != tc.cfg.Preset {
				t.Fatalf("expected config unchanged, got %#v want %#v", got, tc.cfg)
			}
		})
	}
}
