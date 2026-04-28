package agent

import (
	"fmt"
	"testing"

	"github.com/keakon/chord/internal/analytics"
)

func TestCompactionPolicyAnalyticsEventsRecordedInTrackerAndLedger(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetProviderModelRef("p/current")
	a.llmMu.Lock()
	a.runningModelRef = "p/current"
	a.llmMu.Unlock()

	var events []analytics.UsageEvent
	a.SetUsageEventSink(func(event analytics.UsageEvent) {
		events = append(events, event)
	})

	a.recordUsageDrivenCompactionFailure(fmt.Errorf("first failure"))
	if len(events) != 0 {
		t.Fatalf("events after first failure = %d, want 0", len(events))
	}
	a.recordUsageDrivenCompactionFailure(fmt.Errorf("second failure"))
	if len(events) != 1 {
		t.Fatalf("events after breaker trip = %d, want 1", len(events))
	}
	if events[0].Purpose != compactionPolicyAnalyticsPurpose+"/breaker_trip" {
		t.Fatalf("purpose = %q, want %q", events[0].Purpose, compactionPolicyAnalyticsPurpose+"/breaker_trip")
	}

	stats := a.GetUsageStats()
	if len(stats.ByAgent) == 0 {
		t.Fatalf("ByAgent stats empty; stats=%+v", stats)
	}
	agg := stats.ByAgent["main"]
	if agg == nil {
		t.Fatal("expected main agent stats entry")
	}
	if got := agg.LLMCalls; got != 1 {
		t.Fatalf("main agent compaction policy calls = %d, want 1", got)
	}

	summary, err := a.usageLedger.Summary()
	if err != nil {
		t.Fatalf("usageLedger.Summary(): %v", err)
	}
	aggByPurpose, ok := summary.ByPurpose[compactionPolicyAnalyticsPurpose+"/breaker_trip"]
	if !ok || aggByPurpose == nil {
		t.Fatalf("missing breaker_trip summary entry; summary=%+v", summary)
	}
	if got := aggByPurpose.LLMCalls; got != 1 {
		t.Fatalf("summary.ByPurpose[%q].LLMCalls = %d, want 1", compactionPolicyAnalyticsPurpose+"/breaker_trip", got)
	}
}

func TestCompactionFailureAnalyticsEventRecordedInTrackerAndLedger(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetProviderModelRef("qt/gpt-5.5")
	a.llmMu.Lock()
	a.runningModelRef = "qt/gpt-5.5"
	a.llmMu.Unlock()
	a.compactionState.trigger = compactionTrigger{UsageDriven: true}

	var events []analytics.UsageEvent
	a.SetUsageEventSink(func(event analytics.UsageEvent) {
		events = append(events, event)
	})

	errExample := fmt.Errorf("compaction prompt still exceeds reserved context budget")
	class := classifyCompactionFailure(errExample)
	a.recordCompactionFailureAnalyticsEvent(errExample, class, "async")

	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	evt := events[0]
	if evt.Purpose != compactionFailureAnalyticsPurpose {
		t.Fatalf("purpose = %q, want %q", evt.Purpose, compactionFailureAnalyticsPurpose)
	}
	if got := evt.Diagnostic["class"]; got != string(compactionFailureStructural) {
		t.Fatalf("diagnostic class = %q, want %q", got, compactionFailureStructural)
	}
	if got := evt.Diagnostic["stage"]; got != "async" {
		t.Fatalf("diagnostic stage = %q, want async", got)
	}
	if got := evt.Diagnostic["trigger"]; got != "usage_driven" {
		t.Fatalf("diagnostic trigger = %q, want usage_driven", got)
	}
	if got := evt.Diagnostic["reason"]; got == "" {
		t.Fatal("diagnostic reason should not be empty")
	}

	summary, err := a.usageLedger.Summary()
	if err != nil {
		t.Fatalf("usageLedger.Summary(): %v", err)
	}
	aggByPurpose, ok := summary.ByPurpose[compactionFailureAnalyticsPurpose]
	if !ok || aggByPurpose == nil {
		t.Fatalf("missing %q summary entry; summary=%+v", compactionFailureAnalyticsPurpose, summary)
	}
	if got := aggByPurpose.LLMCalls; got != 1 {
		t.Fatalf("summary.ByPurpose[%q].LLMCalls = %d, want 1", compactionFailureAnalyticsPurpose, got)
	}
}

func TestCompactionFailureAnalyticsEventMarksLengthRecoveryTrigger(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.compactionState.trigger = compactionTrigger{LengthRecovery: true}

	var events []analytics.UsageEvent
	a.SetUsageEventSink(func(event analytics.UsageEvent) {
		events = append(events, event)
	})

	errExample := fmt.Errorf("compaction prompt still exceeds reserved context budget")
	class := classifyCompactionFailure(errExample)
	a.recordCompactionFailureAnalyticsEvent(errExample, class, "async")

	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if got := events[0].Diagnostic["trigger"]; got != "length_recovery_driven" {
		t.Fatalf("diagnostic trigger = %q, want length_recovery_driven", got)
	}
}
