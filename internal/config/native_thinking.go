package config

import (
	"fmt"
	"sort"
	"strings"
)

// Canonical shapes for compat.chat_completions.native_thinking. Each one names a
// request shape Chord knows how to build for a Chat Completions endpoint that
// translates the call into the model's native API; NormalizeNativeThinking maps
// the accepted selectors, including the family aliases, onto these values, and
// the request builders switch on them.
const (
	// NativeThinkingAuto infers the shape from the model name.
	NativeThinkingAuto = "auto"
	// NativeThinkingOff leaves the thinking controls out of the request body.
	NativeThinkingOff = "off"
	// NativeThinkingGemini emits extra_body.google.thinking_config.
	NativeThinkingGemini = "gemini"
	// NativeThinkingAnthropic emits the Messages thinking:{type,budget_tokens}
	// object.
	NativeThinkingAnthropic = "anthropic"
	// NativeThinkingObject emits the native thinking:{type} object shared by
	// DeepSeek, GLM, Kimi K2.x, and Doubao.
	NativeThinkingObject = "thinking"
	// NativeThinkingQwen emits DashScope's enable_thinking flag.
	NativeThinkingQwen = "qwen"
)

// nativeThinkingShapes maps every accepted selector onto its canonical shape.
// The family aliases let a model whose id does not reveal its upstream name the
// shape; the empty selector is the unset default and infers from the model
// name, like "auto".
var nativeThinkingShapes = map[string]string{
	"":           NativeThinkingAuto,
	"auto":       NativeThinkingAuto,
	"off":        NativeThinkingOff,
	"none":       NativeThinkingOff,
	"gemini":     NativeThinkingGemini,
	"google":     NativeThinkingGemini,
	"vertex":     NativeThinkingGemini,
	"anthropic":  NativeThinkingAnthropic,
	"claude":     NativeThinkingAnthropic,
	"thinking":   NativeThinkingObject,
	"deepseek":   NativeThinkingObject,
	"glm":        NativeThinkingObject,
	"zhipu":      NativeThinkingObject,
	"kimi":       NativeThinkingObject,
	"moonshot":   NativeThinkingObject,
	"doubao":     NativeThinkingObject,
	"volcengine": NativeThinkingObject,
	"qwen":       NativeThinkingQwen,
	"qwq":        NativeThinkingQwen,
	"dashscope":  NativeThinkingQwen,
	"ali":        NativeThinkingQwen,
}

// NormalizeNativeThinking resolves a compat.chat_completions.native_thinking
// selector to its canonical shape: the vocabulary lives here because config
// validation and the request builders need the same one, and validation cannot
// import the request builders. An unset or "auto" selector means "infer from the
// model name"; an unknown value is an error.
func NormalizeNativeThinking(value string) (string, error) {
	selector := strings.ToLower(strings.TrimSpace(value))
	if shape, ok := nativeThinkingShapes[selector]; ok {
		return shape, nil
	}
	return "", fmt.Errorf("invalid native_thinking %q (allowed: %s)", value, strings.Join(nativeThinkingSelectors(), ", "))
}

// nativeThinkingSelectors lists the accepted selectors in a stable order for
// the validation error, skipping the empty unset selector.
func nativeThinkingSelectors() []string {
	selectors := make([]string, 0, len(nativeThinkingShapes))
	for selector := range nativeThinkingShapes {
		if selector != "" {
			selectors = append(selectors, selector)
		}
	}
	sort.Strings(selectors)
	return selectors
}

// ValidateProviderNativeThinking rejects an unknown
// compat.chat_completions.native_thinking selector set as the provider default
// or on any of the provider's models. The selector is otherwise only parsed
// while building a request, where the error is indistinguishable from a failing
// model and hands the turn to the fallback pool; reporting it against the
// configured provider points at the actual mistake.
func ValidateProviderNativeThinking(providerName string, cfg ProviderConfig) error {
	if compat := providerChatCompletionsCompat(cfg); compat != nil {
		if _, err := NormalizeNativeThinking(compat.NativeThinking); err != nil {
			return fmt.Errorf("%w for provider %q", err, providerName)
		}
	}
	for modelID, model := range cfg.Models {
		compat := modelChatCompletionsCompat(model)
		if compat == nil {
			continue
		}
		if _, err := NormalizeNativeThinking(compat.NativeThinking); err != nil {
			return fmt.Errorf("%w for model %q in provider %q", err, modelID, providerName)
		}
	}
	return nil
}

// validNativeThinking reports whether a selector resolves to a known shape.
func validNativeThinking(value string) bool {
	_, err := NormalizeNativeThinking(value)
	return err == nil
}

// resetInvalidNativeThinking clears native_thinking selectors that failed
// validation, so the provider keeps the infer-from-the-model-name default
// instead of failing every request that needs the field.
func resetInvalidNativeThinking(cfg ProviderConfig) ProviderConfig {
	if compat := providerChatCompletionsCompat(cfg); compat != nil && !validNativeThinking(compat.NativeThinking) {
		compat.NativeThinking = ""
	}
	for _, model := range cfg.Models {
		if compat := modelChatCompletionsCompat(model); compat != nil && !validNativeThinking(compat.NativeThinking) {
			compat.NativeThinking = ""
		}
	}
	return cfg
}

// providerChatCompletionsCompat returns the provider-level Chat Completions
// compat block, or nil when the provider configures none.
func providerChatCompletionsCompat(cfg ProviderConfig) *ChatCompletionsCompatConfig {
	if cfg.Compat == nil {
		return nil
	}
	return cfg.Compat.ChatCompletions
}

// modelChatCompletionsCompat returns the model-level Chat Completions compat
// block, or nil when the model configures none.
func modelChatCompletionsCompat(model ModelConfig) *ChatCompletionsCompatConfig {
	if model.Compat == nil {
		return nil
	}
	return model.Compat.ChatCompletions
}
