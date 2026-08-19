package agent

import (
	"fmt"
	"testing"

	"github.com/keakon/chord/internal/analytics"
)

func TestContextDiagnosticEventsCarryStructuredResults(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	var events []analytics.UsageEvent
	a.SetUsageEventSink(func(event analytics.UsageEvent) { events = append(events, event) })

	a.recordCompactionProvenanceEvent("success", map[string]string{"source_ref_count": "3", "duration_us": "12"})
	a.recordCompactionLifecycleEvent("applied", map[string]string{"message_count": "5"})

	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	if events[0].Purpose != compactionProvenanceAnalyticsPurpose || events[0].Diagnostic["result"] != "success" || events[0].Diagnostic["source_ref_count"] != "3" {
		t.Fatalf("provenance event = %#v", events[0])
	}
	if events[1].Purpose != compactionLifecycleAnalyticsPurpose || events[1].Diagnostic["stage"] != "applied" {
		t.Fatalf("lifecycle event = %#v", events[1])
	}
}

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

	a.recordUsageDrivenCompactionFailureClassified(fmt.Errorf("first failure"), classifyCompactionFailure(fmt.Errorf("first failure")))
	if len(events) != 0 {
		t.Fatalf("events after first failure = %d, want 0", len(events))
	}
	a.recordUsageDrivenCompactionFailureClassified(fmt.Errorf("second failure"), classifyCompactionFailure(fmt.Errorf("second failure")))
	if len(events) != 1 {
		t.Fatalf("events after breaker trip = %d, want 1", len(events))
	}
	if events[0].Purpose != compactionPolicyAnalyticsPurpose+"/breaker_trip" {
		t.Fatalf("purpose = %q, want %q", events[0].Purpose, compactionPolicyAnalyticsPurpose+"/breaker_trip")
	}

	// Diagnostic events land in the durable event log but are not LLM calls:
	// runtime stats and summary aggregates must not count them.
	stats := a.GetUsageStats()
	if stats.LLMCalls != 0 || len(stats.ByAgent) != 0 {
		t.Fatalf("runtime stats counted a diagnostic event; stats=%+v", stats)
	}

	summary, err := a.usageLedger.Summary()
	if err != nil {
		t.Fatalf("usageLedger.Summary(): %v", err)
	}
	if summary.EventCount != 1 {
		t.Fatalf("summary.EventCount = %d, want 1 (event persisted)", summary.EventCount)
	}
	if summary.UsageTotal.LLMCalls != 0 {
		t.Fatalf("summary.UsageTotal.LLMCalls = %d, want 0", summary.UsageTotal.LLMCalls)
	}
	if _, ok := summary.ByPurpose[compactionPolicyAnalyticsPurpose+"/breaker_trip"]; ok {
		t.Fatalf("diagnostic purpose must stay out of ByPurpose aggregates; summary=%+v", summary)
	}
}

func TestCompactionFailureAnalyticsEventRecordedInTrackerAndLedger(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetProviderModelRef("sample/gpt-5.5")
	a.llmMu.Lock()
	a.runningModelRef = "sample/gpt-5.5"
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
	if summary.EventCount != 1 {
		t.Fatalf("summary.EventCount = %d, want 1 (event persisted)", summary.EventCount)
	}
	if summary.UsageTotal.LLMCalls != 0 {
		t.Fatalf("summary.UsageTotal.LLMCalls = %d, want 0 (diagnostic events are not calls)", summary.UsageTotal.LLMCalls)
	}
}

func TestOversizeRecoveryAnalyticsEventRecordedInTrackerAndLedger(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetProviderModelRef("sample/gpt-5.5")
	a.llmMu.Lock()
	a.runningModelRef = "sample/gpt-5.5"
	a.llmMu.Unlock()

	var events []analytics.UsageEvent
	a.SetUsageEventSink(func(event analytics.UsageEvent) {
		events = append(events, event)
	})

	a.recordOversizeRecoveryAnalyticsEvent("trigger_compaction", "main_llm_error", "sample/gpt-5.5", "fallback/gpt-5.5", map[string]string{"trigger": "oversize_driven"})

	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	evt := events[0]
	if evt.Purpose != oversizeRecoveryAnalyticsPurpose+"/trigger_compaction" {
		t.Fatalf("purpose = %q, want %q", evt.Purpose, oversizeRecoveryAnalyticsPurpose+"/trigger_compaction")
	}
	if got := evt.Diagnostic["reason"]; got != "context_length_exceeded" {
		t.Fatalf("diagnostic reason = %q, want context_length_exceeded", got)
	}
	if got := evt.Diagnostic["stage"]; got != "main_llm_error" {
		t.Fatalf("diagnostic stage = %q, want main_llm_error", got)
	}
	if got := evt.Diagnostic["trigger"]; got != "oversize_driven" {
		t.Fatalf("diagnostic trigger = %q, want oversize_driven", got)
	}

	summary, err := a.usageLedger.Summary()
	if err != nil {
		t.Fatalf("usageLedger.Summary(): %v", err)
	}
	if summary.EventCount != 1 {
		t.Fatalf("summary.EventCount = %d, want 1 (event persisted)", summary.EventCount)
	}
	if summary.UsageTotal.LLMCalls != 0 {
		t.Fatalf("summary.UsageTotal.LLMCalls = %d, want 0 (diagnostic events are not calls)", summary.UsageTotal.LLMCalls)
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
