package agent

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
)

func TestHostedAdmissionConcurrencyAndCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newResourceGovernor(config.OrchestrationConfig{MaxActiveLLMRequests: 4, ProviderMaxActiveHostedRequests: map[string]int{"sample": 1}})
		release, err := g.acquireHostedLLM(t.Context(), "sample/model-1", false)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			r, err := g.acquireHostedLLM(ctx, "sample/model-2", false)
			if r != nil {
				r.release(false)
			}
			done <- err
		}()
		synctest.Wait()
		if got := g.snapshot(); got.LLMActive != 1 || got.LLMQueued != 1 {
			t.Fatalf("snapshot = %+v", got)
		}
		// A hosted-only limit must not block ordinary requests or other providers.
		chat, err := g.acquireLLM(t.Context(), "sample/model-2")
		if err != nil {
			t.Fatal(err)
		}
		chat()
		other, err := g.acquireHostedLLM(t.Context(), "other/model-1", false)
		if err != nil {
			t.Fatal(err)
		}
		other.release(false)
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
		release.release(false)
		release.release(false)
		if got := g.snapshot(); got.LLMActive != 0 || got.LLMQueued != 0 {
			t.Fatalf("leaked capacity: %+v", got)
		}
		if g.hosted.active["sample"] != 0 {
			t.Fatal("hosted slot leaked")
		}
	})
}

func TestHostedAdmissionRollingWindowAndRetryBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newResourceGovernor(config.OrchestrationConfig{ProviderHostedRequestsPerMinute: map[string]int{"sample": 3}, ProviderHostedRetriesPerMinute: map[string]int{"sample": 1}})
		run := func(retry bool) error {
			r, err := g.acquireHostedLLM(t.Context(), "sample/model-1", retry)
			if r != nil {
				r.release(false)
			}
			return err
		}
		if err := run(false); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Second)
		if err := run(true); err != nil {
			t.Fatal(err)
		}
		if e, ok := errors.AsType[*llm.HostedAdmissionError](run(true)); !ok || !e.RetryBudget {
			t.Fatalf("retry error = %v", e)
		}
		if err := run(false); err != nil {
			t.Fatal(err)
		}
		// Exhausted retry budget is terminal even when the request window is full.
		if e, ok := errors.AsType[*llm.HostedAdmissionError](run(true)); !ok || !e.RetryBudget {
			t.Fatalf("retry must not wait for the request window: %v", e)
		}
		if e, ok := errors.AsType[*llm.HostedAdmissionError](run(false)); !ok || e.RetryBudget || time.Until(e.RetryAt) != 50*time.Second {
			t.Fatalf("rate error = %+v", e)
		}
		time.Sleep(50 * time.Second)
		if err := run(false); err != nil {
			t.Fatalf("first reservation did not expire: %v", err)
		}
		if err := run(false); err == nil {
			t.Fatal("rolling window was reset wholesale")
		}
		time.Sleep(10 * time.Second)
		if err := run(true); err != nil {
			t.Fatal(err)
		}
	})
}

func TestHostedAdmissionUsesNormalizedProviderKeys(t *testing.T) {
	g := newResourceGovernor(config.OrchestrationConfig{ProviderHostedRequestsPerMinute: map[string]int{" sample ": 1, "disabled": 0}})
	release, err := g.acquireHostedLLM(t.Context(), "sample/model-1", false)
	if err != nil {
		t.Fatal(err)
	}
	release.release(false)
	if _, err := g.acquireHostedLLM(t.Context(), "sample/model-2", false); err == nil {
		t.Fatal("whitespace in the provider limit key bypassed the rate limit")
	}
	for range 2 {
		release, err := g.acquireHostedLLM(t.Context(), "disabled/model-1", false)
		if err != nil {
			t.Fatalf("non-positive limit should be disabled: %v", err)
		}
		release.release(false)
	}
}

func TestHostedAdmissionCancelledQueueDoesNotSpendQuota(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newResourceGovernor(config.OrchestrationConfig{MaxActiveLLMRequests: 1, ProviderHostedRequestsPerMinute: map[string]int{"sample": 1}})
		release, err := g.acquireLLM(t.Context(), "other/model-1")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if _, err := g.acquireHostedLLM(ctx, "sample/model-1", false); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		release()
		first, err := g.acquireHostedLLM(t.Context(), "sample/model-1", false)
		if err != nil {
			t.Fatalf("cancelled waiter consumed quota: %v", err)
		}
		first.release(false)
	})
}

func TestHostedAdmissionEffectiveConfig(t *testing.T) {
	global := &config.Config{Orchestration: config.OrchestrationConfig{
		ProviderMaxActiveHostedRequests: map[string]int{"sample": 2, "other": 1},
		ProviderHostedRequestsPerMinute: map[string]int{"sample": 10, "other": 5},
		ProviderHostedRetriesPerMinute:  map[string]int{"sample": 3, "other": 2},
	}}
	project := &config.Config{Orchestration: config.OrchestrationConfig{
		ProviderMaxActiveHostedRequests: map[string]int{"sample": 0},
		ProviderHostedRequestsPerMinute: map[string]int{"sample": 0},
		ProviderHostedRetriesPerMinute:  map[string]int{"sample": 0},
	}}
	cfg := effectiveOrchestrationConfig(global, project)
	g := newResourceGovernor(cfg)
	for _, limits := range []map[string]int{g.hosted.activeLimits, g.hosted.requestLimits, g.hosted.retryLimits} {
		if limits["sample"] != 0 || limits["other"] <= 0 {
			t.Fatalf("limits=%v", limits)
		}
	}
	if global.Orchestration.ProviderHostedRequestsPerMinute["sample"] != 10 {
		t.Fatal("merge mutated global config")
	}
}
