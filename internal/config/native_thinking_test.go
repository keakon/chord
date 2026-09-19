package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeNativeThinking(t *testing.T) {
	tests := []struct {
		selector string
		want     string
	}{
		{selector: "", want: NativeThinkingAuto},
		{selector: "auto", want: NativeThinkingAuto},
		{selector: "  AUTO ", want: NativeThinkingAuto},
		{selector: "off", want: NativeThinkingOff},
		{selector: "none", want: NativeThinkingOff},
		{selector: "gemini", want: NativeThinkingGemini},
		{selector: "google", want: NativeThinkingGemini},
		{selector: "vertex", want: NativeThinkingGemini},
		{selector: "gemini-3", want: NativeThinkingGemini3},
		{selector: "Claude", want: NativeThinkingAnthropic},
		{selector: "anthropic", want: NativeThinkingAnthropic},
		{selector: "thinking", want: NativeThinkingObject},
		{selector: "deepseek", want: NativeThinkingObject},
		{selector: "zhipu", want: NativeThinkingObject},
		{selector: "moonshot", want: NativeThinkingObject},
		{selector: "volcengine", want: NativeThinkingObject},
		{selector: "qwen", want: NativeThinkingQwen},
		{selector: "dashscope", want: NativeThinkingQwen},
	}
	for _, tt := range tests {
		got, err := NormalizeNativeThinking(tt.selector)
		if err != nil {
			t.Fatalf("NormalizeNativeThinking(%q): %v", tt.selector, err)
		}
		if got != tt.want {
			t.Fatalf("NormalizeNativeThinking(%q) = %q, want %q", tt.selector, got, tt.want)
		}
	}

	_, err := NormalizeNativeThinking("gemeni")
	if err == nil || !strings.Contains(err.Error(), `invalid native_thinking "gemeni"`) {
		t.Fatalf("NormalizeNativeThinking(\"gemeni\") error = %v, want it to name the invalid selector", err)
	}
}

func TestValidateProviderNativeThinkingAcceptsKnownSelectors(t *testing.T) {
	cfg := ProviderConfig{
		Compat: &ProviderCompatConfig{ChatCompletions: &ChatCompletionsCompatConfig{NativeThinking: "off"}},
		Models: map[string]ModelConfig{
			"model-1": {Compat: &ModelCompatConfig{ChatCompletions: &ChatCompletionsCompatConfig{NativeThinking: "gemini"}}},
			"model-2": {Compat: &ModelCompatConfig{ChatCompletions: &ChatCompletionsCompatConfig{NativeThinking: "kimi"}}},
			"model-3": {Compat: &ModelCompatConfig{ChatCompletions: &ChatCompletionsCompatConfig{NativeThinking: "gemini-3"}}},
		},
	}
	if err := ValidateProviderNativeThinking("sample", cfg); err != nil {
		t.Fatalf("ValidateProviderNativeThinking: %v", err)
	}
}

func TestValidateProviderNativeThinkingRejectsUnknownSelectors(t *testing.T) {
	providerLevel := ProviderConfig{
		Compat: &ProviderCompatConfig{ChatCompletions: &ChatCompletionsCompatConfig{NativeThinking: "gemeni"}},
	}
	err := ValidateProviderNativeThinking("sample", providerLevel)
	if err == nil || !strings.Contains(err.Error(), `invalid native_thinking "gemeni"`) || !strings.Contains(err.Error(), `for provider "sample"`) {
		t.Fatalf("provider selector error = %v, want the invalid value reported against the provider", err)
	}

	modelLevel := ProviderConfig{
		Models: map[string]ModelConfig{
			"model-1": {Compat: &ModelCompatConfig{ChatCompletions: &ChatCompletionsCompatConfig{NativeThinking: "gemeni"}}},
		},
	}
	err = ValidateProviderNativeThinking("sample", modelLevel)
	if err == nil || !strings.Contains(err.Error(), `for model "model-1" in provider "sample"`) {
		t.Fatalf("model selector error = %v, want the invalid value reported against the model", err)
	}
}

func TestLoadConfigFromPathResetsInvalidNativeThinking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`providers:
  sample:
    type: chat-completions
    compat:
      chat_completions:
        native_thinking: gemeni
    models:
      model-1:
        compat:
          chat_completions:
            native_thinking: gemeni
      model-2:
        compat:
          chat_completions:
            native_thinking: gemini
`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	providerCfg := cfg.Providers["sample"]
	if got := providerChatCompletionsCompat(providerCfg).NativeThinking; got != "" {
		t.Fatalf("provider native_thinking = %q, want it reset to the inferring default", got)
	}
	if got := modelChatCompletionsCompat(providerCfg.Models["model-1"]).NativeThinking; got != "" {
		t.Fatalf("model-1 native_thinking = %q, want it reset to the inferring default", got)
	}
	if got := modelChatCompletionsCompat(providerCfg.Models["model-2"]).NativeThinking; got != "gemini" {
		t.Fatalf("model-2 native_thinking = %q, want the valid selection preserved", got)
	}
}
