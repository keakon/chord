package llm

import (
	"context"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestClientSetVariantResetsModelDefaultsBeforeApplyingNewVariant(t *testing.T) {
	cfg := NewProviderConfig("anthropic", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"claude": {
				Limit: config.ModelLimit{Context: 128000, Output: 4096},
				Thinking: &config.ThinkingConfig{
					Type:    "adaptive",
					Effort:  "medium",
					Display: "summarized",
				},
				Variants: map[string]config.ModelVariant{
					"high": {
						Thinking: &config.ThinkingConfig{Effort: "high"},
					},
					"hidden": {
						Thinking: &config.ThinkingConfig{Display: "omitted"},
					},
				},
			},
		},
	}, []string{"k"})
	c := NewClient(cfg, &scriptedProvider{}, "claude", 4096, "sys")

	c.SetVariant("high")
	if got := c.tuning.Anthropic.ThinkingEffort; got != "high" {
		t.Fatalf("after @high effort = %q, want high", got)
	}
	if got := c.tuning.Anthropic.ThinkingDisplay; got != "summarized" {
		t.Fatalf("after @high display = %q, want summarized", got)
	}

	c.SetVariant("hidden")
	if got := c.tuning.Anthropic.ThinkingEffort; got != "medium" {
		t.Fatalf("after switching to @hidden effort = %q, want model default medium", got)
	}
	if got := c.tuning.Anthropic.ThinkingDisplay; got != "omitted" {
		t.Fatalf("after switching to @hidden display = %q, want omitted", got)
	}
}

func TestClientSetVariantBudgetOnlyDoesNotImplicitlySwitchThinkingType(t *testing.T) {
	cfg := NewProviderConfig("anthropic", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"claude": {
				Limit: config.ModelLimit{Context: 128000, Output: 4096},
				Thinking: &config.ThinkingConfig{
					Type:    "adaptive",
					Effort:  "medium",
					Display: "summarized",
				},
				Variants: map[string]config.ModelVariant{
					"manual": {
						Thinking: &config.ThinkingConfig{Budget: 12000},
					},
				},
			},
		},
	}, []string{"k"})
	c := NewClient(cfg, &scriptedProvider{}, "claude", 4096, "sys")

	c.SetVariant("manual")
	if got := c.tuning.Anthropic.ThinkingType; got != "adaptive" {
		t.Fatalf("after @manual type = %q, want adaptive", got)
	}
	if got := c.tuning.Anthropic.ThinkingBudget; got != 0 {
		t.Fatalf("after @manual budget = %d, want 0", got)
	}
	if got := c.tuning.Anthropic.ThinkingEffort; got != "medium" {
		t.Fatalf("after @manual effort = %q, want medium", got)
	}
	if got := c.tuning.Anthropic.ThinkingDisplay; got != "summarized" {
		t.Fatalf("after @manual display = %q, want summarized", got)
	}
}

func TestClientSetVariantMergesPromptCacheOverrides(t *testing.T) {
	cacheToolsFalse := false
	cfg := NewProviderConfig("anthropic", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"claude": {
				Limit: config.ModelLimit{Context: 128000, Output: 4096},
				PromptCache: &config.PromptCacheConfig{
					Mode:       "auto",
					TTL:        "1h",
					CacheTools: new(cacheToolsFalse),
				},
				Variants: map[string]config.ModelVariant{
					"explicit-tools": {
						PromptCache: &config.PromptCacheConfig{
							Mode:       "explicit",
							CacheTools: new(true),
						},
					},
				},
			},
		},
	}, []string{"k"})
	c := NewClient(cfg, &scriptedProvider{}, "claude", 4096, "sys")

	c.SetVariant("explicit-tools")
	if got := c.tuning.Anthropic.PromptCacheMode; got != "explicit" {
		t.Fatalf("PromptCacheMode = %q, want explicit", got)
	}
	if got := c.tuning.Anthropic.PromptCacheTTL; got != "1h" {
		t.Fatalf("PromptCacheTTL = %q, want inherited 1h", got)
	}
	if !c.tuning.Anthropic.CacheTools {
		t.Fatal("CacheTools = false, want true")
	}
}

func TestClientSetVariantOverridesParallelToolCalls(t *testing.T) {
	cfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{
			"gpt-5.5": {
				Limit:             config.ModelLimit{Context: 400000, Output: 128000},
				ParallelToolCalls: new(true),
				Variants: map[string]config.ModelVariant{
					"serial": {
						ParallelToolCalls: new(false),
					},
				},
			},
		},
	}, []string{"k"})
	c := NewClient(cfg, &scriptedProvider{}, "gpt-5.5", 4096, "sys")

	if c.tuning.OpenAI.ParallelToolCalls == nil || !*c.tuning.OpenAI.ParallelToolCalls {
		t.Fatalf("default parallel_tool_calls = %#v, want true", c.tuning.OpenAI.ParallelToolCalls)
	}

	c.SetVariant("serial")
	if c.tuning.OpenAI.ParallelToolCalls == nil || *c.tuning.OpenAI.ParallelToolCalls {
		t.Fatalf("after @serial parallel_tool_calls = %#v, want false", c.tuning.OpenAI.ParallelToolCalls)
	}
}

func TestClientProviderParallelToolCallsPrecedence(t *testing.T) {
	// Model-level true wins over provider-level false.
	cfgModelOverrides := NewProviderConfig("openai", config.ProviderConfig{
		Type:              config.ProviderTypeResponses,
		ParallelToolCalls: new(false),
		Models: map[string]config.ModelConfig{
			"gpt-5.5": {
				Limit:             config.ModelLimit{Context: 400000, Output: 128000},
				ParallelToolCalls: new(true),
				Variants: map[string]config.ModelVariant{
					"serial": {ParallelToolCalls: new(false)},
				},
			},
		},
	}, []string{"k"})
	c := NewClient(cfgModelOverrides, &scriptedProvider{}, "gpt-5.5", 4096, "sys")
	if c.tuning.OpenAI.ParallelToolCalls == nil || !*c.tuning.OpenAI.ParallelToolCalls {
		t.Fatalf("model should override provider default to true, got %#v", c.tuning.OpenAI.ParallelToolCalls)
	}
	c.SetVariant("serial")
	if c.tuning.OpenAI.ParallelToolCalls == nil || *c.tuning.OpenAI.ParallelToolCalls {
		t.Fatalf("variant should override model to false, got %#v", c.tuning.OpenAI.ParallelToolCalls)
	}

	// Without a model-level setting, the provider default applies.
	cfgProviderDefault := NewProviderConfig("openai", config.ProviderConfig{
		Type:              config.ProviderTypeResponses,
		ParallelToolCalls: new(false),
		Models: map[string]config.ModelConfig{
			"gpt-5.5": {Limit: config.ModelLimit{Context: 400000, Output: 128000}},
		},
	}, []string{"k"})
	c2 := NewClient(cfgProviderDefault, &scriptedProvider{}, "gpt-5.5", 4096, "sys")
	if c2.tuning.OpenAI.ParallelToolCalls == nil || *c2.tuning.OpenAI.ParallelToolCalls {
		t.Fatalf("provider default false should apply when model is unset, got %#v", c2.tuning.OpenAI.ParallelToolCalls)
	}
}

func TestCompleteStreamAppliesActiveVariantOpenAITuning(t *testing.T) {
	provider := &recordingTuningProvider{}
	cfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{
			"gpt-5.5": {
				Limit:     config.ModelLimit{Context: 400000, Input: 272000, Output: 128000},
				Reasoning: &config.ReasoningConfig{Summary: "auto"},
				Variants: map[string]config.ModelVariant{
					"xhigh": {Reasoning: &config.ReasoningConfig{Effort: "xhigh"}},
				},
			},
		},
	}, []string{"k"})
	c := NewClient(cfg, provider, "gpt-5.5", 4096, "sys")
	c.SetVariant("xhigh")

	if _, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if len(provider.tuning) != 1 {
		t.Fatalf("len(provider.tuning) = %d, want 1", len(provider.tuning))
	}
	got := provider.tuning[0].OpenAI
	if got.ReasoningEffort != "xhigh" || got.ReasoningSummary != "auto" {
		t.Fatalf("OpenAI tuning = %+v, want reasoning effort xhigh summary auto", got)
	}
}

func TestClientServiceTierSkipsUnsupportedModel(t *testing.T) {
	cfg := testProviderConfig("openai", "gpt-5-codex")
	expect := RequestTuning{Anthropic: AnthropicTuning{PromptCacheMode: "explicit"}}
	impl := &scriptedProvider{calls: []scriptedCall{{resp: &message.Response{Content: "ok"}, expectTuning: &expect}}}
	c := NewClient(cfg, impl, "gpt-5-codex", 4096, "sys")
	c.SetStreamRetryRounds(1)
	c.SetServiceTier(config.ServiceTierFast)

	if _, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
}

func TestClientServiceTierDefaultsToCodexPreset(t *testing.T) {
	cfg := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		Preset: config.ProviderPresetCodex,
		Models: map[string]config.ModelConfig{
			"gpt-5-codex": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"test-key"})
	expect := RequestTuning{
		Anthropic:             AnthropicTuning{PromptCacheMode: "explicit", ServiceTier: "fast"},
		OpenAI:                OpenAITuning{ServiceTier: "fast"},
		SupportedServiceTiers: map[config.ServiceTier]bool{config.ServiceTierFast: true, config.ServiceTierSlow: true},
	}
	impl := &scriptedProvider{calls: []scriptedCall{{resp: &message.Response{Content: "ok"}, expectTuning: &expect}}}
	c := NewClient(cfg, impl, "gpt-5-codex", 4096, "sys")
	c.SetStreamRetryRounds(1)
	c.SetServiceTier(config.ServiceTierFast)

	if _, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
}

func TestClientServiceTierOverridesRequestTuning(t *testing.T) {
	cfg := testFastProviderConfig("openai", "gpt-5-codex")
	expect := RequestTuning{
		Anthropic:             AnthropicTuning{PromptCacheMode: "explicit", ServiceTier: "fast"},
		OpenAI:                OpenAITuning{ServiceTier: "fast"},
		SupportedServiceTiers: map[config.ServiceTier]bool{config.ServiceTierFast: true},
	}
	impl := &scriptedProvider{calls: []scriptedCall{{resp: &message.Response{Content: "ok"}, expectTuning: &expect}}}
	c := NewClient(cfg, impl, "gpt-5-codex", 4096, "sys")
	c.SetStreamRetryRounds(1)
	c.SetServiceTier(config.ServiceTierFast)

	if _, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
}

func TestClientServiceTierAppliesToFallbackTuning(t *testing.T) {
	primaryCfg := testProviderConfig("primary", "primary-model")
	fallbackCfg := testFastProviderConfig("fallback", "gpt-5-codex")
	primaryImpl := &scriptedProvider{calls: []scriptedCall{{err: &APIError{StatusCode: 503, Message: "try fallback"}}}}
	expect := RequestTuning{
		Anthropic: AnthropicTuning{PromptCacheMode: "explicit", ServiceTier: "fast"},
		OpenAI:    OpenAITuning{ServiceTier: "fast"},

		SupportedServiceTiers: map[config.ServiceTier]bool{config.ServiceTierFast: true},
	}
	fallbackImpl := &scriptedProvider{calls: []scriptedCall{{resp: &message.Response{Content: "ok"}, expectTuning: &expect}}}
	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetStreamRetryRounds(1)
	c.SetModelPool([]FallbackModel{
		{ProviderConfig: primaryCfg, ProviderImpl: primaryImpl, ModelID: "primary-model", MaxTokens: 4096, ContextLimit: 128000},
		{ProviderConfig: fallbackCfg, ProviderImpl: fallbackImpl, ModelID: "gpt-5-codex", MaxTokens: 4096, ContextLimit: 128000},
	}, 0)
	c.SetServiceTier(config.ServiceTierFast)

	if _, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
}

func TestClientServiceTierToggleAppliesToNextRetryRound(t *testing.T) {
	cfg := testFastProviderConfig("openai", "gpt-5-codex")
	expectFast := RequestTuning{
		Anthropic: AnthropicTuning{PromptCacheMode: "explicit", ServiceTier: "fast"},
		OpenAI:    OpenAITuning{ServiceTier: "fast"},

		SupportedServiceTiers: map[config.ServiceTier]bool{config.ServiceTierFast: true},
	}
	c := NewClient(cfg, nil, "gpt-5-codex", 4096, "sys")
	disableRetryDelayForTest(cfg)
	impl := &serviceTierToggleRetryProvider{client: c, expectFast: expectFast}
	c.providerImpl = impl

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "ok" {
		t.Fatalf("resp = %#v, want ok", resp)
	}
	if impl.calls != 2 {
		t.Fatalf("provider calls = %d, want 2", impl.calls)
	}
	if got := c.LastCallStatus().ServiceTier; got != config.ServiceTierFast {
		t.Fatalf("LastCallStatus().ServiceTier = %q, want fast from successful retry round", got)
	}
}

func TestClientLastCallStatusKeepsActualServiceTierWhenTierChangesDuringRequest(t *testing.T) {
	cfg := testFastProviderConfig("openai", "gpt-5-codex")
	c := NewClient(cfg, nil, "gpt-5-codex", 4096, "sys")
	c.SetStreamRetryRounds(1)
	c.SetServiceTier(config.ServiceTierFast)
	c.providerImpl = &serviceTierSwitchingProvider{client: c}

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "ok" {
		t.Fatalf("resp = %#v, want ok", resp)
	}
	if got := c.ServiceTier(); got != config.ServiceTierStandard {
		t.Fatalf("current ServiceTier() = %q, want standard after provider switched it", got)
	}
	if got := c.LastCallStatus().ServiceTier; got != config.ServiceTierFast {
		t.Fatalf("LastCallStatus().ServiceTier = %q, want actual request tier fast", got)
	}
}

func TestClientServiceTierSet(t *testing.T) {
	c := NewClient(testProviderConfig("openai", "gpt-5-codex"), &scriptedProvider{}, "gpt-5-codex", 4096, "sys")
	if got := c.ServiceTier(); got != config.ServiceTierStandard {
		t.Fatalf("ServiceTier() = %q, want standard by default", got)
	}
	c.SetServiceTier(config.ServiceTierFast)
	if got := c.ServiceTier(); got != config.ServiceTierFast {
		t.Fatalf("ServiceTier() = %q, want fast", got)
	}
	c.SetServiceTier(config.ServiceTierStandard)
	if got := c.ServiceTier(); got != config.ServiceTierStandard {
		t.Fatalf("ServiceTier() = %q, want standard", got)
	}
}

func TestClientNextRequestTuningOverrideIsOneShot(t *testing.T) {
	provider := &recordingTuningProvider{}
	cfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-5.5": {
				Limit:             config.ModelLimit{Context: 400000, Output: 128000},
				Reasoning:         &config.ReasoningConfig{Effort: "high", Summary: "auto"},
				Text:              &config.TextConfig{Verbosity: "low"},
				ParallelToolCalls: new(true),
			},
		},
	}, []string{"k"})
	c := NewClient(cfg, provider, "gpt-5.5", 4096, "sys")
	parallelFalse := false
	parallelTrue := true
	c.SetNextRequestTuningOverride(RequestTuning{OpenAI: OpenAITuning{ParallelToolCalls: &parallelFalse}})
	if _, err := c.CompleteStream(context.Background(), nil, nil, nil); err != nil {
		t.Fatalf("first Complete: %v", err)
	}
	if _, err := c.CompleteStream(context.Background(), nil, nil, nil); err != nil {
		t.Fatalf("second Complete: %v", err)
	}
	if len(provider.tuning) != 2 {
		t.Fatalf("len(provider.tuning) = %d, want 2", len(provider.tuning))
	}
	if provider.tuning[0].OpenAI.ParallelToolCalls == nil || *provider.tuning[0].OpenAI.ParallelToolCalls != parallelFalse {
		t.Fatalf("first tuning = %#v, want parallel_tool_calls=false", provider.tuning[0])
	}
	if got := provider.tuning[0].OpenAI; got.ReasoningEffort != "high" || got.ReasoningSummary != "auto" || got.TextVerbosity != "low" {
		t.Fatalf("first tuning OpenAI = %+v, want model defaults preserved", got)
	}
	if provider.tuning[1].OpenAI.ParallelToolCalls == nil || *provider.tuning[1].OpenAI.ParallelToolCalls != parallelTrue {
		t.Fatalf("second tuning = %#v, want parallel_tool_calls=true", provider.tuning[1])
	}
}

func TestClientEmptyNextRequestTuningOverridePreservesModelDefaults(t *testing.T) {
	tests := []struct {
		name       string
		providerID string
		provider   config.ProviderConfig
		modelID    string
		assert     func(t *testing.T, tuning RequestTuning)
	}{
		{
			name:       "openai",
			providerID: "openai",
			provider: config.ProviderConfig{
				Type: config.ProviderTypeResponses,
				Models: map[string]config.ModelConfig{
					"gpt-5.5": {
						Limit:     config.ModelLimit{Context: 400000, Output: 128000},
						Reasoning: &config.ReasoningConfig{Effort: "high", Summary: "auto"},
						Text:      &config.TextConfig{Verbosity: "low"},
					},
				},
			},
			modelID: "gpt-5.5",
			assert: func(t *testing.T, tuning RequestTuning) {
				t.Helper()
				got := tuning.OpenAI
				if got.ReasoningEffort != "high" || got.ReasoningSummary != "auto" || got.TextVerbosity != "low" {
					t.Fatalf("OpenAI tuning = %+v, want model defaults preserved", got)
				}
			},
		},
		{
			name:       "anthropic",
			providerID: "anthropic",
			provider: config.ProviderConfig{
				Type: config.ProviderTypeMessages,
				Models: map[string]config.ModelConfig{
					"claude-sonnet": {
						Limit: config.ModelLimit{Context: 200000, Output: 8192},
						Thinking: &config.ThinkingConfig{
							Type:    "adaptive",
							Effort:  "medium",
							Display: "summarized",
						},
						PromptCache: &config.PromptCacheConfig{
							Mode:       "auto",
							TTL:        "1h",
							CacheTools: new(true),
						},
					},
				},
			},
			modelID: "claude-sonnet",
			assert: func(t *testing.T, tuning RequestTuning) {
				t.Helper()
				got := tuning.Anthropic
				if got.ThinkingType != "adaptive" || got.ThinkingEffort != "medium" || got.ThinkingDisplay != "summarized" {
					t.Fatalf("Anthropic thinking tuning = %+v, want model defaults preserved", got)
				}
				if got.PromptCacheMode != "auto" || got.PromptCacheTTL != "1h" || !got.CacheTools {
					t.Fatalf("Anthropic prompt cache tuning = %+v, want model defaults preserved", got)
				}
			},
		},
		{
			name:       "gemini",
			providerID: "gemini",
			provider: config.ProviderConfig{
				Type: config.ProviderTypeGenerateContent,
				Models: map[string]config.ModelConfig{
					"gemini-test": {
						Limit: config.ModelLimit{Context: 1000000, Output: 8192},
						Thinking: &config.ThinkingConfig{
							Budget:          1024,
							Level:           "high",
							IncludeThoughts: new(true),
						},
					},
				},
			},
			modelID: "gemini-test",
			assert: func(t *testing.T, tuning RequestTuning) {
				t.Helper()
				got := tuning.Gemini
				// The client keeps both configured knobs; the Gemini wire layer
				// collapses them at request construction (explicit level wins),
				// which the recording stub below bypasses.
				if got.ThinkingLevel != "high" {
					t.Fatalf("Gemini thinking level = %q, want high", got.ThinkingLevel)
				}
				if got.ThinkingBudget == nil || *got.ThinkingBudget != 1024 {
					t.Fatalf("Gemini thinking budget = %#v, want 1024 alongside a level", got.ThinkingBudget)
				}
				if got.IncludeThoughts == nil || !*got.IncludeThoughts {
					t.Fatalf("Gemini include thoughts = %#v, want true", got.IncludeThoughts)
				}
			},
		},
		{
			name:       "gemini budget only",
			providerID: "gemini",
			provider: config.ProviderConfig{
				Type: config.ProviderTypeGenerateContent,
				Models: map[string]config.ModelConfig{
					"gemini-test": {
						Limit: config.ModelLimit{Context: 1000000, Output: 8192},
						Thinking: &config.ThinkingConfig{
							Budget:          1024,
							IncludeThoughts: new(true),
						},
					},
				},
			},
			modelID: "gemini-test",
			assert: func(t *testing.T, tuning RequestTuning) {
				t.Helper()
				got := tuning.Gemini
				if got.ThinkingBudget == nil || *got.ThinkingBudget != 1024 {
					t.Fatalf("Gemini thinking budget = %#v, want 1024", got.ThinkingBudget)
				}
				if got.ThinkingLevel != "" {
					t.Fatalf("Gemini thinking level = %q, want empty", got.ThinkingLevel)
				}
				if got.IncludeThoughts == nil || !*got.IncludeThoughts {
					t.Fatalf("Gemini include thoughts = %#v, want true", got.IncludeThoughts)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &recordingTuningProvider{}
			cfg := NewProviderConfig(tt.providerID, tt.provider, []string{"k"})
			c := NewClient(cfg, provider, tt.modelID, 4096, "sys")
			c.SetNextRequestTuningOverride(RequestTuning{})

			if _, err := c.CompleteStream(context.Background(), nil, nil, nil); err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if len(provider.tuning) != 1 {
				t.Fatalf("len(provider.tuning) = %d, want 1", len(provider.tuning))
			}
			tt.assert(t, provider.tuning[0])
		})
	}
}

func TestCompleteStreamFallbackVariantCarriesPromptCacheTuning(t *testing.T) {
	expect := RequestTuning{Anthropic: AnthropicTuning{PromptCacheMode: "auto", PromptCacheTTL: "1h", CacheTools: true}}
	primaryCfg := NewProviderConfig("primary", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"claude-primary": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"k1"})
	fallbackCfg := NewProviderConfig("fallback", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"claude-fallback": {
				Limit: config.ModelLimit{Context: 128000, Output: 4096},
				PromptCache: &config.PromptCacheConfig{
					Mode:       "explicit",
					TTL:        "1h",
					CacheTools: new(false),
				},
				Variants: map[string]config.ModelVariant{
					"fastcache": {
						PromptCache: &config.PromptCacheConfig{
							Mode:       "auto",
							CacheTools: new(true),
						},
					},
				},
			},
		},
	}, []string{"k2"})
	primaryImpl := &scriptedProvider{calls: []scriptedCall{{err: &APIError{StatusCode: 500, Message: "upstream unavailable"}}}}
	fallbackImpl := &scriptedProvider{calls: []scriptedCall{{resp: &message.Response{Content: "fallback ok"}, expectTuning: &expect}}}
	c := NewClient(primaryCfg, primaryImpl, "claude-primary", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "claude-fallback",
		MaxTokens:      4096,
		ContextLimit:   128000,
		Variant:        "fastcache",
	}})

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "fallback ok" {
		t.Fatalf("unexpected response: %#v", resp)
	}
}
