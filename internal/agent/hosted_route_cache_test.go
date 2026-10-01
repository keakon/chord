package agent

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/keakon/golog"
	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/logtest"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestHostedInFlightCompletionAfterClientSwitch(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	started, release := make(chan struct{}), make(chan struct{})
	impl := &hostedScriptProvider{respond: func(_ int, ctx context.Context) (*message.Response, error) {
		close(started)
		select {
		case <-release:
			return hostedWebSearchResponse(), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	target := newHostedTestTarget("sample", hostedTestTargetOpts{typ: config.ProviderTypeResponses, modelID: "model-1", providerHosted: []string{tools.NameWebSearch}}, impl)
	setHostedTestPool(a, target)
	b := newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil)).(*hostedBackend)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := b.Run(ctx, tools.NameWebSearch, map[string]any{"query": "sample query"})
		finished <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("provider did not start")
	}
	a.SwapLLMClient(singleModelClient(target), "model-1", 8192)
	close(release)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("old hosted request did not finish")
	}
	if len(b.routes) != 0 {
		t.Fatal("old in-flight request republished affinity")
	}
}

func TestHostedHiddenToolDiagnosesOnce(t *testing.T) {
	for _, reason := range []string{"protocol", "compat", "authorization"} {
		t.Run(reason, func(t *testing.T) {
			var output bytes.Buffer
			log.SetDefaultLogger(logtest.NewLogger(&output, golog.WarnLevel))
			defer log.SetDefaultLogger(logtest.NewLogger(nil, golog.InfoLevel))
			opts := hostedTestTargetOpts{typ: config.ProviderTypeResponses, modelID: "model-1"}
			want := "compat entry missing"
			if reason == "protocol" {
				opts.typ = config.ProviderTypeChatCompletions
				want = "no compatible protocol family"
			}
			if reason == "authorization" {
				opts.providerHosted = []string{tools.NameWebSearch}
				want = "pool not authorized for caller"
			}
			target := newHostedTestTarget("sample", opts, &hostedScriptProvider{})
			a := &MainAgent{parentCtx: context.Background(), llmClient: singleModelClient(target), subs: newSubAgentRegistry()}
			defer a.llmClient.Close()
			catalog := tools.ResolveHostedToolCatalog(nil)
			pools := map[string][]string(nil)
			if reason == "authorization" {
				catalog = tools.ResolveHostedToolCatalog(map[string]config.HostedToolConfig{tools.NameWebSearch: {ModelPool: "tools"}})
				pools = map[string][]string{"tools": {"sample/model-1"}}
				a.activeConfig = &config.AgentConfig{Models: map[string][]string{"own": {"sample/model-1"}}}
				a.SetModelSwitchFactory(func(string, []string, string) (*llm.Client, string, int, error) {
					return singleModelClient(target), "model-1", 8192, nil
				})
			}
			b, err := NewHostedBackend(a, catalog, pools)
			if err != nil {
				t.Fatal(err)
			}
			for range 3 {
				if b.Available(tools.NameWebSearch) {
					t.Fatal("tool must be hidden")
				}
			}
			if got := strings.Count(output.String(), want); got != 1 {
				t.Fatalf("diagnostic count=%d, output=%q", got, output.String())
			}
		})
	}
}

func TestHostedCallerRejectsInactiveRuntime(t *testing.T) {
	for _, state := range []string{string(SubAgentStateCompleted), string(SubAgentStateFailed), string(SubAgentStateCancelled), "closed", "context_cancelled", "removed"} {
		t.Run(state, func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			impl := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) { return hostedWebSearchResponse(), nil }}
			target := newHostedTestTarget("sample", hostedTestTargetOpts{typ: config.ProviderTypeResponses, modelID: "model-1", providerHosted: []string{tools.NameWebSearch}}, impl)
			sub := newControllableTestSubAgent(t, a, "task-1")
			sub.switchModel(singleModelClient(target), "model-1", 8192)
			b := newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil)).(*hostedBackend)
			view := b.ForCaller(sub.instanceID)
			if !view.Available(tools.NameWebSearch) {
				t.Fatal("active caller must see tool")
			}
			switch state {
			case "closed":
				sub.llmClient.Close()
			case "context_cancelled":
				sub.cancel()
			case "removed":
				a.closeSubAgent(sub.instanceID)
			default:
				sub.setState(SubAgentState(state), "Finished")
			}
			if view.Available(tools.NameWebSearch) {
				t.Fatal("inactive caller must hide tool")
			}
			for _, ctx := range []context.Context{context.Background(), tools.WithAgentID(context.Background(), sub.instanceID)} {
				if _, err := view.Run(ctx, tools.NameWebSearch, map[string]any{"query": "sample query"}); err == nil {
					t.Fatal("inactive caller must fail closed")
				}
			}
			if impl.callCount() != 0 {
				t.Fatalf("inactive caller sent %d requests", impl.callCount())
			}
			if _, ok := b.callerBindings[sub.instanceID]; ok {
				t.Fatal("inactive caller retained cache binding")
			}
		})
	}
}

func TestHostedOldCompletionCannotRestoreAffinity(t *testing.T) {
	for _, callerKind := range []string{"main", "sub"} {
		t.Run(callerKind, func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			primary := &hostedScriptProvider{}
			opts := hostedTestTargetOpts{typ: config.ProviderTypeResponses, modelID: "model-1", providerHosted: []string{tools.NameWebSearch}}
			first := newHostedTestTarget("sample", opts, primary)
			var second llm.FallbackModel
			var replace func(*llm.Client)
			secondary := &hostedScriptProvider{respond: func(index int, _ context.Context) (*message.Response, error) {
				if index == 0 {
					replace(singleModelClient(first))
					// Restore identical targets before the old call returns: an ABA switch.
					restored := singleModelClient(first)
					restored.SetModelPool([]llm.FallbackModel{first, second}, 0)
					replace(restored)
				}
				return hostedWebSearchResponse(), nil
			}}
			opts.modelID = "model-2"
			second = newHostedTestTarget("sample", opts, secondary)
			client := setHostedTestPool(a, first, second)
			b := newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil)).(*hostedBackend)
			var view tools.HostedToolBackend = b
			if callerKind == "sub" {
				sub := newControllableTestSubAgent(t, a, "task-1")
				sub.switchModel(client, "model-1", 8192)
				replace = func(next *llm.Client) { sub.switchModel(next, "model-1", 8192) }
				view = b.ForCaller(sub.instanceID)
			} else {
				replace = func(next *llm.Client) { a.SwapLLMClient(next, "model-1", 8192) }
			}
			args := map[string]any{"query": "sample query"}
			if _, err := view.Run(context.Background(), tools.NameWebSearch, args); err != nil {
				t.Fatal(err)
			}
			if len(b.routes) != 0 {
				t.Fatal("old completion republished a route after client switches")
			}
			if _, err := view.Run(context.Background(), tools.NameWebSearch, args); err != nil {
				t.Fatal(err)
			}
			if primary.callCount() != 2 || secondary.callCount() != 2 {
				t.Fatalf("attempts primary=%d secondary=%d", primary.callCount(), secondary.callCount())
			}
		})
	}
}

func TestHostedNamedAffinitySurvivesCallerLifecycle(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	primary := &hostedScriptProvider{}
	success := &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) { return hostedWebSearchResponse(), nil }}
	opts := hostedTestTargetOpts{typ: config.ProviderTypeResponses, modelID: "model-1", providerHosted: []string{tools.NameWebSearch}}
	first := newHostedTestTarget("sample", opts, primary)
	opts.modelID = "model-2"
	second := newHostedTestTarget("sample", opts, success)
	poolName := "search/tools"
	a.globalConfig.ModelPools = map[string][]string{poolName: {"sample/model-1", "sample/model-2"}}
	a.SetModelSwitchFactory(func(ref string, _ []string, _ string) (*llm.Client, string, int, error) {
		switch ref {
		case "sample/model-1":
			return singleModelClient(first), "model-1", 8192, nil
		case "sample/model-2":
			return singleModelClient(second), "model-2", 8192, nil
		default:
			return nil, "", 0, fmt.Errorf("unknown model ref %q", ref)
		}
	})
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder": {Name: "builder", Models: map[string][]string{poolName: {"sample/model-1", "sample/model-2"}}},
		"worker":  {Name: "worker", Models: map[string][]string{poolName: {"sample/model-1", "sample/model-2"}}},
	})
	catalog := tools.ResolveHostedToolCatalog(map[string]config.HostedToolConfig{tools.NameWebSearch: {ModelPool: poolName}})
	b := newTestHostedBackend(t, a, catalog).(*hostedBackend)
	a.tools.Register(tools.NewHostedTool(catalog[tools.NameWebSearch], b))
	args := map[string]any{"query": "sample query"}
	if _, err := b.Run(context.Background(), tools.NameWebSearch, args); err != nil {
		t.Fatal(err)
	}
	// A real spawn must rebind the cloned tool and share the named pool affinity.
	sub := newControllableTestSubAgent(t, a, "task-1")
	if _, err := sub.tools.Execute(context.Background(), tools.NameWebSearch, []byte(`{"query":"sample query"}`)); err != nil {
		t.Fatal(err)
	}
	if primary.callCount() != 1 || success.callCount() != 2 {
		t.Fatalf("shared affinity attempts=%d/%d", primary.callCount(), success.callCount())
	}
	a.SwapLLMClient(newTestLLMClient(), "model-3", 8192)
	if !b.Available(tools.NameWebSearch) {
		t.Fatal("named route disappeared after caller client switch")
	}
	if _, err := b.Run(context.Background(), tools.NameWebSearch, args); err != nil {
		t.Fatal(err)
	}
	sub.setState(SubAgentStateCompleted, "Finished")
	a.closeSubAgent(sub.instanceID)
	a.resetSessionRuntimeState()
	if len(b.callerBindings) != 0 {
		t.Fatal("session reset retained caller bindings")
	}
	if _, err := b.Run(context.Background(), tools.NameWebSearch, args); err != nil {
		t.Fatal(err)
	}
	if primary.callCount() != 1 || success.callCount() != 4 {
		t.Fatalf("named affinity was lost across lifecycle: attempts=%d/%d", primary.callCount(), success.callCount())
	}
	// Main-role authorization is enforced independently of a remembered route.
	a.SetAgentConfigs(map[string]*config.AgentConfig{"builder": {Name: "builder", Models: map[string][]string{"own": {"sample/model-3"}}}})
	if b.Available(tools.NameWebSearch) {
		t.Fatal("unauthorized main role exposed named tool")
	}
	if _, err := b.Run(context.Background(), tools.NameWebSearch, args); err == nil {
		t.Fatal("unauthorized main role used cached route")
	}
	if success.callCount() != 4 {
		t.Fatal("unauthorized main role sent request")
	}
}

func TestHostedNamedAffinitySurvivesCallerSwitchDuringCompletion(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	target := newHostedTestTarget("sample", hostedTestTargetOpts{
		typ:            config.ProviderTypeResponses,
		modelID:        "model-1",
		providerHosted: []string{tools.NameWebSearch},
	}, &hostedScriptProvider{})
	poolName := "search/tools"
	a.globalConfig = &config.Config{ModelPools: map[string][]string{poolName: {"sample/model-1"}}}
	a.SetModelSwitchFactory(func(ref string, _ []string, _ string) (*llm.Client, string, int, error) {
		if ref != "sample/model-1" {
			return nil, "", 0, fmt.Errorf("unknown model ref %q", ref)
		}
		return singleModelClient(target), "model-1", 8192, nil
	})
	setHostedTestPool(a, target)
	catalog := tools.ResolveHostedToolCatalog(map[string]config.HostedToolConfig{
		tools.NameWebSearch: {ModelPool: poolName},
	})
	b := newTestHostedBackend(t, a, catalog).(*hostedBackend)
	spec, ok := b.catalogTool(tools.NameWebSearch)
	if !ok {
		t.Fatal("web_search catalog entry missing")
	}
	caller, err := b.resolveCaller("", nil)
	if err != nil {
		t.Fatal(err)
	}
	plan := b.planRoute(caller, spec)
	if len(plan.targets) != 1 {
		t.Fatalf("named route targets = %d, want 1", len(plan.targets))
	}

	// The completion captured the old caller, but the named pool itself is
	// unchanged. A successful target should still update the shared named route.
	a.SwapLLMClient(singleModelClient(target), "model-1", 8192)
	b.rememberTarget(caller, spec, plan, tools.NameWebSearch, target)
	key := hostedRouteCacheKey{source: plan.source, tool: tools.NameWebSearch}
	b.mu.Lock()
	_, remembered := b.routes[key]
	b.mu.Unlock()
	if !remembered {
		t.Fatal("named route lost affinity after caller model switch")
	}
}

func TestHostedCallerCacheRemovedWithRuntime(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	target := newHostedTestTarget("sample", hostedTestTargetOpts{typ: config.ProviderTypeResponses, modelID: "model-1", providerHosted: []string{tools.NameWebSearch}}, &hostedScriptProvider{respond: func(int, context.Context) (*message.Response, error) { return hostedWebSearchResponse(), nil }})
	b := newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil)).(*hostedBackend)
	sub := newControllableTestSubAgent(t, a, "task-1")
	sub.switchModel(singleModelClient(target), "model-1", 8192)
	view := b.ForCaller(sub.instanceID)
	if _, err := view.Run(context.Background(), tools.NameWebSearch, map[string]any{"query": "sample query"}); err != nil {
		t.Fatal(err)
	}
	a.closeSubAgent(sub.instanceID)
	if len(b.routes) != 0 || len(b.callerBindings) != 0 {
		t.Fatal("removed runtime retained route cache")
	}
	// A replacement runtime gets a distinct caller binding; the retired view fails.
	ctx, cancel := context.WithCancel(a.parentCtx)
	next := NewSubAgent(SubAgentConfig{InstanceID: "worker-2", TaskID: "task-1", AgentDefName: "worker", LLMClient: singleModelClient(target), Parent: a, ParentCtx: ctx, Cancel: cancel, BaseTools: a.tools, WorkDir: a.contentRoot, SessionDir: a.sessionDir, ModelName: "model-1"})
	a.subs.add(next)
	if view.Available(tools.NameWebSearch) {
		t.Fatal("retired view exposed replacement runtime")
	}
	if !b.ForCaller(next.instanceID).Available(tools.NameWebSearch) {
		t.Fatal("replacement runtime lost access")
	}
}

func TestHostedCallerDiagnosticsResetOnPoolSwitch(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	target := newHostedTestTarget("sample", hostedTestTargetOpts{typ: config.ProviderTypeResponses, modelID: "model-1"}, &hostedScriptProvider{})
	setHostedTestPool(a, target)
	b := newTestHostedBackend(t, a, tools.ResolveHostedToolCatalog(nil)).(*hostedBackend)
	if b.Available(tools.NameWebSearch) {
		t.Fatal("expected hidden tool")
	}
	if len(b.seenDiag) != 1 {
		t.Fatal("hidden tool diagnostic missing")
	}
	a.SwapLLMClient(singleModelClient(target), "model-1", 8192)
	if len(b.seenDiag) != 0 || len(b.callerBindings) != 0 {
		t.Fatal("pool switch retained caller diagnostics")
	}
	b.Available(tools.NameWebSearch)
	a.resetSessionRuntimeState()
	if len(b.seenDiag) != 0 || len(b.callerBindings) != 0 {
		t.Fatal("session reset retained caller diagnostics")
	}
}
