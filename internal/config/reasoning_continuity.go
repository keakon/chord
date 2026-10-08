package config

import (
	"fmt"
	"strings"
)

// Canonical compat.reasoning_continuity selectors. The mode and replay values
// mirror the continuity constants in internal/modelcompat; these strings are
// the contract between config validation and the request resolver.
//
// The mode set is the one a config file can select. "anthropic_blocks" is
// deliberately absent: the resolver returns it from the model's thinking type,
// never from the configured mode, so a config that sets it selects nothing.
const (
	// ReasoningContractDeepSeek enables DeepSeek's strict tool-history
	// reasoning passback and request tuning for an explicitly configured route.
	ReasoningContractDeepSeek = "deepseek"
	// ReasoningContractGemini3 enables missing thought-signature repair on the
	// native generate-content endpoint. When the contract is unset there, a
	// model ID whose final component starts with "gemini-3" (case-insensitive,
	// "models/" prefix allowed; see IsGemini3ModelID) selects it automatically.
	// A Chat Completions gateway only follows native_thinking: gemini-3.
	ReasoningContractGemini3 = "gemini-3"
	// ReasoningContractNone explicitly opts a route out of every endpoint
	// contract, including the ones inferred from a DeepSeek model name or a
	// Gemini 3 model ID on the native endpoint.
	ReasoningContractNone = "none"
	// ReasoningContinuityModeNone replays no provider-specific reasoning state.
	ReasoningContinuityModeNone = "none"
	// ReasoningContinuityModeOpenAIVisible replays OpenAI-compatible visible
	// assistant reasoning without injecting provider-specific request fields.
	ReasoningContinuityModeOpenAIVisible = "openai_visible"
	// ReasoningContinuityModeAnthropicUnsigned replays visible, unsigned
	// Anthropic thinking for the same configured provider/model target.
	ReasoningContinuityModeAnthropicUnsigned = "anthropic_unsigned"

	// ReasoningReplayAll replays completed-turn reasoning unchanged.
	ReasoningReplayAll = "all"
	// ReasoningReplayCurrentTurn keeps only the reasoning after the last user
	// message. It is the default when the field is unset.
	ReasoningReplayCurrentTurn = "current_turn"
	// ReasoningReplayNone strips reasoning everywhere, current turn included.
	ReasoningReplayNone = "none"
)

// validReasoningContract reports whether v is a contract a config file can
// select. The empty value means unset; the runtime then infers the contract
// from the model name and the wire.
func validReasoningContract(v string) bool {
	switch v {
	case "", ReasoningContractDeepSeek, ReasoningContractGemini3, ReasoningContractNone:
		return true
	}
	return false
}

// validReasoningContinuityMode reports whether v is a mode a config file can
// select. The empty value means unset; the resolver then infers the mode from
// the wire family, the thinking type and the model name.
func validReasoningContinuityMode(v string) bool {
	switch strings.TrimSpace(v) {
	case "", ReasoningContinuityModeNone, ReasoningContinuityModeOpenAIVisible,
		ReasoningContinuityModeAnthropicUnsigned:
		return true
	}
	return false
}

// validReasoningReplay reports whether v is a supported reasoning replay
// window. The empty value means unset and takes the current_turn default.
func validReasoningReplay(v string) bool {
	switch strings.TrimSpace(v) {
	case "", ReasoningReplayAll, ReasoningReplayCurrentTurn, ReasoningReplayNone:
		return true
	}
	return false
}

// ValidateProviderReasoningContinuity rejects unknown
// compat.reasoning_continuity selectors set as the provider default or on any
// of the provider's models. Both fields are otherwise only read while
// resolving a request, where an unknown value is indistinguishable from unset:
// a misspelled reasoning_replay silently drops the cross-turn reasoning
// continuity the backend's contract requires, and a misspelled mode silently
// replays nothing. Validate both fields so unknown values are reported before
// any request is sent.
func ValidateProviderReasoningContinuity(providerName string, cfg ProviderConfig) error {
	if compat := providerReasoningContinuity(cfg); compat != nil {
		if err := validateReasoningContinuityConfig(compat, fmt.Sprintf("provider %q", providerName)); err != nil {
			return err
		}
	}
	for modelID, model := range cfg.Models {
		compat := modelReasoningContinuity(model)
		if compat == nil {
			continue
		}
		if err := validateReasoningContinuityConfig(compat, fmt.Sprintf("model %q in provider %q", modelID, providerName)); err != nil {
			return err
		}
	}
	return nil
}

func validateReasoningContinuityConfig(compat *ReasoningContinuityCompatConfig, where string) error {
	if contract := compat.ReasoningContractValue(); !validReasoningContract(contract) {
		return fmt.Errorf("invalid reasoning_continuity contract %q for %s (allowed: %q, %q, %q)", contract, where,
			ReasoningContractDeepSeek, ReasoningContractGemini3, ReasoningContractNone)
	}
	if mode := compat.EffectiveMode(); !validReasoningContinuityMode(mode) {
		return fmt.Errorf("invalid reasoning_continuity mode %q for %s (allowed: %q, %q, %q)", mode, where,
			ReasoningContinuityModeNone, ReasoningContinuityModeOpenAIVisible, ReasoningContinuityModeAnthropicUnsigned)
	}
	if replay := compat.ReasoningReplayValue(); !validReasoningReplay(replay) {
		return fmt.Errorf("invalid reasoning_replay %q for %s (allowed: %q, %q, %q)", replay, where,
			ReasoningReplayAll, ReasoningReplayCurrentTurn, ReasoningReplayNone)
	}
	return nil
}

// resetInvalidReasoningContinuity clears continuity selectors that failed
// validation, restoring the unset state rather than leaving a value the
// resolver can only treat as unset. Clearing (not writing the default) is what
// keeps the reset behavior-preserving: an unset mode still infers from the
// wire family and the thinking type, and an unset reasoning_replay still takes
// the current_turn default.
func resetInvalidReasoningContinuity(cfg ProviderConfig) ProviderConfig {
	if compat := providerReasoningContinuity(cfg); compat != nil {
		resetInvalidReasoningContinuityConfig(compat)
	}
	for _, model := range cfg.Models {
		if compat := modelReasoningContinuity(model); compat != nil {
			resetInvalidReasoningContinuityConfig(compat)
		}
	}
	return cfg
}

func resetInvalidReasoningContinuityConfig(compat *ReasoningContinuityCompatConfig) {
	if !validReasoningContract(compat.ReasoningContractValue()) {
		compat.Contract = ""
	}
	if !validReasoningContinuityMode(compat.EffectiveMode()) {
		compat.Mode = ""
	}
	if !validReasoningReplay(compat.ReasoningReplayValue()) {
		compat.ReasoningReplay = ""
	}
}

// providerReasoningContinuity returns the provider-level reasoning continuity
// block, or nil when the provider configures none.
func providerReasoningContinuity(cfg ProviderConfig) *ReasoningContinuityCompatConfig {
	if cfg.Compat == nil {
		return nil
	}
	return cfg.Compat.ReasoningContinuity
}

// gemini3ContractPlacementAdvisory reports a config that names the Gemini 3
// thought-signature repair on a route that can never activate it. The repair
// runs on the native generate-content endpoint or, behind a Chat Completions
// gateway, on a route that also pins compat.chat_completions.native_thinking
// to gemini-3; on a messages or responses provider the contract loads but
// stays inert, and the repair the config asked for never happens. The check is
// a route-level heuristic: it cannot see which single model a provider-level
// contract was meant for, so a gemini-3 pin on any one model keeps it quiet.
func gemini3ContractPlacementAdvisory(providerName string, cfg ProviderConfig) string {
	hasGemini3Contract := func(compat *ReasoningContinuityCompatConfig) bool {
		return compat != nil && compat.ReasoningContractValue() == ReasoningContractGemini3
	}
	contractConfigured := hasGemini3Contract(providerReasoningContinuity(cfg))
	for _, model := range cfg.Models {
		if hasGemini3Contract(modelReasoningContinuity(model)) {
			contractConfigured = true
		}
	}
	if !contractConfigured {
		return ""
	}
	switch EffectiveProviderType(cfg) {
	case ProviderTypeChatCompletions:
		if gemini3SelectorPinned(cfg) {
			return ""
		}
	case ProviderTypeMessages, ProviderTypeResponses:
		// The contract never activates on these wires.
	default:
		// The native endpoint activates it; an unresolved type is refused by
		// the runtime and reported there.
		return ""
	}
	return fmt.Sprintf("provider %q sets reasoning_continuity contract %q, but the route never activates it: the Gemini 3 signature repair needs the native generate-content endpoint or a chat-completions route that also pins native_thinking %q",
		providerName, ReasoningContractGemini3, NativeThinkingGemini3)
}

// gemini3SelectorPinned reports whether any chat-completions compat block on
// the provider or one of its models pins the native_thinking selector to the
// Gemini 3 shape.
func gemini3SelectorPinned(cfg ProviderConfig) bool {
	pinned := func(compat *ChatCompletionsCompatConfig) bool {
		if compat == nil {
			return false
		}
		shape, err := NormalizeNativeThinking(compat.NativeThinkingValue())
		return err == nil && shape == NativeThinkingGemini3
	}
	if cfg.Compat != nil && pinned(cfg.Compat.ChatCompletions) {
		return true
	}
	for _, model := range cfg.Models {
		if model.Compat != nil && pinned(model.Compat.ChatCompletions) {
			return true
		}
	}
	return false
}

// routeReasoningContract returns the endpoint contract a model's requests
// resolve: the model's own contract when set, otherwise the provider default.
func routeReasoningContract(cfg ProviderConfig, model ModelConfig) string {
	if contract := modelReasoningContinuity(model).ReasoningContractValue(); contract != "" {
		return contract
	}
	return providerReasoningContinuity(cfg).ReasoningContractValue()
}

// modelReasoningContinuity returns the model-level reasoning continuity block,
// or nil when the model configures none.
func modelReasoningContinuity(model ModelConfig) *ReasoningContinuityCompatConfig {
	if model.Compat == nil {
		return nil
	}
	return model.Compat.ReasoningContinuity
}
