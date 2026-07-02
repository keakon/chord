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

func TestReasoningContinuityCompatConfig_Defaults(t *testing.T) {
	var cfg ReasoningContinuityCompatConfig
	if cfg.EffectiveMode() != "" {
		t.Fatalf("expected EffectiveMode default empty, got %q", cfg.EffectiveMode())
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

func TestConfigYAML_ModelCompatReasoningContinuity(t *testing.T) {
	const raw = `
providers:
  glm-main:
    type: "chat-completions"
    models:
      glm-5.2:
        limit:
          context: 1000000
          output: 64000
        compat:
          reasoning_continuity:
            mode: "openai_visible"
`

	var cfg Config
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("yaml unmarshal failed: %v", err)
	}

	model := cfg.Providers["glm-main"].Models["glm-5.2"]
	if model.Compat == nil || model.Compat.ReasoningContinuity == nil {
		t.Fatal("expected compat.reasoning_continuity to be present")
	}
	if got := model.Compat.ReasoningContinuity.EffectiveMode(); got != "openai_visible" {
		t.Fatalf("EffectiveMode = %q, want openai_visible", got)
	}
}

func TestConfigYAML_ProviderCompatReasoningContinuity(t *testing.T) {
	const raw = `
providers:
  glm-main:
    type: "chat-completions"
    compat:
      reasoning_continuity:
        mode: "openai_visible"
    models:
      glm-5.2:
        limit:
          context: 1000000
          output: 64000
`

	var cfg Config
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("yaml unmarshal failed: %v", err)
	}

	provider := cfg.Providers["glm-main"]
	if provider.Compat == nil || provider.Compat.ReasoningContinuity == nil {
		t.Fatal("expected provider compat.reasoning_continuity to be present")
	}
	if got := provider.Compat.ReasoningContinuity.EffectiveMode(); got != "openai_visible" {
		t.Fatalf("EffectiveMode = %q, want openai_visible", got)
	}
}

func TestConfigYAML_ProviderUserAgent(t *testing.T) {
	const raw = `
providers:
  openai-main:
    type: "chat-completions"
    user_agent: "ProviderUA/1.0"
    models:
      gpt-5:
        limit:
          context: 400000
          output: 128000
`

	var cfg Config
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("yaml unmarshal failed: %v", err)
	}

	provider := cfg.Providers["openai-main"]
	if provider.UserAgent != "ProviderUA/1.0" {
		t.Fatalf("unexpected user_agent: %q", provider.UserAgent)
	}
}

func TestConfigYAML_ModelTextVerbosity(t *testing.T) {
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
