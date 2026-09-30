package agent

import (
	"context"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestHostedBackendRemembersSuccessfulRoute(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	primary := &hostedScriptProvider{} // No hosted observation: target fails its contract.
	fallback := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) { return hostedWebSearchResponse(), nil }}
	opts := hostedTestTargetOpts{typ: config.ProviderTypeResponses, modelID: "test-model", providerHosted: []string{tools.NameWebSearch}}
	first := newHostedTestTarget("primary", opts, primary)
	second := newHostedTestTarget("fallback", opts, fallback)
	client := setHostedTestPool(a, first, second)
	backend := NewHostedBackend(a, tools.ResolveHostedToolCatalog(nil))
	for range 2 {
		if _, err := backend.Run(context.Background(), tools.NameWebSearch, map[string]any{"query": "sample query"}); err != nil {
			t.Fatal(err)
		}
	}
	if primary.callCount() != 1 || fallback.callCount() != 2 {
		t.Fatalf("calls primary=%d fallback=%d", primary.callCount(), fallback.callCount())
	}
	if _, cursor := client.ModelPoolSnapshot(); cursor != 0 {
		t.Fatalf("main cursor moved: %d", cursor)
	}
	// A removed fallback must never be resurrected by the remembered route.
	client.SetModelPool([]llm.FallbackModel{first}, 0)
	if _, err := backend.Run(context.Background(), tools.NameWebSearch, map[string]any{"query": "sample query"}); err == nil {
		t.Fatal("removed fallback was used")
	}
	if primary.callCount() != 2 || fallback.callCount() != 2 {
		t.Fatalf("calls after removal primary=%d fallback=%d", primary.callCount(), fallback.callCount())
	}
	// Replacing the client resets affinity even if it reuses the same targets.
	setHostedTestPool(a, first, second)
	if _, err := backend.Run(context.Background(), tools.NameWebSearch, map[string]any{"query": "sample query"}); err != nil {
		t.Fatal(err)
	}
	if primary.callCount() != 3 || fallback.callCount() != 3 {
		t.Fatalf("calls after client switch primary=%d fallback=%d", primary.callCount(), fallback.callCount())
	}
}

func TestHostedRouteIsScopedToToolAndVariant(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	opts := hostedTestTargetOpts{typ: config.ProviderTypeResponses, modelID: "test-model", providerHosted: []string{tools.NameWebSearch}}
	first := newHostedTestTarget("sample", opts, &hostedScriptProvider{})
	first.Variant = "low"
	second := first
	second.Variant = "high"
	client := setHostedTestPool(a, first, second)
	b := NewHostedBackend(a, tools.ResolveHostedToolCatalog(nil)).(*hostedBackend)
	b.rememberTarget(tools.NameWebSearch, client, second)
	targets := b.preferredTargets(tools.NameWebSearch, client, []llm.FallbackModel{first, second})
	if targets[0].Variant != "high" || targets[1].Variant != "low" {
		t.Fatalf("targets = %#v", targets)
	}
	targets = b.preferredTargets(sampleHostedTool, client, []llm.FallbackModel{first, second})
	if targets[0].Variant != "low" {
		t.Fatal("another tool inherited the route")
	}
	newClient := setHostedTestPool(a, first, second)
	b.rememberTarget(tools.NameWebSearch, newClient, first)
	b.rememberTarget(tools.NameWebSearch, client, second) // Old in-flight call completes.
	targets = b.preferredTargets(tools.NameWebSearch, newClient, []llm.FallbackModel{first, second})
	if targets[0].Variant != "low" {
		t.Fatal("old client replaced the current route")
	}
}
