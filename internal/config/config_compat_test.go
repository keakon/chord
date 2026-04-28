package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestThinkingToolcallCompatConfig_Defaults(t *testing.T) {
	var cfg ThinkingToolcallCompatConfig
	if cfg.EnabledValue() {
		t.Fatal("expected EnabledValue default false")
	}
}

func TestConfigYAML_ModelCompatThinkingToolcall(t *testing.T) {
	const raw = `
providers:
  openai-main:
    type: "chat-completions"
    models:
      moonshotai/kimi-k2.5:
        limit:
          context: 200000
          output: 16384
        compat:
          thinking_toolcall:
            enabled: true
`

	var cfg Config
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("yaml unmarshal failed: %v", err)
	}

	provider, ok := cfg.Providers["openai-main"]
	if !ok {
		t.Fatal("provider openai-main not found")
	}
	model, ok := provider.Models["moonshotai/kimi-k2.5"]
	if !ok {
		t.Fatal("model moonshotai/kimi-k2.5 not found")
	}
	if model.Compat == nil || model.Compat.ThinkingToolcall == nil {
		t.Fatal("expected compat.thinking_toolcall to be present")
	}
	tc := model.Compat.ThinkingToolcall
	if !tc.EnabledValue() {
		t.Fatal("expected compat.thinking_toolcall.enabled=true")
	}
}

func TestConfigYAML_ProviderCompatThinkingToolcall(t *testing.T) {
	const raw = `
providers:
  openai-main:
    type: "chat-completions"
    compat:
      thinking_toolcall:
        enabled: true
    models:
      moonshotai/kimi-k2.5:
        limit:
          context: 200000
          output: 16384
`

	var cfg Config
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("yaml unmarshal failed: %v", err)
	}

	provider, ok := cfg.Providers["openai-main"]
	if !ok {
		t.Fatal("provider openai-main not found")
	}
	if provider.Compat == nil || provider.Compat.ThinkingToolcall == nil {
		t.Fatal("expected provider compat.thinking_toolcall to be present")
	}
	if !provider.Compat.ThinkingToolcall.EnabledValue() {
		t.Fatal("expected provider compat.thinking_toolcall.enabled=true")
	}
}

func TestConfigYAML_ProviderCompatAnthropicTransport(t *testing.T) {
	const raw = `
providers:
  anthropic-main:
    type: "messages"
    compat:
      anthropic_transport:
        system_prefix: "[prefixed]\n"
        extra_beta:
          - beta-a
          - beta-b
        user_agent: "Chord-Test/1.0"
        metadata_user_id: true
    models:
      claude-sonnet:
        limit:
          context: 200000
          output: 8192
`

	var cfg Config
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("yaml unmarshal failed: %v", err)
	}

	provider, ok := cfg.Providers["anthropic-main"]
	if !ok {
		t.Fatal("provider anthropic-main not found")
	}
	if provider.Compat == nil || provider.Compat.AnthropicTransport == nil {
		t.Fatal("expected provider compat.anthropic_transport to be present")
	}
	transport := provider.Compat.AnthropicTransport
	if transport.SystemPrefix != "[prefixed]\n" {
		t.Fatalf("unexpected system_prefix: %q", transport.SystemPrefix)
	}
	if len(transport.ExtraBeta) != 2 || transport.ExtraBeta[0] != "beta-a" || transport.ExtraBeta[1] != "beta-b" {
		t.Fatalf("unexpected extra_beta: %#v", transport.ExtraBeta)
	}
	if transport.UserAgent != "Chord-Test/1.0" {
		t.Fatalf("unexpected user_agent: %q", transport.UserAgent)
	}
	if !transport.MetadataUserID {
		t.Fatal("expected metadata_user_id=true")
	}
}

func TestConfigYAML_ModelTextVerbosityAndLegacyFallback(t *testing.T) {
	const raw = `
providers:
  openai-main:
    type: "chat-completions"
    models:
      gpt-5.5:
        limit:
          context: 400000
          output: 128000
        reasoning:
          effort: "xhigh"
        text:
          verbosity: "medium"
`

	var cfg Config
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("yaml unmarshal failed: %v", err)
	}

	model := cfg.Providers["openai-main"].Models["gpt-5.5"]
	if model.EffectiveTextVerbosity() != "medium" {
		t.Fatalf("expected text.verbosity to win, got %q", model.EffectiveTextVerbosity())
	}
}

func TestConfigYAML_ModelParallelToolCalls(t *testing.T) {
	const raw = `
providers:
  openai-main:
    type: "chat-completions"
    models:
      gpt-5.5:
        limit:
          context: 400000
          output: 128000
        parallel_tool_calls: false
        variants:
          explicit:
            parallel_tool_calls: true
`

	var cfg Config
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("yaml unmarshal failed: %v", err)
	}

	model := cfg.Providers["openai-main"].Models["gpt-5.5"]
	if model.ParallelToolCalls == nil || *model.ParallelToolCalls {
		t.Fatalf("expected model parallel_tool_calls=false, got %#v", model.ParallelToolCalls)
	}
	variant, ok := model.Variants["explicit"]
	if !ok {
		t.Fatal("variant explicit not found")
	}
	if variant.ParallelToolCalls == nil || !*variant.ParallelToolCalls {
		t.Fatalf("expected variant parallel_tool_calls=true, got %#v", variant.ParallelToolCalls)
	}
}

func TestThinkingConfigBudgetWithoutTypeStaysUnset(t *testing.T) {
	mc := ModelConfig{Thinking: &ThinkingConfig{Budget: 1024}}
	if got := mc.EffectiveThinkingType(); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestThinkingConfigAdaptiveWithEffort(t *testing.T) {
	mc := ModelConfig{Thinking: &ThinkingConfig{Type: "adaptive", Effort: "medium"}}
	if got := mc.EffectiveThinkingType(); got != "adaptive" {
		t.Fatalf("got %q, want adaptive", got)
	}
	if got := mc.EffectiveThinkingEffort(); got != "medium" {
		t.Fatalf("got %q, want medium", got)
	}
}

func TestThinkingConfigDisplayOmitted(t *testing.T) {
	mc := ModelConfig{Thinking: &ThinkingConfig{Type: "enabled", Budget: 512, Display: "omitted"}}
	if got := mc.EffectiveThinkingDisplay(); got != "omitted" {
		t.Fatalf("got %q, want omitted", got)
	}
}

func TestPromptCacheModeAuto(t *testing.T) {
	mc := ModelConfig{PromptCache: &PromptCacheConfig{Mode: "auto"}}
	if got := mc.EffectivePromptCacheMode(); got != "auto" {
		t.Fatalf("got %q, want auto", got)
	}
}

func TestPromptCacheModeDefault(t *testing.T) {
	if got := (ModelConfig{}).EffectivePromptCacheMode(); got != "explicit" {
		t.Fatalf("got %q, want explicit", got)
	}
}

func TestVariantEffortOnly(t *testing.T) {
	v := ModelVariant{Thinking: &ThinkingConfig{Effort: "high"}}
	if got := v.EffectiveThinkingType(); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
	if v.Thinking.Effort != "high" {
		t.Fatalf("effort = %q, want high", v.Thinking.Effort)
	}
}

func TestVariantBudgetWithoutTypeStaysUnset(t *testing.T) {
	v := ModelVariant{Thinking: &ThinkingConfig{Budget: 2048}}
	if got := v.EffectiveThinkingType(); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}
