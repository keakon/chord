package analytics

import (
	"testing"
	"time"

	"github.com/keakon/chord/internal/identity"
)

const speedTestModel = "provider/model-1"

func speedSampleEvent(model string, outputTokens, requestNs, firstOutputNs int64) UsageEvent {
	return UsageEvent{
		EventID:          "evt-" + model,
		AgentID:          identity.MainAgentID,
		AgentKind:        identity.MainAgentID,
		Purpose:          UsagePurposeChat,
		SelectedModelRef: model,
		RunningModelRef:  model,
		UsageRaw:         UsageSnapshot{InputTokens: 100, OutputTokens: outputTokens},
		BillingUsage:     BillingUsage{InputTokens: 100, BillingTotalTokens: 100 + outputTokens},
		RequestNs:        requestNs,
		FirstOutputNs:    firstOutputNs,
	}
}

const (
	secondNs = int64(time.Second)
	latencyA = int64(300 * time.Millisecond)
	latencyB = int64(500 * time.Millisecond)
)

func TestSpeedSamplesFoldIntoModelStats(t *testing.T) {
	tracker := NewUsageTracker()
	tracker.AddUsageEvent(speedSampleEvent(speedTestModel, 100, secondNs, latencyA))
	tracker.AddUsageEvent(speedSampleEvent(speedTestModel, 100, 3*secondNs, latencyB))

	ms := tracker.SessionStats().ByModel[speedTestModel]
	if ms == nil {
		t.Fatal("missing model stats")
	}
	if ms.ThroughputTokens != 200 || ms.ThroughputNs != 4*secondNs || ms.ThroughputSamples != 2 {
		t.Fatalf("throughput = (%d, %d, %d), want (200, 4s, 2)", ms.ThroughputTokens, ms.ThroughputNs, ms.ThroughputSamples)
	}
	if ms.FirstOutputTotalNs != latencyA+latencyB || ms.FirstOutputSamples != 2 {
		t.Fatalf("first output = (%d, %d), want (800ms, 2)", ms.FirstOutputTotalNs, ms.FirstOutputSamples)
	}
	// The all-purpose accumulators still span every event.
	if ms.OutputTokens != 200 || ms.Calls != 2 {
		t.Fatalf("calls/output = (%d, %d), want (2, 200)", ms.Calls, ms.OutputTokens)
	}
}

func TestSpeedSampleGateExcludesSubAgentAndAuxiliaryPurposes(t *testing.T) {
	tracker := NewUsageTracker()
	tracker.AddUsageEvent(speedSampleEvent(speedTestModel, 100, secondNs, latencyA))

	sub := speedSampleEvent(speedTestModel, 500, secondNs, latencyB)
	sub.AgentID = "sub-1"
	tracker.AddUsageEvent(sub)

	compaction := speedSampleEvent(speedTestModel, 700, secondNs, latencyB)
	compaction.Purpose = UsagePurposeCompaction
	tracker.AddUsageEvent(compaction)

	// Pre-timing historical event: chat + main but no measured request.
	tracker.AddUsageEvent(speedSampleEvent(speedTestModel, 900, 0, 0))

	ms := tracker.SessionStats().ByModel[speedTestModel]
	if ms.ThroughputSamples != 1 || ms.ThroughputTokens != 100 || ms.ThroughputNs != secondNs {
		t.Fatalf("throughput = (%d, %d, %d), want only the main chat sample", ms.ThroughputTokens, ms.ThroughputNs, ms.ThroughputSamples)
	}
	if ms.FirstOutputSamples != 1 || ms.FirstOutputTotalNs != latencyA {
		t.Fatalf("first output = (%d, %d), want only the main chat sample", ms.FirstOutputTotalNs, ms.FirstOutputSamples)
	}
	// Tokens still accumulate across every event.
	if ms.OutputTokens != 100+500+700+900 || ms.Calls != 4 {
		t.Fatalf("calls/output = (%d, %d), want (4, 2200)", ms.Calls, ms.OutputTokens)
	}
}

func TestSpeedSampleRequiresUsableOutputTokens(t *testing.T) {
	tracker := NewUsageTracker()
	tracker.AddUsageEvent(speedSampleEvent(speedTestModel, 0, secondNs, latencyA))

	ms := tracker.SessionStats().ByModel[speedTestModel]
	if ms.ThroughputSamples != 0 || ms.ThroughputTokens != 0 || ms.ThroughputNs != 0 {
		t.Fatalf("throughput = (%d, %d, %d), want no sample without output tokens", ms.ThroughputTokens, ms.ThroughputNs, ms.ThroughputSamples)
	}
	// The latency sample does not depend on usage counts.
	if ms.FirstOutputSamples != 1 || ms.FirstOutputTotalNs != latencyA {
		t.Fatalf("first output = (%d, %d), want one latency sample", ms.FirstOutputTotalNs, ms.FirstOutputSamples)
	}
}

func TestUsageSummaryFoldsSpeedSamples(t *testing.T) {
	summary := &SessionUsageSummary{
		ByProvider:     map[string]*UsageAggregate{},
		ByModelRef:     map[string]*UsageAggregate{},
		ByAgent:        map[string]*UsageAggregate{},
		ByPurpose:      map[string]*UsageAggregate{},
		ByDate:         map[string]*UsageAggregate{},
		ByDateModelRef: map[string]map[string]*UsageAggregate{},
		ByDateAgent:    map[string]map[string]*UsageAggregate{},
	}
	applyUsageEvent(summary, speedSampleEvent(speedTestModel, 100, secondNs, latencyA))
	applyUsageEvent(summary, speedSampleEvent(speedTestModel, 100, 3*secondNs, latencyB))

	sub := speedSampleEvent(speedTestModel, 500, secondNs, latencyB)
	sub.AgentID = "sub-1"
	applyUsageEvent(summary, sub)

	compaction := speedSampleEvent(speedTestModel, 700, secondNs, latencyB)
	compaction.Purpose = UsagePurposeCompaction
	applyUsageEvent(summary, compaction)

	if summary.UsageTotal.ThroughputTokens != 200 || summary.UsageTotal.ThroughputSamples != 2 {
		t.Fatalf("total throughput = (%d, %d), want (200, 2)", summary.UsageTotal.ThroughputTokens, summary.UsageTotal.ThroughputSamples)
	}
	byModel := summary.ByModelRef[speedTestModel]
	if byModel == nil || byModel.ThroughputSamples != 2 || byModel.ThroughputNs != 4*secondNs {
		t.Fatalf("model throughput = %+v, want two samples", byModel)
	}
	byAgent := summary.ByAgent[identity.MainAgentID]
	if byAgent == nil || byAgent.FirstOutputSamples != 2 {
		t.Fatalf("main agent aggregate = %+v, want two latency samples", byAgent)
	}
	if subAgent := summary.ByAgent["sub-1"]; subAgent == nil || subAgent.ThroughputSamples != 0 {
		t.Fatalf("sub agent aggregate = %+v, want no speed samples", subAgent)
	}
	if byPurpose := summary.ByPurpose[UsagePurposeCompaction]; byPurpose == nil || byPurpose.ThroughputSamples != 0 {
		t.Fatalf("compaction purpose aggregate = %+v, want no speed samples", byPurpose)
	}
}

func TestSliceSummaryForRangeKeepsSpeedSamples(t *testing.T) {
	summary := &SessionUsageSummary{
		UsageTotal: UsageAggregate{LLMCalls: 2, ThroughputTokens: 500, ThroughputNs: 3 * secondNs, ThroughputSamples: 2, FirstOutputTotalNs: latencyA, FirstOutputSamples: 1},
		ByDate: map[string]*UsageAggregate{
			"2026-11-12": {LLMCalls: 1, ThroughputTokens: 100, ThroughputNs: secondNs, ThroughputSamples: 1, FirstOutputTotalNs: latencyA, FirstOutputSamples: 1},
			"2026-10-01": {LLMCalls: 1, ThroughputTokens: 400, ThroughputNs: 2 * secondNs, ThroughputSamples: 1},
		},
		ByDateModelRef: map[string]map[string]*UsageAggregate{
			"2026-11-12": {speedTestModel: {LLMCalls: 1, ThroughputTokens: 100, ThroughputNs: secondNs, ThroughputSamples: 1, FirstOutputTotalNs: latencyA, FirstOutputSamples: 1}},
			"2026-10-01": {speedTestModel: {LLMCalls: 1, ThroughputTokens: 400, ThroughputNs: 2 * secondNs, ThroughputSamples: 1}},
		},
	}

	sliced, _, _ := sliceSummaryForRange(summary, StatsRangeLast7D, "2026-11-06", true)
	if sliced == nil {
		t.Fatal("expected a non-empty slice")
	}
	if sliced.UsageTotal.ThroughputTokens != 100 || sliced.UsageTotal.ThroughputSamples != 1 {
		t.Fatalf("sliced total = (%d, %d), want only the in-range sample", sliced.UsageTotal.ThroughputTokens, sliced.UsageTotal.ThroughputSamples)
	}
	byModel := sliced.ByModelRef[speedTestModel]
	if byModel == nil || byModel.ThroughputNs != secondNs || byModel.FirstOutputSamples != 1 {
		t.Fatalf("sliced model aggregate = %+v, want the in-range samples", byModel)
	}

	allTime, _, _ := sliceSummaryForRange(summary, StatsRangeAllTime, "", false)
	if allTime.UsageTotal.ThroughputTokens != 500 || allTime.UsageTotal.ThroughputSamples != 2 {
		t.Fatalf("all-time total = (%d, %d), want both samples", allTime.UsageTotal.ThroughputTokens, allTime.UsageTotal.ThroughputSamples)
	}
}

func TestMergeUsageAggregateCarriesSpeedSums(t *testing.T) {
	dst := &UsageAggregate{ThroughputTokens: 100, ThroughputNs: secondNs, ThroughputSamples: 1, FirstOutputTotalNs: latencyA, FirstOutputSamples: 1}
	mergeUsageAggregate(dst, &UsageAggregate{ThroughputTokens: 50, ThroughputNs: secondNs, ThroughputSamples: 1, FirstOutputTotalNs: latencyB, FirstOutputSamples: 1})
	if dst.ThroughputTokens != 150 || dst.ThroughputNs != 2*secondNs || dst.ThroughputSamples != 2 || dst.FirstOutputTotalNs != latencyA+latencyB || dst.FirstOutputSamples != 2 {
		t.Fatalf("merged = %+v", dst)
	}
}
