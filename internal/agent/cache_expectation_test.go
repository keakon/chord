package agent

import (
	"github.com/keakon/chord/internal/agent/promptcache"
	"github.com/keakon/chord/internal/tools"

	"testing"
	"time"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func TestObservedCacheHitAcceptsSplitInputUsage(t *testing.T) {
	a := &MainAgent{llmClient: newTestLLMClient(), tools: tools.NewRegistry(), cacheHitTracker: promptcache.NewHitTracker()}
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

// Transient tail overlays sit after the cache boundary and never enter the
// cacheable prefix, so replacing them with new durable messages on the next
// request must read as an append, not as a chord-side prefix rewrite.
func TestNoteCacheExpectationIgnoresTailOverlayChurn(t *testing.T) {
	a := &MainAgent{llmClient: newTestLLMClient(), tools: tools.NewRegistry(), cacheHitTracker: promptcache.NewHitTracker()}
	cfg := llm.NewProviderConfig("p", config.ProviderConfig{Type: config.ProviderTypeResponses, Models: map[string]config.ModelConfig{"gpt-6.1-sol": {}}}, []string{"test-key"})
	a.llmClient = llm.NewClient(cfg, &recordingLoopTuningProvider{}, "gpt-6.1-sol", 4096, "sys")
	hash := a.computeToolDefinitionHash()
	durable := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", Content: "a1"},
	}
	withOverlay := append(append([]message.Message(nil), durable...),
		message.Message{Role: "user", Kind: message.KindTurnOverlay, Content: "<system-reminder>turn hint</system-reminder>"})

	a.noteCacheExpectation("p/gpt-6.1-sol", withOverlay, 1, hash, time.Now(), nil)

	grown := append(append([]message.Message(nil), durable...),
		message.Message{Role: "assistant", Content: "a2"},
		message.Message{Role: "user", Content: "u2"})
	diag := a.noteCacheExpectation("p/gpt-6.1-sol", grown, 0, hash, time.Now(), nil)
	if diag["cache_prefix_divergence"] != "1" || diag["cache_divergence_kind"] != "append" {
		t.Fatalf("overlay churn diag = %v", diag)
	}
	if diag["cache_outcome"] == "local_rewrite" {
		t.Fatalf("overlay churn misread as local rewrite: %v", diag)
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
	if diag["cache_outcome"] != "local_rewrite" || diag["cache_outcome_reason"] != "system_prompt_changed" {
		t.Fatalf("system prompt change outcome diag = %v", diag)
	}

	a.activateLoadedSession(&loadedSessionState{
		SessionPath: t.TempDir(),
		Messages:    []message.Message{{Role: message.RoleUser, Content: "new session"}},
	})
	if a.cacheExpectations.Warm("p/m", time.Now()) {
		t.Fatal("new session inherited the previous session's warm cache signal")
	}
	if _, ok := a.cacheHitTracker.HitRate("p/m"); ok {
		t.Fatal("new session inherited the previous session's cache hit observations")
	}
}

func TestNoteCacheExpectationKeepsOverlayWithoutBoundaryContract(t *testing.T) {
	a := &MainAgent{llmClient: newTestLLMClient(), tools: tools.NewRegistry(), cacheHitTracker: promptcache.NewHitTracker()}
	hash := a.computeToolDefinitionHash()
	first := []message.Message{{Role: message.RoleUser, Content: "request"}, {Role: message.RoleUser, Kind: message.KindTurnOverlay, Content: "hint"}}
	a.noteCacheExpectation("p/m", first, 1, hash, time.Now(), nil)
	next := []message.Message{{Role: message.RoleUser, Content: "request"}, {Role: message.RoleAssistant, Content: "response"}, {Role: message.RoleUser, Kind: message.KindTurnOverlay, Content: "hint"}}
	diag := a.noteCacheExpectation("p/m", next, 1, hash, time.Now(), &message.TokenUsage{CacheReadTokens: 1, CacheWriteTokens: 2000})
	if diag["cache_outcome"] != "local_rewrite" || diag["cache_prev_messages"] != "2" {
		t.Fatalf("unmarked overlay was hidden: %v", diag)
	}
}
