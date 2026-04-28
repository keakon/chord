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
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64 `json:"cache_write_tokens,omitempty"`
	ReasoningTokens  int64 `json:"reasoning_tokens,omitempty"`
}

func UsageSnapshotIsZero(usage UsageSnapshot) bool {
	return usage.InputTokens == 0 &&
		usage.OutputTokens == 0 &&
		usage.CacheReadTokens == 0 &&
		usage.CacheWriteTokens == 0 &&
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
	Source               string  `json:"source"`
	InputPerMillion      float64 `json:"input_per_million"`
	OutputPerMillion     float64 `json:"output_per_million"`
	CacheReadPerMillion  float64 `json:"cache_read_per_million"`
	CacheWritePerMillion float64 `json:"cache_write_per_million"`
}

// UsageSnapshotFromTokenUsage converts runtime usage into a persisted snapshot.
func UsageSnapshotFromTokenUsage(usage message.TokenUsage) UsageSnapshot {
	return UsageSnapshot{
		InputTokens:      int64(usage.InputTokens),
		OutputTokens:     int64(usage.OutputTokens),
		CacheReadTokens:  int64(usage.CacheReadTokens),
		CacheWriteTokens: int64(usage.CacheWriteTokens),
		ReasoningTokens:  int64(usage.ReasoningTokens),
	}
}

// NormalizeBillingUsage converts raw provider usage into mutually-exclusive billing buckets.
func NormalizeBillingUsage(raw UsageSnapshot) BillingUsage {
	inputTokens := raw.InputTokens - raw.CacheReadTokens
	if inputTokens < 0 {
		inputTokens = 0
	}
	out := BillingUsage{
		InputTokens:      inputTokens,
		OutputTokens:     raw.OutputTokens,
		CacheReadTokens:  raw.CacheReadTokens,
		CacheWriteTokens: raw.CacheWriteTokens,
	}
	out.BillingTotalTokens = out.InputTokens + out.OutputTokens + out.CacheReadTokens + out.CacheWriteTokens
	return out
}

// CalculateUsageCost calculates event cost from normalized billing usage.
func CalculateUsageCost(cost *config.ModelCost, billing BillingUsage) UsageCost {
	out := UsageCost{Currency: "USD"}
	if cost == nil {
		return out
	}
	out.InputCost = float64(billing.InputTokens) / 1_000_000 * cost.Input
	out.OutputCost = float64(billing.OutputTokens) / 1_000_000 * cost.Output
	out.CacheReadCost = float64(billing.CacheReadTokens) / 1_000_000 * cost.CacheRead
	out.CacheWriteCost = float64(billing.CacheWriteTokens) / 1_000_000 * cost.CacheWrite
	out.TotalCost = out.InputCost + out.OutputCost + out.CacheReadCost + out.CacheWriteCost
	return out
}

// PricingSnapshotFromCost freezes the price config used for a usage event.
func PricingSnapshotFromCost(cost *config.ModelCost) PricingSnapshot {
	out := PricingSnapshot{Source: "config"}
	if cost == nil {
		return out
	}
	out.InputPerMillion = cost.Input
	out.OutputPerMillion = cost.Output
	out.CacheReadPerMillion = cost.CacheRead
	out.CacheWritePerMillion = cost.CacheWrite
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
