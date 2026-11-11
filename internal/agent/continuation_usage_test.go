package agent

import (
	"testing"
	"time"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/contextdiag"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func TestContinuationUsagePinsLedgerAndIgnoresLateUI(t *testing.T) {
	oldDir := t.TempDir()
	oldLedger := analytics.NewUsageLedger(oldDir, "")
	recorder := newWalltimeRecorder(oldLedger, nil, nil)
	target := recorder.captureAt("worker-1", "worker", 7)
	agent := &MainAgent{walltime: recorder, eventCh: make(chan Event, 4), usageTracker: analytics.NewUsageTracker()}
	var visible []analytics.UsageEvent
	agent.usageEventSink = func(event analytics.UsageEvent) { visible = append(visible, event) }
	source := &config.ModelCost{Input: 1, Output: 2}
	provider := llm.NewProviderConfig("sample", config.ProviderConfig{Models: map[string]config.ModelConfig{"model-1": {Cost: source}}}, nil)
	client := llm.NewClient(provider, nil, "model-1", 100, "")
	observer := agent.continuationUsageObserver(client, target, usageAttribution{AgentID: "worker-1", AgentKind: agentKindSub, AgentName: "worker", TaskID: "task-1", Purpose: analytics.UsagePurposeChat, SelectedModelRef: "sample/model-1", TurnID: 7})
	source.Input = 99
	observer(llm.ContinuationUsage{ChainID: "chain", Request: 1, ModelRef: "sample/model-1", Outcome: "checkpoint", UsageState: contextdiag.Partial, Usage: &message.TokenUsage{InputTokens: 1000000}})
	first := (<-agent.eventCh).Payload.(continuationUsagePayload)
	if first.event.Cost.TotalCost != 1 || first.event.Diagnostic["usage_state"] != contextdiag.Partial {
		t.Fatalf("receipt=%+v", first)
	}
	if first.event.TaskID != "task-1" || first.event.AgentID != "worker-1" || first.event.TurnID != 7 {
		t.Fatalf("continuation attribution = %+v", first.event)
	}
	agent.handleContinuationUsage(first)
	if len(visible) != 1 {
		t.Fatal("missing current receipt")
	}
	newDir := t.TempDir()
	newLedger := analytics.NewUsageLedger(newDir, "")
	recorder.repointLedger(newLedger)
	observer(llm.ContinuationUsage{ChainID: "chain", Request: 2, ModelRef: "sample/model-1", Outcome: "checkpoint", UsageState: contextdiag.Partial, Usage: &message.TokenUsage{InputTokens: 100}})
	second := (<-agent.eventCh).Payload.(continuationUsagePayload)

	agent.handleContinuationUsage(second)
	if len(visible) != 1 {
		t.Fatal("late receipt modified new session")
	}
	summary, err := analytics.LoadSessionUsageSummary(oldDir)
	if err != nil {
		t.Fatal(err)
	}
	if summary == nil || summary.EventCount != 2 || summary.LastEventID != "continuation:chain:2" {
		t.Fatalf("old ledger lost receipts: %+v", summary)
	}
	current, err := newLedger.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if current != nil && current.EventCount != 0 {
		t.Fatalf("late receipt reached new ledger: %+v", current)
	}
}

func TestContinuationUsageCarriesHopTiming(t *testing.T) {
	oldDir := t.TempDir()
	oldLedger := analytics.NewUsageLedger(oldDir, "")
	recorder := newWalltimeRecorder(oldLedger, nil, nil)
	target := recorder.captureAt("worker-1", "worker", 7)
	agent := &MainAgent{walltime: recorder, eventCh: make(chan Event, 4), usageTracker: analytics.NewUsageTracker()}
	provider := llm.NewProviderConfig("sample", config.ProviderConfig{Models: map[string]config.ModelConfig{"model-1": {Cost: &config.ModelCost{Input: 1, Output: 2}}}}, nil)
	client := llm.NewClient(provider, nil, "model-1", 100, "")
	observer := agent.continuationUsageObserver(client, target, usageAttribution{AgentID: "worker-1", AgentKind: agentKindSub, AgentName: "worker", TaskID: "task-1", Purpose: analytics.UsagePurposeChat, SelectedModelRef: "sample/model-1", TurnID: 7})

	hopNs := int64(2 * time.Second)
	latencyNs := int64(300 * time.Millisecond)
	observer(llm.ContinuationUsage{ChainID: "chain", Request: 1, ModelRef: "sample/model-1", Outcome: "completed", UsageState: contextdiag.Reported, Usage: &message.TokenUsage{InputTokens: 100, OutputTokens: 50}, RequestNs: hopNs, FirstOutputNs: latencyNs})
	payload := (<-agent.eventCh).Payload.(continuationUsagePayload)
	if payload.event.RequestNs != hopNs {
		t.Fatalf("event request ns = %d, want %d", payload.event.RequestNs, hopNs)
	}
	if payload.event.FirstOutputNs != latencyNs {
		t.Fatalf("event first output ns = %d, want %d", payload.event.FirstOutputNs, latencyNs)
	}

	agent.handleContinuationUsage(payload)
	// The timing rides the event, but the worker is a sub agent, so the
	// speed-sample gate keeps it out of the per-model throughput columns.
	stats := agent.usageTracker.SessionStats().ByModel["sample/model-1"]
	if stats == nil || stats.ThroughputSamples != 0 || stats.FirstOutputSamples != 0 {
		t.Fatalf("model speed stats = %+v, want no samples for a sub agent receipt", stats)
	}
	if stats.OutputTokens != 50 {
		t.Fatalf("model output tokens = %d, want 50", stats.OutputTokens)
	}
}
