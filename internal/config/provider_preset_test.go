package config

import "testing"

func TestNormalizeProviderPreset_PresetDefaults(t *testing.T) {
	cfg := ProviderConfig{
		Preset: ProviderPresetCodex,
	}

	got, meta, err := NormalizeProviderPreset(cfg)
	if err != nil {
		t.Fatalf("NormalizeProviderPreset returned error: %v", err)
	}
	if !meta.Enabled || !meta.Strict || meta.Source != CodexTransportSourcePreset {
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

func TestNormalizeProviderPreset_NormalizesPresetCase(t *testing.T) {
	cases := []struct {
		name string
		cfg  ProviderConfig
		want string
	}{
		{
			name: "codex",
			cfg:  ProviderConfig{Preset: " CODEX "},
			want: ProviderPresetCodex,
		},
		{
			name: "azure",
			cfg: ProviderConfig{
				Preset: " Azure ",
				APIURL: "https://example.openai.azure.com/openai/v1/responses?api-version=preview",
			},
			want: ProviderPresetAzure,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := NormalizeProviderPreset(tc.cfg)
			if err != nil {
				t.Fatalf("NormalizeProviderPreset returned error: %v", err)
			}
			if got.Preset != tc.want {
				t.Fatalf("Preset = %q, want %q", got.Preset, tc.want)
			}
		})
	}
}

func TestNormalizeProviderPreset_PresetConflict(t *testing.T) {
	cfg := ProviderConfig{
		Type:   ProviderTypeResponses,
		Preset: ProviderPresetCodex,
		APIURL: "https://example.com/v1/responses",
	}

	_, _, err := NormalizeProviderPreset(cfg)
	if err == nil {
		t.Fatal("expected error for conflicting preset config")
	}
}

func TestEffectiveStore(t *testing.T) {
	if EffectiveStore(nil, nil) != false {
		t.Error("both nil: want false")
	}
	if EffectiveStore(new(true), nil) != true {
		t.Error("provider true, model nil: want true")
	}
	if EffectiveStore(nil, new(true)) != true {
		t.Error("provider nil, model true: want true")
	}
	if EffectiveStore(new(true), new(false)) != false {
		t.Error("provider true, model false: model wins, want false")
	}
	if EffectiveStore(new(false), new(true)) != true {
		t.Error("provider false, model true: model wins, want true")
	}
}

func TestNormalizeProviderPreset_StoreDefault(t *testing.T) {
	// preset: codex does not default store to true; explicit config still wins.
	cfg := ProviderConfig{Preset: ProviderPresetCodex}
	got, _, err := NormalizeProviderPreset(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Store != nil {
		t.Fatalf("preset codex: want Store unset (nil), got %v", got.Store)
	}

	// explicit true is preserved and reaches the Responses request body.
	cfg1 := ProviderConfig{Type: ProviderTypeResponses, Preset: ProviderPresetCodex, Store: new(true)}
	got1, _, err := NormalizeProviderPreset(cfg1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got1.Store == nil || *got1.Store != true {
		t.Fatalf("preset codex explicit true: want Store=true, got %v", got1.Store)
	}

	// explicit false must be preserved.
	cfg2 := ProviderConfig{Type: ProviderTypeResponses, Preset: ProviderPresetCodex, Store: new(false)}
	got2, _, err := NormalizeProviderPreset(cfg2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got2.Store == nil || *got2.Store != false {
		t.Fatalf("preset codex explicit false: want Store=false, got %v", got2.Store)
	}
}

func TestNormalizeProviderPreset_PresetRejectsUnsupportedType(t *testing.T) {
	cfg := ProviderConfig{Type: ProviderTypeChatCompletions, Preset: ProviderPresetCodex}
	_, _, err := NormalizeProviderPreset(cfg)
	if err == nil {
		t.Fatal("expected preset codex to reject unsupported type")
	}
}

func TestNormalizeProviderPreset_AzurePresetDefaults(t *testing.T) {
	cfg := ProviderConfig{
		Preset: ProviderPresetAzure,
		APIURL: "https://example.openai.azure.com/openai/v1/responses?api-version=preview",
	}

	got, meta, err := NormalizeProviderPreset(cfg)
	if err != nil {
		t.Fatalf("NormalizeProviderPreset returned error: %v", err)
	}
	if meta.Enabled || meta.Strict || meta.Source != "" {
		t.Fatalf("azure preset should not enable Codex OAuth metadata: %#v", meta)
	}
	if got.Type != ProviderTypeResponses {
		t.Fatalf("azure preset type = %q, want %q", got.Type, ProviderTypeResponses)
	}
	if got.Store == nil || *got.Store != true {
		t.Fatalf("azure preset Store = %v, want true", got.Store)
	}
}

func TestNormalizeProviderPreset_AzurePresetPreservesExplicitStore(t *testing.T) {
	storeFalse := false
	cfg := ProviderConfig{
		Preset: ProviderPresetAzure,
		APIURL: "https://example.openai.azure.com/openai/v1/responses?api-version=preview",
		Store:  &storeFalse,
	}

	got, _, err := NormalizeProviderPreset(cfg)
	if err != nil {
		t.Fatalf("NormalizeProviderPreset returned error: %v", err)
	}
	if got.Store == nil || *got.Store != false {
		t.Fatalf("azure preset explicit Store = %v, want false", got.Store)
	}
}

func TestNormalizeProviderPreset_AzurePresetRejectsUnsupportedType(t *testing.T) {
	cfg := ProviderConfig{
		Type:   ProviderTypeChatCompletions,
		Preset: ProviderPresetAzure,
		APIURL: "https://example.openai.azure.com/openai/v1/responses?api-version=preview",
	}
	_, _, err := NormalizeProviderPreset(cfg)
	if err == nil {
		t.Fatal("expected preset azure to reject unsupported type")
	}
}

func TestNormalizeProviderPreset_AzurePresetRequiresResponsesPath(t *testing.T) {
	cases := []struct {
		name   string
		apiURL string
	}{
		{
			name:   "empty",
			apiURL: "",
		},
		{
			name:   "chat completions with query",
			apiURL: "https://example.openai.azure.com/openai/v1/chat/completions?api-version=preview",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := NormalizeProviderPreset(ProviderConfig{
				Preset: ProviderPresetAzure,
				APIURL: tc.apiURL,
			})
			if err == nil {
				t.Fatal("expected preset azure to reject non-Responses api_url")
			}
		})
	}
}

func TestNormalizeProviderPreset_WithoutPresetDisabled(t *testing.T) {
	cases := []struct {
		name string
		cfg  ProviderConfig
	}{
		{
			name: "plain openai",
			cfg:  ProviderConfig{Type: ProviderTypeChatCompletions},
		},
		{
			name: "token url without preset",
			cfg: ProviderConfig{
				Type:     ProviderTypeChatCompletions,
				TokenURL: OpenAIOAuthTokenURL,
				APIURL:   "https://example.com/openai/v1/responses",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, meta, err := NormalizeProviderPreset(tc.cfg)
			if err != nil {
				t.Fatalf("NormalizeProviderPreset returned error: %v", err)
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
