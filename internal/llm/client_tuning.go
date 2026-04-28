package llm

import (
	"github.com/keakon/chord/internal/config"
)

func cloneBoolPtr(v *bool) *bool {
	if v == nil {
		return nil
	}
	b := *v
	return &b
}

// tuningFromModel builds a RequestTuning from a ModelConfig.
func tuningFromModel(m config.ModelConfig) RequestTuning {
	var t RequestTuning
	t.Anthropic = mergeAnthropicThinkingTuning(t.Anthropic, m.Thinking)
	t.Anthropic.PromptCacheMode = m.EffectivePromptCacheMode()
	if m.PromptCache != nil {
		t.Anthropic.PromptCacheTTL = m.PromptCache.TTL
		t.Anthropic.CacheTools = m.PromptCache.CacheToolsEnabled()
	}
	if m.Reasoning != nil {
		t.OpenAI.ReasoningEffort = m.Reasoning.Effort
		t.OpenAI.ReasoningSummary = m.Reasoning.Summary
	}
	t.OpenAI.TextVerbosity = m.EffectiveTextVerbosity()
	t.OpenAI.ParallelToolCalls = cloneBoolPtr(m.ParallelToolCalls)
	return t
}

// mergeAnthropicThinkingTuning overlays Anthropic thinking fields according to
// the effective thinking type. Only fields that belong to the resolved type are
// carried forward; switching types clears inherited fields from the old type so
// manual/adaptive/disabled settings do not mix.
func mergeAnthropicThinkingTuning(base AnthropicTuning, thinking *config.ThinkingConfig) AnthropicTuning {
	if thinking == nil {
		return base
	}
	nextType := thinking.EffectiveType()
	prevType := base.ThinkingType
	if nextType != "" {
		if nextType != prevType {
			switch nextType {
			case "enabled":
				base.ThinkingBudget = 0
				base.ThinkingEffort = ""
			case "adaptive":
				base.ThinkingBudget = 0
				base.ThinkingEffort = ""
			case "disabled":
				base.ThinkingBudget = 0
				base.ThinkingEffort = ""
				base.ThinkingDisplay = ""
			}
		}
		base.ThinkingType = nextType
	}
	switch base.ThinkingType {
	case "enabled":
		if thinking.Budget > 0 {
			base.ThinkingBudget = thinking.Budget
		}
		if thinking.Display != "" {
			base.ThinkingDisplay = thinking.Display
		}
	case "adaptive":
		if thinking.Effort != "" {
			base.ThinkingEffort = thinking.Effort
		}
		if thinking.Display != "" {
			base.ThinkingDisplay = thinking.Display
		}
	case "disabled":
		// No additional thinking fields are parsed in disabled mode.
	case "":
		// No effective type: ignore type-specific knobs until a type is selected.
	}
	return base
}

// mergeVariantTuning overlays a named variant's fields onto a base tuning.
// Only non-zero/non-empty fields in the variant overwrite the base.
func mergeVariantTuning(base RequestTuning, v config.ModelVariant) RequestTuning {
	base.Anthropic = mergeAnthropicThinkingTuning(base.Anthropic, v.Thinking)
	if v.PromptCache != nil {
		if v.PromptCache.Mode != "" {
			base.Anthropic.PromptCacheMode = v.PromptCache.Mode
		}
		if v.PromptCache.TTL != "" {
			base.Anthropic.PromptCacheTTL = v.PromptCache.TTL
		}
		if v.PromptCache.CacheTools != nil {
			base.Anthropic.CacheTools = *v.PromptCache.CacheTools
		}
	}
	if v.Reasoning != nil {
		if v.Reasoning.Effort != "" {
			base.OpenAI.ReasoningEffort = v.Reasoning.Effort
		}
		if v.Reasoning.Summary != "" {
			base.OpenAI.ReasoningSummary = v.Reasoning.Summary
		}
	}
	if tv := v.EffectiveTextVerbosity(); tv != "" {
		base.OpenAI.TextVerbosity = tv
	}
	if v.ParallelToolCalls != nil {
		base.OpenAI.ParallelToolCalls = cloneBoolPtr(v.ParallelToolCalls)
	}
	return base
}
