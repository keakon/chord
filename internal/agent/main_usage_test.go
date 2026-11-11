package agent

import (
	"testing"
	"time"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/identity"
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
		usageAttribution{
			AgentID:          "sub-1",
			AgentKind:        agentKindSub,
			AgentName:        "worker",
			TaskID:           "task-1",
			Purpose:          analytics.UsagePurposeChat,
			TurnID:           42,
			SelectedModelRef: "provider/model",
			RunningModelRef:  "provider/model",
		},
		&message.TokenUsage{InputTokens: 1_000_000, OutputTokens: 1_000_000},
		config.ServiceTierFast,
		nil,
		usageTiming{},
	)

	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1", len(events))
	}
	event := events[0]
	if event.TaskID != "task-1" {
		t.Fatalf("task id = %q, want task-1", event.TaskID)
	}
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

func TestRecordUsageCarriesRequestTiming(t *testing.T) {
	a := &MainAgent{}
	var events []analytics.UsageEvent
	a.SetUsageEventSink(func(event analytics.UsageEvent) {
		events = append(events, event)
	})

	a.recordUsage(
		usageAttribution{
			AgentID:          identity.MainAgentID,
			AgentKind:        identity.MainAgentID,
			AgentName:        "builder",
			Purpose:          analytics.UsagePurposeChat,
			SelectedModelRef: "provider/model-1",
			RunningModelRef:  "provider/model-1",
		},
		&message.TokenUsage{InputTokens: 100, OutputTokens: 50},
		"",
		nil,
		usageTiming{RequestNs: int64(time.Second), FirstOutputNs: int64(300 * time.Millisecond)},
	)

	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1", len(events))
	}
	event := events[0]
	if event.RequestNs != int64(time.Second) {
		t.Fatalf("request ns = %d, want 1s", event.RequestNs)
	}
	if event.FirstOutputNs != int64(300*time.Millisecond) {
		t.Fatalf("first output ns = %d, want 300ms", event.FirstOutputNs)
	}
}

func TestResponseUsageTimingFromResponse(t *testing.T) {
	if got := responseUsageTiming(nil); got != (usageTiming{}) {
		t.Fatalf("nil response timing = %+v, want zero", got)
	}
	resp := &message.Response{RequestNs: 1, FirstOutputNs: 2}
	if got := responseUsageTiming(resp); got.RequestNs != 1 || got.FirstOutputNs != 2 {
		t.Fatalf("response timing = %+v, want (1, 2)", got)
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
