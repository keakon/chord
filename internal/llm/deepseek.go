package llm

import (
	"fmt"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/modelcompat"
)

// DeepSeek's reasoning contract is independent of its Chat and Messages wires.
// A tool conversation preserves reasoning across user turns, and a missing
// reasoning block never changes the requested generation effort.
//
// An explicit compat.reasoning_continuity.contract always wins over the name
// shortcut: "deepseek" keeps the contract on a route whose model ID does not
// identify the backend, and "none" opts a deepseek-named gateway alias that
// serves another backend out of it.
func deepSeekTarget(provider *ProviderConfig, model string) bool {
	if provider == nil {
		return false
	}
	wire := providerWireFamily(provider)
	wireIsDeepSeek := wire == modelcompat.WireFamilyAnthropic || wire == modelcompat.WireFamilyOpenAIChat
	compat := provider.ReasoningContinuityCompat(model)
	if compat != nil {
		if contract := compat.ReasoningContractValue(); contract != "" {
			return contract == config.ReasoningContractDeepSeek && wireIsDeepSeek
		}
	}
	if modelcompat.ModelNativeFamily(model) != modelcompat.NativeFamilyDeepSeek {
		return false
	}
	return wireIsDeepSeek
}

func deepSeekRequestTuning(t RequestTuning) RequestTuning {
	if t.Anthropic.ThinkingType == config.ThinkingTypeDisabled || t.OpenAI.ReasoningEffort == config.ThinkingEffortNone || t.Anthropic.ThinkingEffort == config.ThinkingEffortNone {
		t.Anthropic.ThinkingType = config.ThinkingTypeDisabled
		t.Anthropic.ThinkingBudget = 0
		t.Anthropic.ThinkingDisplay = ""
		t.OpenAI.ReasoningEffort = ""
		t.Anthropic.ThinkingEffort = ""
		return t
	}
	t.Anthropic.ThinkingType = config.ThinkingTypeEnabled
	t.Anthropic.ThinkingBudget = 0
	t.Anthropic.ThinkingDisplay = ""
	if t.OpenAI.ReasoningEffort == "" {
		t.OpenAI.ReasoningEffort = t.Anthropic.ThinkingEffort
	}
	if t.Anthropic.ThinkingEffort == "" {
		t.Anthropic.ThinkingEffort = t.OpenAI.EffectiveReasoningEffort()
	}
	return t
}

func validateMessagesThinking(t RequestTuning, deepSeek bool) (AnthropicTuning, error) {
	if !deepSeek {
		at := t.Anthropic
		// thinking.effort is merged for type enabled because DeepSeek pairs
		// the two. Claude's enabled thinking is budget-only, so the effort is
		// ignored there, as it was before DeepSeek needed it.
		if effectiveAnthropicThinkingType(at) == config.ThinkingTypeEnabled {
			at.ThinkingEffort = ""
		}
		return validateAnthropicTuning(at)
	}
	switch t.Anthropic.ThinkingType {
	case "", config.ThinkingTypeEnabled, config.ThinkingTypeAdaptive, config.ThinkingTypeDisabled:
	default:
		return t.Anthropic, fmt.Errorf("unsupported DeepSeek thinking type %q", t.Anthropic.ThinkingType)
	}
	// The retry layer already applied deepSeekRequestTuning to the request
	// tuning (replayCompatibleRequestTuning); reuse transport/cache validation
	// without imposing Claude's budget rules.
	at := t.Anthropic
	typ, effort := at.ThinkingType, at.ThinkingEffort
	at.ThinkingType, at.ThinkingEffort = "", ""
	at, err := validateAnthropicTuning(at)
	at.ThinkingType, at.ThinkingEffort = typ, effort
	return at, err
}
