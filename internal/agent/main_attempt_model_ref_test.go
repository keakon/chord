package agent

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// TestFocusedModelStateDisplayRefNamesInflightFallback pins the agent-level
// display contract: while a fallback attempt is in flight and has produced no
// output, FocusedModelState().DisplayRef names the fallback so the sidebar can
// show the model actually being used, while RunningRef — which drives the token
// budgets and compaction thresholds — still names the confirmed primary. The
// shared key/rate-limit resolver must agree with the MODEL row, so it names the
// attempt target too while the request is in flight.
func TestFocusedModelStateDisplayRefNamesInflightFallback(t *testing.T) {
	a := newReadyTestMainAgent(t)

	primaryImpl := &blockingStreamProvider{calls: []scriptedStreamCall{{
		err: &llm.APIError{StatusCode: 500, Message: "primary unavailable"},
	}}}
	fallbackImpl := &blockingStreamProvider{
		streamedCh: make(chan struct{}),
		releaseCh:  make(chan struct{}),
		calls:      []scriptedStreamCall{{holdAfterStreams: true}},
	}
	client := llm.NewClient(newSidebarTestProviderConfig("primary-prov", "primary-model", 128000, 100000), primaryImpl, "primary-model", 4096, "sys")
	client.SetFallbackModels([]llm.FallbackModel{{
		ProviderConfig: newSidebarTestProviderConfig("fallback-prov", "fallback-model", 64000, 64000),
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   64000,
		InputLimit:     64000,
	}})
	a.swapLLMClientWithRef(client, "primary-model", 128000, "primary-prov/primary-model")

	if got := a.FocusedModelState().DisplayRef; got != "primary-prov/primary-model" {
		t.Fatalf("DisplayRef before the request = %q, want the next-request model", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := a.callLLMForRequest(ctx, []message.Message{{Role: "user", Content: "hi"}}, 0)
		done <- err
	}()
	// Mirror the main request lifecycle; the display target itself is read
	// from the captured client.
	a.mainLLMRequestInFlight.Store(true)

	select {
	case <-fallbackImpl.streamedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the fallback attempt to start")
	}

	state := a.FocusedModelState()
	if state.DisplayRef != "fallback-prov/fallback-model" {
		t.Fatalf("FocusedModelState().DisplayRef = %q, want the in-flight fallback", state.DisplayRef)
	}
	if state.RunningRef != "primary-prov/primary-model" {
		t.Fatalf("FocusedModelState().RunningRef = %q, want the confirmed primary", state.RunningRef)
	}
	if _, ref := a.tuiFocusedLLMAndRef(); ref != "fallback-prov/fallback-model" {
		t.Fatalf("tuiFocusedLLMAndRef ref = %q, want the in-flight fallback", ref)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the cancelled request to unwind")
	}
	a.mainLLMRequestInFlight.Store(false)

	if got := a.FocusedModelState().DisplayRef; got != "primary-prov/primary-model" {
		t.Fatalf("DisplayRef after the request returned = %q, want the next-request model", got)
	}
}

// TestFocusedModelStateDisplayRefFollowsFocusedSubAgent covers the worker half
// of the scope: a focused SubAgent's in-flight fallback must reach the sidebar
// through the same field, and the shared key/rate-limit resolver must follow the
// worker's client rather than the main agent's.
func TestFocusedModelStateDisplayRefFollowsFocusedSubAgent(t *testing.T) {
	a := newReadyTestMainAgent(t)

	subPrimary := &blockingStreamProvider{calls: []scriptedStreamCall{{
		err: &llm.APIError{StatusCode: 500, Message: "sub primary unavailable"},
	}}}
	subFallback := &blockingStreamProvider{
		streamedCh: make(chan struct{}),
		releaseCh:  make(chan struct{}),
		calls:      []scriptedStreamCall{{holdAfterStreams: true}},
	}
	subClient := llm.NewClient(newSidebarTestProviderConfig("sub-prov", "sub-model", 128000, 100000), subPrimary, "sub-model", 4096, "sys")
	subClient.SetFallbackModels([]llm.FallbackModel{{
		ProviderConfig: newSidebarTestProviderConfig("sub-fallback-prov", "sub-fallback-model", 64000, 64000),
		ProviderImpl:   subFallback,
		ModelID:        "sub-fallback-model",
		MaxTokens:      4096,
		ContextLimit:   64000,
		InputLimit:     64000,
	}})

	sub := &SubAgent{instanceID: "sub-attempt", agentDefName: "worker", llmClient: subClient, cancel: func() {}}
	a.subs.mu.Lock()
	a.subs.subAgents[sub.instanceID] = sub
	a.subs.mu.Unlock()
	a.focusedAgent.Store(sub)
	t.Cleanup(func() { a.focusedAgent.Store(nil) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := subClient.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
		done <- err
	}()

	select {
	case <-subFallback.streamedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the worker's fallback attempt to start")
	}

	state := a.FocusedModelState()
	if state.DisplayRef != "sub-fallback-prov/sub-fallback-model" {
		t.Fatalf("focused SubAgent DisplayRef = %q, want the in-flight fallback", state.DisplayRef)
	}
	if state.RunningRef != "sub-prov/sub-model" {
		t.Fatalf("focused SubAgent RunningRef = %q, want the confirmed sub-prov/sub-model", state.RunningRef)
	}
	client, ref := a.tuiFocusedLLMAndRef()
	if client != subClient {
		t.Fatal("tuiFocusedLLMAndRef did not resolve the focused SubAgent's client")
	}
	if ref != "sub-fallback-prov/sub-fallback-model" {
		t.Fatalf("tuiFocusedLLMAndRef ref = %q, want the worker's in-flight fallback", ref)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the cancelled worker request to unwind")
	}
}

func TestFocusedModelStatePreviewsPoolDuringToolPhase(t *testing.T) {
	a := newToolPhaseSwitchTestAgent(t)
	a.newTurn()
	a.mainLLMRequestInFlight.Store(true)
	a.SetCurrentModelPool("fast")
	dispatchPendingEvents(t, a)
	a.mainLLMRequestInFlight.Store(false)
	a.turn.PendingToolCalls.Store(1)
	state := a.FocusedModelState()
	if state.DisplayRef != "provider/claude-opus-4" || state.RunningRef != "provider/gpt-5.5" {
		t.Fatalf("pending pool display = %#v", state)
	}
	if state.KeysTotal != 0 || state.RateLimit != nil {
		t.Fatal("uninstalled target reused another model's provider data")
	}
	if got := a.ctxMgr.GetMaxTokens(); got != 8192 {
		t.Fatalf("display preview changed the context window to %d", got)
	}
	if _, ok := a.mainVisibleLLMToolNames()[tools.NameApplyPatch]; !ok {
		t.Fatal("display preview changed the active tool surface")
	}
}

func TestFocusedModelStateTierAndKeysFollowInflightTarget(t *testing.T) {
	a := newReadyTestMainAgent(t)
	primary := llm.NewProviderConfig("provider-a", config.ProviderConfig{
		Type:                  config.ProviderTypeChatCompletions,
		SupportedServiceTiers: []config.ServiceTier{config.ServiceTierFast},
		Models:                map[string]config.ModelConfig{"model-a": {Limit: config.ModelLimit{Context: 64000, Output: 4096}}},
	}, []string{"key-a", "key-b"})
	fallback := &blockingStreamProvider{streamedCh: make(chan struct{}), releaseCh: make(chan struct{}), calls: []scriptedStreamCall{{holdAfterStreams: true}}}
	client := llm.NewClient(primary, &blockingStreamProvider{calls: []scriptedStreamCall{{err: &llm.APIError{StatusCode: 500, Message: "unavailable"}}}}, "model-a", 4096, "sys")
	client.SetFallbackModels([]llm.FallbackModel{{
		ProviderConfig: newSidebarTestProviderConfig("provider-b", "model-b", 128000, 128000),
		ProviderImpl:   fallback, ModelID: "model-b", MaxTokens: 4096,
	}})
	client.SetServiceTier(config.ServiceTierFast)
	a.swapLLMClientWithRef(client, "model-a", 64000, "provider-a/model-a")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := client.CompleteStream(ctx, nil, nil, nil); done <- err }()
	select {
	case <-fallback.streamedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for fallback")
	}
	state := a.FocusedModelState()
	if state.DisplayRef != "provider-b/model-b" || state.RunningRef != "provider-a/model-a" || state.KeysTotal != 1 {
		t.Fatalf("target snapshot = %#v", state)
	}
	if state.ServiceTier != config.ServiceTierFast || state.EffectiveTier != config.ServiceTierStandard || a.EffectiveServiceTier() != config.ServiceTierStandard {
		t.Fatalf("target tier = requested %q, effective %q", state.ServiceTier, state.EffectiveTier)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for cancellation")
	}
}

func TestFocusedModelStatePreviewsOverlappingPoolAtSelectedModel(t *testing.T) {
	a := newToolPhaseSwitchTestAgent(t)
	a.activeConfig.Models["fast"] = []string{"provider/gpt-5.5", "provider/claude-opus-4"}
	client := a.llmClient
	pool, _ := client.ModelPoolSnapshot()
	other, _, _, err := a.modelSwitchFactory("provider/claude-opus-4", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	pool = append(pool, llm.FallbackModel{ProviderConfig: other.ProviderConfig(), ModelID: "claude-opus-4"})
	client.SetModelPool(pool, 1)
	a.newTurn()
	a.turn.PendingToolCalls.Store(1)
	a.SetCurrentModelPool("fast")
	dispatchPendingEvents(t, a)
	if got := a.FocusedModelState().DisplayRef; got != "provider/gpt-5.5" {
		t.Fatalf("overlapping pool preview = %q, want selected model", got)
	}
	a.turn.PendingToolCalls.Store(0)
	a.applyPendingModelPoolSwitchesAtRequestBoundary()
	if got := a.FocusedModelState().DisplayRef; got != "provider/gpt-5.5" {
		t.Fatalf("installed pool display = %q, want previewed model", got)
	}
}

func TestPendingPoolTierCapabilitiesAreAvailableBeforeInstallation(t *testing.T) {
	a := newToolPhaseSwitchTestAgent(t)
	a.projectConfig = &config.Config{Providers: map[string]config.ProviderConfig{
		"provider": {Models: map[string]config.ModelConfig{
			"claude-opus-4": {SupportedServiceTiers: []config.ServiceTier{config.ServiceTierFast}},
		}},
	}}
	a.llmClient.SetServiceTier(config.ServiceTierFast)
	a.newTurn()
	a.turn.PendingToolCalls.Store(1)
	a.SetCurrentModelPool("fast")
	dispatchPendingEvents(t, a)
	state := a.FocusedModelState()
	if state.DisplayRef != "provider/claude-opus-4" || state.EffectiveTier != config.ServiceTierFast || a.EffectiveServiceTier() != config.ServiceTierFast {
		t.Fatalf("pending target tier = %#v", state)
	}
	if got := a.SupportedServiceTiers(); !slices.Contains(got, config.ServiceTierFast) {
		t.Fatalf("supported tiers = %v", got)
	}
	a.handleTierCommand("/tier fast", true)
	for _, evt := range drainAgentEvents(a.Events()) {
		if toast, ok := evt.(ToastEvent); ok && toast.Level == "error" {
			t.Fatalf("pending target tier was rejected: %s", toast.Message)
		}
	}
}
