package llm

import (
	"slices"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/modelcompat"
)

func TestProviderResponsesCompatIncludesCatalogBindingDefaults(t *testing.T) {
	p := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		Preset: "openai",
		Models: map[string]config.ModelConfig{"gpt-6.1-sol": {}},
	}, []string{"key"})
	compat := p.ResponsesCompat("gpt-6.1-sol")
	if compat == nil || compat.SendStore == nil || !*compat.SendStore || compat.SendParallelToolCalls == nil || !*compat.SendParallelToolCalls {
		t.Fatalf("ResponsesCompat = %+v, want catalog binding defaults", compat)
	}
}

func TestProviderResponsesCompatPrecedenceKeepsUserOverrides(t *testing.T) {
	p := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		Preset: "openai",
		Compat: &config.ProviderCompatConfig{Responses: &config.ResponsesCompatConfig{SendParallelToolCalls: new(false)}},
		Models: map[string]config.ModelConfig{
			"gpt-6.1-sol": {Compat: &config.ModelCompatConfig{Responses: &config.ResponsesCompatConfig{SendParallelToolCalls: new(true)}}},
		},
	}, []string{"key"})
	compat := p.ResponsesCompat("gpt-6.1-sol")
	if compat == nil || compat.SendParallelToolCalls == nil || !*compat.SendParallelToolCalls {
		t.Fatalf("ResponsesCompat = %+v, want model override true", compat)
	}
}

func TestProviderConfig_ThinkingToolcallCompat_ModelOnly(t *testing.T) {
	cfg := config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"m1": {
				Compat: &config.ModelCompatConfig{
					ThinkingToolcall: &config.ThinkingToolcallCompatConfig{
						Enabled: new(true),
					},
				},
			},
		},
	}
	p := NewProviderConfig("test", cfg, []string{"k"})
	got := p.ThinkingToolcallCompat("m1")
	if got == nil || !got.EnabledValue() {
		t.Fatalf("expected model compat enabled=true, got %#v", got)
	}
}

func TestProviderConfig_RequestOverrides_ModelOverridesProvider(t *testing.T) {
	maxTokens := "max_tokens"
	providerTrace := "provider"
	modelTrace := "model"
	cfg := config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Compat: &config.ProviderCompatConfig{
			RequestOverrides: &config.RequestOverridesConfig{
				Body:    map[string]any{"thinking": map[string]any{"type": "enabled"}},
				Headers: map[string]*string{"x-trace": &providerTrace, "anthropic-beta": nil},
			},
		},
		Models: map[string]config.ModelConfig{
			"m1": {
				Compat: &config.ModelCompatConfig{
					RequestOverrides: &config.RequestOverridesConfig{
						RenameBodyFields: map[string]*string{"max_completion_tokens": &maxTokens},
						Headers:          map[string]*string{"x-trace": &modelTrace},
					},
				},
			},
			"m2": {},
		},
	}
	p := NewProviderConfig("test", cfg, []string{"k"})

	got := p.RequestOverrides("m1")
	if got.Body["thinking"].(map[string]any)["type"] != "enabled" {
		t.Fatalf("m1 body = %#v, want provider default", got.Body)
	}
	if rename := got.RenameBodyFields["max_completion_tokens"]; rename == nil || *rename != "max_tokens" {
		t.Fatalf("m1 rename = %#v, want max_tokens", rename)
	}
	if header := got.Headers["X-Trace"]; header == nil || *header != "model" {
		t.Fatalf("m1 x-trace = %#v, want model", header)
	}
	if value, ok := got.Headers["Anthropic-Beta"]; !ok || value != nil {
		t.Fatalf("m1 anthropic-beta = %#v, %v, want provider null", value, ok)
	}

	got.Body["thinking"].(map[string]any)["type"] = "mutated"
	*got.RenameBodyFields["max_completion_tokens"] = "mutated"
	*got.Headers["X-Trace"] = "mutated"
	again := p.RequestOverrides("m1")
	if value := again.Body["thinking"].(map[string]any)["type"]; value != "enabled" {
		t.Fatalf("second body = %#v, want isolated provider config", again.Body)
	}
	if value := *again.RenameBodyFields["max_completion_tokens"]; value != "max_tokens" {
		t.Fatalf("second rename = %q, want max_tokens", value)
	}
	if value := *again.Headers["X-Trace"]; value != "model" {
		t.Fatalf("second x-trace = %q, want model", value)
	}
}

func TestProviderConfig_ThinkingToolcallCompat_ProviderDefault(t *testing.T) {
	cfg := config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Compat: &config.ProviderCompatConfig{
			ThinkingToolcall: &config.ThinkingToolcallCompatConfig{
				Enabled: new(true),
			},
		},
		Models: map[string]config.ModelConfig{
			"m1": {},
		},
	}
	p := NewProviderConfig("test", cfg, []string{"k"})
	got := p.ThinkingToolcallCompat("m1")
	if got == nil || !got.EnabledValue() {
		t.Fatalf("expected provider default compat enabled=true, got %#v", got)
	}
}

func TestProviderConfig_ThinkingToolcallCompat_ModelOverride(t *testing.T) {
	cfg := config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Compat: &config.ProviderCompatConfig{
			ThinkingToolcall: &config.ThinkingToolcallCompatConfig{
				Enabled: new(true),
			},
		},
		Models: map[string]config.ModelConfig{
			"m1": {
				Compat: &config.ModelCompatConfig{
					ThinkingToolcall: &config.ThinkingToolcallCompatConfig{
						Enabled: new(false),
					},
				},
			},
		},
	}
	p := NewProviderConfig("test", cfg, []string{"k"})
	got := p.ThinkingToolcallCompat("m1")
	if got == nil {
		t.Fatal("expected non-nil compat config")
	}
	if got.EnabledValue() {
		t.Fatalf("expected model override enabled=false, got %#v", got)
	}
}

func TestProviderConfig_ReasoningContinuityCompat_ModelOnly(t *testing.T) {
	cfg := config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"m1": {
				Compat: &config.ModelCompatConfig{
					ReasoningContinuity: &config.ReasoningContinuityCompatConfig{Mode: "openai_visible"},
				},
			},
		},
	}
	p := NewProviderConfig("test", cfg, []string{"k"})
	got := p.ReasoningContinuityCompat("m1")
	if got == nil || got.EffectiveMode() != "openai_visible" {
		t.Fatalf("expected model compat mode=openai_visible, got %#v", got)
	}
}

func TestProviderConfig_ReasoningContinuityCompat_ProviderDefault(t *testing.T) {
	cfg := config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Compat: &config.ProviderCompatConfig{
			ReasoningContinuity: &config.ReasoningContinuityCompatConfig{Mode: "openai_visible"},
		},
		Models: map[string]config.ModelConfig{
			"m1": {},
		},
	}
	p := NewProviderConfig("test", cfg, []string{"k"})
	got := p.ReasoningContinuityCompat("m1")
	if got == nil || got.EffectiveMode() != "openai_visible" {
		t.Fatalf("expected provider default mode=openai_visible, got %#v", got)
	}
}

func TestProviderConfig_ReasoningContinuityCompat_ModelOverride(t *testing.T) {
	cfg := config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Compat: &config.ProviderCompatConfig{
			ReasoningContinuity: &config.ReasoningContinuityCompatConfig{Mode: "openai_visible"},
		},
		Models: map[string]config.ModelConfig{
			"m1": {
				Compat: &config.ModelCompatConfig{
					ReasoningContinuity: &config.ReasoningContinuityCompatConfig{Mode: "none"},
				},
			},
		},
	}
	p := NewProviderConfig("test", cfg, []string{"k"})
	got := p.ReasoningContinuityCompat("m1")
	if got == nil || got.EffectiveMode() != "none" {
		t.Fatalf("expected model override mode=none, got %#v", got)
	}
}

func TestProviderConfig_ReasoningContinuityCompat_ReasoningReplayMerge(t *testing.T) {
	cfg := config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Compat: &config.ProviderCompatConfig{
			ReasoningContinuity: &config.ReasoningContinuityCompatConfig{Mode: "openai_visible", ReasoningReplay: "all"},
		},
		Models: map[string]config.ModelConfig{
			"inherits": {},
			"overrides": {
				Compat: &config.ModelCompatConfig{
					ReasoningContinuity: &config.ReasoningContinuityCompatConfig{ReasoningReplay: "current_turn"},
				},
			},
		},
	}
	p := NewProviderConfig("test", cfg, []string{"k"})
	if got := p.ReasoningContinuityCompat("inherits"); got.ReasoningReplayValue() != "all" {
		t.Fatalf("expected provider-level reasoning_replay=all to be inherited, got %#v", got)
	}
	if got := p.ReasoningContinuityCompat("overrides"); got.ReasoningReplayValue() != "current_turn" {
		t.Fatalf("expected model-level reasoning_replay=current_turn to override, got %#v", got)
	}
}

func TestProviderConfig_ReasoningContinuityCompat_ContractMerge(t *testing.T) {
	p := NewProviderConfig("sample", config.ProviderConfig{
		Compat: &config.ProviderCompatConfig{ReasoningContinuity: &config.ReasoningContinuityCompatConfig{
			Contract: config.ReasoningContractDeepSeek,
		}},
		Models: map[string]config.ModelConfig{
			"model-1": {Compat: &config.ModelCompatConfig{ReasoningContinuity: &config.ReasoningContinuityCompatConfig{
				Mode: "openai_visible",
			}}},
		},
	}, nil)
	got := p.ReasoningContinuityCompat("model-1")
	if got == nil || got.ReasoningContractValue() != config.ReasoningContractDeepSeek || got.EffectiveMode() != "openai_visible" {
		t.Fatalf("compat = %+v, want provider contract and model mode", got)
	}
}

func TestProviderConfigNativeFamilyChatGateway(t *testing.T) {
	cases := []struct {
		name       string
		model      string
		compat     *config.ChatCompletionsCompatConfig
		wantFamily string
	}{
		{name: "gateway alias stays unknown", model: "deployment-a", wantFamily: modelcompat.NativeFamilyUnknown},
		{name: "gemini name alone is not inferred", model: "gemini-2.5-pro", wantFamily: modelcompat.NativeFamilyUnknown},
		{name: "claude name alone is not inferred", model: "claude-sonnet-4-5", wantFamily: modelcompat.NativeFamilyUnknown},
		{name: "deepseek name is the one name default", model: "vendor/deepseek-v4.1", wantFamily: modelcompat.NativeFamilyDeepSeek},
		{name: "embedded deepseek text is not the family", model: "my-deepseek-proxy", wantFamily: modelcompat.NativeFamilyUnknown},
		{name: "gemini selector", model: "deployment-a", compat: &config.ChatCompletionsCompatConfig{NativeThinking: "gemini-3"}, wantFamily: modelcompat.NativeFamilyGemini},
		{name: "anthropic selector", model: "deployment-a", compat: &config.ChatCompletionsCompatConfig{NativeThinking: "anthropic"}, wantFamily: modelcompat.NativeFamilyAnthropic},
		{name: "off selector suppresses the name default", model: "deepseek-v4.1-flash", compat: &config.ChatCompletionsCompatConfig{NativeThinking: "off"}, wantFamily: modelcompat.NativeFamilyUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			providerCfg := config.ProviderConfig{
				Type:   config.ProviderTypeChatCompletions,
				Models: map[string]config.ModelConfig{tc.model: {}},
			}
			if tc.compat != nil {
				providerCfg.Compat = &config.ProviderCompatConfig{ChatCompletions: tc.compat}
			}
			p := NewProviderConfig("sample", providerCfg, nil)
			if got := p.NativeFamily(tc.model); got != tc.wantFamily {
				t.Fatalf("NativeFamily(%q) = %q, want %q", tc.model, got, tc.wantFamily)
			}
		})
	}
}

func TestProviderConfig_ForcedToolChoiceCompat_Merge(t *testing.T) {
	cfg := config.ProviderConfig{
		Type: config.ProviderTypeResponses,
		Compat: &config.ProviderCompatConfig{
			ForcedToolChoice: &config.ForcedToolChoiceCompatConfig{SuppressInThinking: new(true), AutoOnly: new(false)},
		},
		Models: map[string]config.ModelConfig{
			"inherits": {},
			"overrides": {
				Compat: &config.ModelCompatConfig{
					ForcedToolChoice: &config.ForcedToolChoiceCompatConfig{SuppressInThinking: new(false), AutoOnly: new(true)},
				},
			},
			"unconfigured": {},
		},
	}
	p := NewProviderConfig("test", cfg, []string{"k"})
	if got := p.ForcedToolChoiceCompat("inherits"); got == nil || got.SuppressInThinking == nil || !*got.SuppressInThinking {
		t.Fatalf("expected provider-level suppress to be inherited, got %#v", got)
	}
	if got := p.ForcedToolChoiceCompat("overrides"); got == nil || got.SuppressInThinking == nil || *got.SuppressInThinking {
		t.Fatalf("expected model-level suppress=false to override, got %#v", got)
	}
	if got := p.ForcedToolChoiceCompat("unconfigured"); got == nil || got.SuppressInThinking == nil || !*got.SuppressInThinking {
		t.Fatalf("expected empty model to inherit provider default, got %#v", got)
	}
	if got := p.ForcedToolChoiceCompat("inherits"); got == nil || got.AutoOnly == nil || *got.AutoOnly {
		t.Fatalf("expected provider-level auto_only=false to be inherited, got %#v", got)
	}
	if got := p.ForcedToolChoiceCompat("overrides"); got == nil || got.AutoOnly == nil || !*got.AutoOnly {
		t.Fatalf("expected model-level auto_only=true to override, got %#v", got)
	}
	if forcedToolChoiceAutoOnly(p, "overrides") != true {
		t.Fatal("expected forcedToolChoiceAutoOnly=true for overrides")
	}
	if forcedToolChoiceAutoOnly(p, "inherits") != false {
		t.Fatal("expected forcedToolChoiceAutoOnly=false for inherits")
	}
}

func TestProviderConfigNativeFamilyFollowsDeepSeekContract(t *testing.T) {
	cases := []struct {
		name       string
		typ        string
		model      string
		contract   string
		wantFamily string
	}{
		{name: "messages deepseek name", typ: config.ProviderTypeMessages, model: "deepseek-v4.1", wantFamily: modelcompat.NativeFamilyDeepSeek},
		{name: "messages none opts a deepseek name out", typ: config.ProviderTypeMessages, model: "deepseek-v4.1", contract: config.ReasoningContractNone, wantFamily: modelcompat.NativeFamilyAnthropic},
		{name: "messages deepseek contract on an alias", typ: config.ProviderTypeMessages, model: "deployment-a", contract: config.ReasoningContractDeepSeek, wantFamily: modelcompat.NativeFamilyDeepSeek},
		{name: "chat none opts a deepseek name out", typ: config.ProviderTypeChatCompletions, model: "deepseek-v4.1", contract: config.ReasoningContractNone, wantFamily: modelcompat.NativeFamilyUnknown},
		{name: "chat deepseek contract on an alias", typ: config.ProviderTypeChatCompletions, model: "deployment-a", contract: config.ReasoningContractDeepSeek, wantFamily: modelcompat.NativeFamilyDeepSeek},
		{name: "responses deepseek name has no deepseek contract", typ: config.ProviderTypeResponses, model: "deepseek-v4.1", wantFamily: modelcompat.NativeFamilyOpenAI},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			modelCfg := config.ModelConfig{}
			if tc.contract != "" {
				modelCfg.Compat = &config.ModelCompatConfig{ReasoningContinuity: &config.ReasoningContinuityCompatConfig{Contract: tc.contract}}
			}
			p := NewProviderConfig("sample", config.ProviderConfig{
				Type:   tc.typ,
				Models: map[string]config.ModelConfig{tc.model: modelCfg},
			}, nil)
			if got := p.NativeFamily(tc.model); got != tc.wantFamily {
				t.Fatalf("NativeFamily(%q) = %q, want %q", tc.model, got, tc.wantFamily)
			}
		})
	}
}

func TestProviderConfigHostedToolsCompatMerge(t *testing.T) {
	provider := NewProviderConfig("sample", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		APIURL: "https://example.invalid/v1",
		Compat: &config.ProviderCompatConfig{HostedTools: new([]string{"web_search"})},
		Models: map[string]config.ModelConfig{
			"provider/model-1": {Compat: &config.ModelCompatConfig{HostedTools: new([]string{"code_execution"})}},
			"provider/model-2": {},
		},
	}, nil)
	if got := provider.HostedToolsCompat("provider/model-1"); !slices.Equal(got, []string{"code_execution"}) {
		t.Errorf("model-level override = %v, want [code_execution]", got)
	}
	if got := provider.HostedToolsCompat("provider/model-2"); !slices.Equal(got, []string{"web_search"}) {
		t.Errorf("provider default = %v, want [web_search]", got)
	}
	if got := provider.HostedToolsCompat("provider/unlisted"); !slices.Equal(got, []string{"web_search"}) {
		t.Errorf("provider default for an unlisted model = %v, want [web_search]", got)
	}

	plain := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeResponses, APIURL: "https://example.invalid/v1"}, nil)
	if got := plain.HostedToolsCompat("provider/model-1"); len(got) != 0 {
		t.Errorf("unconfigured HostedToolsCompat = %v, want empty", got)
	}
}

func TestProviderResponsesCompatProviderOverridesCatalog(t *testing.T) {
	p := NewProviderConfig("sample", config.ProviderConfig{
		Preset: "openai", Type: config.ProviderTypeResponses,
		Compat: &config.ProviderCompatConfig{Responses: &config.ResponsesCompatConfig{SendParallelToolCalls: new(false)}},
		Models: map[string]config.ModelConfig{"gpt-6.1-sol": {}},
	}, nil)
	defer p.Close()
	compat := p.ResponsesCompat("gpt-6.1-sol")
	if compat == nil || compat.SendParallelToolCalls == nil || *compat.SendParallelToolCalls {
		t.Fatalf("compat = %+v, want provider false over catalog true", compat)
	}
}

func TestProviderResponsesCompatRetainsResolvedSnapshot(t *testing.T) {
	send := new(false)
	cfg := config.ProviderConfig{
		Preset: "openai", Type: config.ProviderTypeResponses,
		Compat: &config.ProviderCompatConfig{Responses: &config.ResponsesCompatConfig{SendParallelToolCalls: send}},
		Models: map[string]config.ModelConfig{"gpt-6.1-sol": {}},
	}
	p := NewProviderConfig("sample", cfg, nil)
	defer p.Close()
	*send = true
	if compat := p.ResponsesCompat("gpt-6.1-sol"); compat == nil || compat.SendParallelToolCalls == nil || *compat.SendParallelToolCalls {
		t.Fatalf("compat = %+v, want immutable resolved false", compat)
	}
}
