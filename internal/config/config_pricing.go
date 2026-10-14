package config

import (
	"strings"
)

// ServiceTier identifies the user-facing service tier abstraction.
type ServiceTier string

const (
	ServiceTierStandard ServiceTier = "standard"
	ServiceTierFast     ServiceTier = "fast"
	ServiceTierSlow     ServiceTier = "slow"
)

// NormalizeServiceTier folds arbitrary input into one of the supported tiers.
// Unknown or empty values default to standard.
func NormalizeServiceTier(raw string) ServiceTier {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(ServiceTierFast):
		return ServiceTierFast
	case string(ServiceTierSlow):
		return ServiceTierSlow
	default:
		return ServiceTierStandard
	}
}

// ServiceTierMultipliers stores optional per-tier price multipliers.
type ServiceTierMultipliers struct {
	Fast float64 `json:"fast,omitempty" yaml:"fast,omitempty"`
	Slow float64 `json:"slow,omitempty" yaml:"slow,omitempty"`
}

// ModelCostInputTier overrides the flat top-level prices above a strict token threshold.
type ModelCostInputTier struct {
	AboveInputTokens int64   `json:"above_input_tokens" yaml:"above_input_tokens"`
	Input            float64 `json:"input" yaml:"input"`
	Output           float64 `json:"output" yaml:"output"`
	CacheRead        float64 `json:"cache_read" yaml:"cache_read"`
	CacheWrite       float64 `json:"cache_write" yaml:"cache_write"`
	CacheWrite1h     float64 `json:"cache_write_1h" yaml:"cache_write_1h"`
}

// ResolvedModelCost captures the final prices after input-tier selection and service-tier multipliers.
type ResolvedModelCost struct {
	Input                 float64     `json:"input"`
	Output                float64     `json:"output"`
	CacheRead             float64     `json:"cache_read"`
	CacheWrite            float64     `json:"cache_write"`
	CacheWrite1h          float64     `json:"cache_write_1h"`
	ServiceTier           ServiceTier `json:"service_tier"`
	ServiceTierMultiplier float64     `json:"service_tier_multiplier"`
	InputTierAboveTokens  int64       `json:"input_tier_above_tokens"`
}

// ModelCost records per-token pricing for cost tracking (USD per 1M tokens).
type ModelCost struct {
	Input                  float64                 `json:"input" yaml:"input"`
	Output                 float64                 `json:"output" yaml:"output"`
	CacheRead              float64                 `json:"cache_read" yaml:"cache_read"`
	CacheWrite             float64                 `json:"cache_write" yaml:"cache_write"`
	CacheWrite1h           float64                 `json:"cache_write_1h" yaml:"cache_write_1h"`
	ServiceTierMultipliers *ServiceTierMultipliers `json:"service_tier_multipliers,omitempty" yaml:"service_tier_multipliers,omitempty"`
	InputTiers             []ModelCostInputTier    `json:"input_tiers,omitempty" yaml:"input_tiers,omitempty"`
}

func effectiveCacheWritePrice(input, cacheWrite float64) float64 {
	if cacheWrite > 0 {
		return cacheWrite
	}
	return input
}

func effectiveCacheWrite1hPrice(input, cacheWrite, cacheWrite1h float64) float64 {
	if cacheWrite1h > 0 {
		return cacheWrite1h
	}
	return effectiveCacheWritePrice(input, cacheWrite)
}

// ResolvePricing returns the final token prices for a given billable input size and service tier.
func (c *ModelCost) ResolvePricing(billableInputTokens int64, tier ServiceTier) ResolvedModelCost {
	if c == nil {
		return ResolvedModelCost{ServiceTier: NormalizeServiceTier(string(tier)), ServiceTierMultiplier: 1}
	}
	resolved := ResolvedModelCost{
		Input:                 c.Input,
		Output:                c.Output,
		CacheRead:             c.CacheRead,
		CacheWrite:            effectiveCacheWritePrice(c.Input, c.CacheWrite),
		CacheWrite1h:          effectiveCacheWrite1hPrice(c.Input, c.CacheWrite, c.CacheWrite1h),
		ServiceTier:           NormalizeServiceTier(string(tier)),
		ServiceTierMultiplier: 1,
		InputTierAboveTokens:  -1,
	}
	for _, inputTier := range c.InputTiers {
		if inputTier.AboveInputTokens < billableInputTokens && inputTier.AboveInputTokens > resolved.InputTierAboveTokens {
			resolved.Input = inputTier.Input
			resolved.Output = inputTier.Output
			resolved.CacheRead = inputTier.CacheRead
			resolved.CacheWrite = effectiveCacheWritePrice(inputTier.Input, inputTier.CacheWrite)
			resolved.CacheWrite1h = effectiveCacheWrite1hPrice(inputTier.Input, inputTier.CacheWrite, inputTier.CacheWrite1h)
			resolved.InputTierAboveTokens = inputTier.AboveInputTokens
		}
	}
	if c.ServiceTierMultipliers != nil {
		switch resolved.ServiceTier {
		case ServiceTierFast:
			if c.ServiceTierMultipliers.Fast > 0 {
				resolved.ServiceTierMultiplier = c.ServiceTierMultipliers.Fast
			}
		case ServiceTierSlow:
			if c.ServiceTierMultipliers.Slow > 0 {
				resolved.ServiceTierMultiplier = c.ServiceTierMultipliers.Slow
			}
		}
	}
	if resolved.ServiceTierMultiplier != 1 {
		resolved.Input *= resolved.ServiceTierMultiplier
		resolved.Output *= resolved.ServiceTierMultiplier
		resolved.CacheRead *= resolved.ServiceTierMultiplier
		resolved.CacheWrite *= resolved.ServiceTierMultiplier
		resolved.CacheWrite1h *= resolved.ServiceTierMultiplier
	}
	return resolved
}
