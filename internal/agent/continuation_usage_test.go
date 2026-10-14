package agent

import (
	"testing"

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
	observer := agent.continuationUsageObserver(client, target, "sub", "worker", "chat", "sample/model-1", 7)
	source.Input = 99
	observer(llm.ContinuationUsage{ChainID: "chain", Request: 1, ModelRef: "sample/model-1", Outcome: "checkpoint", UsageState: contextdiag.Partial, Usage: &message.TokenUsage{InputTokens: 1000000}})
	first := (<-agent.eventCh).Payload.(continuationUsagePayload)
	if first.event.Cost.TotalCost != 1 || first.event.Diagnostic["usage_state"] != contextdiag.Partial {
		t.Fatalf("receipt=%+v", first)
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
