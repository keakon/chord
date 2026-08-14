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
          request_overrides:
            rename_body_fields:
              max_completion_tokens: "max_tokens"
            body:
              thinking:
                type: "enabled"
                clear_thinking: false
            headers:
              anthropic-beta: null
          reasoning_continuity:
            mode: "openai_visible"
            preserve_history: true
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
	if !model.Compat.ReasoningContinuity.PreserveHistoryValue() {
		t.Fatal("expected compat.reasoning_continuity.preserve_history=true")
	}
	if got := model.Compat.RequestOverrides.RenameBodyFields["max_completion_tokens"]; got == nil || *got != "max_tokens" {
		t.Fatalf("rename max_completion_tokens = %#v, want max_tokens", got)
	}
	thinking, ok := model.Compat.RequestOverrides.Body["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking override = %#v, want object", model.Compat.RequestOverrides.Body["thinking"])
	}
	if got := thinking["type"]; got != "enabled" {
		t.Fatalf("thinking.type = %#v, want enabled", got)
	}
	if got := thinking["clear_thinking"]; got != false {
		t.Fatalf("thinking.clear_thinking = %#v, want false", got)
	}
	if got, ok := model.Compat.RequestOverrides.Headers["anthropic-beta"]; !ok || got != nil {
		t.Fatalf("anthropic-beta override = %#v, %v, want explicit null", got, ok)
	}
}

func TestConfigYAML_AnthropicUnsignedReasoningContinuity(t *testing.T) {
	const raw = `
providers:
  deepseek-messages:
    type: messages
    models:
      deepseek-v4-pro:
        limit:
          context: 1000000
          output: 64000
        thinking:
          type: adaptive
        compat:
          reasoning_continuity:
            mode: anthropic_unsigned
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("yaml unmarshal failed: %v", err)
	}
	model := cfg.Providers["deepseek-messages"].Models["deepseek-v4-pro"]
	if model.Compat == nil || model.Compat.ReasoningContinuity == nil {
		t.Fatal("expected compat.reasoning_continuity")
	}
	if got := model.Compat.ReasoningContinuity.EffectiveMode(); got != "anthropic_unsigned" {
		t.Fatalf("EffectiveMode = %q, want anthropic_unsigned", got)
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

func TestConfigYAML_ModelCompatForcedToolChoice(t *testing.T) {
	const raw = `
providers:
  sample:
    type: responses
    models:
      test-model:
        limit:
          context: 1000000
          output: 64000
        reasoning:
          effort: high
        compat:
          forced_tool_choice:
            suppress_in_thinking: true
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("yaml unmarshal failed: %v", err)
	}
	model := cfg.Providers["sample"].Models["test-model"]
	if model.Compat == nil || model.Compat.ForcedToolChoice == nil {
		t.Fatal("expected compat.forced_tool_choice to be present")
	}
	if got := model.Compat.ForcedToolChoice.SuppressInThinking; got == nil || !*got {
		t.Fatalf("suppress_in_thinking = %#v, want true", got)
	}
}

func TestConfigYAML_ModelReasoningEffortMap(t *testing.T) {
	const raw = `
providers:
  sample:
    type: chat-completions
    models:
      test-model:
        reasoning:
          effort: high
          effort_map:
            high: max
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("yaml unmarshal failed: %v", err)
	}
	model := cfg.Providers["sample"].Models["test-model"]
	if model.Reasoning == nil || model.Reasoning.Effort != "high" || model.Reasoning.EffortMap["high"] != "max" {
		t.Fatalf("reasoning effort map not parsed: %+v", model.Reasoning)
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

func TestConfigYAML_ProviderParallelToolCallsAndWireCompat(t *testing.T) {
	const raw = `
providers:
  gateway:
    type: "chat-completions"
    parallel_tool_calls: false
    compat:
      responses:
        send_store: false
        send_tool_choice: false
        send_prompt_cache_key: false
        send_reasoning_include: false
        send_max_output_tokens: true
      chat_completions:
        send_stream_options: false
        infer_finish_reason: true
        requires_tool_result_name: true
        requires_assistant_after_tool_result: true
      usage:
        input_includes_cache_read: false
        input_includes_cache_write: false
    models:
      gpt-5.5:
        limit: {context: 400000, output: 128000}
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("yaml unmarshal failed: %v", err)
	}
	prov := cfg.Providers["gateway"]
	if prov.ParallelToolCalls == nil || *prov.ParallelToolCalls {
		t.Fatalf("provider parallel_tool_calls = %#v, want false", prov.ParallelToolCalls)
	}
	if prov.Compat == nil || prov.Compat.Responses == nil || prov.Compat.ChatCompletions == nil {
		t.Fatalf("compat not parsed: %#v", prov.Compat)
	}
	if prov.Compat.Usage == nil || prov.Compat.Usage.InputIncludesCacheRead == nil || *prov.Compat.Usage.InputIncludesCacheRead || prov.Compat.Usage.InputIncludesCacheWrite == nil || *prov.Compat.Usage.InputIncludesCacheWrite {
		t.Fatalf("usage compat = %#v, want both input cache flags false", prov.Compat.Usage)
	}
	r := prov.Compat.Responses
	if r.SendStore == nil || *r.SendStore {
		t.Fatalf("send_store = %#v, want false", r.SendStore)
	}
	if r.SendToolChoice == nil || *r.SendToolChoice {
		t.Fatalf("send_tool_choice = %#v, want false", r.SendToolChoice)
	}
	if r.SendPromptCacheKey == nil || *r.SendPromptCacheKey {
		t.Fatalf("send_prompt_cache_key = %#v, want false", r.SendPromptCacheKey)
	}
	if r.SendReasoningInclude == nil || *r.SendReasoningInclude {
		t.Fatalf("send_reasoning_include = %#v, want false", r.SendReasoningInclude)
	}
	if r.SendMaxOutputTokens == nil || !*r.SendMaxOutputTokens {
		t.Fatalf("send_max_output_tokens = %#v, want true", r.SendMaxOutputTokens)
	}
	cc := prov.Compat.ChatCompletions
	if cc.SendStreamOptions == nil || *cc.SendStreamOptions {
		t.Fatalf("send_stream_options = %#v, want false", cc.SendStreamOptions)
	}
	if cc.InferFinishReason == nil || !*cc.InferFinishReason {
		t.Fatalf("infer_finish_reason = %#v, want true", cc.InferFinishReason)
	}
	if cc.RequiresToolResultName == nil || !*cc.RequiresToolResultName {
		t.Fatalf("requires_tool_result_name = %#v, want true", cc.RequiresToolResultName)
	}
	if cc.RequiresAssistantAfterToolResult == nil || !*cc.RequiresAssistantAfterToolResult {
		t.Fatalf("requires_assistant_after_tool_result = %#v, want true", cc.RequiresAssistantAfterToolResult)
	}
}
