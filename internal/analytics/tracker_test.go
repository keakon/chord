package analytics

import (
	"strings"
	"sync"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestNewUsageTracker(t *testing.T) {
	tracker := NewUsageTracker()
	stats := tracker.SessionStats()

	if stats.LLMCalls != 0 {
		t.Errorf("expected 0 LLM calls, got %d", stats.LLMCalls)
	}
	if stats.InputTokens != 0 {
		t.Errorf("expected 0 input tokens, got %d", stats.InputTokens)
	}
	if stats.OutputTokens != 0 {
		t.Errorf("expected 0 output tokens, got %d", stats.OutputTokens)
	}
	if stats.CacheReadTokens != 0 {
		t.Errorf("expected 0 cache read tokens, got %d", stats.CacheReadTokens)
	}
	if stats.CacheWriteTokens != 0 {
		t.Errorf("expected 0 cache write tokens, got %d", stats.CacheWriteTokens)
	}
	if stats.EstimatedCost != 0 {
		t.Errorf("expected 0 estimated cost, got %f", stats.EstimatedCost)
	}
	if len(stats.ByModel) != 0 {
		t.Errorf("expected empty ByModel map, got %d entries", len(stats.ByModel))
	}
	if len(stats.ByAgent) != 0 {
		t.Errorf("expected empty ByAgent map, got %d entries", len(stats.ByAgent))
	}
}

func TestRecord_BasicTracking(t *testing.T) {
	tracker := NewUsageTracker()
	usage := message.TokenUsage{
		InputTokens:      1000,
		OutputTokens:     500,
		CacheReadTokens:  200,
		CacheWriteTokens: 100,
	}

	tracker.Record("claude-opus-4.7", nil, usage)

	stats := tracker.SessionStats()
	if stats.LLMCalls != 1 {
		t.Errorf("expected 1 LLM call, got %d", stats.LLMCalls)
	}
	if stats.InputTokens != 1000 {
		t.Errorf("expected 1000 input tokens, got %d", stats.InputTokens)
	}
	if stats.OutputTokens != 500 {
		t.Errorf("expected 500 output tokens, got %d", stats.OutputTokens)
	}
	if stats.CacheReadTokens != 200 {
		t.Errorf("expected 200 cache read tokens, got %d", stats.CacheReadTokens)
	}
	if stats.CacheWriteTokens != 100 {
		t.Errorf("expected 100 cache write tokens, got %d", stats.CacheWriteTokens)
	}

	// Check per-model stats.
	ms, ok := stats.ByModel["claude-opus-4.7"]
	if !ok {
		t.Fatal("expected model entry for claude-opus-4.7")
	}
	if ms.Calls != 1 {
		t.Errorf("expected 1 call for model, got %d", ms.Calls)
	}
	if ms.InputTokens != 1000 {
		t.Errorf("expected 1000 input tokens for model, got %d", ms.InputTokens)
	}
	if ms.OutputTokens != 500 {
		t.Errorf("expected 500 output tokens for model, got %d", ms.OutputTokens)
	}

	as, ok := stats.ByAgent["main"]
	if !ok {
		t.Fatal("expected main agent entry")
	}
	if as.LLMCalls != 1 {
		t.Errorf("expected 1 call for main agent, got %d", as.LLMCalls)
	}
	if as.ByModel["claude-opus-4.7"] == nil {
		t.Fatal("expected main agent per-model entry")
	}

	mainOnly := tracker.SessionStatsForAgent("main")
	if mainOnly.LLMCalls != 1 || mainOnly.InputTokens != 1000 {
		t.Fatalf("SessionStatsForAgent(main) = %+v, want main-only totals", mainOnly)
	}
	if len(mainOnly.ByAgent) != 0 {
		t.Fatalf("SessionStatsForAgent should leave ByAgent empty, got %d", len(mainOnly.ByAgent))
	}

	subOnly := tracker.SessionStatsForAgent("sub-1")
	if subOnly.LLMCalls != 0 {
		t.Fatalf("SessionStatsForAgent(missing id) LLMCalls = %d, want 0", subOnly.LLMCalls)
	}
}

func TestSessionStatsForAgent_IsolatesPerAgent(t *testing.T) {
	tracker := NewUsageTracker()
	uMain := message.TokenUsage{InputTokens: 100, OutputTokens: 10}
	uSub := message.TokenUsage{InputTokens: 50, OutputTokens: 5}
	tracker.RecordForAgent("main", "m", nil, uMain)
	tracker.RecordForAgent("agent-x", "m", nil, uSub)

	gotMain := tracker.SessionStatsForAgent("main")
	if gotMain.InputTokens != 100 || gotMain.OutputTokens != 10 {
		t.Fatalf("main slice: %+v", gotMain)
	}
	gotSub := tracker.SessionStatsForAgent("agent-x")
	if gotSub.InputTokens != 50 || gotSub.OutputTokens != 5 {
		t.Fatalf("sub slice: %+v", gotSub)
	}
	full := tracker.SessionStats()
	if full.InputTokens != 150 {
		t.Fatalf("session total input = %d, want 150", full.InputTokens)
	}
}

func TestRecord_NoCost(t *testing.T) {
	tracker := NewUsageTracker()
	usage := message.TokenUsage{
		InputTokens:  5000,
		OutputTokens: 2000,
	}

	tracker.Record("gpt-5.5", nil, usage)

	stats := tracker.SessionStats()
	if stats.EstimatedCost != 0 {
		t.Errorf("expected 0 cost with nil cost config, got %f", stats.EstimatedCost)
	}
	if stats.ByModel["gpt-5.5"].EstimatedCost != 0 {
		t.Errorf("expected 0 model cost with nil cost config, got %f", stats.ByModel["gpt-5.5"].EstimatedCost)
	}
}

func TestRecord_WithCost(t *testing.T) {
	tracker := NewUsageTracker()
	cost := &config.ModelCost{
		Input:      3.0,  // $3.00 per 1M input tokens
		Output:     15.0, // $15.00 per 1M output tokens
		CacheRead:  0.30, // $0.30 per 1M cache read tokens
		CacheWrite: 3.75, // $3.75 per 1M cache write tokens
	}
	usage := message.TokenUsage{
		InputTokens:      1_000_000,
		OutputTokens:     100_000,
		CacheReadTokens:  500_000,
		CacheWriteTokens: 200_000,
	}

	tracker.Record("claude-opus-4.7", cost, usage)

	stats := tracker.SessionStats()

	// Expected cost (billing input excludes cache read):
	// input:       500_000 / 1_000_000 * 3.0     = $1.50
	// output:      100_000 / 1_000_000 * 15.0     = $1.50
	// cache_read:  500_000 / 1_000_000 * 0.30     = $0.15
	// cache_write: 200_000 / 1_000_000 * 3.75     = $0.75
	// total = $3.90
	expectedCost := 1.5 + 1.5 + 0.15 + 0.75
	if !almostEqual(stats.EstimatedCost, expectedCost, 0.0001) {
		t.Errorf("expected session cost %.4f, got %.4f", expectedCost, stats.EstimatedCost)
	}

	ms := stats.ByModel["claude-opus-4.7"]
	if !almostEqual(ms.EstimatedCost, expectedCost, 0.0001) {
		t.Errorf("expected model cost %.4f, got %.4f", expectedCost, ms.EstimatedCost)
	}
}

func TestRecord_MultipleCalls(t *testing.T) {
	tracker := NewUsageTracker()
	cost := &config.ModelCost{
		Input:  3.0,
		Output: 15.0,
	}

	// First call
	tracker.Record("model-a", cost, message.TokenUsage{
		InputTokens:  1000,
		OutputTokens: 500,
	})
	// Second call
	tracker.Record("model-a", cost, message.TokenUsage{
		InputTokens:  2000,
		OutputTokens: 1000,
	})

	stats := tracker.SessionStats()
	if stats.LLMCalls != 2 {
		t.Errorf("expected 2 LLM calls, got %d", stats.LLMCalls)
	}
	if stats.InputTokens != 3000 {
		t.Errorf("expected 3000 total input tokens, got %d", stats.InputTokens)
	}
	if stats.OutputTokens != 1500 {
		t.Errorf("expected 1500 total output tokens, got %d", stats.OutputTokens)
	}

	ms := stats.ByModel["model-a"]
	if ms.Calls != 2 {
		t.Errorf("expected 2 calls for model-a, got %d", ms.Calls)
	}
	if ms.InputTokens != 3000 {
		t.Errorf("expected 3000 input tokens for model-a, got %d", ms.InputTokens)
	}
}

func TestRecord_MultipleModels(t *testing.T) {
	tracker := NewUsageTracker()
	costA := &config.ModelCost{Input: 3.0, Output: 15.0}
	costB := &config.ModelCost{Input: 5.0, Output: 25.0}

	tracker.Record("model-a", costA, message.TokenUsage{
		InputTokens:  1000,
		OutputTokens: 500,
	})
	tracker.Record("model-b", costB, message.TokenUsage{
		InputTokens:  2000,
		OutputTokens: 1000,
	})
	tracker.Record("model-a", costA, message.TokenUsage{
		InputTokens:  500,
		OutputTokens: 200,
	})

	stats := tracker.SessionStats()
	if stats.LLMCalls != 3 {
		t.Errorf("expected 3 LLM calls, got %d", stats.LLMCalls)
	}
	if stats.InputTokens != 3500 {
		t.Errorf("expected 3500 total input tokens, got %d", stats.InputTokens)
	}
	if len(stats.ByModel) != 2 {
		t.Errorf("expected 2 models, got %d", len(stats.ByModel))
	}

	msA := stats.ByModel["model-a"]
	if msA == nil {
		t.Fatal("expected model-a entry")
	}
	if msA.Calls != 2 {
		t.Errorf("expected 2 calls for model-a, got %d", msA.Calls)
	}
	if msA.InputTokens != 1500 {
		t.Errorf("expected 1500 input tokens for model-a, got %d", msA.InputTokens)
	}

	msB := stats.ByModel["model-b"]
	if msB == nil {
		t.Fatal("expected model-b entry")
	}
	if msB.Calls != 1 {
		t.Errorf("expected 1 call for model-b, got %d", msB.Calls)
	}
	if msB.InputTokens != 2000 {
		t.Errorf("expected 2000 input tokens for model-b, got %d", msB.InputTokens)
	}
}

func TestRecord_CacheTokensPerModel(t *testing.T) {
	tracker := NewUsageTracker()
	usage := message.TokenUsage{
		InputTokens:      1000,
		OutputTokens:     500,
		CacheReadTokens:  300,
		CacheWriteTokens: 150,
	}

	tracker.Record("model-x", nil, usage)

	stats := tracker.SessionStats()
	ms := stats.ByModel["model-x"]
	if ms.CacheReadTokens != 300 {
		t.Errorf("expected 300 cache read tokens for model, got %d", ms.CacheReadTokens)
	}
	if ms.CacheWriteTokens != 150 {
		t.Errorf("expected 150 cache write tokens for model, got %d", ms.CacheWriteTokens)
	}
}

func TestSessionStats_SnapshotIsolation(t *testing.T) {
	tracker := NewUsageTracker()
	tracker.Record("model-a", nil, message.TokenUsage{InputTokens: 100})

	// Take snapshot.
	snap := tracker.SessionStats()

	// Mutate the snapshot.
	snap.InputTokens = 999999
	snap.ByModel["model-a"].Calls = 999
	snap.ByAgent["main"].LLMCalls = 999

	// Verify the tracker is not affected.
	fresh := tracker.SessionStats()
	if fresh.InputTokens != 100 {
		t.Errorf("snapshot mutation affected tracker: expected 100, got %d", fresh.InputTokens)
	}
	if fresh.ByModel["model-a"].Calls != 1 {
		t.Errorf("snapshot mutation affected tracker model stats: expected 1, got %d", fresh.ByModel["model-a"].Calls)
	}
	if fresh.ByAgent["main"].LLMCalls != 1 {
		t.Errorf("snapshot mutation affected tracker agent stats: expected 1, got %d", fresh.ByAgent["main"].LLMCalls)
	}
}

func TestSessionStats_SnapshotMapIsolation(t *testing.T) {
	tracker := NewUsageTracker()
	tracker.Record("model-a", nil, message.TokenUsage{InputTokens: 100})

	snap := tracker.SessionStats()

	// Add a new model to the snapshot map.
	snap.ByModel["model-fake"] = &ModelStats{Calls: 42}

	// Verify the tracker doesn't have the fake model.
	fresh := tracker.SessionStats()
	if _, ok := fresh.ByModel["model-fake"]; ok {
		t.Error("adding to snapshot ByModel map affected the tracker")
	}
	snap.ByAgent["fake-agent"] = &AgentStats{LLMCalls: 42}
	fresh = tracker.SessionStats()
	if _, ok := fresh.ByAgent["fake-agent"]; ok {
		t.Error("adding to snapshot ByAgent map affected the tracker")
	}
}

func TestRecordForAgent_GroupsByAgent(t *testing.T) {
	tracker := NewUsageTracker()
	tracker.RecordForAgent("main", "model-a", nil, message.TokenUsage{InputTokens: 100, OutputTokens: 10})
	tracker.RecordForAgent("agent-1", "model-a", nil, message.TokenUsage{InputTokens: 200, OutputTokens: 20})
	tracker.RecordForAgent("agent-1", "model-b", nil, message.TokenUsage{InputTokens: 300, OutputTokens: 30})

	stats := tracker.SessionStats()
	if len(stats.ByAgent) != 2 {
		t.Fatalf("expected 2 agent entries, got %d", len(stats.ByAgent))
	}
	if stats.ByAgent["main"].InputTokens != 100 {
		t.Fatalf("main agent input tokens = %d, want 100", stats.ByAgent["main"].InputTokens)
	}
	agent1 := stats.ByAgent["agent-1"]
	if agent1 == nil {
		t.Fatal("expected agent-1 entry")
	}
	if agent1.LLMCalls != 2 {
		t.Fatalf("agent-1 llm_calls = %d, want 2", agent1.LLMCalls)
	}
	if len(agent1.ByModel) != 2 {
		t.Fatalf("agent-1 by_model entries = %d, want 2", len(agent1.ByModel))
	}
}

func TestRestoreStats_RestoresByAgent(t *testing.T) {
	tracker := NewUsageTracker()
	tracker.RestoreStats(SessionStats{
		InputTokens: 10,
		ByModel: map[string]*ModelStats{
			"model-a": {Calls: 1, InputTokens: 10},
		},
		ByAgent: map[string]*AgentStats{
			"agent-1": {
				InputTokens: 10,
				LLMCalls:    1,
				ByModel: map[string]*ModelStats{
					"model-a": {Calls: 1, InputTokens: 10},
				},
			},
		},
	})

	stats := tracker.SessionStats()
	if stats.ByAgent["agent-1"] == nil {
		t.Fatal("expected restored agent stats")
	}
	stats.ByAgent["agent-1"].LLMCalls = 999

	fresh := tracker.SessionStats()
	if fresh.ByAgent["agent-1"].LLMCalls != 1 {
		t.Fatalf("restored agent stats mutated through snapshot: %d", fresh.ByAgent["agent-1"].LLMCalls)
	}
}

func TestRecord_ConcurrentSafety(t *testing.T) {
	tracker := NewUsageTracker()
	cost := &config.ModelCost{Input: 3.0, Output: 15.0}

	const goroutines = 100
	const callsPerGoroutine = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			model := "model-a"
			if id%2 == 0 {
				model = "model-b"
			}
			for j := 0; j < callsPerGoroutine; j++ {
				tracker.Record(model, cost, message.TokenUsage{
					InputTokens:  10,
					OutputTokens: 5,
				})
			}
		}(i)
	}

	wg.Wait()

	stats := tracker.SessionStats()
	expectedCalls := int64(goroutines * callsPerGoroutine)
	if stats.LLMCalls != expectedCalls {
		t.Errorf("expected %d calls, got %d", expectedCalls, stats.LLMCalls)
	}

	expectedInput := expectedCalls * 10
	if stats.InputTokens != expectedInput {
		t.Errorf("expected %d input tokens, got %d", expectedInput, stats.InputTokens)
	}

	expectedOutput := expectedCalls * 5
	if stats.OutputTokens != expectedOutput {
		t.Errorf("expected %d output tokens, got %d", expectedOutput, stats.OutputTokens)
	}

	// Verify per-model counts sum correctly.
	var totalModelCalls int64
	for _, ms := range stats.ByModel {
		totalModelCalls += ms.Calls
	}
	if totalModelCalls != expectedCalls {
		t.Errorf("per-model call sum %d != total %d", totalModelCalls, expectedCalls)
	}
}

func TestRecord_ConcurrentReadsAndWrites(t *testing.T) {
	tracker := NewUsageTracker()
	cost := &config.ModelCost{Input: 3.0, Output: 15.0}

	const iterations = 200
	var wg sync.WaitGroup
	wg.Add(iterations * 2) // writers + readers

	// Writers
	for i := 0; i < iterations; i++ {
		go func() {
			defer wg.Done()
			tracker.Record("model-a", cost, message.TokenUsage{
				InputTokens:  100,
				OutputTokens: 50,
			})
		}()
	}

	// Concurrent readers
	for i := 0; i < iterations; i++ {
		go func() {
			defer wg.Done()
			stats := tracker.SessionStats()
			// Just verify it doesn't panic and returns consistent data.
			if stats.LLMCalls < 0 {
				t.Errorf("negative LLM calls: %d", stats.LLMCalls)
			}
		}()
	}

	wg.Wait()

	// Final check
	stats := tracker.SessionStats()
	if stats.LLMCalls != iterations {
		t.Errorf("expected %d calls, got %d", iterations, stats.LLMCalls)
	}
}

func TestRecord_ZeroUsage(t *testing.T) {
	tracker := NewUsageTracker()
	usage := message.TokenUsage{} // all zeros

	tracker.Record("model-a", nil, usage)

	stats := tracker.SessionStats()
	if stats.LLMCalls != 1 {
		t.Errorf("expected 1 LLM call even with zero usage, got %d", stats.LLMCalls)
	}
	if stats.InputTokens != 0 {
		t.Errorf("expected 0 input tokens, got %d", stats.InputTokens)
	}
	ms := stats.ByModel["model-a"]
	if ms == nil {
		t.Fatal("expected model entry even with zero usage")
	}
	if ms.Calls != 1 {
		t.Errorf("expected 1 call for model, got %d", ms.Calls)
	}
}

func TestRecord_CostAccumulation(t *testing.T) {
	tracker := NewUsageTracker()
	cost := &config.ModelCost{
		Input:      3.0,
		Output:     15.0,
		CacheRead:  0.30,
		CacheWrite: 3.75,
	}

	// Three calls
	for i := 0; i < 3; i++ {
		tracker.Record("model-a", cost, message.TokenUsage{
			InputTokens:      100_000,
			OutputTokens:     10_000,
			CacheReadTokens:  50_000,
			CacheWriteTokens: 20_000,
		})
	}

	stats := tracker.SessionStats()

	// Per call:
	// input:       50_000 / 1_000_000 * 3.0    = $0.15
	// output:      10_000 / 1_000_000 * 15.0    = $0.15
	// cache_read:  50_000 / 1_000_000 * 0.30    = $0.015
	// cache_write: 20_000 / 1_000_000 * 3.75    = $0.075
	// per call = $0.39
	// total = $1.17
	perCall := 0.15 + 0.15 + 0.015 + 0.075
	expectedTotal := perCall * 3
	if !almostEqual(stats.EstimatedCost, expectedTotal, 0.0001) {
		t.Errorf("expected total cost %.4f, got %.4f", expectedTotal, stats.EstimatedCost)
	}
}

func TestRecord_MixedCostAndNoCost(t *testing.T) {
	tracker := NewUsageTracker()
	cost := &config.ModelCost{Input: 3.0, Output: 15.0}

	// Call with cost.
	tracker.Record("model-a", cost, message.TokenUsage{
		InputTokens:  1_000_000,
		OutputTokens: 100_000,
	})
	// Call without cost.
	tracker.Record("model-b", nil, message.TokenUsage{
		InputTokens:  500_000,
		OutputTokens: 50_000,
	})

	stats := tracker.SessionStats()

	// Only model-a contributes to cost: $3.00 + $1.50 = $4.50
	expectedCost := 3.0 + 1.5
	if !almostEqual(stats.EstimatedCost, expectedCost, 0.0001) {
		t.Errorf("expected cost %.4f, got %.4f", expectedCost, stats.EstimatedCost)
	}

	if stats.ByModel["model-b"].EstimatedCost != 0 {
		t.Errorf("expected 0 cost for model-b, got %f", stats.ByModel["model-b"].EstimatedCost)
	}
}

// ---------------------------------------------------------------------------
// FormatStats tests
// ---------------------------------------------------------------------------

func TestFormatStats_Empty(t *testing.T) {
	tracker := NewUsageTracker()
	output := tracker.FormatStats()

	if !strings.Contains(output, "Session Usage Statistics") {
		t.Error("expected header in output")
	}
	if !strings.Contains(output, "Total LLM Calls:   0") {
		t.Error("expected 0 LLM calls")
	}
	if !strings.Contains(output, "$0.00") {
		t.Error("expected $0.00 cost")
	}
}

func TestFormatStats_WithData(t *testing.T) {
	tracker := NewUsageTracker()
	cost := &config.ModelCost{Input: 3.0, Output: 15.0}
	tracker.Record("claude-opus-4.7", cost, message.TokenUsage{
		InputTokens:  150_000,
		OutputTokens: 25_000,
	})
	tracker.Record("gpt-5.5", nil, message.TokenUsage{
		InputTokens:  80_000,
		OutputTokens: 10_000,
	})

	output := tracker.FormatStats()

	if !strings.Contains(output, "Total LLM Calls:   2") {
		t.Errorf("expected 2 LLM calls in output, got:\n%s", output)
	}
	if !strings.Contains(output, "Per-Model Breakdown") {
		t.Error("expected per-model section")
	}
	if !strings.Contains(output, "claude-opus-4.7") {
		t.Error("expected claude model name in output")
	}
	if !strings.Contains(output, "gpt-5.5") {
		t.Error("expected gpt model name in output")
	}
}

func TestFormatStats_CacheTokensShown(t *testing.T) {
	tracker := NewUsageTracker()
	tracker.Record("model-a", nil, message.TokenUsage{
		InputTokens:      1000,
		OutputTokens:     500,
		CacheReadTokens:  300,
		CacheWriteTokens: 100,
	})

	output := tracker.FormatStats()
	if !strings.Contains(output, "Cache Read:") {
		t.Error("expected cache read in output when > 0")
	}
	if !strings.Contains(output, "Cache Write:") {
		t.Error("expected cache write in output when > 0")
	}
}

func TestFormatStats_CacheTokensHiddenWhenZero(t *testing.T) {
	tracker := NewUsageTracker()
	tracker.Record("model-a", nil, message.TokenUsage{
		InputTokens:  1000,
		OutputTokens: 500,
	})

	output := tracker.FormatStats()
	if strings.Contains(output, "Cache Read:") {
		t.Error("cache read should not appear when 0")
	}
	if strings.Contains(output, "Cache Write:") {
		t.Error("cache write should not appear when 0")
	}
}

func TestFormatStats_ModelsSortedAlphabetically(t *testing.T) {
	tracker := NewUsageTracker()
	tracker.Record("zeta-model", nil, message.TokenUsage{InputTokens: 100})
	tracker.Record("alpha-model", nil, message.TokenUsage{InputTokens: 100})
	tracker.Record("middle-model", nil, message.TokenUsage{InputTokens: 100})

	output := tracker.FormatStats()

	alphaIdx := strings.Index(output, "alpha-model")
	middleIdx := strings.Index(output, "middle-model")
	zetaIdx := strings.Index(output, "zeta-model")

	if alphaIdx == -1 || middleIdx == -1 || zetaIdx == -1 {
		t.Fatalf("expected all models in output:\n%s", output)
	}
	if !(alphaIdx < middleIdx && middleIdx < zetaIdx) {
		t.Errorf("expected models sorted alphabetically: alpha(%d) < middle(%d) < zeta(%d)",
			alphaIdx, middleIdx, zetaIdx)
	}
}

// ---------------------------------------------------------------------------
// Formatting helper tests
// ---------------------------------------------------------------------------

func TestFormatTokenCount(t *testing.T) {
	tests := []struct {
		input    int64
		contains string // expected substring
	}{
		{0, "0"},
		{42, "42"},
		{999, "999"},
		{1000, "1.0k"},
		{1500, "1.5k"},
		{999_999, "1000.0k"},
		{1_000_000, "1.0M"},
		{2_500_000, "2.5M"},
	}
	for _, tt := range tests {
		result := formatTokenCount(tt.input)
		if !strings.Contains(result, tt.contains) {
			t.Errorf("formatTokenCount(%d) = %q, expected to contain %q", tt.input, result, tt.contains)
		}
	}
}

func TestFormatInt(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{0, "0"},
		{1, "1"},
		{12, "12"},
		{123, "123"},
		{1234, "1,234"},
		{12345, "12,345"},
		{123456, "123,456"},
		{1234567, "1,234,567"},
	}
	for _, tt := range tests {
		result := formatInt(tt.input)
		if result != tt.expected {
			t.Errorf("formatInt(%d) = %q, expected %q", tt.input, result, tt.expected)
		}
	}
}

func TestFormatUSD(t *testing.T) {
	tests := []struct {
		input    float64
		expected string
	}{
		{0, "$0.00"},
		{0.001, "$0.0010"},
		{0.009, "$0.0090"},
		{0.01, "$0.01"},
		{0.15, "$0.15"},
		{1.50, "$1.50"},
		{123.456, "$123.46"},
	}
	for _, tt := range tests {
		result := formatUSD(tt.input)
		if result != tt.expected {
			t.Errorf("formatUSD(%f) = %q, expected %q", tt.input, result, tt.expected)
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func almostEqual(a, b, epsilon float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff < epsilon
}
