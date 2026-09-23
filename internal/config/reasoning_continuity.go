package config

import (
	"fmt"
	"strings"
)

// Canonical compat.reasoning_continuity selectors. The vocabulary is
// duplicated here rather than imported from internal/modelcompat because
// config validation cannot depend on the request builders; these strings are
// the contract between the two packages.
//
// The mode set is the one a config file can select. "anthropic_blocks" is
// deliberately absent: the resolver returns it from the model's thinking type,
// never from the configured mode, so a config that sets it selects nothing.
const (
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
// replays nothing. The field these replaced (preserve_history) is instead
// rejected loudly by the strict decoder, so a typo in the same block is
// reported on one side and swallowed on the other.
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

// modelReasoningContinuity returns the model-level reasoning continuity block,
// or nil when the model configures none.
func modelReasoningContinuity(model ModelConfig) *ReasoningContinuityCompatConfig {
	if model.Compat == nil {
		return nil
	}
	return model.Compat.ReasoningContinuity
}
