package agent

import (
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
)

func TestCacheHitTrackerRollingRate(t *testing.T) {
	tr := newCacheHitTracker()
	if _, ok := tr.HitRate("p/m"); ok {
		t.Fatal("expected no rate before observations")
	}
	tr.Observe("p/m", 100000, 90000)
	tr.Observe("p/m", 100000, 90000)
	if _, ok := tr.HitRate("p/m"); ok {
		t.Fatal("expected no rate below min observations")
	}
	tr.Observe("p/m", 100000, 90000)
	rate, ok := tr.HitRate("p/m")
	if !ok || rate < 0.89 || rate > 0.91 {
		t.Fatalf("rate = %v ok=%v, want ~0.9", rate, ok)
	}
	// A run of misses drags the rate down quickly.
	for range 10 {
		tr.Observe("p/m", 100000, 0)
	}
	rate, _ = tr.HitRate("p/m")
	if rate > 0.4 {
		t.Fatalf("rate after misses = %v, want < 0.4", rate)
	}
	// Cache read above input is clamped.
	tr.Observe("q/m", 1000, 5000)
	tr.Observe("q/m", 1000, 5000)
	tr.Observe("q/m", 1000, 5000)
	if rate, _ := tr.HitRate("q/m"); rate > 1 {
		t.Fatalf("clamped rate = %v, want <= 1", rate)
	}
}

func TestNoteCacheExpectationAttributesDivergence(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	base := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", Content: "a1"},
	}
	hash := a.computeToolDefinitionHash()

	diag := a.noteCacheExpectation("p/m", base, hash)
	if diag["cache_first_request"] != "true" {
		t.Fatalf("first request diag = %v", diag)
	}

	// Append-only growth: divergence at the old length, expectation covers the
	// full previous request.
	grown := append(append([]message.Message(nil), base...), message.Message{Role: "user", Content: "u2"})
	diag = a.noteCacheExpectation("p/m", grown, hash)
	if diag["cache_prefix_divergence"] != "2" || diag["cache_divergence_kind"] != "append" {
		t.Fatalf("append diag = %v", diag)
	}
	if diag["cache_expected_tokens"] == "0" {
		t.Fatalf("expected nonzero cache expectation, diag = %v", diag)
	}

	// In-place rewrite of an early message: divergence at that index.
	mutated := append([]message.Message(nil), grown...)
	mutated[0].Content = "u1 rewritten"
	diag = a.noteCacheExpectation("p/m", mutated, hash)
	if diag["cache_prefix_divergence"] != "0" || diag["cache_divergence_kind"] != "rewrite" {
		t.Fatalf("rewrite diag = %v", diag)
	}

	// A different ref tracks its own expectation independently.
	diag = a.noteCacheExpectation("q/m", mutated, hash)
	if diag["cache_first_request"] != "true" {
		t.Fatalf("other-ref diag = %v", diag)
	}
}

func TestCacheExpectationInvalidatesAcrossPromptAndSessionBoundaries(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	msgs := []message.Message{{Role: message.RoleUser, Content: "same user message"}}
	hash := a.computeToolDefinitionHash()

	a.installSystemPrompt("first system prompt")
	a.noteCacheExpectation("p/m", msgs, hash)
	a.installSystemPrompt("different system prompt")
	diag := a.noteCacheExpectation("p/m", msgs, hash)
	if diag["cache_expected_tokens"] != "0" || diag["cache_system_prompt_changed"] != "true" {
		t.Fatalf("system prompt change did not invalidate expectation: %v", diag)
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
