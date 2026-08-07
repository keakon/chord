package analytics

import (
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestUsageSnapshotFromTokenUsageProducesMutuallyExclusiveBuckets(t *testing.T) {
	got := UsageSnapshotFromTokenUsage(message.TokenUsage{InputTokens: 30_002, CacheReadTokens: 30_000})
	if got.InputTokens != 2 || got.CacheReadTokens != 30_000 {
		t.Fatalf("UsageSnapshotFromTokenUsage() = %+v, want input=2 cache_read=30000", got)
	}
	billing := NormalizeBillingUsage(got)
	if billing.InputTokens != 2 || billing.CacheReadTokens != 30_000 || billing.BillingTotalTokens != 30_002 {
		t.Fatalf("NormalizeBillingUsage() = %+v, want input=2 cache_read=30000 total=30002", billing)
	}
}

func TestCalculateUsageCostUsesCacheWrite1hPrice(t *testing.T) {
	cost := &config.ModelCost{
		Input:        5,
		Output:       25,
		CacheRead:    0.5,
		CacheWrite:   6.25,
		CacheWrite1h: 10,
	}
	billing := NormalizeBillingUsage(UsageSnapshotFromTokenUsage(message.TokenUsage{
		InputTokens:        1_000_000,
		OutputTokens:       100_000,
		CacheReadTokens:    200_000,
		CacheWriteTokens:   300_000,
		CacheWrite1hTokens: 100_000,
	}))

	got := CalculateUsageCost(cost, billing, config.ServiceTierStandard)
	// Input billing excludes cache reads: 800k * $5 = $4.
	// Cache writes split into 200k 5m * $6.25 + 100k 1h * $10 = $2.25.
	want := 4.0 + 2.5 + 0.1 + 2.25
	if !almostEqual(got.TotalCost, want, 0.0001) {
		t.Fatalf("total cost = %.4f, want %.4f (%+v)", got.TotalCost, want, got)
	}
	if !almostEqual(got.CacheWriteCost, 2.25, 0.0001) {
		t.Fatalf("cache write cost = %.4f, want 2.25", got.CacheWriteCost)
	}

	snapshot := PricingSnapshotFromCost(cost, billing, config.ServiceTierStandard)
	if snapshot.CacheWritePerMillion != 6.25 || snapshot.CacheWrite1hPerMillion != 10 {
		t.Fatalf("unexpected cache write pricing snapshot: %+v", snapshot)
	}
}
