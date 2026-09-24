package llm

import (
	"fmt"

	"github.com/keakon/chord/internal/modelcompat"
)

// DeepSeek's reasoning contract is independent of its Chat and Messages wires.
// A tool conversation preserves reasoning across user turns, and a missing
// reasoning block never changes the requested generation effort.
func deepSeekTarget(provider *ProviderConfig, model string) bool {
	if provider == nil || modelcompat.ModelNativeFamily(model) != modelcompat.NativeFamilyDeepSeek {
		return false
	}
	wire := providerWireFamily(provider)
	return wire == modelcompat.WireFamilyAnthropic || wire == modelcompat.WireFamilyOpenAIChat
}

func deepSeekRequestTuning(t RequestTuning) RequestTuning {
	if t.Anthropic.ThinkingType == "disabled" {
		t.OpenAI.ReasoningEffort = ""
		t.Anthropic.ThinkingEffort = ""
		return t
	}
	t.Anthropic.ThinkingType = "enabled"
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
		if effectiveAnthropicThinkingType(at) == "enabled" {
			at.ThinkingEffort = ""
		}
		return validateAnthropicTuning(at)
	}
	switch t.Anthropic.ThinkingType {
	case "", "enabled", "adaptive", "disabled":
	default:
		return t.Anthropic, fmt.Errorf("unsupported DeepSeek thinking type %q", t.Anthropic.ThinkingType)
	}
	at := deepSeekRequestTuning(t).Anthropic
	// Reuse transport/cache validation without imposing Claude's budget rules.
	typ, effort := at.ThinkingType, at.ThinkingEffort
	at.ThinkingType, at.ThinkingEffort = "", ""
	at, err := validateAnthropicTuning(at)
	at.ThinkingType, at.ThinkingEffort = typ, effort
	return at, err
}
