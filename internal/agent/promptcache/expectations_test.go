package promptcache

import (
	"crypto/sha256"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent/requestshape"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
)

func TestCacheHitTrackerRollingRate(t *testing.T) {
	tr := NewHitTracker()
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

func TestIncrementalCacheExpectationShapesMatchFullComputation(t *testing.T) {
	base := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, Content: "a1", ToolCalls: []message.ToolCall{{ID: "t1", Name: "grep", Args: []byte(`{"pattern":"x"}`)}}},
		{Role: message.RoleTool, ToolCallID: "t1", Content: "match"},
	}
	assertMatchesFull := func(t *testing.T, msgs []message.Message, shapes []requestshape.Shape, tokens []int) {
		t.Helper()
		want := requestshape.Shapes(msgs)
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
	record := &expectationRecord{Source: source, Shapes: shapes, Tokens: tokens}

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

func TestClassifyCacheOutcome(t *testing.T) {
	warm := cacheWarmWindow / 2
	cases := []struct {
		name             string
		localRewrite     string
		expected         int
		sinceLastRequest time.Duration
		usage            *message.TokenUsage
		cacheObserved    bool
		wantOutcome      string
		wantReason       string
	}{
		{"local rewrite wins over usage", "prefix_rewrite", 50_000, warm,
			&message.TokenUsage{CacheReadTokens: 50_000}, true, cacheOutcomeLocalRewrite, "prefix_rewrite"},
		{"missing usage is unknown", "", 50_000, warm,
			nil, true, cacheOutcomeUnknown, "no_cache_usage"},
		{"all-zero cache fields without history are unknown", "", 50_000, warm,
			&message.TokenUsage{InputTokens: 60_000}, false, cacheOutcomeUnknown, "no_cache_usage"},
		{"all-zero cache fields after prior reads are a miss", "", 50_000, warm,
			&message.TokenUsage{InputTokens: 60_000}, true, cacheOutcomeSuspectedProviderMiss, "low_cache_read"},
		{"all-zero cache fields after prior reads outside the window are unknown", "", 50_000, cacheWarmWindow + time.Second,
			&message.TokenUsage{InputTokens: 60_000}, true, cacheOutcomeUnknown, "warm_window_elapsed"},
		{"tiny expectation stays append", "", minAttributableCacheTokens - 1, warm,
			&message.TokenUsage{CacheWriteTokens: 500}, false, cacheOutcomeAppend, ""},
		{"consistent cache read is append", "", 50_000, warm,
			&message.TokenUsage{CacheReadTokens: 30_000}, true, cacheOutcomeAppend, ""},
		{"low read inside warm window is suspected", "", 50_000, warm,
			&message.TokenUsage{CacheReadTokens: 100, CacheWriteTokens: 49_000}, false, cacheOutcomeSuspectedProviderMiss, "low_cache_read"},
		{"low read after warm window is unknown", "", 50_000, cacheWarmWindow + time.Second,
			&message.TokenUsage{CacheReadTokens: 100, CacheWriteTokens: 49_000}, false, cacheOutcomeUnknown, "warm_window_elapsed"},
	}
	for _, tc := range cases {
		outcome, reason := classifyCacheOutcome(tc.localRewrite, tc.expected, tc.sinceLastRequest, tc.usage, tc.cacheObserved)
		if outcome != tc.wantOutcome || reason != tc.wantReason {
			t.Errorf("%s: outcome=%q reason=%q, want %q %q", tc.name, outcome, reason, tc.wantOutcome, tc.wantReason)
		}
	}
}

func TestNoteCacheExpectationAttributesDivergence(t *testing.T) {
	state := &Expectations{}
	note := func(modelRef string, messages []message.Message, toolDefHash [sha256.Size]byte, sentAt time.Time, usage *message.TokenUsage) map[string]string {
		return state.Note(Request{ModelRef: modelRef, Messages: messages, ToolDefHash: toolDefHash, SentAt: sentAt, Usage: usage, PromptHash: func() [sha256.Size]byte { return sha256.Sum256([]byte("system")) }})
	}
	base := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", Content: "a1"},
	}
	hash := sha256.Sum256([]byte("tools"))

	diag := note("p/m", base, hash, time.Now(), nil)
	if diag["cache_first_request"] != "true" || diag["cache_outcome"] != cacheOutcomeUnknown {
		t.Fatalf("first request diag = %v", diag)
	}

	// Append-only growth: divergence at the old length, expectation covers the
	// full previous request.
	grown := append(append([]message.Message(nil), base...), message.Message{Role: "user", Content: "u2"})
	diag = note("p/m", grown, hash, time.Now(), nil)
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
	diag = note("p/m", mutated, hash, time.Now(), &message.TokenUsage{CacheReadTokens: 10})
	if diag["cache_prefix_divergence"] != "0" || diag["cache_divergence_kind"] != "rewrite" {
		t.Fatalf("rewrite diag = %v", diag)
	}
	if diag["cache_outcome"] != cacheOutcomeLocalRewrite || diag["cache_outcome_reason"] != "prefix_rewrite" {
		t.Fatalf("rewrite outcome diag = %v", diag)
	}

	// A different ref tracks its own expectation independently.
	diag = note("q/m", mutated, hash, time.Now(), nil)
	if diag["cache_first_request"] != "true" {
		t.Fatalf("other-ref diag = %v", diag)
	}
}

// The estimator attributes bytes to payload the provider prices differently
// (encrypted reasoning is the large case), so the expectation is bounded by the
// prompt size the provider itself reported.
func TestNoteCacheExpectationCapsByProviderPromptSize(t *testing.T) {
	state := &Expectations{}
	hash := sha256.Sum256([]byte("tools"))
	prompt := func() [sha256.Size]byte { return sha256.Sum256([]byte("system")) }
	note := func(messages []message.Message, sentAt time.Time, usage *message.TokenUsage) map[string]string {
		return state.Note(Request{ModelRef: "p/m", Messages: messages, ToolDefHash: hash, SentAt: sentAt, Usage: usage, PromptHash: prompt})
	}
	base := []message.Message{{Role: message.RoleUser, Content: strings.Repeat("payload ", 2000)}}
	estimated := ctxmgr.EstimateMessageTokens(base[0])
	providerTokens := estimated / 3
	if providerTokens < minAttributableCacheTokens {
		t.Fatalf("sample too small: estimate=%d", estimated)
	}
	note(base, time.Unix(1000, 0), &message.TokenUsage{InputTokens: providerTokens, InputSemanticsKnown: true, InputIncludesCacheRead: true})
	grown := append(append([]message.Message(nil), base...), message.Message{Role: message.RoleUser, Content: "next"})
	diag := note(grown, time.Unix(1010, 0), &message.TokenUsage{InputTokens: providerTokens + 5, CacheReadTokens: providerTokens - 100, InputSemanticsKnown: true, InputIncludesCacheRead: true})
	if got := diag["cache_expected_tokens"]; got != strconv.Itoa(providerTokens) {
		t.Fatalf("expected tokens = %s, want provider-bounded %d (local estimate %d)", got, providerTokens, estimated)
	}
	if diag["cache_outcome"] != cacheOutcomeAppend {
		t.Fatalf("provider-bounded expectation still misread as a miss: %v", diag)
	}
}

// A zero cache read is a lost cache, not a provider without caching, once this
// ref has reported a cache read before.
func TestNoteCacheExpectationReportsLostCacheAfterObservedReads(t *testing.T) {
	state := &Expectations{}
	hash := sha256.Sum256([]byte("tools"))
	prompt := func() [sha256.Size]byte { return sha256.Sum256([]byte("system")) }
	note := func(messages []message.Message, sentAt time.Time, usage *message.TokenUsage) map[string]string {
		return state.Note(Request{ModelRef: "p/m", Messages: messages, ToolDefHash: hash, SentAt: sentAt, Usage: usage, PromptHash: prompt})
	}
	base := []message.Message{{Role: message.RoleUser, Content: strings.Repeat("payload ", 2000)}}
	note(base, time.Unix(1000, 0), &message.TokenUsage{InputTokens: 4_000, CacheReadTokens: 3_000, InputSemanticsKnown: true, InputIncludesCacheRead: true})
	grown := append(append([]message.Message(nil), base...), message.Message{Role: message.RoleUser, Content: "next"})
	diag := note(grown, time.Unix(1010, 0), &message.TokenUsage{InputTokens: 4_100, InputSemanticsKnown: true, InputIncludesCacheRead: true})
	if diag["cache_outcome"] != cacheOutcomeSuspectedProviderMiss || diag["cache_outcome_reason"] != "low_cache_read" {
		t.Fatalf("lost cache after observed reads misclassified: %v", diag)
	}
}

func TestExpectationPromptSnapshotPrecedesPublication(t *testing.T) {
	state := &Expectations{}
	sentAt := time.Unix(1000, 0)
	prompt := sha256.Sum256([]byte("system"))
	calls := 0
	request := Request{ModelRef: "sample/model", Messages: []message.Message{{Role: message.RoleUser, Content: "sample"}}, SentAt: sentAt,
		PromptHash: func() [sha256.Size]byte {
			calls++
			// Reentrant observation also verifies that the prompt supplier is outside
			// the expectation mutex and that the new record has not yet been published.
			if state.Warm("sample/model", sentAt) {
				t.Fatal("expectation published before prompt snapshot")
			}
			return prompt
		},
	}
	state.Note(request)
	if calls != 1 || state.byRef[request.ModelRef].PromptHash != prompt || !state.Warm(request.ModelRef, sentAt) {
		t.Fatal("prompt snapshot or publication was lost")
	}
	if state.Warm(request.ModelRef, sentAt.Add(cacheWarmWindow)) {
		t.Fatal("expired dispatch remained warm")
	}
	state.Reset()
	if state.Warm(request.ModelRef, sentAt) {
		t.Fatal("reset retained prior session evidence")
	}
	// Protocol projection can remove all messages even when the incoming request
	// was nonempty. Such a completed request still replaces the previous record.
	request.Messages = nil
	result := state.Note(request)
	if calls != 2 || result["cache_messages"] != "0" || len(state.byRef[request.ModelRef].Source) != 0 {
		t.Fatalf("empty projected surface was discarded: %v", result)
	}
}
