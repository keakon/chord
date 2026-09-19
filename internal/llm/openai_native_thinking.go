package llm

import (
	"strings"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/modelcompat"
)

// A Chat Completions endpoint is frequently a gateway that translates the call
// into the target model's native API. Chord's thinking keys are
// wire-independent, but the gateway only reads the controls in the shape its own
// translation understands, so the chat body carries a dialect-specific field
// instead of the native generationConfig / thinking blocks. The dialect is
// inferred from the model name and can be forced with
// `compat.chat_completions.native_thinking`.

type nativeThinkingDialect string

const (
	// nativeThinkingOff leaves the thinking controls out of the body.
	nativeThinkingOff nativeThinkingDialect = ""
	// The concrete dialects reuse the canonical shape names of
	// config.NormalizeNativeThinking, so a normalized selector converts to a
	// dialect directly. nativeThinkingGemini emits
	// extra_body.google.thinking_config, the shape Google's compatibility
	// endpoint and gateways copying it read.
	nativeThinkingGemini nativeThinkingDialect = config.NativeThinkingGemini
	// nativeThinkingGemini3 emits the same request shape and identifies a
	// pinned Gemini 3 target for signature replay repair.
	nativeThinkingGemini3 nativeThinkingDialect = config.NativeThinkingGemini3
	// nativeThinkingAnthropic emits the Anthropic thinking object that
	// gateways translating the call into a Messages request read.
	nativeThinkingAnthropic nativeThinkingDialect = config.NativeThinkingAnthropic
	// nativeThinkingObject emits the native thinking:{type} object shared by
	// DeepSeek, GLM, Kimi K2.x, and Doubao.
	nativeThinkingObject nativeThinkingDialect = config.NativeThinkingObject
	// nativeThinkingQwen emits DashScope's boolean enable_thinking flag.
	nativeThinkingQwen nativeThinkingDialect = config.NativeThinkingQwen
)

// pinnedNativeThinkingDialect reports whether the compat block pins the chat
// dialect explicitly, i.e. with a value other than unset or `auto`. The model
// name cannot identify an aliased endpoint, so a pin is the only signal that an
// unrecognized ID really speaks a known native dialect.
func pinnedNativeThinkingDialect(compat *config.ChatCompletionsCompatConfig) bool {
	if compat == nil {
		return false
	}
	selector := strings.TrimSpace(compat.NativeThinkingValue())
	return selector != "" && !strings.EqualFold(selector, config.NativeThinkingAuto)
}

// resolveNativeThinkingDialect picks the dialect for a Chat Completions target.
// The configured selector wins over inference, and both are limited to the
// dialects Chord knows how to build: a wrong selector fails the request with a
// clear message instead of silently dropping the model's thinking settings. The
// selector vocabulary lives in config so the loader can reject an unknown value
// before it reaches a request.
func resolveNativeThinkingDialect(modelID, selector string) (nativeThinkingDialect, error) {
	shape, err := config.NormalizeNativeThinking(selector)
	if err != nil {
		return nativeThinkingOff, err
	}
	switch shape {
	case config.NativeThinkingAuto:
		return inferNativeThinkingDialect(modelID), nil
	case config.NativeThinkingOff:
		return nativeThinkingOff, nil
	}
	return nativeThinkingDialect(shape), nil
}

// inferNativeThinkingDialect maps a model name onto the shape its upstream API
// expects. Models outside the known families keep the thinking controls out of
// the chat body: sending a guessed field to an endpoint that rejects it would
// fail the request, and the previous behavior (an inert thinking block) is the
// safer default. Those setups name the dialect explicitly instead.
func inferNativeThinkingDialect(modelID string) nativeThinkingDialect {
	switch modelcompat.ModelNativeFamily(modelID) {
	case modelcompat.NativeFamilyGemini:
		return nativeThinkingGemini
	case modelcompat.NativeFamilyAnthropic:
		return nativeThinkingAnthropic
	}
	m := strings.ToLower(modelID)
	switch {
	case strings.Contains(m, "qwen"), strings.Contains(m, "qwq"):
		return nativeThinkingQwen
	case strings.Contains(m, "deepseek"), strings.Contains(m, "glm"), strings.Contains(m, "zhipu"),
		strings.Contains(m, "kimi"), strings.Contains(m, "moonshot"), strings.Contains(m, "doubao"):
		return nativeThinkingObject
	}
	return nativeThinkingOff
}

// nativeThinkingConfigured reports whether the model configures the
// wire-independent thinking block. On the Chat Completions wire Chord then
// writes a native thinking field into the body (see applyNativeThinking), which
// makes the request a thinking request for the replay-continuity checks even
// when no reasoning effort or request override is configured.
func nativeThinkingConfigured(tuning RequestTuning) bool {
	if tuning.Anthropic.ThinkingType != "" || tuning.Anthropic.ThinkingBudget > 0 {
		return true
	}
	return tuning.Gemini.ThinkingLevel != "" || tuning.Gemini.ThinkingBudget != nil || tuning.Gemini.IncludeThoughts != nil
}

// applyNativeThinking adds the dialect-specific thinking field to the chat body.
// Each dialect reads the knobs its upstream controls: Gemini takes
// thinking.level / budget / include_thoughts, while the other shapes take the
// shared thinking.type / budget / display block that also drives the Messages
// wire.
func applyNativeThinking(req *openAIRequest, dialect nativeThinkingDialect, tuning RequestTuning) {
	switch dialect {
	case nativeThinkingGemini, nativeThinkingGemini3:
		req.ExtraBody = geminiThinkingExtraBody(tuning.Gemini)
	case nativeThinkingAnthropic:
		req.Thinking = anthropicChatThinking(tuning.Anthropic)
	case nativeThinkingObject:
		req.Thinking = nativeThinkingObjectFor(tuning.Anthropic)
	case nativeThinkingQwen:
		req.EnableThinking = qwenEnableThinking(tuning.Anthropic)
	}
}

// openAIExtraBody is the provider-scoped passthrough object on the Chat
// Completions body.
type openAIExtraBody struct {
	Google *openAIGoogleExtraBody `json:"google,omitempty"`
}

type openAIGoogleExtraBody struct {
	ThinkingConfig *openAIGoogleThinkingConfig `json:"thinking_config,omitempty"`
}

// openAIGoogleThinkingConfig mirrors generationConfig.thinkingConfig with the
// snake_case keys of the chat-completions compatibility convention.
type openAIGoogleThinkingConfig struct {
	ThinkingBudget  *int   `json:"thinking_budget,omitempty"`
	ThinkingLevel   string `json:"thinking_level,omitempty"`
	IncludeThoughts *bool  `json:"include_thoughts,omitempty"`
}

// geminiThinkingExtraBody maps the configured Gemini thinking knobs onto the
// extra_body block. It shares normalizeGeminiThinking and geminiIncludeThoughts
// with the native generate-content body so both wires describe the same
// thinking request, including the budget/level conflict rule and the
// include_thoughts default. Returns nil when nothing is configured, which keeps
// the field out of the body.
func geminiThinkingExtraBody(t GeminiTuning) *openAIExtraBody {
	normalized := normalizeGeminiThinking(t)
	thinking := openAIGoogleThinkingConfig{
		ThinkingBudget:  normalized.ThinkingBudget,
		ThinkingLevel:   normalized.ThinkingLevel,
		IncludeThoughts: geminiIncludeThoughts(normalized),
	}
	if thinking.ThinkingBudget == nil && thinking.ThinkingLevel == "" && thinking.IncludeThoughts == nil {
		return nil
	}
	return &openAIExtraBody{Google: &openAIGoogleExtraBody{ThinkingConfig: &thinking}}
}

// anthropicChatThinking mirrors the Messages wire thinking object for endpoints
// that translate the chat body into an Anthropic request. A budget without a
// type means manual mode, matching the native wire; the budget is dropped for
// the modes that reject it, so the emitted object stays valid.
func anthropicChatThinking(t AnthropicTuning) *anthropicThinking {
	typ := t.ThinkingType
	if typ == "" && t.ThinkingBudget > 0 {
		typ = "enabled"
	}
	if typ == "" {
		return nil
	}
	obj := &anthropicThinking{Type: typ, Display: t.ThinkingDisplay}
	if typ == "enabled" {
		obj.BudgetTokens = t.ThinkingBudget
	}
	return obj
}

// nativeThinkingObjectFor emits the family-native thinking object, mapping the
// shared thinking modes onto the enabled/disabled toggle those APIs accept.
func nativeThinkingObjectFor(t AnthropicTuning) *anthropicThinking {
	switch {
	case t.ThinkingType == "disabled":
		return &anthropicThinking{Type: "disabled"}
	case t.ThinkingType == "enabled", t.ThinkingType == "adaptive", t.ThinkingBudget > 0:
		return &anthropicThinking{Type: "enabled"}
	}
	return nil
}

// qwenEnableThinking maps the shared thinking modes onto the DashScope boolean
// flag.
func qwenEnableThinking(t AnthropicTuning) *bool {
	switch {
	case t.ThinkingType == "disabled":
		return new(false)
	case t.ThinkingType == "enabled", t.ThinkingType == "adaptive", t.ThinkingBudget > 0:
		return new(true)
	}
	return nil
}

// chatCompletionsNativeThinking resolves the dialect for one Chat Completions
// request.
func chatCompletionsNativeThinking(modelID string, compat *config.ChatCompletionsCompatConfig) (nativeThinkingDialect, error) {
	return resolveNativeThinkingDialect(modelID, compat.NativeThinkingValue())
}
