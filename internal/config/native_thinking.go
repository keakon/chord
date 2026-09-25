package config

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/keakon/chord/internal/modelcompat"
)

// Canonical shapes for compat.chat_completions.native_thinking. Each one names a
// request shape Chord knows how to build for a Chat Completions endpoint that
// translates the call into the model's native API; NormalizeNativeThinking maps
// the accepted selectors, including the family aliases, onto these values, and
// the request builders switch on them.
const (
	// NativeThinkingOff leaves the thinking controls out of the request body.
	NativeThinkingOff = "off"
	// NativeThinkingGemini emits extra_body.google.thinking_config.
	NativeThinkingGemini = "gemini"
	// NativeThinkingGemini3 emits the Gemini request shape and identifies a
	// pinned Gemini 3 target for signature replay repair.
	NativeThinkingGemini3 = "gemini-3"
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
// The family aliases let a model whose id does not reveal its upstream name
// declare the shape. An unset selector is handled by NormalizeNativeThinking
// as off; the Chat Completions request builder only still recognizes a
// DeepSeek model name on its own, and every other backend must name its shape.
var nativeThinkingShapes = map[string]string{
	"off":        NativeThinkingOff,
	"none":       NativeThinkingOff,
	"gemini":     NativeThinkingGemini,
	"google":     NativeThinkingGemini,
	"vertex":     NativeThinkingGemini,
	"gemini-3":   NativeThinkingGemini3,
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
// import the request builders. An unset selector disables native thinking
// translation; an unknown value is an error.
func NormalizeNativeThinking(value string) (string, error) {
	selector := strings.ToLower(strings.TrimSpace(value))
	if selector == "" {
		return NativeThinkingOff, nil
	}
	if shape, ok := nativeThinkingShapes[selector]; ok {
		return shape, nil
	}
	return "", fmt.Errorf("invalid native_thinking %q (allowed: %s)", value, strings.Join(nativeThinkingSelectors(), ", "))
}

// nativeThinkingSelectors lists the accepted selectors in a stable order for
// the validation error.
func nativeThinkingSelectors() []string {
	selectors := make([]string, 0, len(nativeThinkingShapes))
	for selector := range nativeThinkingShapes {
		selectors = append(selectors, selector)
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
// validation, so the provider returns to the protocol-generic default instead
// of failing every request that needs the field.
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

// nativeThinkingSelectorAdvisories reports chat-completions models whose
// configuration only works through a native_thinking selector they do not
// set. A Chat Completions endpoint is usually a gateway, so the model ID alone
// never selects a thinking dialect (DeepSeek names aside): without a selector
// the configured thinking block is left out of the request body, and Chord
// cannot tell that the gateway fronts Gemini, so Gemini thought signatures are
// not replayed. The request builder still adds the thinking object for a
// DeepSeek target (an explicit deepseek reasoning contract, or a DeepSeek name
// without an explicit contract), and an OpenAI model takes reasoning.effort
// rather than a thinking block, so neither is reported; a DeepSeek-named alias
// that opts out with another contract is. A Gemini 3 ID is reported even
// without a thinking block:
// Gemini 3 always thinks, and its follow-up tool calls are rejected when the
// signatures it returned are not written back.
func nativeThinkingSelectorAdvisories(providerName string, cfg ProviderConfig) []string {
	if EffectiveProviderType(cfg) != ProviderTypeChatCompletions {
		return nil
	}
	providerSelector := ""
	if compat := providerChatCompletionsCompat(cfg); compat != nil {
		providerSelector = compat.NativeThinkingValue()
	}
	var advisories []string
	for _, modelID := range slices.Sorted(maps.Keys(cfg.Models)) {
		model := cfg.Models[modelID]
		selector := providerSelector
		if compat := modelChatCompletionsCompat(model); compat != nil && strings.TrimSpace(compat.NativeThinkingValue()) != "" {
			selector = compat.NativeThinkingValue()
		}
		// A family selector preserves real signatures, but does not repair
		// missing signatures required by Gemini 3 tool-call history.
		shape, _ := NormalizeNativeThinking(selector)
		if shape == NativeThinkingGemini && IsGemini3ModelID(modelID) {
			advisories = append(advisories, fmt.Sprintf("provider %q model %q uses native_thinking: %s: real thought signatures are preserved, but missing-signature repair is disabled; select native_thinking: %s for Gemini 3", providerName, modelID, NativeThinkingGemini, NativeThinkingGemini3))
		}
		// Other set selectors are either valid or already reported as invalid.
		if strings.TrimSpace(selector) != "" {
			continue
		}
		family := modelcompat.ModelNativeFamily(modelID)
		contract := routeReasoningContract(cfg, model)
		deepSeek := contract == ReasoningContractDeepSeek || (contract == "" && family == modelcompat.NativeFamilyDeepSeek)
		if deepSeek || family == modelcompat.NativeFamilyOpenAI {
			continue
		}
		where := fmt.Sprintf("provider %q model %q is on a chat-completions route without compat.chat_completions.native_thinking", providerName, modelID)
		switch {
		case IsGemini3ModelID(modelID):
			advisories = append(advisories, fmt.Sprintf("%s: Gemini 3 thought signatures are not written back, so the gateway rejects follow-up tool calls (HTTP 400); set native_thinking: %s",
				where, NativeThinkingGemini3))
		case !modelRequestsThinking(model):
		case family == modelcompat.NativeFamilyGemini:
			advisories = append(advisories, fmt.Sprintf("%s: the thinking settings are left out of the request and Gemini thought signatures are not written back; set native_thinking: %s",
				where, NativeThinkingGemini))
		case family == modelcompat.NativeFamilyAnthropic:
			advisories = append(advisories, fmt.Sprintf("%s: the thinking settings are left out of the request; set native_thinking: %s",
				where, NativeThinkingAnthropic))
		default:
			advisories = append(advisories, fmt.Sprintf("%s: the thinking settings are left out of the request; for an OpenAI backend use reasoning.effort instead of a thinking block; otherwise set native_thinking to the shape the backend reads (%s, %s, %s, %s or %s)",
				where, NativeThinkingObject, NativeThinkingQwen, NativeThinkingAnthropic, NativeThinkingGemini, NativeThinkingGemini3))
		}
	}
	return advisories
}

// gemini3ModelIDPrefix starts every Gemini 3 model ID ("gemini-3-pro-preview",
// "gemini-3.1-flash", ...).
const gemini3ModelIDPrefix = "gemini-3"

// IsGemini3ModelID reports whether a model ID names a Gemini 3 model: its final
// "/" component (which also covers the native "models/<id>" resource form and
// gateway vendor prefixes) starts with "gemini-3".
func IsGemini3ModelID(modelID string) bool {
	base := strings.ToLower(strings.TrimSpace(modelID))
	if slash := strings.LastIndex(base, "/"); slash >= 0 {
		base = base[slash+1:]
	}
	return strings.HasPrefix(base, gemini3ModelIDPrefix)
}

// modelRequestsThinking reports whether the model or one of its variants
// configures a thinking block that turns thinking on. A block that only sets
// type: disabled asks for nothing a missing selector could drop.
func modelRequestsThinking(model ModelConfig) bool {
	if thinkingRequested(model.Thinking) {
		return true
	}
	for _, variant := range model.Variants {
		if thinkingRequested(variant.Thinking) {
			return true
		}
	}
	return false
}

// thinkingRequested reports whether a thinking block enables thinking: an
// enabled or adaptive type, or, without a type, any budget, level or
// include_thoughts request.
func thinkingRequested(t *ThinkingConfig) bool {
	if t == nil {
		return false
	}
	switch t.EffectiveType() {
	case ThinkingTypeEnabled, ThinkingTypeAdaptive:
		return true
	case "":
		return t.Budget != 0 || strings.TrimSpace(t.Level) != "" || (t.IncludeThoughts != nil && *t.IncludeThoughts)
	}
	return false
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
