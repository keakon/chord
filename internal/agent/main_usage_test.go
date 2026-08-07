package agent

import (
	"testing"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestRecordUsageUsesProvidedServiceTier(t *testing.T) {
	a := &MainAgent{
		projectConfig: &config.Config{Providers: map[string]config.ProviderConfig{
			"provider": {
				Models: map[string]config.ModelConfig{
					"model": {
						Cost: &config.ModelCost{
							Input:                  1,
							Output:                 2,
							ServiceTierMultipliers: &config.ServiceTierMultipliers{Fast: 2},
						},
					},
				},
			},
		}},
	}
	var events []analytics.UsageEvent
	a.SetUsageEventSink(func(event analytics.UsageEvent) {
		events = append(events, event)
	})

	a.recordUsage(
		"sub-1",
		"sub",
		"worker",
		"chat",
		"provider/model",
		"provider/model",
		42,
		&message.TokenUsage{InputTokens: 1_000_000, OutputTokens: 1_000_000},
		config.ServiceTierFast,
		nil,
	)

	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1", len(events))
	}
	event := events[0]
	if event.PricingSnapshot.ServiceTier != config.ServiceTierFast {
		t.Fatalf("service tier = %q, want %q", event.PricingSnapshot.ServiceTier, config.ServiceTierFast)
	}
	if event.PricingSnapshot.ServiceTierMultiplier != 2 {
		t.Fatalf("service tier multiplier = %v, want 2", event.PricingSnapshot.ServiceTierMultiplier)
	}
	if event.Cost.TotalCost != 6 {
		t.Fatalf("total cost = %v, want 6", event.Cost.TotalCost)
	}
}

func TestContextReductionDiagnosticForTurn(t *testing.T) {
	a := &MainAgent{}
	a.lastPreparedLLMTurnID = 7
	a.lastPreparedReductionStats = ContextReductionStats{
		Messages:     2,
		Bytes:        1200,
		TokensSaved:  300,
		CurrentBytes: 8000,
		ByToolAndRule: map[string]ContextReductionBucket{
			tools.NameShell + "/diff": {Messages: 1},
		},
		OverCompression: map[string]int{
			contextReductionOverCompressionRereadSameRevision: 1,
		},
	}
	d := a.contextReductionDiagnosticForTurn(7)
	if d["reduction_messages"] != "2" || d["reduction_bytes"] != "1200" || d["reduction_tokens_saved"] != "300" {
		t.Fatalf("unexpected reduction diagnostic: %+v", d)
	}
	if d["overcompression."+contextReductionOverCompressionRereadSameRevision] != "1" {
		t.Fatalf("missing over-compression diagnostic: %+v", d)
	}
	if d["reduction_rule."+tools.NameShell+"/diff"] != "1" {
		t.Fatalf("missing reduction rule diagnostic: %+v", d)
	}
}
