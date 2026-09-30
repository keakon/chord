package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func hostedRetryTarget(name string, keys []string, impl llm.Provider) llm.FallbackModel {
	target := newHostedTestTarget(name, hostedTestTargetOpts{typ: config.ProviderTypeResponses, modelID: "model-1", providerHosted: []string{tools.NameWebSearch}}, impl)
	target.ProviderConfig = llm.NewProviderConfig(name, config.ProviderConfig{
		Type: config.ProviderTypeResponses, APIURL: "https://example.invalid/v1",
		RetryBackoff: config.RetryBackoffNone,
		Compat:       &config.ProviderCompatConfig{HostedTools: new([]string{tools.NameWebSearch})},
		Models:       map[string]config.ModelConfig{"model-1": {Limit: config.ModelLimit{Context: 128000, Output: 4096}}},
	}, keys)
	return target
}

func TestHostedBackendTransientRetry(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
	}{
		{"rate_limit", &llm.APIError{StatusCode: 429, Message: "Too many concurrent requests"}},
		{"upstream", &llm.APIError{StatusCode: 503, Message: "service unavailable"}},
		{"dependency_unavailable", &llm.APIError{StatusCode: 424, Message: "The requested service is currently unavailable"}},
		{"transport", io.ErrUnexpectedEOF},
	} {
		for _, recover := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/recover_%t", tc.name, recover), func(t *testing.T) {
				a := newTestMainAgent(t, t.TempDir())
				impl := &hostedScriptProvider{respond: func(i int, _ context.Context) (*message.Response, error) {
					if recover && i == 1 {
						return hostedWebSearchResponse(), nil
					}
					return nil, tc.failure
				}}
				setHostedTestPool(a, hostedRetryTarget("sample", []string{"key-1"}, impl))
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				_, err := NewHostedBackend(a, tools.ResolveHostedToolCatalog(nil)).Run(ctx, tools.NameWebSearch, map[string]any{"query": "sample query"})
				wantCalls := hostedToolRetryRounds
				if recover {
					wantCalls = 2
				}
				if impl.callCount() != wantCalls || (err == nil) != recover {
					t.Fatalf("calls=%d error=%v, want calls=%d recover=%t", impl.callCount(), err, wantCalls, recover)
				}
				if !recover && strings.Contains(err.Error(), "compat.hosted_tools") {
					t.Fatalf("transient error suggests changing declarations: %v", err)
				}
			})
		}
	}
}

func TestHostedBackendQuotaDoesNotRetryRounds(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	quota := &llm.APIError{StatusCode: 429, Code: "insufficient_quota", Message: "account quota exhausted"}
	first := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) { return nil, quota }}
	second := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) { return hostedWebSearchResponse(), nil }}
	setHostedTestPool(a, hostedRetryTarget("first", []string{"key-1", "key-2"}, first), hostedRetryTarget("second", []string{"key-3"}, second))
	_, err := NewHostedBackend(a, tools.ResolveHostedToolCatalog(nil)).Run(t.Context(), tools.NameWebSearch, map[string]any{"query": "sample query"})
	if err != nil || first.callCount() != 2 || second.callCount() != 1 {
		t.Fatalf("calls=%d/%d error=%v", first.callCount(), second.callCount(), err)
	}
}

func TestHostedBackendBackoffReleasesCapacityAndCancels(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.governor = newResourceGovernor(config.OrchestrationConfig{MaxActiveLLMRequests: 1})
	started := make(chan struct{})
	impl := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) {
		close(started)
		return nil, &llm.APIError{StatusCode: 429, Message: "rate limited", RetryAfter: time.Second}
	}}
	setHostedTestPool(a, hostedRetryTarget("sample", []string{"key-1"}, impl))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := NewHostedBackend(a, tools.ResolveHostedToolCatalog(nil)).Run(ctx, tools.NameWebSearch, map[string]any{"query": "sample query"})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("provider did not start")
	}
	admissionCtx, admissionCancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer admissionCancel()
	release, err := a.governor.acquireLLM(admissionCtx, "sample/model-2")
	if err != nil {
		t.Fatalf("backoff retained capacity: %v", err)
	}
	release()
	// The one-second Retry-After keeps the wire attempt count at one while the
	// other request acquires capacity; cancellation must interrupt this wait.
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("retry wait did not cancel")
	}
	if impl.callCount() != 1 {
		t.Fatalf("Retry-After ignored: calls=%d", impl.callCount())
	}
	if got := a.governor.snapshot(); got.LLMActive != 0 || got.LLMQueued != 0 {
		t.Fatalf("capacity leaked: %+v", got)
	}
}

func TestHostedFailureAggregation(t *testing.T) {
	rate := &llm.APIError{StatusCode: 429, Message: "provider request details", RetryAfter: time.Second}
	noCall := &llm.HostedCallNotObservedError{Tool: tools.NameWebSearch}
	err := newHostedAllTargetsFailedError(tools.NameWebSearch, []hostedTargetFailure{{target: "first/model-1", cause: rate}, {target: "second/model-2", cause: noCall}})
	for _, part := range []string{"first/model-1 [rate_limited]", "second/model-2 [call_not_observed]", "Retry later", "compat.hosted_tools"} {
		if !strings.Contains(err.Error(), part) {
			t.Fatalf("missing %q: %v", part, err)
		}
	}
	if !errors.Is(err, rate) || !errors.Is(err, noCall) {
		t.Fatal("aggregation lost error causes")
	}
	if strings.Contains(err.Error(), rate.Message) || strings.Contains(err.Error(), "endpoint may not support") {
		t.Fatalf("raw or misleading failure: %v", err)
	}
	quotaErr := newHostedAllTargetsFailedError(tools.NameWebSearch, []hostedTargetFailure{{target: "sample/model-1", cause: &llm.APIError{StatusCode: 429, Code: "insufficient_quota"}}})
	if !strings.Contains(quotaErr.Error(), "quota_exhausted") || strings.Contains(quotaErr.Error(), "Retry later") {
		t.Fatalf("quota advice: %v", quotaErr)
	}
}
