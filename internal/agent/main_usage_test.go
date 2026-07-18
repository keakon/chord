package agent

import (
	"testing"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
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
