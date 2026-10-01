package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestHostedBackendReleasesLLMCapacity(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "provider_error"
		}
		t.Run(name, func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			a.governor = newResourceGovernor(config.OrchestrationConfig{MaxActiveLLMRequests: 1})
			impl := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) {
				got := a.governor.snapshot()
				if got.LLMActive != 1 || got.ProviderActive["sample"] != 1 || got.ModelActive["sample/model-1"] != 1 {
					t.Errorf("request capacity = %+v, want one slot for sample/model-1", got)
				}
				if fail {
					return nil, &llm.APIError{StatusCode: 400, Code: "query_too_long", Message: "query too long"}
				}
				return hostedWebSearchResponse(), nil
			}}
			setHostedTestPool(a, newHostedTestTarget("sample", hostedTestTargetOpts{
				typ: config.ProviderTypeMessages, modelID: "model-1", providerHosted: []string{tools.NameWebSearch},
			}, impl))
			_, err := newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil)).Run(
				context.Background(), tools.NameWebSearch, map[string]any{"query": "sample query"})
			if (err != nil) != fail {
				t.Fatalf("Run error = %v, want failure %v", err, fail)
			}
			if impl.callCount() != 1 {
				t.Fatalf("provider calls = %d, want 1", impl.callCount())
			}
			if got := a.governor.snapshot(); got.LLMActive != 0 || got.LLMQueued != 0 || len(got.ProviderActive) != 0 || len(got.ModelActive) != 0 {
				t.Fatalf("capacity after Run = %+v, want no active or queued requests", got)
			}
		})
	}
}

func TestHostedBackendWaitsForLLMCapacity(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cfg        config.OrchestrationConfig
		blockedRef string
	}{
		{"process", config.OrchestrationConfig{MaxActiveLLMRequests: 1}, "other/model-2"},
		{"provider", config.OrchestrationConfig{MaxActiveLLMRequests: 2, ProviderMaxActiveRequests: map[string]int{"sample": 1}}, "sample/model-2"},
		{"model", config.OrchestrationConfig{MaxActiveLLMRequests: 2, ModelMaxActiveRequests: map[string]int{"sample/model-1": 1}}, "sample/model-1@high"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			a.governor = newResourceGovernor(tc.cfg)
			release, err := a.governor.acquireLLM(context.Background(), tc.blockedRef)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			impl := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) {
				return hostedWebSearchResponse(), nil
			}}
			setHostedTestPool(a, newHostedTestTarget("sample", hostedTestTargetOpts{
				typ: config.ProviderTypeMessages, modelID: "model-1", providerHosted: []string{tools.NameWebSearch},
			}, impl))
			backend := newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil))
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if _, err := backend.Run(ctx, tools.NameWebSearch, map[string]any{"query": "sample query"}); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Run error = %v, want cancellation while waiting for capacity", err)
			}
			if impl.callCount() != 0 {
				t.Fatal("a capacity-blocked request must not reach the provider")
			}
			if got := a.governor.snapshot(); got.LLMActive != 1 || got.LLMQueued != 0 {
				t.Fatalf("capacity after cancellation = %+v, want only the original request", got)
			}
			release()
			if _, err := backend.Run(context.Background(), tools.NameWebSearch, map[string]any{"query": "sample query"}); err != nil {
				t.Fatalf("Run after releasing capacity: %v", err)
			}
			if got := a.governor.snapshot(); got.LLMActive != 0 || got.LLMQueued != 0 {
				t.Fatalf("capacity after completion = %+v", got)
			}
		})
	}
}
