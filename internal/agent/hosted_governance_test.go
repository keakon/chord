package agent

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestHostedPoolTriesFallbackBeforeRetry(t *testing.T) {
	for _, status := range []int{429, 503} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			first := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) {
				return nil, &llm.APIError{StatusCode: status, RetryAfter: time.Hour}
			}}
			second := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) { return hostedWebSearchResponse(), nil }}
			setHostedTestPool(a, hostedRetryTarget("first", []string{"key"}, first), hostedRetryTarget("second", []string{"key"}, second))
			var events []analytics.UsageEvent
			a.SetUsageEventSink(func(e analytics.UsageEvent) { events = append(events, e) })
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			_, err := newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil)).Run(ctx, tools.NameWebSearch, map[string]any{"query": "sample"})
			if err != nil || first.callCount() != 1 || second.callCount() != 1 {
				t.Fatalf("calls=%d/%d err=%v", first.callCount(), second.callCount(), err)
			}
			if len(events) != 2 || events[1].Diagnostic["retry"] != "false" || events[1].Diagnostic["request_reason"] != hostedAttemptFallback {
				t.Fatalf("events=%+v", events)
			}
		})
	}
}

func TestHostedPoolRoundsAndTerminalTargets(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	var order []string
	makeTarget := func(name string, terminal bool) llm.FallbackModel {
		return hostedRetryTarget(name, []string{"key"}, &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) {
			order = append(order, name)
			if terminal {
				return &message.Response{Content: "Sample answer"}, nil
			}
			return nil, &llm.APIError{StatusCode: 503}
		}})
	}
	setHostedTestPool(a, makeTarget("first", false), makeTarget("terminal", true), makeTarget("second", false))
	_, err := newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil)).Run(t.Context(), tools.NameWebSearch, map[string]any{"query": "sample"})
	if err == nil || !reflect.DeepEqual(order, []string{"first", "terminal", "second", "first", "second", "first", "second"}) {
		t.Fatalf("order=%v err=%v", order, err)
	}
}

func TestHostedLocalLimitsPreserveFallback(t *testing.T) {
	for _, budget := range []bool{false, true} {
		t.Run(map[bool]string{false: "rate", true: "retry_budget"}[budget], func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			cfg := config.OrchestrationConfig{ProviderHostedRequestsPerMinute: map[string]int{"first": 1}}
			if budget {
				cfg = config.OrchestrationConfig{ProviderHostedRetriesPerMinute: map[string]int{"first": 1}}
			}
			a.governor = newResourceGovernor(cfg)
			release, err := a.governor.acquireHostedLLM(t.Context(), "first/model-1", budget)
			if err != nil {
				t.Fatal(err)
			}
			release.release(false)
			first := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) { return nil, &llm.APIError{StatusCode: 503} }}
			second := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) { return hostedWebSearchResponse(), nil }}
			setHostedTestPool(a, hostedRetryTarget("first", []string{"key-1", "key-2"}, first), hostedRetryTarget("second", []string{"key"}, second))
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			_, err = newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil)).Run(ctx, tools.NameWebSearch, map[string]any{"query": "sample"})
			want := 0
			if budget {
				want = 1
			}
			if err != nil || first.callCount() != want || second.callCount() != 1 {
				t.Fatalf("calls=%d/%d err=%v", first.callCount(), second.callCount(), err)
			}
		})
	}
}

func TestHostedLocalAdmissionIsNotUnknown(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.governor = newResourceGovernor(config.OrchestrationConfig{ProviderHostedRequestsPerMinute: map[string]int{"sample": 1}})
	release, err := a.governor.acquireHostedLLM(t.Context(), "sample/model-1", false)
	if err != nil {
		t.Fatal(err)
	}
	release.release(false)
	provider := &hostedScriptProvider{}
	target := hostedRetryTarget("sample", []string{"key"}, provider)
	setHostedTestPool(a, target)
	b := newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil)).(*hostedBackend)
	caller, err := b.resolveCaller("", nil)
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := b.catalogTool(tools.NameWebSearch)
	spec.RetrySafe = false
	_, err = b.runAttempt(t.Context(), caller, target, spec, map[string]any{"query": "sample"}, false)
	if _, ok := errors.AsType[*llm.HostedAdmissionError](err); !ok {
		t.Fatalf("error=%v", err)
	}
	if _, ok := errors.AsType[*llm.HostedOutcomeUnknownError](err); ok {
		t.Fatalf("unknown=%v", err)
	}
	// Removing the local rate gate must leave the same credential immediately usable.
	a.governor = newResourceGovernor(config.OrchestrationConfig{})
	_, _ = b.runAttempt(t.Context(), caller, target, spec, map[string]any{"query": "sample"}, false)
	if provider.callCount() != 1 {
		t.Fatalf("calls=%d", provider.callCount())
	}
}

func TestHostedHealthRecoveryAndProbeOwnership(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := newTestMainAgent(t, t.TempDir())
		impl := &hostedScriptProvider{respond: func(i int, _ context.Context) (*message.Response, error) {
			if i < 2 {
				return &message.Response{Content: "Sample answer"}, nil
			}
			return hostedWebSearchResponse(), nil
		}}
		target := hostedRetryTarget("sample", []string{"key"}, impl)
		setHostedTestPool(a, target)
		b := newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil)).(*hostedBackend)
		run := func() error {
			_, err := b.Run(t.Context(), tools.NameWebSearch, map[string]any{"query": "sample"})
			return err
		}
		for range 3 {
			if run() == nil {
				t.Fatal("missing receipt accepted")
			}
		}
		if impl.callCount() != 2 {
			t.Fatalf("cooldown calls=%d", impl.callCount())
		}
		time.Sleep(hostedNoCallCooldown)
		if err := run(); err != nil {
			t.Fatal(err)
		}
		if err := run(); err != nil {
			t.Fatal(err)
		}
		if impl.callCount() != 4 || len(b.health) != 0 {
			t.Fatal("successful probe did not restore health")
		}
		caller, err := b.resolveCaller("", nil)
		if err != nil {
			t.Fatal(err)
		}
		// A named pool remains shared after its caller exits.
		key := hostedHealthKey{source: hostedRouteSource{kind: hostedRouteSourcePool, id: "tools"}, tool: tools.NameWebSearch, provider: target.ProviderConfig, model: target.ModelID}
		old, _ := b.acquireHostedProbe(key)
		failure := &llm.HostedCallNotObservedError{Tool: tools.NameWebSearch}
		b.finishHostedProbe(caller, key, old, failure)
		b.finishHostedProbe(caller, key, old, failure)
		time.Sleep(hostedNoCallCooldown)
		probe, err := b.acquireHostedProbe(key)
		if err != nil {
			t.Fatal("probe denied")
		}
		if _, err := b.acquireHostedProbe(key); err == nil {
			t.Fatal("parallel probe admitted")
		}
		b.finishHostedProbe(caller, key, old, nil)
		if _, err := b.acquireHostedProbe(key); err == nil {
			t.Fatal("old completion released current probe")
		}
		b.mu.Lock()
		b.forgetCallerLocked(caller.id)
		b.mu.Unlock()
		b.finishHostedProbe(caller, key, probe, nil)
		next, err := b.acquireHostedProbe(key)
		if err != nil {
			t.Fatal("retired caller leaked probe")
		}
		b.mu.Lock()
		clear(b.health)
		b.mu.Unlock()
		b.finishHostedProbe(caller, key, next, failure)
		if len(b.health) != 0 {
			t.Fatal("old probe restored cleared health")
		}
	})
}

func TestHostedRetryAfterUsesProviderCap(t *testing.T) {
	provider := llm.NewProviderConfig("sample", config.ProviderConfig{RetryAfterMaxS: new(2), RetryBackoff: config.RetryBackoffNone}, []string{"key"})
	now := time.Now()
	deadline := hostedRetryAt(provider, &llm.APIError{StatusCode: 429, RetryAfter: time.Hour}, 1)
	if wait := deadline.Sub(now); wait < 2*time.Second || wait > 3*time.Second {
		t.Fatalf("wait=%v", wait)
	}
}

func TestHostedRateWaitPreservesFirstAttemptBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := newTestMainAgent(t, t.TempDir())
		a.governor = newResourceGovernor(config.OrchestrationConfig{
			ProviderHostedRequestsPerMinute: map[string]int{"sample": 2},
			ProviderHostedRetriesPerMinute:  map[string]int{"sample": 1},
		})
		release, err := a.governor.acquireHostedLLM(t.Context(), "sample/model-1", false)
		if err != nil {
			t.Fatal(err)
		}
		release.release(false)
		time.Sleep(10 * time.Second)
		release, err = a.governor.acquireHostedLLM(t.Context(), "sample/model-1", true)
		if err != nil {
			t.Fatal(err)
		}
		release.release(false)
		provider := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) { return hostedWebSearchResponse(), nil }}
		setHostedTestPool(a, hostedRetryTarget("sample", []string{"key"}, provider))
		ctx, cancel := context.WithTimeout(t.Context(), 55*time.Second)
		defer cancel()
		_, err = newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil)).Run(ctx, tools.NameWebSearch, map[string]any{"query": "sample"})
		if err != nil || provider.callCount() != 1 {
			t.Fatalf("first attempt after rate wait: calls=%d err=%v", provider.callCount(), err)
		}
	})
}
