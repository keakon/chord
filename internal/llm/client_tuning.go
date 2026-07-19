package llm

import (
	"maps"

	"github.com/keakon/chord/internal/config"
)

func geminiLevelFromThinking(t *config.ThinkingConfig) string {
	if t == nil {
		return ""
	}
	return t.Level
}

// tuningFromModel builds a RequestTuning from a ModelConfig.
func tuningFromModel(m config.ModelConfig, providerPreset string, providerTiers []config.ServiceTier) RequestTuning {
	var t RequestTuning
	t.SupportedServiceTiers = (&m).SupportedServiceTierSet(providerPreset, providerTiers)
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
	if m.ParallelToolCalls != nil {
		t.OpenAI.ParallelToolCalls = new(*m.ParallelToolCalls)
	}
	if m.Thinking != nil {
		if m.Thinking.Budget != 0 {
			t.Gemini.ThinkingBudget = new(m.Thinking.Budget)
		}
		t.Gemini.ThinkingLevel = geminiLevelFromThinking(m.Thinking)
		if m.Thinking.IncludeThoughts != nil {
			t.Gemini.IncludeThoughts = new(*m.Thinking.IncludeThoughts)
		}
	}
	return t
}

func serviceTierTuning(t RequestTuning, tier config.ServiceTier) RequestTuning {
	tier = config.NormalizeServiceTier(string(tier))
	if !t.SupportedServiceTiers[tier] {
		return t
	}
	switch tier {
	case config.ServiceTierFast:
		// Align with Codex: "fast" is a service tier / transport-level acceleration.
		// It should NOT change behavior knobs like reasoning effort / verbosity.
		//
		// - OpenAI Responses: set service_tier="fast".
		// - Anthropic: set speed="fast" on wire (mapped from the same service tier).
		// - Gemini: there is no generic "speed" knob; the fast tier is a no-op
		//   (callers can still choose a cheaper model pool / thinking level via variants).
		t.OpenAI.ServiceTier = "fast"
		t.Anthropic.ServiceTier = "fast"
	case config.ServiceTierSlow:
		// OpenAI exposes the lower-cost/lower-priority tier as flex. Other providers
		// either do not expose a generic slow tier through this abstraction or use
		// provider-specific routing; leave those wire fields unset.
		t.OpenAI.ServiceTier = "flex"
	}
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
		base.OpenAI.ParallelToolCalls = new(*v.ParallelToolCalls)
	}
	if v.Thinking != nil {
		if v.Thinking.Budget != 0 {
			base.Gemini.ThinkingBudget = new(v.Thinking.Budget)
		}
		if lvl := geminiLevelFromThinking(v.Thinking); lvl != "" {
			base.Gemini.ThinkingLevel = lvl
		}
		if v.Thinking.IncludeThoughts != nil {
			base.Gemini.IncludeThoughts = new(*v.Thinking.IncludeThoughts)
		}
	}
	return base
}

func cloneServiceTiers(tiers map[config.ServiceTier]bool) map[config.ServiceTier]bool {
	if tiers == nil {
		return nil
	}
	out := make(map[config.ServiceTier]bool, len(tiers))
	maps.Copy(out, tiers)
	return out
}

func cloneRequestTuning(tuning RequestTuning) RequestTuning {
	copy := tuning
	if tuning.Anthropic.Temperature != nil {
		copy.Anthropic.Temperature = new(*tuning.Anthropic.Temperature)
	}
	if tuning.OpenAI.ParallelToolCalls != nil {
		copy.OpenAI.ParallelToolCalls = new(*tuning.OpenAI.ParallelToolCalls)
	}
	if tuning.Gemini.ThinkingBudget != nil {
		copy.Gemini.ThinkingBudget = new(*tuning.Gemini.ThinkingBudget)
	}
	if tuning.Gemini.IncludeThoughts != nil {
		copy.Gemini.IncludeThoughts = new(*tuning.Gemini.IncludeThoughts)
	}
	copy.SupportedServiceTiers = cloneServiceTiers(tuning.SupportedServiceTiers)
	return copy
}

func mergeRequestTuning(base, tuning RequestTuning) RequestTuning {
	if tuning.Anthropic.ThinkingType != "" {
		base.Anthropic.ThinkingType = tuning.Anthropic.ThinkingType
	}
	if tuning.Anthropic.ThinkingBudget != 0 {
		base.Anthropic.ThinkingBudget = tuning.Anthropic.ThinkingBudget
	}
	if tuning.Anthropic.ThinkingEffort != "" {
		base.Anthropic.ThinkingEffort = tuning.Anthropic.ThinkingEffort
	}
	if tuning.Anthropic.ThinkingDisplay != "" {
		base.Anthropic.ThinkingDisplay = tuning.Anthropic.ThinkingDisplay
	}
	if tuning.Anthropic.PromptCacheMode != "" {
		base.Anthropic.PromptCacheMode = tuning.Anthropic.PromptCacheMode
	}
	if tuning.Anthropic.PromptCacheTTL != "" {
		base.Anthropic.PromptCacheTTL = tuning.Anthropic.PromptCacheTTL
	}
	if tuning.Anthropic.CacheTools {
		base.Anthropic.CacheTools = true
	}
	if tuning.Anthropic.CacheBoundary.Valid {
		base.Anthropic.CacheBoundary = tuning.Anthropic.CacheBoundary
	}
	if tuning.Anthropic.ServiceTier != "" {
		base.Anthropic.ServiceTier = tuning.Anthropic.ServiceTier
	}
	if tuning.Anthropic.ToolChoice != "" {
		base.Anthropic.ToolChoice = tuning.Anthropic.ToolChoice
	}
	if tuning.Anthropic.Temperature != nil {
		base.Anthropic.Temperature = new(*tuning.Anthropic.Temperature)
	}
	if tuning.OpenAI.ReasoningEffort != "" {
		base.OpenAI.ReasoningEffort = tuning.OpenAI.ReasoningEffort
	}
	if tuning.OpenAI.ReasoningSummary != "" {
		base.OpenAI.ReasoningSummary = tuning.OpenAI.ReasoningSummary
	}
	if tuning.OpenAI.TextVerbosity != "" {
		base.OpenAI.TextVerbosity = tuning.OpenAI.TextVerbosity
	}
	if tuning.OpenAI.ParallelToolCalls != nil {
		base.OpenAI.ParallelToolCalls = new(*tuning.OpenAI.ParallelToolCalls)
	}
	if tuning.OpenAI.ToolChoice != "" {
		base.OpenAI.ToolChoice = tuning.OpenAI.ToolChoice
	}
	if tuning.Gemini.ToolChoice != "" {
		base.Gemini.ToolChoice = tuning.Gemini.ToolChoice
	}
	if tuning.Gemini.ThinkingBudget != nil {
		base.Gemini.ThinkingBudget = new(*tuning.Gemini.ThinkingBudget)
	}
	if tuning.Gemini.ThinkingLevel != "" {
		base.Gemini.ThinkingLevel = tuning.Gemini.ThinkingLevel
	}
	if tuning.Gemini.IncludeThoughts != nil {
		base.Gemini.IncludeThoughts = new(*tuning.Gemini.IncludeThoughts)
	}
	if len(tuning.SupportedServiceTiers) > 0 {
		base.SupportedServiceTiers = cloneServiceTiers(tuning.SupportedServiceTiers)
	}
	return base
}
