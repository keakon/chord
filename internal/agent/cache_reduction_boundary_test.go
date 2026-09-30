package agent

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// newBoundaryReductionTestAgent builds the boundary-reduction harness with a
// model cost entry registered for the running ref "p/m", so the flush decision
// resolves pricing instead of falling back to the fixed ratio.
func newBoundaryReductionTestAgent(t *testing.T, cost *config.ModelCost) *MainAgent {
	t.Helper()
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			ReadLikeAgeTurns:     1,
			ReadLikeOutputBytes:  80,
			MinIncrementalTokens: 1 << 20,
		}},
		Providers: map[string]config.ProviderConfig{
			"p": {Models: map[string]config.ModelConfig{"m": {Cost: cost}}},
		},
	}
	a.runningModelRef = "p/m"
	a.recordLLMModelRun("p/m")
	a.recordLLMModelRun("p/m")
	turnCtx, turnCancel := context.WithCancel(context.Background())
	t.Cleanup(turnCancel)
	a.turn = &Turn{ID: 1, Ctx: turnCtx, Cancel: turnCancel}
	return a
}

func TestBoundaryCacheMissPenaltyRatio(t *testing.T) {
	tests := []struct {
		name   string
		cost   *config.ModelCost
		tokens int64
		tier   config.ServiceTier
		ttl    string
		want   float64
		ok     bool
	}{
		{name: "no cost entry", ok: false},
		{name: "zero input price", cost: &config.ModelCost{CacheRead: 0.1}, ok: false},
		{name: "ten to one caching", cost: &config.ModelCost{Input: 1, CacheRead: 0.1, CacheWrite: 1}, want: 9, ok: true},
		{name: "fifty to one caching", cost: &config.ModelCost{Input: 1, CacheRead: 0.02}, want: 49, ok: true},
		{name: "two to one caching", cost: &config.ModelCost{Input: 1, CacheRead: 0.5}, want: 1, ok: true},
		{name: "unconfigured cache write falls back to input price", cost: &config.ModelCost{Input: 2, CacheRead: 0.2}, want: 9, ok: true},
		{name: "explicit 1h cache write price", cost: &config.ModelCost{Input: 1, CacheRead: 0.1, CacheWrite: 1.25, CacheWrite1h: 2}, ttl: "1h", want: 19, ok: true},
		{name: "default cache TTL uses ordinary write price", cost: &config.ModelCost{Input: 1, CacheRead: 0.1, CacheWrite: 1.25, CacheWrite1h: 2}, want: 11.5, ok: true},
		{name: "explicit 5m cache TTL uses ordinary write price", cost: &config.ModelCost{Input: 1, CacheRead: 0.1, CacheWrite: 1.25, CacheWrite1h: 2}, ttl: "5m", want: 11.5, ok: true},
		{name: "free cache reads hit the ceiling", cost: &config.ModelCost{Input: 1, CacheWrite: 1}, want: cacheMissPenaltyRatioCeiling, ok: true},
		{name: "cache writes not cheaper than reads", cost: &config.ModelCost{Input: 1, CacheRead: 1, CacheWrite: 1}, want: 0, ok: true},
		{
			name: "service tier multiplier cancels out",
			cost: &config.ModelCost{Input: 1, CacheRead: 0.1, ServiceTierMultipliers: &config.ServiceTierMultipliers{Fast: 3}},
			tier: config.ServiceTierFast,
			want: 9, ok: true,
		},
		{
			name: "input tier above threshold is resolved",
			cost: &config.ModelCost{
				Input: 1, CacheRead: 0.1,
				InputTiers: []config.ModelCostInputTier{{AboveInputTokens: 1000, Input: 2, CacheRead: 0.1, CacheWrite: 2}},
			},
			tokens: 1001,
			want:   19, ok: true,
		},
		{
			name: "input tier at threshold keeps flat prices",
			cost: &config.ModelCost{
				Input: 1, CacheRead: 0.5,
				InputTiers: []config.ModelCostInputTier{{AboveInputTokens: 1000, Input: 2, CacheRead: 0.1, CacheWrite: 2}},
			},
			tokens: 1000,
			want:   1, ok: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := boundaryCacheMissPenaltyRatio(tt.cost, tt.tokens, tt.tier, tt.ttl)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if ok && math.Abs(got-tt.want) > 1e-9 {
				t.Fatalf("ratio = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReductionFlushHorizonStopsAtPendingCompaction(t *testing.T) {
	a := newBoundaryReductionTestAgent(t, &config.ModelCost{Input: 1, CacheRead: 0.02})
	for range 200 {
		a.recordLLMModelRun("p/m")
	}
	if got := a.reductionFlushHorizon(); got != reductionFlushHorizonRequests {
		t.Fatalf("horizon = %d; past requests must not inflate the future horizon", got)
	}
	a.autoCompactRequested.Store(true)
	if got := a.reductionFlushHorizon(); got != 0 {
		t.Fatalf("queued compaction horizon = %d, want 0", got)
	}
	a.autoCompactRequested.Store(false)
	a.compactionSlotActive.Store(true)
	if got := a.reductionFlushHorizon(); got != 0 {
		t.Fatalf("running compaction horizon = %d, want 0", got)
	}
}

func TestBoundaryFlushPenaltyRatioFallsBackWithoutPricing(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.runningModelRef = "p/m"
	if got := a.boundaryFlushPenaltyRatio(a.llmModelContinuitySnapshot(), 1000); got != cacheMissPenaltyRatioFallback {
		t.Fatalf("penalty ratio without pricing = %v, want fallback %v", got, cacheMissPenaltyRatioFallback)
	}
}

func TestBoundaryFlushPricingUsesExactProviderAndRequestTTL(t *testing.T) {
	cost := &config.ModelCost{Input: 1, CacheRead: 0.1, CacheWrite: 1.25, CacheWrite1h: 2}
	a := newBoundaryReductionTestAgent(t, cost)
	for _, tc := range []struct {
		mode, ttl string
		want      float64
	}{
		{mode: "explicit", want: 11.5}, {mode: "explicit", ttl: "5m", want: 11.5},
		{mode: "explicit", ttl: "1h", want: 19}, {mode: "auto", ttl: "1h", want: 19},
		{mode: "off", ttl: "1h", want: 0},
	} {
		model := config.ModelConfig{Cost: cost, PromptCache: &config.PromptCacheConfig{Mode: tc.mode, TTL: tc.ttl}}
		provider := llm.NewProviderConfig("p", config.ProviderConfig{Type: config.ProviderTypeMessages, APIURL: "https://example.invalid/v1/messages", Models: map[string]config.ModelConfig{"m": model}}, nil)
		a.llmClient = llm.NewClient(provider, nil, "m", 4096, "")
		got := a.boundaryFlushPenaltyRatio(a.llmModelContinuitySnapshot(), 1000)
		if math.Abs(got-tc.want) > 1e-9 {
			t.Fatalf("mode=%q ttl=%q: ratio=%v, want %v", tc.mode, tc.ttl, got, tc.want)
		}
	}
	a.llmClient = newTestLLMClient()
	a.projectConfig.Providers["p"] = config.ProviderConfig{Models: map[string]config.ModelConfig{"m": {}}}
	a.projectConfig.Providers["q"] = config.ProviderConfig{Models: map[string]config.ModelConfig{"m": {Cost: &config.ModelCost{Input: 1, CacheRead: 0.02}}}}
	if got := a.boundaryFlushPenaltyRatio(a.llmModelContinuitySnapshot(), 1000); got != cacheMissPenaltyRatioFallback {
		t.Fatalf("model p/m without cost borrowed q/m pricing: %v", got)
	}
}

func TestBoundaryFlushRespectsCheckpointAndCachedTail(t *testing.T) {
	for _, pending := range []bool{false, true} {
		a := newBoundaryReductionTestAgent(t, &config.ModelCost{Input: 1, CacheRead: 0.5})
		readContent := strings.Repeat("sample result line\n", 300)
		msgs := []message.Message{
			{Role: message.RoleUser, Content: "request"},
			{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "read", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.invalid/page"}`)}}},
			{Role: message.RoleTool, ToolCallID: "read", ToolStatus: message.ToolStatusSuccess, Content: readContent},
		}
		setTestRequestBatch(a, msgs, 1)
		if prepared := a.prepareMessagesForLLM(msgs); prepared[2].Content != readContent {
			t.Fatal("fresh result reduced before establishing the cache")
		}
		setTestRequestBatch(a, nil, 2)
		// This append is not part of the already cached tail. Charging it as a
		// rewrite penalty would defer an otherwise inexpensive reduction.
		msgs = append(msgs, message.Message{Role: message.RoleAssistant, Content: "next"}, message.Message{Role: message.RoleUser, Content: strings.Repeat("additional context ", 20000)})
		a.autoCompactRequested.Store(pending)
		prepared := a.prepareMessagesForLLM(msgs)
		if reduced := prepared[2].Content != readContent; reduced == pending {
			t.Fatalf("pending checkpoint=%v: cached result reduced=%v", pending, reduced)
		}
	}
}

func setTestRequestBatch(a *MainAgent, messages []message.Message, batch uint64) {
	a.requestBatches.mu.Lock()
	a.requestBatches.sessionEpoch = a.sessionEpoch
	a.requestBatches.sequence = batch
	a.requestBatches.mu.Unlock()
	for i := range messages {
		if messages[i].Role == message.RoleAssistant && len(messages[i].ToolCalls) > 0 && messages[i].RequestBatch == 0 {
			messages[i].RequestBatch = batch
			return
		}
	}
}

// TestPrepareMessagesDefersBoundaryReductions verifies that a small reduction
// inside the previously sent prefix is deferred (cache-stability wins) while
// new tail content is still reduced immediately.
func TestPrepareMessagesDefersBoundaryReductions(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			ReadLikeAgeTurns:     1,
			ReadLikeOutputBytes:  80,
			MinIncrementalTokens: 1 << 20,
		}},
	}
	a.runningModelRef = "p/m"
	a.recordLLMModelRun("p/m")
	a.recordLLMModelRun("p/m")
	setTestRequestBatch(a, nil, 1)

	readContent := strings.Repeat("line content for fetched page\n", 120)
	bigTail := strings.Repeat("user context that cannot be reduced ", 20000)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", RequestBatch: 1, ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/a"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: readContent},
		{Role: "assistant", Content: bigTail},
	}

	turnCtx, turnCancel := context.WithCancel(context.Background())
	defer turnCancel()
	a.turn = &Turn{ID: 1, Ctx: turnCtx, Cancel: turnCancel}
	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content != readContent {
		t.Fatalf("request 1 should not reduce a fresh webfetch result")
	}

	setTestRequestBatch(a, nil, 2)
	msgs = append(msgs,
		message.Message{Role: "assistant", Content: "done"},
		message.Message{Role: "user", Content: "u2"},
	)
	prepared = a.prepareMessagesForLLM(msgs)
	if prepared[2].Content != readContent {
		t.Fatalf("boundary reduction should be deferred for cache stability, got: %.80s", prepared[2].Content)
	}
	stats := a.GetContextReductionStats()
	if stats.SkippedByReason[contextReductionSkipDeferredCache] == 0 {
		t.Fatalf("expected deferred_for_cache skip, stats=%+v", stats)
	}

	a.resetLLMModelRun()
	a.recordLLMModelRun("q/m")
	prepared = a.prepareMessagesForLLM(msgs)
	if prepared[2].Content == readContent {
		t.Fatal("boundary reduction should flush when the cache is cold")
	}
}

func TestStableReductionSurfaceSurvivesUserTurnBoundary(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			ReadLikeAgeTurns:     1,
			ReadLikeOutputBytes:  80,
			MinIncrementalTokens: 1 << 20,
		}},
	}
	a.runningModelRef = "p/m"
	a.recordLLMModelRun("p/m")
	a.recordLLMModelRun("p/m")

	readContent := strings.Repeat("line content for fetched page\n", 120)
	bigTail := strings.Repeat("user context that cannot be reduced ", 20000)
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/a"}`)}}},
		{Role: message.RoleTool, ToolCallID: "tc1", Content: readContent},
		{Role: message.RoleAssistant, Content: bigTail},
	}
	setTestRequestBatch(a, msgs, 1)
	turnCtx, turnCancel := context.WithCancel(context.Background())
	defer turnCancel()
	a.turn = &Turn{ID: 1, Ctx: turnCtx, Cancel: turnCancel}
	if prepared := a.prepareMessagesForLLM(msgs); prepared[2].Content != readContent {
		t.Fatal("fresh webfetch result was unexpectedly reduced")
	}

	a.clearReductionCache(false)
	setTestRequestBatch(a, nil, 2)
	msgs = append(msgs,
		message.Message{Role: message.RoleAssistant, Content: "done"},
		message.Message{Role: message.RoleUser, Content: "u2"},
	)
	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content != readContent {
		t.Fatal("turn boundary discarded the stable reduction surface")
	}
	if a.GetContextReductionStats().SkippedByReason[contextReductionSkipDeferredCache] == 0 {
		t.Fatal("expected boundary reduction to stay deferred across the turn boundary")
	}
}

func TestDeferredBoundaryReductionDoesNotReportOverCompression(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			ReadLikeAgeTurns:     1,
			ReadLikeOutputBytes:  80,
			MinIncrementalTokens: 1 << 20,
		}},
	}
	a.runningModelRef = "p/m"
	a.recordLLMModelRun("p/m")
	a.recordLLMModelRun("p/m")

	readArgs := json.RawMessage(`{"url":"https://example.com/a"}`)
	readContent := strings.Repeat("line content for fetched page\n", 120)
	bigTail := strings.Repeat("user context that cannot be reduced ", 20000)
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameWebFetch, Args: readArgs}}},
		{Role: message.RoleTool, ToolCallID: "tc1", Content: readContent},
		{Role: message.RoleAssistant, Content: bigTail},
	}
	setTestRequestBatch(a, msgs, 1)
	turnCtx, turnCancel := context.WithCancel(context.Background())
	defer turnCancel()
	a.turn = &Turn{ID: 1, Ctx: turnCtx, Cancel: turnCancel}
	a.prepareMessagesForLLM(msgs)

	setTestRequestBatch(a, nil, 2)
	msgs = append(msgs,
		message.Message{Role: message.RoleAssistant, Content: "done"},
		message.Message{Role: message.RoleUser, Content: "u2"},
		message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "tc2", Name: tools.NameWebFetch, Args: readArgs}}},
		message.Message{Role: message.RoleTool, ToolCallID: "tc2", Content: "fresh reread"},
	)
	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content != readContent {
		t.Fatal("boundary reduction should remain deferred")
	}
	if got := a.GetContextReductionStats().OverCompression[contextReductionOverCompressionReread]; got != 0 {
		t.Fatalf("deferred reduction reported reread over-compression = %d", got)
	}
}

// TestBoundaryReductionIgnoresContextUsage verifies that input usage cannot
// change request-level reduction. A warm cached prefix remains deferred even
// when the current context manager reports an oversized prompt; compaction owns
// pressure-driven size control.
func TestBoundaryReductionIgnoresContextUsage(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			ReadLikeAgeTurns:     1,
			ReadLikeOutputBytes:  80,
			MinIncrementalTokens: 1 << 20,
		}},
	}
	a.runningModelRef = "p/m"
	a.recordLLMModelRun("p/m")
	a.recordLLMModelRun("p/m")

	readContent := strings.Repeat("line content for fetched page\n", 120)
	bigTail := strings.Repeat("user context that cannot be reduced ", 20000)
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/a"}`)}}},
		{Role: message.RoleTool, ToolCallID: "tc1", Content: readContent},
		{Role: message.RoleAssistant, Content: bigTail},
	}
	setTestRequestBatch(a, msgs, 1)
	turnCtx, turnCancel := context.WithCancel(context.Background())
	defer turnCancel()
	a.turn = &Turn{ID: 1, Ctx: turnCtx, Cancel: turnCancel}
	if prepared := a.prepareMessagesForLLM(msgs); prepared[2].Content != readContent {
		t.Fatal("fresh webfetch result was unexpectedly reduced")
	}

	setTestRequestBatch(a, nil, 2)
	msgs = append(msgs,
		message.Message{Role: message.RoleAssistant, Content: "done"},
		message.Message{Role: message.RoleUser, Content: "u2"},
	)
	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content != readContent {
		t.Fatal("context usage changed the reduction surface")
	}
	if got := a.GetContextReductionStats().SkippedByReason[contextReductionSkipDeferredCache]; got == 0 {
		t.Fatal("expected warm boundary reduction to remain deferred")
	}
}

// TestBoundaryReductionFlushesWhenSavingsAmortize pins the amortization flush
// condition: with a warm cache and low pressure, a boundary reduction whose
// pending savings dominate the re-billed tail is applied instead of deferred.
func TestBoundaryReductionFlushesWhenSavingsAmortize(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			ReadLikeAgeTurns:     1,
			ReadLikeOutputBytes:  80,
			MinIncrementalTokens: 1 << 20,
		}},
	}
	a.runningModelRef = "p/m"
	a.recordLLMModelRun("p/m")
	a.recordLLMModelRun("p/m")

	// A huge reducible read result followed by a tiny tail: the savings
	// amortize the re-billing well inside the flush horizon
	// (savings*30 >= 9*tail), unlike the deferral tests where an
	// irreducible tail dominates.
	readContent := strings.Repeat("line content for fetched page\n", 3000)
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/a"}`)}}},
		{Role: message.RoleTool, ToolCallID: "tc1", Content: readContent},
		{Role: message.RoleAssistant, Content: "short reply"},
	}
	setTestRequestBatch(a, msgs, 1)
	turnCtx, turnCancel := context.WithCancel(context.Background())
	defer turnCancel()
	a.turn = &Turn{ID: 1, Ctx: turnCtx, Cancel: turnCancel}
	if prepared := a.prepareMessagesForLLM(msgs); prepared[2].Content != readContent {
		t.Fatal("fresh webfetch result was unexpectedly reduced")
	}

	setTestRequestBatch(a, nil, 2)
	msgs = append(msgs,
		message.Message{Role: message.RoleAssistant, Content: "done"},
		message.Message{Role: message.RoleUser, Content: "u2"},
	)
	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content == readContent {
		t.Fatal("amortized savings should flush the boundary reduction")
	}
	if got := a.GetContextReductionStats().SkippedByReason[contextReductionSkipDeferredCache]; got != 0 {
		t.Fatalf("amortized flush still recorded deferred_for_cache = %d", got)
	}
}

// TestPrepareMessagesDefersExternalReadInvalidation pins the read half of the
// boundary rule: an invalidated read that is still only in the sent prefix is
// a boundary proposal like any other, so cache stability defers it while the
// amortization gate is unsatisfied and a cold cache flushes it. Lazy stat
// validation detects the out-of-band edit; no tool call names it.
func TestPrepareMessagesDefersExternalReadInvalidation(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "a.go")
	if err := os.WriteFile(path, []byte("package main\n\nconst value = 1\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a := newTestMainAgent(t, projectRoot)
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			ReadLikeAgeTurns:     1,
			ReadLikeOutputBytes:  40,
			MinIncrementalTokens: 1 << 20,
		}},
	}
	a.runningModelRef = "p/m"
	a.recordLLMModelRun("p/m")
	a.recordLLMModelRun("p/m")

	readContent := "READ_RESULT lines=1-100 total=100\n" + strings.Repeat("source line\n", 100)
	bigTail := strings.Repeat("user context that cannot be reduced ", 20000)
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{{ID: "read", Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "read", ToolStatus: "success", Content: readContent, FileState: readFileStateForTest(path)},
		{Role: message.RoleAssistant, Content: bigTail},
	}
	setTestRequestBatch(a, msgs, 1)
	turnCtx, turnCancel := context.WithCancel(context.Background())
	defer turnCancel()
	a.turn = &Turn{ID: 1, Ctx: turnCtx, Cancel: turnCancel}
	if prepared := a.prepareMessagesForLLM(msgs); prepared[2].Content != readContent {
		t.Fatalf("a valid read must stay full on request 1: %.80s", prepared[2].Content)
	}

	// Out-of-band write, no new tool call: lazy validation flags the read.
	if err := os.WriteFile(path, []byte("package main\n\nconst value = 2\n"), 0o644); err != nil {
		t.Fatalf("WriteFile changed: %v", err)
	}
	setTestRequestBatch(a, nil, 2)
	msgs = append(msgs,
		message.Message{Role: message.RoleAssistant, Content: "done"},
		message.Message{Role: message.RoleUser, Content: "u2"},
	)
	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content != readContent {
		t.Fatalf("external read invalidation should defer for cache stability, got: %.80s", prepared[2].Content)
	}
	stats := a.GetContextReductionStats()
	if stats.SkippedByReason[contextReductionSkipDeferredCache] == 0 {
		t.Fatalf("expected deferred_for_cache skip, stats=%+v", stats)
	}

	a.resetLLMModelRun()
	a.recordLLMModelRun("q/m")
	prepared = a.prepareMessagesForLLM(msgs)
	if !strings.Contains(prepared[2].Content, "truncated="+tools.ReadTruncatedStale) {
		t.Fatalf("cold cache should render the stale marker, got: %.80s", prepared[2].Content)
	}
}

// TestPrepareMessagesKeepsFrozenReadMarkerWhenSuperseded pins the other read
// branch: a frozen marker that already carries the stale/superseded guidance
// must not be re-rendered when the read flips to superseded, because that
// rewrites cached bytes without changing what the model can act on.
func TestPrepareMessagesKeepsFrozenReadMarkerWhenSuperseded(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "a.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a := newTestMainAgent(t, projectRoot)
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			ReadLikeAgeTurns:     1,
			ReadLikeOutputBytes:  40,
			MinIncrementalTokens: 1,
		}},
	}
	a.runningModelRef = "p/m"
	a.recordLLMModelRun("p/m")
	a.recordLLMModelRun("p/m")

	readContent := "READ_RESULT lines=1-100 total=100\n" + strings.Repeat("source line\n", 100)
	firstMessages := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{{ID: "r1", Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "r1", ToolStatus: "success", Content: readContent, FileState: readFileStateForTest(path)},
		{Role: message.RoleAssistant, RequestBatch: 2, ToolCalls: []message.ToolCall{{ID: "r2", Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "r2", ToolStatus: "success", Content: readContent, FileState: readFileStateForTest(path)},
	}
	setTestRequestBatch(a, firstMessages, 2)
	turnCtx, turnCancel := context.WithCancel(context.Background())
	defer turnCancel()
	a.turn = &Turn{ID: 1, Ctx: turnCtx, Cancel: turnCancel}
	first := a.prepareMessagesForLLM(firstMessages)
	if !strings.Contains(first[2].Content, "truncated="+tools.ReadTruncatedSuperseded) {
		t.Fatalf("first request did not render the superseded marker: %q", first[2].Content)
	}

	// A later edit invalidates the same read as well. The frozen marker already
	// carries the superseded guidance, so it must not be rewritten to the stale
	// shape: the model cannot act differently on the two, and the rewrite would
	// re-bill the cached prefix.
	messages := append(append([]message.Message(nil), firstMessages...),
		message.Message{Role: message.RoleAssistant, RequestBatch: 3, ToolCalls: []message.ToolCall{{ID: "e1", Name: tools.NameEdit, Args: json.RawMessage(`{"path":"a.go","old_string":"x","new_string":"y"}`)}}},
		message.Message{Role: message.RoleTool, ToolCallID: "e1", ToolStatus: "success", Content: "edited"},
	)
	setTestRequestBatch(a, messages, 3)
	second := a.prepareMessagesForLLM(messages)
	if second[2].Content != first[2].Content {
		t.Fatalf("frozen read marker rewritten after its validity widened:\nfirst:  %q\nsecond: %q", first[2].Content, second[2].Content)
	}
}

// TestContextNoticeRowKeepsStableSurfaceReusable pins the cache side of the
// context-pressure notice policy: the row that records a pressure cycle is
// appended at the tail of the durable history, so its arrival must leave the
// recorded stable surface prefix-compatible. A row inserted before the frozen
// prefix (or a durable rewrite) would fail compatibility and make every later
// request in the same window pay a full reduction scan.
func TestContextNoticeRowKeepsStableSurfaceReusable(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			ReadLikeAgeTurns:     1,
			ReadLikeOutputBytes:  80,
			MinIncrementalTokens: 1 << 20,
		}},
	}
	a.runningModelRef = "p/m"
	a.recordLLMModelRun("p/m")

	readContent := strings.Repeat("line content for fetched page\n", 120)
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/a"}`)}}},
		{Role: message.RoleTool, ToolCallID: "tc1", Content: readContent},
		{Role: message.RoleAssistant, Content: "done"},
	}
	setTestRequestBatch(a, nil, 2)
	turnCtx, turnCancel := context.WithCancel(context.Background())
	defer turnCancel()
	a.turn = &Turn{ID: 1, Ctx: turnCtx, Cancel: turnCancel}

	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content == readContent {
		t.Fatal("aged webfetch result should be reduced on request 1")
	}
	a.updatePreparedLLMRequestSurface(a.currentTurnID(), prepared)

	notice := message.Message{
		Role:            message.RoleUser,
		Kind:            message.KindContextNotice,
		NoticeLevel:     contextNoticePressure,
		PressureCycleID: 1,
		Content:         "<system-reminder>\nThe context is approaching the configured automatic-compaction threshold.\n</system-reminder>",
	}
	setTestRequestBatch(a, nil, 3)
	prepared2 := a.prepareMessagesForLLM(append(append([]message.Message(nil), msgs...), notice))
	if stats := a.GetContextReductionStats(); !stats.ReusedStable {
		t.Fatalf("stable surface should be reused after the durable notice row, stats=%+v", stats)
	}
	if prepared2[2].Content != prepared[2].Content {
		t.Fatal("frozen reduced read marker should stay byte-stable across requests")
	}
}

// TestBoundaryReductionDefersDeepRewriteAtRelayPricing pins the pricing-derived
// penalty: at a 50:1 cache-read price the rewrite of a long tail no longer
// amortizes within the conservative horizon, so a proposal the historical 10:1
// constant would have flushed stays deferred, including on a long model run.
func TestBoundaryReductionDefersDeepRewriteAtRelayPricing(t *testing.T) {
	a := newBoundaryReductionTestAgent(t, &config.ModelCost{Input: 1, CacheRead: 0.02})

	readContent := strings.Repeat("line content for fetched page\n", 3000)
	// The irreducible tail makes the rewrite expensive at this cache price.
	tailContent := strings.Repeat("user context that cannot be reduced ", 1500)
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/a"}`)}}},
		{Role: message.RoleTool, ToolCallID: "tc1", Content: readContent},
		{Role: message.RoleAssistant, Content: tailContent},
	}
	setTestRequestBatch(a, msgs, 1)
	if prepared := a.prepareMessagesForLLM(msgs); prepared[2].Content != readContent {
		t.Fatal("fresh webfetch result was unexpectedly reduced")
	}

	setTestRequestBatch(a, nil, 2)
	msgs = append(msgs,
		message.Message{Role: message.RoleAssistant, Content: "done"},
		message.Message{Role: message.RoleUser, Content: "u2"},
	)
	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content != readContent {
		t.Fatal("50:1 pricing should defer the deep rewrite at the conservative horizon")
	}
	if got := a.GetContextReductionStats().SkippedByReason[contextReductionSkipDeferredCache]; got == 0 {
		t.Fatal("expected deferred_for_cache skip under 50:1 pricing")
	}

	// Past requests do not extend the remaining reuse period.
	for range 110 {
		a.recordLLMModelRun("p/m")
	}
	prepared = a.prepareMessagesForLLM(msgs)
	if prepared[2].Content != readContent {
		t.Fatal("a long model run must not speculate on a longer future reuse period")
	}

	// A model switch invalidates the cache anyway: the rewrite is free and the
	// flush must not wait for any amortization.
	a.resetLLMModelRun()
	a.recordLLMModelRun("q/m")
	prepared = a.prepareMessagesForLLM(msgs)
	if prepared[2].Content == readContent {
		t.Fatal("cold cache should flush the boundary rewrite regardless of pricing")
	}
}

// TestBoundaryReductionFlushesSoonerUnderTwoToOneCaching pins the other side of
// the pricing range: at a 2:1 cache-read price the rewrite penalty is small, so
// a proposal the 10:1 constant deferred flushes immediately.
func TestBoundaryReductionFlushesSoonerUnderTwoToOneCaching(t *testing.T) {
	a := newBoundaryReductionTestAgent(t, &config.ModelCost{Input: 1, CacheRead: 0.5})

	readContent := strings.Repeat("line content for fetched page\n", 3000)
	tailContent := strings.Repeat("user context that cannot be reduced ", 20000)
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/a"}`)}}},
		{Role: message.RoleTool, ToolCallID: "tc1", Content: readContent},
		{Role: message.RoleAssistant, Content: tailContent},
	}
	setTestRequestBatch(a, msgs, 1)
	if prepared := a.prepareMessagesForLLM(msgs); prepared[2].Content != readContent {
		t.Fatal("fresh webfetch result was unexpectedly reduced")
	}

	setTestRequestBatch(a, nil, 2)
	msgs = append(msgs,
		message.Message{Role: message.RoleAssistant, Content: "done"},
		message.Message{Role: message.RoleUser, Content: "u2"},
	)
	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content == readContent {
		t.Fatal("2:1 pricing should flush a rewrite the 10:1 constant deferred")
	}
	if got := a.GetContextReductionStats().SkippedByReason[contextReductionSkipDeferredCache]; got != 0 {
		t.Fatalf("2:1 flush still recorded deferred_for_cache = %d", got)
	}
}
