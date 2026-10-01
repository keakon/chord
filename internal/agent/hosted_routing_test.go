package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/analytics"
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
	backend := newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil))
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
	// A removed fallback must never be resurrected by the remembered route:
	// shrinking the pool starts a new route generation, so the old sticky
	// route no longer matches.
	client.SetModelPool([]llm.FallbackModel{first}, 0)
	if _, err := backend.Run(context.Background(), tools.NameWebSearch, map[string]any{"query": "sample query"}); err == nil {
		t.Fatal("removed fallback was used")
	}
	if primary.callCount() != 2 || fallback.callCount() != 2 {
		t.Fatalf("calls after removal primary=%d fallback=%d", primary.callCount(), fallback.callCount())
	}
	// A client replacement starts fresh even when it restores a previous pool.
	setHostedTestPool(a, first, second)
	if _, err := backend.Run(context.Background(), tools.NameWebSearch, map[string]any{"query": "sample query"}); err != nil {
		t.Fatal(err)
	}
	if primary.callCount() != 3 || fallback.callCount() != 3 {
		t.Fatalf("calls after client switch primary=%d fallback=%d", primary.callCount(), fallback.callCount())
	}
}

func TestHostedRouteIsScopedToToolGenerationAndCaller(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	opts := hostedTestTargetOpts{typ: config.ProviderTypeResponses, modelID: "model-1", providerHosted: []string{tools.NameWebSearch}}
	first := newHostedTestTarget("sample", opts, &hostedScriptProvider{})
	opts.modelID = "model-2"
	second := newHostedTestTarget("sample", opts, &hostedScriptProvider{})
	client := setHostedTestPool(a, first, second)
	b := newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil)).(*hostedBackend)
	spec, _ := b.catalogTool(tools.NameWebSearch)
	caller, err := b.resolveCaller("", nil)
	if err != nil {
		t.Fatal(err)
	}
	plan := b.planRoute(caller, spec)
	b.rememberTarget(caller, spec, plan, tools.NameWebSearch, second)
	if targets := b.preferredTargets(plan, tools.NameWebSearch); !sameProviderImpl(targets[0], second.ProviderImpl) {
		t.Fatal("sticky route not preferred")
	}
	if targets := b.preferredTargets(plan, sampleHostedTool); !sameProviderImpl(targets[0], first.ProviderImpl) {
		t.Fatal("another tool inherited affinity")
	}

	// Mutating the same client also changes the content generation.
	client.SetModelPool([]llm.FallbackModel{first}, 0)
	shrunk, err := b.resolveCaller("", nil)
	if err != nil {
		t.Fatal(err)
	}
	b.rememberTarget(caller, spec, plan, tools.NameWebSearch, second)
	if len(b.routes) != 0 {
		t.Fatal("old completion restored an invalid route")
	}
	if len(b.planRoute(shrunk, spec).targets) != 1 {
		t.Fatal("unexpected shrunk pool")
	}
	setHostedTestPool(a, first, second)
	restored, err := b.resolveCaller("", nil)
	if err != nil {
		t.Fatal(err)
	}
	restoredPlan := b.planRoute(restored, spec)
	if targets := b.preferredTargets(restoredPlan, tools.NameWebSearch); !sameProviderImpl(targets[0], first.ProviderImpl) {
		t.Fatal("restored pool resurrected old affinity")
	}
	b.rememberTarget(restored, spec, restoredPlan, tools.NameWebSearch, second)

	sub := &SubAgent{instanceID: "worker-1", parent: a, llmClient: singleModelClient(first), cancel: func() {}}
	a.subs.add(sub)
	subCaller, err := b.resolveCaller(sub.instanceID, nil)
	if err != nil {
		t.Fatal(err)
	}
	subPlan := b.planRoute(subCaller, spec)
	b.rememberTarget(subCaller, spec, subPlan, tools.NameWebSearch, first)
	if targets := b.preferredTargets(restoredPlan, tools.NameWebSearch); !sameProviderImpl(targets[0], second.ProviderImpl) {
		t.Fatal("subagent route leaked into main caller")
	}
	if targets := b.preferredTargets(subPlan, tools.NameWebSearch); !sameProviderImpl(targets[0], first.ProviderImpl) {
		t.Fatal("subagent affinity lost")
	}
}

func sameProviderImpl(target llm.FallbackModel, impl llm.Provider) bool {
	return target.ProviderImpl == impl
}

// TestHostedRoutingFollowsCallerAndView pins the subagent bug fix: an unset
// model_pool follows the calling agent's own pool, availability is evaluated
// per caller view, and stale or dead callers fail closed.
func TestHostedRoutingFollowsCallerAndView(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	mainImpl := &hostedScriptProvider{}
	subImpl := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) { return hostedWebSearchResponse(), nil }}
	// The main pool never enables web_search, so the old main-pool-bound
	// backend would hide the tool from subagents entirely.
	setHostedTestPool(a, newHostedTestTarget("main", hostedTestTargetOpts{typ: config.ProviderTypeResponses, modelID: "main-model"}, mainImpl))
	subTarget := newHostedTestTarget("sub", hostedTestTargetOpts{typ: config.ProviderTypeResponses, modelID: "sub-model", providerHosted: []string{tools.NameWebSearch}}, subImpl)
	subClient := llm.NewClient(subTarget.ProviderConfig, subTarget.ProviderImpl, subTarget.ModelID, subTarget.MaxTokens, "")
	subClient.SetModelPool([]llm.FallbackModel{subTarget}, 0)
	a.subs.mu.Lock()
	a.subs.subAgents["worker-1"] = &SubAgent{instanceID: "worker-1", agentDefName: "researcher", llmClient: subClient}
	a.subs.mu.Unlock()

	backend := newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil))
	if backend.Available(tools.NameWebSearch) {
		t.Fatal("main view must hide the tool when the main pool has no compatible target")
	}
	view := a.HostedBackendForCaller("worker-1")
	if view == nil {
		t.Fatal("subagent view missing")
	}
	if !view.Available(tools.NameWebSearch) {
		t.Fatal("subagent view must surface the tool from the subagent's own pool")
	}

	var events []analytics.UsageEvent
	a.SetUsageEventSink(func(e analytics.UsageEvent) { events = append(events, e) })
	obs, err := view.Run(tools.WithAgentID(tools.WithTurnID(context.Background(), 7), "worker-1"), tools.NameWebSearch, map[string]any{"query": "sample query"})
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || len(obs.Calls) != 1 {
		t.Fatalf("observation = %#v", obs)
	}
	if subImpl.callCount() != 1 || mainImpl.callCount() != 0 {
		t.Fatalf("calls sub=%d main=%d, want the subagent's own pool", subImpl.callCount(), mainImpl.callCount())
	}
	if len(events) != 1 || events[0].AgentID != "worker-1" || events[0].AgentKind != "sub" || events[0].TurnID != 7 {
		t.Fatalf("usage = %+v", events)
	}

	// A context from a different agent fails closed on the view.
	if _, err := view.Run(tools.WithAgentID(context.Background(), "worker-2"), tools.NameWebSearch, map[string]any{"query": "sample query"}); err == nil {
		t.Fatal("stale context agent id must fail closed")
	}
	// An unknown default caller never falls back to the main pool.
	if _, err := a.HostedBackendForCaller("ghost").Run(context.Background(), tools.NameWebSearch, map[string]any{"query": "sample query"}); err == nil {
		t.Fatal("unknown caller must fail closed")
	}
	// A subagent that has finished loses hosted access even with a matching id.
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.mu.Unlock()
	if _, err := view.Run(tools.WithAgentID(context.Background(), "worker-1"), tools.NameWebSearch, map[string]any{"query": "sample query"}); err == nil {
		t.Fatal("finished subagent must fail closed")
	}
}

func TestHostedSubAgentUsageUsesViewCallerWithoutContextAgentID(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	mainImpl := &hostedScriptProvider{}
	subImpl := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) {
		resp := hostedWebSearchResponse()
		resp.Usage = &message.TokenUsage{InputTokens: 3, OutputTokens: 2}
		return resp, nil
	}}
	setHostedTestPool(a, newHostedTestTarget("main", hostedTestTargetOpts{typ: config.ProviderTypeResponses, modelID: "main-model"}, mainImpl))
	subTarget := newHostedTestTarget("sub", hostedTestTargetOpts{typ: config.ProviderTypeResponses, modelID: "sub-model", providerHosted: []string{tools.NameWebSearch}}, subImpl)
	subClient := singleModelClient(subTarget)
	a.subs.mu.Lock()
	a.subs.subAgents["worker-1"] = &SubAgent{instanceID: "worker-1", agentDefName: "researcher", llmClient: subClient, cancel: func() {}, turn: &Turn{ID: 7}}
	a.subs.mu.Unlock()
	_ = newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil))
	var events []analytics.UsageEvent
	a.SetUsageEventSink(func(e analytics.UsageEvent) { events = append(events, e) })

	if _, err := a.HostedBackendForCaller("worker-1").Run(context.Background(), tools.NameWebSearch, map[string]any{"query": "sample query"}); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].AgentID != "worker-1" || events[0].AgentKind != "sub" || events[0].TurnID != 7 {
		t.Fatalf("usage = %+v, want the view caller", events)
	}
}

// TestHostedNamedPoolRouting covers the model_pool field: named-pool routing,
// agent pool authorization, compat and family filtering inside the pool, and
// the startup-fatal unknown pool reference.
func TestHostedNamedPoolRouting(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	toolImpl := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) { return hostedWebSearchResponse(), nil }}
	chatImpl := &hostedScriptProvider{}
	noCompatImpl := &hostedScriptProvider{}
	responsesTarget := newHostedTestTarget("sample", hostedTestTargetOpts{typ: config.ProviderTypeResponses, modelID: "model-1", providerHosted: []string{tools.NameWebSearch}}, toolImpl)
	chatTarget := newHostedTestTarget("chat", hostedTestTargetOpts{typ: config.ProviderTypeChatCompletions, modelID: "model-2", providerHosted: []string{tools.NameWebSearch}}, chatImpl)
	noCompatTarget := newHostedTestTarget("sample", hostedTestTargetOpts{typ: config.ProviderTypeResponses, modelID: "model-3"}, noCompatImpl)
	targets := map[string]llm.FallbackModel{
		"sample/model-1": responsesTarget,
		"chat/model-2":   chatTarget,
		"sample/model-3": noCompatTarget,
	}
	a.globalConfig.ModelPools = map[string][]string{"tools": {"sample/model-1", "chat/model-2", "sample/model-3"}}
	a.SetModelSwitchFactory(func(providerModel string, _ []string, _ string) (*llm.Client, string, int, error) {
		target, ok := targets[providerModel]
		if !ok {
			return nil, "", 0, fmt.Errorf("unknown model ref %q", providerModel)
		}
		return singleModelClient(target), providerModel, 0, nil
	})
	catalog := tools.ResolveHostedToolCatalog(map[string]config.HostedToolConfig{
		tools.NameWebSearch: {ModelPool: "tools"},
	})
	backend := newTestHostedBackend(t, a, catalog)
	if _, err := backend.Run(context.Background(), tools.NameWebSearch, map[string]any{"query": "sample query"}); err != nil {
		t.Fatal(err)
	}
	if toolImpl.callCount() != 1 || chatImpl.callCount() != 0 || noCompatImpl.callCount() != 0 {
		t.Fatalf("calls tool=%d chat=%d noCompat=%d, want only the compatible responses target", toolImpl.callCount(), chatImpl.callCount(), noCompatImpl.callCount())
	}
	// Sticky affinity is shared across callers of the same pool snapshot: a
	// subagent view hits the remembered target without re-walking.
	a.subs.mu.Lock()
	a.subs.subAgents["worker-1"] = &SubAgent{instanceID: "worker-1", agentDefName: "researcher", llmClient: singleModelClient(responsesTarget), cancel: func() {}}
	a.subs.mu.Unlock()
	view := a.HostedBackendForCaller("worker-1")
	if !view.Available(tools.NameWebSearch) {
		t.Fatal("subagent view must see a tool routed to an authorized pool")
	}
	if _, err := view.Run(context.Background(), tools.NameWebSearch, map[string]any{"query": "sample query"}); err != nil {
		t.Fatal(err)
	}
	if toolImpl.callCount() != 2 {
		t.Fatalf("tool calls = %d, want the shared pool affinity to keep the same target", toolImpl.callCount())
	}
}

// TestHostedNamedPoolAuthorization pins the provider resource boundary: a
// named model_pool outside the calling agent's model_pools hides the tool and
// fails closed, for subagent views and the main view alike.
func TestHostedNamedPoolAuthorization(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	toolImpl := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) { return hostedWebSearchResponse(), nil }}
	target := newHostedTestTarget("sample", hostedTestTargetOpts{typ: config.ProviderTypeResponses, modelID: "model-1", providerHosted: []string{tools.NameWebSearch}}, toolImpl)
	a.globalConfig.ModelPools = map[string][]string{"tools": {"sample/model-1"}}
	a.SetModelSwitchFactory(func(providerModel string, _ []string, _ string) (*llm.Client, string, int, error) {
		if providerModel != "sample/model-1" {
			return nil, "", 0, fmt.Errorf("unknown model ref %q", providerModel)
		}
		return singleModelClient(target), providerModel, 0, nil
	})
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"researcher": {Name: "researcher", Models: map[string][]string{"own": {"sample/model-9"}}},
	})
	a.subs.mu.Lock()
	a.subs.subAgents["worker-1"] = &SubAgent{instanceID: "worker-1", agentDefName: "researcher", llmClient: singleModelClient(target), cancel: func() {}}
	a.subs.mu.Unlock()

	catalog := tools.ResolveHostedToolCatalog(map[string]config.HostedToolConfig{
		tools.NameWebSearch: {ModelPool: "tools"},
	})
	backend := newTestHostedBackend(t, a, catalog)
	// The main view's role config is unrestricted here, so it still sees the
	// authorized pool: the boundary is per calling agent, not global.
	if !backend.Available(tools.NameWebSearch) {
		t.Fatal("unrestricted main view must see the authorized pool")
	}
	view := a.HostedBackendForCaller("worker-1")
	if view.Available(tools.NameWebSearch) {
		t.Fatal("tool must be hidden when the routing pool is outside the agent's model_pools")
	}
	if _, err := view.Run(tools.WithAgentID(context.Background(), "worker-1"), tools.NameWebSearch, map[string]any{"query": "sample query"}); err == nil {
		t.Fatal("unauthorized pool must fail closed")
	}
	if toolImpl.callCount() != 0 {
		t.Fatalf("tool calls = %d, want zero for an unauthorized pool", toolImpl.callCount())
	}
}

func TestHostedNamedPoolUnknownReferenceFailsStartup(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.SetModelSwitchFactory(func(providerModel string, _ []string, _ string) (*llm.Client, string, int, error) {
		return nil, "", 0, fmt.Errorf("unexpected model ref %q", providerModel)
	})
	catalog := tools.ResolveHostedToolCatalog(map[string]config.HostedToolConfig{
		tools.NameWebSearch: {ModelPool: "missing"},
	})
	if _, err := NewHostedBackend(a, catalog, a.globalConfig.ModelPools); err == nil {
		t.Fatal("unknown model_pool must fail startup")
	}
	// A pool whose refs are all blank resolves as empty: also fatal.
	a.globalConfig.ModelPools = map[string][]string{"blank": {"   "}}
	catalog = tools.ResolveHostedToolCatalog(map[string]config.HostedToolConfig{
		tools.NameWebSearch: {ModelPool: "blank"},
	})
	if _, err := NewHostedBackend(a, catalog, a.globalConfig.ModelPools); err == nil {
		t.Fatal("an empty pool must fail startup")
	}
	// A blank model_pool normalizes to unset: no snapshot, no error.
	catalog = tools.ResolveHostedToolCatalog(map[string]config.HostedToolConfig{
		tools.NameWebSearch: {ModelPool: "   "},
	})
	b := newTestHostedBackend(t, a, catalog).(*hostedBackend)
	if len(b.pools) != 0 {
		t.Fatalf("blank model_pool built snapshots: %#v", b.pools)
	}
}

// TestHostedNamedPoolRefConstructionFailure pins the failure classification: a
// partially broken pool keeps its usable targets; a pool whose refs all fail
// construction hides the tool instead of falling back to the caller's pool.
func TestHostedNamedPoolRefConstructionFailure(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	toolImpl := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) { return hostedWebSearchResponse(), nil }}
	good := newHostedTestTarget("sample", hostedTestTargetOpts{typ: config.ProviderTypeResponses, modelID: "model-1", providerHosted: []string{tools.NameWebSearch}}, toolImpl)
	a.globalConfig.ModelPools = map[string][]string{"mixed": {"sample/model-1", "broken/model-x"}, "broken": {"broken/model-x"}}
	a.SetModelSwitchFactory(func(providerModel string, _ []string, _ string) (*llm.Client, string, int, error) {
		switch providerModel {
		case "sample/model-1":
			return singleModelClient(good), providerModel, 0, nil
		default:
			return nil, "", 0, fmt.Errorf("ref cannot be constructed")
		}
	})
	catalog := tools.ResolveHostedToolCatalog(map[string]config.HostedToolConfig{
		tools.NameWebSearch: {ModelPool: "mixed"},
	})
	backend := newTestHostedBackend(t, a, catalog)
	if _, err := backend.Run(context.Background(), tools.NameWebSearch, map[string]any{"query": "sample query"}); err != nil {
		t.Fatal(err)
	}
	if toolImpl.callCount() != 1 {
		t.Fatalf("tool calls = %d, want the surviving ref to serve the call", toolImpl.callCount())
	}

	allBroken := tools.ResolveHostedToolCatalog(map[string]config.HostedToolConfig{
		tools.NameWebSearch: {ModelPool: "broken"},
	})
	if newTestHostedBackend(t, a, allBroken).Available(tools.NameWebSearch) {
		t.Fatal("a pool whose refs all fail must hide the tool")
	}
	if _, runErr := newTestHostedBackend(t, a, allBroken).Run(context.Background(), tools.NameWebSearch, map[string]any{"query": "sample query"}); runErr == nil {
		t.Fatal("an all-broken pool must fail with an explicit unavailable error")
	} else if strings.Contains(runErr.Error(), "no Anthropic Messages") || !strings.Contains(runErr.Error(), "failed to construct") {
		t.Fatalf("all-broken pool error = %q, want construction failure classification", runErr)
	}
}

func TestHostedNamedPoolUsesExplicitEmptyProjectOverride(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.globalConfig.ModelPools = map[string][]string{"tools": {"sample/model"}}
	a.projectConfig = &config.Config{ModelPools: map[string][]string{"tools": {}}}
	catalog := tools.ResolveHostedToolCatalog(map[string]config.HostedToolConfig{
		tools.NameWebSearch: {ModelPool: "tools"},
	})
	if _, err := NewHostedBackend(a, catalog, map[string][]string{"tools": {}}); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("NewHostedBackend error = %v, want explicit empty project override", err)
	}
}

func singleModelClient(target llm.FallbackModel) *llm.Client {
	client := llm.NewClient(target.ProviderConfig, target.ProviderImpl, target.ModelID, target.MaxTokens, "")
	client.SetModelPool([]llm.FallbackModel{target}, 0)
	return client
}
