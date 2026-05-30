package analytics

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

const (
	usageEventVersion   = 1
	usageSummaryVersion = 2
)

// UsageSnapshot records provider-reported usage fields as-is.
type UsageSnapshot struct {
	InputTokens        int64 `json:"input_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
	CacheReadTokens    int64 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens   int64 `json:"cache_write_tokens,omitempty"`
	CacheWrite1hTokens int64 `json:"cache_write_1h_tokens,omitempty"`
	ReasoningTokens    int64 `json:"reasoning_tokens,omitempty"`
}

func UsageSnapshotIsZero(usage UsageSnapshot) bool {
	return usage.InputTokens == 0 &&
		usage.OutputTokens == 0 &&
		usage.CacheReadTokens == 0 &&
		usage.CacheWriteTokens == 0 &&
		usage.CacheWrite1hTokens == 0 &&
		usage.ReasoningTokens == 0
}

// BillingUsage records normalized, mutually-exclusive billing buckets.
// InputTokens excludes cache-read tokens so cost calculation does not charge
// the same tokens as both normal input and cache reads.
type BillingUsage struct {
	InputTokens        int64 `json:"input_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
	CacheReadTokens    int64 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens   int64 `json:"cache_write_tokens,omitempty"`
	CacheWrite1hTokens int64 `json:"cache_write_1h_tokens,omitempty"`
	BillingTotalTokens int64 `json:"billing_total_tokens"`
}

// UsageCost stores the cost breakdown for a single usage event.
type UsageCost struct {
	Currency       string  `json:"currency"`
	InputCost      float64 `json:"input_cost"`
	OutputCost     float64 `json:"output_cost"`
	CacheReadCost  float64 `json:"cache_read_cost"`
	CacheWriteCost float64 `json:"cache_write_cost"`
	TotalCost      float64 `json:"total_cost"`
}

// PricingSnapshot stores the per-1M token prices used to compute the event cost.
type PricingSnapshot struct {
	Source                 string             `json:"source"`
	InputPerMillion        float64            `json:"input_per_million"`
	OutputPerMillion       float64            `json:"output_per_million"`
	CacheReadPerMillion    float64            `json:"cache_read_per_million"`
	CacheWritePerMillion   float64            `json:"cache_write_per_million"`
	CacheWrite1hPerMillion float64            `json:"cache_write_1h_per_million,omitempty"`
	ServiceTier            config.ServiceTier `json:"service_tier,omitempty"`
	ServiceTierMultiplier  float64            `json:"service_tier_multiplier,omitempty"`
	InputTierAboveTokens   int64              `json:"input_tier_above_tokens,omitempty"`
}

// UsageSnapshotFromTokenUsage converts runtime usage into a persisted snapshot.
func UsageSnapshotFromTokenUsage(usage message.TokenUsage) UsageSnapshot {
	return UsageSnapshot{
		InputTokens:        int64(usage.InputTokens),
		OutputTokens:       int64(usage.OutputTokens),
		CacheReadTokens:    int64(usage.CacheReadTokens),
		CacheWriteTokens:   int64(usage.CacheWriteTokens),
		CacheWrite1hTokens: int64(usage.CacheWrite1hTokens),
		ReasoningTokens:    int64(usage.ReasoningTokens),
	}
}

// NormalizeBillingUsage converts raw provider usage into mutually-exclusive billing buckets.
func NormalizeBillingUsage(raw UsageSnapshot) BillingUsage {
	inputTokens := max(raw.InputTokens-raw.CacheReadTokens, 0)
	out := BillingUsage{
		InputTokens:        inputTokens,
		OutputTokens:       raw.OutputTokens,
		CacheReadTokens:    raw.CacheReadTokens,
		CacheWriteTokens:   raw.CacheWriteTokens,
		CacheWrite1hTokens: raw.CacheWrite1hTokens,
	}
	out.BillingTotalTokens = out.InputTokens + out.OutputTokens + out.CacheReadTokens + out.CacheWriteTokens
	return out
}

// CalculateUsageCost calculates event cost from normalized billing usage.
func CalculateUsageCost(cost *config.ModelCost, billing BillingUsage, tier config.ServiceTier) UsageCost {
	out := UsageCost{Currency: "USD"}
	if cost == nil {
		return out
	}
	resolved := cost.ResolvePricing(billing.InputTokens, tier)
	cacheWrite1hTokens := min(billing.CacheWrite1hTokens, billing.CacheWriteTokens)
	cacheWriteDefaultTokens := billing.CacheWriteTokens - cacheWrite1hTokens
	out.InputCost = float64(billing.InputTokens) / 1_000_000 * resolved.Input
	out.OutputCost = float64(billing.OutputTokens) / 1_000_000 * resolved.Output
	out.CacheReadCost = float64(billing.CacheReadTokens) / 1_000_000 * resolved.CacheRead
	out.CacheWriteCost = float64(cacheWriteDefaultTokens)/1_000_000*resolved.CacheWrite + float64(cacheWrite1hTokens)/1_000_000*resolved.CacheWrite1h
	out.TotalCost = out.InputCost + out.OutputCost + out.CacheReadCost + out.CacheWriteCost
	return out
}

// PricingSnapshotFromCost freezes the price config used for a usage event.
func PricingSnapshotFromCost(cost *config.ModelCost, billing BillingUsage, tier config.ServiceTier) PricingSnapshot {
	out := PricingSnapshot{Source: "config"}
	if cost == nil {
		return out
	}
	resolved := cost.ResolvePricing(billing.InputTokens, tier)
	out.InputPerMillion = resolved.Input
	out.OutputPerMillion = resolved.Output
	out.CacheReadPerMillion = resolved.CacheRead
	out.CacheWritePerMillion = resolved.CacheWrite
	out.CacheWrite1hPerMillion = resolved.CacheWrite1h
	out.ServiceTier = resolved.ServiceTier
	out.ServiceTierMultiplier = resolved.ServiceTierMultiplier
	out.InputTierAboveTokens = resolved.InputTierAboveTokens
	return out
}

// SplitModelRef splits a provider/model ref into its provider and model ID.
// Any inline @variant suffix is stripped before returning the model ID.
func SplitModelRef(ref string) (provider string, modelID string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", ""
	}
	ref = config.NormalizeModelRef(ref)
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) == 2 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	}
	return "", ref
}

// ProjectIDForPath returns a stable, path-based project ID.
func ProjectIDForPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:8])
}
