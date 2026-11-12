package analytics

import (
	"reflect"
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

func TestSpeedSampleScopeSeparatesSessionAndAgent(t *testing.T) {
	for _, taskID := range []string{"", "task-1"} {
		t.Run("task="+taskID, func(t *testing.T) {
			tracker := NewUsageTracker()
			tracker.AddUsageEvent(speedSampleEvent(speedTestModel, 100, secondNs, latencyA))

			sub := speedSampleEvent(speedTestModel, 500, secondNs, latencyB)
			sub.AgentID = "sub-1"
			sub.TaskID = taskID
			tracker.AddUsageEvent(sub)

			compaction := speedSampleEvent(speedTestModel, 700, secondNs, latencyB)
			compaction.Purpose = UsagePurposeCompaction
			tracker.AddUsageEvent(compaction)
			auxiliary := sub
			auxiliary.Purpose = UsagePurposeCompaction
			tracker.AddUsageEvent(auxiliary)

			// Pre-timing historical event: chat + main but no measured request.
			tracker.AddUsageEvent(speedSampleEvent(speedTestModel, 900, 0, 0))

			stats := tracker.SessionStats()
			ms := stats.ByModel[speedTestModel]
			if ms.ThroughputSamples != 1 || ms.ThroughputTokens != 100 || ms.ThroughputNs != secondNs {
				t.Fatalf("session throughput = %+v, want only the main chat sample", ms)
			}
			if ms.FirstOutputSamples != 1 || ms.FirstOutputTotalNs != latencyA {
				t.Fatalf("session first output = %+v, want only the main chat sample", ms)
			}
			if ms.OutputTokens != 100+500+700+500+900 || ms.Calls != 5 {
				t.Fatalf("calls/output = (%d, %d), want (5, 2700)", ms.Calls, ms.OutputTokens)
			}
			worker := tracker.SessionStatsForAgent(sub.AgentID).ByModel[speedTestModel]
			if worker.ThroughputTokens != 500 || worker.ThroughputNs != secondNs || worker.ThroughputSamples != 1 ||
				worker.FirstOutputTotalNs != latencyB || worker.FirstOutputSamples != 1 {
				t.Fatalf("worker speed = %+v, want only its own chat sample", worker)
			}
			partition := stats.Untracked
			if taskID != "" {
				partition = stats.ByTask[taskID]
			}
			if got := partition.ByModel[speedTestModel]; *got != *worker {
				t.Fatalf("partition model = %+v, want worker model %+v", got, worker)
			}
		})
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
	if subAgent := summary.ByAgent["sub-1"]; subAgent == nil || subAgent.ThroughputSamples != 0 ||
		subAgent.ThroughputTokens != 0 || subAgent.ThroughputNs != 0 ||
		subAgent.FirstOutputTotalNs != 0 || subAgent.FirstOutputSamples != 0 ||
		subAgent.LLMCalls != 1 || subAgent.OutputTokens != 500 {
		t.Fatalf("sub agent aggregate = %+v, want billed usage without speed samples", subAgent)
	}
	if byPurpose := summary.ByPurpose[UsagePurposeCompaction]; byPurpose == nil || byPurpose.ThroughputSamples != 0 {
		t.Fatalf("compaction purpose aggregate = %+v, want no speed samples", byPurpose)
	}
	if byPurpose := summary.ByPurpose[UsagePurposeChat]; byPurpose == nil || byPurpose.ThroughputTokens != 200 {
		t.Fatalf("chat purpose aggregate = %+v, want only main speed samples", byPurpose)
	}
	byDate := summary.ByDate["unknown"]
	byDateModel := summary.ByDateModelRef["unknown"][speedTestModel]
	if byDate.ThroughputTokens != 200 || byDateModel.ThroughputTokens != 200 {
		t.Fatalf("date/model speed = %+v / %+v, want only main speed samples", byDate, byDateModel)
	}
	if byDateAgent := summary.ByDateAgent["unknown"]["sub-1"]; byDateAgent.ThroughputTokens != 0 ||
		byDateAgent.ThroughputNs != 0 || byDateAgent.ThroughputSamples != 0 ||
		byDateAgent.FirstOutputTotalNs != 0 || byDateAgent.FirstOutputSamples != 0 ||
		byDateAgent.LLMCalls != 1 || byDateAgent.OutputTokens != 500 {
		t.Fatalf("date/agent aggregate = %+v, want billed usage without speed samples", byDateAgent)
	}
}

func TestUsageSummaryAndTrackerAgreeOnSpeedSampleEligibility(t *testing.T) {
	for _, tc := range []struct {
		name       string
		agentID    string
		purpose    string
		output     int64
		requestNs  int64
		firstNs    int64
		throughput bool
		latency    bool
	}{
		{"chat", identity.MainAgentID, UsagePurposeChat, 100, secondNs, latencyA, true, true},
		{"implicit main", "", UsagePurposeChat, 100, secondNs, latencyA, true, true},
		{"missing usage", identity.MainAgentID, UsagePurposeChat, 0, secondNs, latencyA, false, true},
		{"missing duration", identity.MainAgentID, UsagePurposeChat, 100, 0, latencyA, false, true},
		{"missing latency", identity.MainAgentID, UsagePurposeChat, 100, secondNs, 0, true, false},
		{"unmeasured", identity.MainAgentID, UsagePurposeChat, 100, 0, 0, false, false},
		{"compaction", identity.MainAgentID, UsagePurposeCompaction, 100, secondNs, latencyA, false, false},
		{"worker", "worker-1", UsagePurposeChat, 100, secondNs, latencyA, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := speedSampleEvent(speedTestModel, tc.output, tc.requestNs, tc.firstNs)
			event.AgentID = tc.agentID
			event.Purpose = tc.purpose
			tracker := NewUsageTracker()
			tracker.AddUsageEvent(event)
			summary := &SessionUsageSummary{}
			applyUsageEvent(summary, event)

			model := tracker.SessionStats().ByModel[speedTestModel]
			got := summary.UsageTotal
			if got.ThroughputTokens != model.ThroughputTokens || got.ThroughputNs != model.ThroughputNs ||
				got.ThroughputSamples != model.ThroughputSamples || got.FirstOutputTotalNs != model.FirstOutputTotalNs ||
				got.FirstOutputSamples != model.FirstOutputSamples {
				t.Fatalf("summary speed = %+v, want tracker speed %+v", got, model)
			}
			var want UsageAggregate
			if tc.throughput {
				want.ThroughputTokens = tc.output
				want.ThroughputNs = tc.requestNs
				want.ThroughputSamples = 1
			}
			if tc.latency {
				want.FirstOutputTotalNs = tc.firstNs
				want.FirstOutputSamples = 1
			}
			if got.ThroughputTokens != want.ThroughputTokens || got.ThroughputNs != want.ThroughputNs ||
				got.ThroughputSamples != want.ThroughputSamples || got.FirstOutputTotalNs != want.FirstOutputTotalNs ||
				got.FirstOutputSamples != want.FirstOutputSamples {
				t.Fatalf("summary speed = %+v, want %+v", got, want)
			}
		})
	}
}

func TestSpeedSamplesSurviveLedgerReplayAndTrackerRestore(t *testing.T) {
	ledger := NewUsageLedger(t.TempDir(), "")
	t.Cleanup(func() {
		if err := ledger.Close(); err != nil {
			t.Errorf("close ledger: %v", err)
		}
	})
	live := NewUsageTracker()
	main := speedSampleEvent(speedTestModel, 100, secondNs, latencyA)
	sub := speedSampleEvent(speedTestModel, 500, 2*secondNs, latencyB)
	sub.EventID = "sub-chat"
	sub.AgentID = "sub-1"
	sub.TaskID = "task-1"
	compaction := sub
	compaction.EventID = "sub-compaction"
	compaction.Purpose = UsagePurposeCompaction
	for _, event := range []UsageEvent{main, sub, compaction} {
		live.AddUsageEvent(event)
		if err := ledger.AppendEvent(event); err != nil {
			t.Fatalf("append usage: %v", err)
		}
	}
	stats, _, err := ledger.BuildSessionStats()
	if err != nil {
		t.Fatalf("replay usage: %v", err)
	}
	restored := NewUsageTracker()
	restored.RestoreStats(stats)
	if got, want := restored.SessionStats(), live.SessionStats(); !reflect.DeepEqual(got, want) {
		t.Fatalf("restored stats = %+v, want live stats %+v", got, want)
	}
	if got := restored.SessionStatsForAgent(sub.AgentID).ByModel[speedTestModel]; got.ThroughputTokens != 500 || got.ThroughputSamples != 1 {
		t.Fatalf("restored worker model = %+v, want its own chat sample", got)
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
