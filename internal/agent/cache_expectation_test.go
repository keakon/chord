package agent

import (
	"testing"
	"time"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
)

func TestCacheHitTrackerRollingRate(t *testing.T) {
	tr := newCacheHitTracker()
	if _, ok := tr.HitRate("p/m"); ok {
		t.Fatal("expected no rate before observations")
	}
	tr.Observe("p/m", 100000, 90000)
	tr.Observe("p/m", 100000, 90000)
	rate, ok := tr.HitRate("p/m")
	if !ok || rate < 0.89 || rate > 0.91 {
		t.Fatalf("rate = %v ok=%v, want ~0.9", rate, ok)
	}
	// Misses contribute their full input tokens to the exact aggregate.
	for range 10 {
		tr.Observe("p/m", 100000, 0)
	}
	rate, _ = tr.HitRate("p/m")
	if rate < 0.1 || rate > 0.2 {
		t.Fatalf("rate after misses = %v, want ~0.15", rate)
	}
	// Cache read above input is clamped.
	tr.Observe("q/m", 1000, 5000)
	tr.Observe("q/m", 1000, 5000)
	tr.Observe("q/m", 1000, 5000)
	if rate, _ := tr.HitRate("q/m"); rate > 1 {
		t.Fatalf("clamped rate = %v, want <= 1", rate)
	}
}

func TestObservedCacheHitAcceptsSplitInputUsage(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.observedCacheHit("p/m", &message.TokenUsage{InputTokens: 2, CacheReadTokens: 30000})
	rate, ok := a.cacheHitTracker.HitRate("p/m")
	if !ok {
		t.Fatal("expected split input usage to be observed")
	}
	if rate <= 0.98 || rate > 1 {
		t.Fatalf("rate = %v, want approximately 100%% and never above 100%%", rate)
	}
}

func TestActivateLoadedSessionRestoresHistoricalCacheHitRate(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.activateLoadedSession(&loadedSessionState{
		SessionPath: t.TempDir(),
		Messages:    []message.Message{{Role: message.RoleUser, Content: "restored"}},
		UsageStats: analytics.SessionStats{ByModel: map[string]*analytics.ModelStats{
			"p/m": {InputTokens: 2, CacheReadTokens: 30_000},
		}},
	})
	rate, ok := a.cacheHitTracker.HitRate("p/m")
	if !ok || rate < 0.999 || rate > 1 {
		t.Fatalf("restored rate = %v ok=%v, want 30000/30002", rate, ok)
	}
}

func TestIncrementalCacheExpectationShapesMatchFullComputation(t *testing.T) {
	base := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, Content: "a1", ToolCalls: []message.ToolCall{{ID: "t1", Name: "grep", Args: []byte(`{"pattern":"x"}`)}}},
		{Role: message.RoleTool, ToolCallID: "t1", Content: "match"},
	}
	assertMatchesFull := func(t *testing.T, msgs []message.Message, shapes []stableReductionMessageShape, tokens []int) {
		t.Helper()
		want := stableReductionMessageShapes(msgs)
		if len(shapes) != len(want) || len(tokens) != len(msgs) {
			t.Fatalf("incremental lengths shapes=%d tokens=%d, want %d", len(shapes), len(tokens), len(want))
		}
		for i := range want {
			if shapes[i] != want[i] {
				t.Fatalf("shape[%d] diverges from full computation", i)
			}
			if est := ctxmgr.EstimateMessageTokens(msgs[i]); tokens[i] != est {
				t.Fatalf("tokens[%d] = %d, want %d", i, tokens[i], est)
			}
		}
	}

	shapes, tokens, source := incrementalCacheExpectationShapes(nil, base)
	assertMatchesFull(t, base, shapes, tokens)
	record := &cacheExpectationRecord{Source: source, Shapes: shapes, Tokens: tokens}

	// Identical request: the previous slices are reused without reallocation.
	sameShapes, sameTokens, sameSource := incrementalCacheExpectationShapes(record, base)
	if &sameShapes[0] != &record.Shapes[0] || &sameTokens[0] != &record.Tokens[0] || &sameSource[0] != &record.Source[0] {
		t.Fatal("unchanged request did not reuse the previous record's slices")
	}

	// Append-only growth: reused prefix plus freshly hashed tail.
	grown := append(append([]message.Message(nil), base...), message.Message{Role: message.RoleUser, Content: "u2"})
	shapes, tokens, _ = incrementalCacheExpectationShapes(record, grown)
	assertMatchesFull(t, grown, shapes, tokens)

	// In-place rewrite: everything from the mutated index is recomputed.
	mutated := append([]message.Message(nil), grown...)
	mutated[1].Content = "a1 rewritten"
	shapes, tokens, _ = incrementalCacheExpectationShapes(record, mutated)
	assertMatchesFull(t, mutated, shapes, tokens)

	// Shrunk request: shorter than the previous record.
	shapes, tokens, _ = incrementalCacheExpectationShapes(record, base[:1])
	assertMatchesFull(t, base[:1], shapes, tokens)
}

func TestNoteCacheExpectationAttributesDivergence(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	base := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", Content: "a1"},
	}
	hash := a.computeToolDefinitionHash()

	diag := a.noteCacheExpectation("p/m", base, 0, hash, time.Now(), nil)
	if diag["cache_first_request"] != "true" || diag["cache_outcome"] != cacheOutcomeUnknown {
		t.Fatalf("first request diag = %v", diag)
	}

	// Append-only growth: divergence at the old length, expectation covers the
	// full previous request.
	grown := append(append([]message.Message(nil), base...), message.Message{Role: "user", Content: "u2"})
	diag = a.noteCacheExpectation("p/m", grown, 0, hash, time.Now(), nil)
	if diag["cache_prefix_divergence"] != "2" || diag["cache_divergence_kind"] != "append" {
		t.Fatalf("append diag = %v", diag)
	}
	if diag["cache_expected_tokens"] == "0" {
		t.Fatalf("expected nonzero cache expectation, diag = %v", diag)
	}
	if diag["cache_outcome"] != cacheOutcomeUnknown || diag["cache_outcome_reason"] != "no_cache_usage" {
		t.Fatalf("append without usage diag = %v", diag)
	}

	// In-place rewrite of an early message: divergence at that index, and the
	// outcome blames the local rewrite regardless of provider usage.
	mutated := append([]message.Message(nil), grown...)
	mutated[0].Content = "u1 rewritten"
	diag = a.noteCacheExpectation("p/m", mutated, 0, hash, time.Now(), &message.TokenUsage{CacheReadTokens: 10})
	if diag["cache_prefix_divergence"] != "0" || diag["cache_divergence_kind"] != "rewrite" {
		t.Fatalf("rewrite diag = %v", diag)
	}
	if diag["cache_outcome"] != cacheOutcomeLocalRewrite || diag["cache_outcome_reason"] != "prefix_rewrite" {
		t.Fatalf("rewrite outcome diag = %v", diag)
	}

	// A different ref tracks its own expectation independently.
	diag = a.noteCacheExpectation("q/m", mutated, 0, hash, time.Now(), nil)
	if diag["cache_first_request"] != "true" {
		t.Fatalf("other-ref diag = %v", diag)
	}
}

// Transient tail overlays sit after the cache boundary and never enter the
// cacheable prefix, so replacing them with new durable messages on the next
// request must read as an append, not as a chord-side prefix rewrite.
func TestNoteCacheExpectationIgnoresTailOverlayChurn(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	hash := a.computeToolDefinitionHash()
	durable := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", Content: "a1"},
	}
	withOverlay := append(append([]message.Message(nil), durable...),
		message.Message{Role: "user", Content: "<system-reminder>turn hint</system-reminder>"})

	a.noteCacheExpectation("p/m", withOverlay, 1, hash, time.Now(), nil)

	grown := append(append([]message.Message(nil), durable...),
		message.Message{Role: "assistant", Content: "a2"},
		message.Message{Role: "user", Content: "u2"})
	diag := a.noteCacheExpectation("p/m", grown, 0, hash, time.Now(), nil)
	if diag["cache_prefix_divergence"] != "2" || diag["cache_divergence_kind"] != "append" {
		t.Fatalf("overlay churn diag = %v", diag)
	}
	if diag["cache_outcome"] == cacheOutcomeLocalRewrite {
		t.Fatalf("overlay churn misread as local rewrite: %v", diag)
	}
}

func TestClassifyCacheOutcome(t *testing.T) {
	warm := cacheWarmWindow / 2
	cases := []struct {
		name             string
		localRewrite     string
		expected         int
		sinceLastRequest time.Duration
		usage            *message.TokenUsage
		wantOutcome      string
		wantReason       string
	}{
		{"local rewrite wins over usage", "prefix_rewrite", 50_000, warm,
			&message.TokenUsage{CacheReadTokens: 50_000}, cacheOutcomeLocalRewrite, "prefix_rewrite"},
		{"missing usage is unknown", "", 50_000, warm,
			nil, cacheOutcomeUnknown, "no_cache_usage"},
		{"all-zero cache fields are unknown", "", 50_000, warm,
			&message.TokenUsage{InputTokens: 60_000}, cacheOutcomeUnknown, "no_cache_usage"},
		{"tiny expectation stays append", "", minAttributableCacheTokens - 1, warm,
			&message.TokenUsage{CacheWriteTokens: 500}, cacheOutcomeAppend, ""},
		{"consistent cache read is append", "", 50_000, warm,
			&message.TokenUsage{CacheReadTokens: 30_000}, cacheOutcomeAppend, ""},
		{"low read inside warm window is suspected", "", 50_000, warm,
			&message.TokenUsage{CacheReadTokens: 100, CacheWriteTokens: 49_000}, cacheOutcomeSuspectedProviderMiss, "low_cache_read"},
		{"low read after warm window is unknown", "", 50_000, cacheWarmWindow + time.Second,
			&message.TokenUsage{CacheReadTokens: 100, CacheWriteTokens: 49_000}, cacheOutcomeUnknown, "warm_window_elapsed"},
	}
	for _, tc := range cases {
		outcome, reason := classifyCacheOutcome(tc.localRewrite, tc.expected, tc.sinceLastRequest, tc.usage)
		if outcome != tc.wantOutcome || reason != tc.wantReason {
			t.Errorf("%s: outcome=%q reason=%q, want %q %q", tc.name, outcome, reason, tc.wantOutcome, tc.wantReason)
		}
	}
}

func TestCacheExpectationInvalidatesAcrossPromptAndSessionBoundaries(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	msgs := []message.Message{{Role: message.RoleUser, Content: "same user message"}}
	hash := a.computeToolDefinitionHash()

	a.installSystemPrompt("first system prompt")
	a.noteCacheExpectation("p/m", msgs, 0, hash, time.Now(), nil)
	a.installSystemPrompt("different system prompt")
	diag := a.noteCacheExpectation("p/m", msgs, 0, hash, time.Now(), nil)
	if diag["cache_expected_tokens"] != "0" || diag["cache_system_prompt_changed"] != "true" {
		t.Fatalf("system prompt change did not invalidate expectation: %v", diag)
	}
	if diag["cache_outcome"] != cacheOutcomeLocalRewrite || diag["cache_outcome_reason"] != "system_prompt_changed" {
		t.Fatalf("system prompt change outcome diag = %v", diag)
	}

	a.activateLoadedSession(&loadedSessionState{
		SessionPath: t.TempDir(),
		Messages:    []message.Message{{Role: message.RoleUser, Content: "new session"}},
	})
	if a.refCacheWarm("p/m", time.Now()) {
		t.Fatal("new session inherited the previous session's warm cache signal")
	}
	if _, ok := a.cacheHitTracker.HitRate("p/m"); ok {
		t.Fatal("new session inherited the previous session's cache hit observations")
	}
}
