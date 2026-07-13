package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

// dispatchPendingEvents drains any events sitting on the agent's internal
// event bus and dispatches them synchronously. Used in tests that bypass
// Run() but need event-loop-side handling to actually execute.
func dispatchPendingEvents(t *testing.T, a *MainAgent) {
	t.Helper()
	for {
		select {
		case evt := <-a.eventCh:
			a.dispatch(evt)
		default:
			return
		}
	}
}

// drainEventsByType collects events and groups them by Go type name. Useful for
// asserting which event kinds were emitted without depending on order.
func drainEventsByType(t *testing.T, events <-chan AgentEvent) map[string]int {
	t.Helper()
	out := drainAgentEvents(events)
	counts := make(map[string]int, len(out))
	for _, ev := range out {
		switch ev.(type) {
		case ModelSelectEvent:
			counts["ModelSelectEvent"]++
		case InfoEvent:
			counts["InfoEvent"]++
		case ToastEvent:
			counts["ToastEvent"]++
		case IdleEvent:
			counts["IdleEvent"]++
		case AgentActivityEvent:
			counts["AgentActivityEvent"]++
		case RequestCycleStartedEvent:
			counts["RequestCycleStartedEvent"]++
		case ErrorEvent:
			counts["ErrorEvent"]++
		default:
			counts["other"]++
		}
	}
	return counts
}

// installPoolPolicyForTest wires up a minimal RuntimeModelPoolPolicy and
// agentConfigs so /models <pool> can resolve a target. The test agent uses the
// "test" role; we register two pools so SetCurrentModelPool has somewhere to go.
func installPoolPolicyForTest(t *testing.T, a *MainAgent) {
	t.Helper()
	policy := NewRuntimeModelPoolPolicy()
	policy.SetCurrentModelPool("base")
	a.SetModelPoolPolicy(policy, "")
	a.SetModelSwitchFactory(func(providerModel string) (*llm.Client, string, int, error) {
		providerCfg := llm.NewProviderConfig("provider", config.ProviderConfig{
			Type: config.ProviderTypeChatCompletions,
			Models: map[string]config.ModelConfig{
				"model-a": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
				"model-b": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
			},
		}, []string{"test-key"})
		modelID := strings.TrimPrefix(providerModel, "provider/")
		return llm.NewClient(providerCfg, stubProvider{}, modelID, 1024, ""), modelID, 8192, nil
	})
	cfg := &config.AgentConfig{
		Name: "test",
		Mode: config.AgentModeMain,
		Models: map[string][]string{
			"base": {"provider/model-a"},
			"fast": {"provider/model-b"},
		},
	}
	a.agentConfigs = map[string]*config.AgentConfig{"test": cfg}
	a.activeConfig = cfg
}

func TestTUISetCurrentModelPoolRunsOnEventLoopBeforeQueuedUserMessageDrain(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	installPoolPolicyForTest(t, a)
	a.ApplyInitialModel("provider/model-a")
	drainAgentEvents(a.Events())

	a.newTurn()
	turn := a.turn
	a.QueuePendingUserDraft("draft-1", []message.ContentPart{{Type: "text", Text: "queued message"}})
	a.SetCurrentModelPool("fast")
	if got := a.ModelPoolPolicy().CurrentModelPool(); got != "base" {
		t.Fatalf("CurrentModelPool changed before event-loop dispatch = %q, want base", got)
	}

	dispatchPendingEvents(t, a)
	if got := a.ModelPoolPolicy().CurrentModelPool(); got != "fast" {
		t.Fatalf("CurrentModelPool after dispatch = %q, want fast", got)
	}
	if got := a.ProviderModelRef(); got != "provider/model-b" {
		t.Fatalf("ProviderModelRef after switch = %q, want provider/model-b", got)
	}
	if a.turn != turn {
		t.Fatal("model pool switch dispatch should not clear the active turn")
	}
	if got := a.PendingUserMessageCount(); got != 1 {
		t.Fatalf("PendingUserMessageCount = %d, want queued draft preserved until request boundary", got)
	}
}

func TestInFlightModelPoolSwitchDefersClientSwapUntilRequestBoundary(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	installPoolPolicyForTest(t, a)
	if err := a.ApplyInitialModel("provider/model-a"); err != nil {
		t.Fatalf("ApplyInitialModel: %v", err)
	}
	drainAgentEvents(a.Events())

	a.newTurn()
	a.mainLLMRequestInFlight.Store(true)
	a.SetCurrentModelPool("fast")
	dispatchPendingEvents(t, a)

	if got := a.ModelPoolPolicy().CurrentModelPool(); got != "fast" {
		t.Fatalf("CurrentModelPool after dispatch = %q, want fast", got)
	}
	if got := a.ProviderModelRef(); got != "provider/model-a" {
		t.Fatalf("ProviderModelRef while request in flight = %q, want provider/model-a", got)
	}
	if !a.pendingMainModelPoolSwitch {
		t.Fatal("pendingMainModelPoolSwitch = false, want true")
	}

	a.mainLLMRequestInFlight.Store(false)
	a.applyPendingModelPoolSwitchesAtRequestBoundary()

	if got := a.ProviderModelRef(); got != "provider/model-b" {
		t.Fatalf("ProviderModelRef after request boundary = %q, want provider/model-b", got)
	}
	if a.pendingMainModelPoolSwitch {
		t.Fatal("pendingMainModelPoolSwitch = true after apply, want false")
	}
}

func TestInFlightModelPoolSwitchStaysDeferredAcrossOversizeSuspend(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	installPoolPolicyForTest(t, a)
	if err := a.ApplyInitialModel("provider/model-a"); err != nil {
		t.Fatalf("ApplyInitialModel: %v", err)
	}
	drainAgentEvents(a.Events())

	a.newTurn()
	turnID := a.turn.ID
	turnEpoch := a.turn.Epoch
	a.mainLLMRequestInFlight.Store(true)
	a.SetCurrentModelPool("fast")
	dispatchPendingEvents(t, a)

	a.handleCompactionOversizeSuspend(Event{Payload: &pendingMainLLMCall{
		continuation:     compactionResumeMainLLM,
		turnID:           turnID,
		turnEpoch:        turnEpoch,
		sessionEpoch:     a.sessionEpoch,
		agentErrSourceID: "main",
	}})

	if got := a.ProviderModelRef(); got != "provider/model-a" {
		t.Fatalf("ProviderModelRef after oversize suspend = %q, want provider/model-a", got)
	}
	if !a.mainLLMRequestInFlight.Load() {
		t.Fatal("mainLLMRequestInFlight = false after oversize suspend, want true")
	}
	if !a.pendingMainModelPoolSwitch {
		t.Fatal("pendingMainModelPoolSwitch = false after oversize suspend, want true")
	}

	a.mainLLMRequestInFlight.Store(false)
	a.applyPendingModelPoolSwitchesAtRequestBoundary()

	if got := a.ProviderModelRef(); got != "provider/model-b" {
		t.Fatalf("ProviderModelRef after real request boundary = %q, want provider/model-b", got)
	}
	if a.pendingMainModelPoolSwitch {
		t.Fatal("pendingMainModelPoolSwitch = true after real boundary, want false")
	}
}

func TestInFlightModelPoolSwitchRollsBackWhenDeferredClientSwapFails(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	installPoolPolicyForTest(t, a)
	if err := a.ApplyInitialModel("provider/model-a"); err != nil {
		t.Fatalf("ApplyInitialModel: %v", err)
	}
	drainAgentEvents(a.Events())

	a.SetModelSwitchFactory(func(providerModel string) (*llm.Client, string, int, error) {
		return nil, "", 0, errors.New("factory unavailable for " + providerModel)
	})

	a.newTurn()
	a.mainLLMRequestInFlight.Store(true)
	a.SetCurrentModelPool("fast")
	dispatchPendingEvents(t, a)

	if got := a.ModelPoolPolicy().CurrentModelPool(); got != "fast" {
		t.Fatalf("CurrentModelPool after pending switch = %q, want fast", got)
	}
	if got := a.ProviderModelRef(); got != "provider/model-a" {
		t.Fatalf("ProviderModelRef while request in flight = %q, want provider/model-a", got)
	}

	a.mainLLMRequestInFlight.Store(false)
	a.applyPendingModelPoolSwitchesAtRequestBoundary()

	if got := a.ModelPoolPolicy().CurrentModelPool(); got != "base" {
		t.Fatalf("CurrentModelPool after failed deferred apply = %q, want rollback to base", got)
	}
	if got := a.ProviderModelRef(); got != "provider/model-a" {
		t.Fatalf("ProviderModelRef after failed deferred apply = %q, want provider/model-a", got)
	}
	if a.pendingMainModelPoolSwitch {
		t.Fatal("pendingMainModelPoolSwitch = true after failed apply, want false")
	}
	if a.pendingModelPoolRollback != nil {
		t.Fatal("pendingModelPoolRollback not cleared after failed apply")
	}
	events := drainEventsByType(t, a.Events())
	if events["ErrorEvent"] != 1 {
		t.Fatalf("events = %#v, want one ErrorEvent", events)
	}
}

func TestInFlightModelPoolSwitchRestoresOnlyFailedDeferredSelections(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	installPoolPolicyForTest(t, a)
	if err := a.ApplyInitialModel("provider/model-a"); err != nil {
		t.Fatalf("ApplyInitialModel: %v", err)
	}
	drainAgentEvents(a.Events())

	workerCfg := &config.AgentConfig{
		Name: "worker",
		Mode: config.AgentModeSubAgent,
		Models: map[string][]string{
			"base": {"provider/model-a"},
			"fast": {"provider/model-c"},
		},
	}
	a.agentConfigs["worker"] = workerCfg
	workerProvider := llm.NewProviderConfig("provider", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"model-a": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"test-key"})
	subCtx, subCancel := context.WithCancel(context.Background())
	t.Cleanup(subCancel)
	sub := &SubAgent{
		instanceID:   "worker-1",
		agentDefName: "worker",
		llmClient:    llm.NewClient(workerProvider, stubProvider{}, "model-a", 1024, ""),
		parent:       a,
		parentCtx:    subCtx,
		cancel:       subCancel,
	}
	a.subs.subAgents[sub.instanceID] = sub

	a.SetModelSwitchFactory(func(providerModel string) (*llm.Client, string, int, error) {
		if providerModel == "provider/model-c" {
			return nil, "", 0, errors.New("factory unavailable for " + providerModel)
		}
		providerCfg := llm.NewProviderConfig("provider", config.ProviderConfig{
			Type: config.ProviderTypeChatCompletions,
			Models: map[string]config.ModelConfig{
				"model-a": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
				"model-b": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
				"model-c": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
			},
		}, []string{"test-key"})
		modelID := strings.TrimPrefix(providerModel, "provider/")
		return llm.NewClient(providerCfg, stubProvider{}, modelID, 1024, ""), modelID, 8192, nil
	})

	a.newTurn()
	a.mainLLMRequestInFlight.Store(true)
	a.SetCurrentModelPool("fast")
	a.SetAgentModelPool("worker", "fast")
	dispatchPendingEvents(t, a)

	if got := a.ModelPoolPolicy().CurrentModelPool(); got != "fast" {
		t.Fatalf("CurrentModelPool after pending switch = %q, want fast", got)
	}
	if got, ok := a.ModelPoolPolicy().AgentOverride("worker"); !ok || got != "fast" {
		t.Fatalf("AgentOverride(worker) after pending switch = (%q, %v), want (fast, true)", got, ok)
	}

	a.mainLLMRequestInFlight.Store(false)
	a.applyPendingModelPoolSwitchesAtRequestBoundary()

	if got := a.ModelPoolPolicy().CurrentModelPool(); got != "fast" {
		t.Fatalf("CurrentModelPool after partial apply = %q, want successful main switch kept", got)
	}
	if got := a.ProviderModelRef(); got != "provider/model-b" {
		t.Fatalf("ProviderModelRef after partial apply = %q, want provider/model-b", got)
	}
	if got, ok := a.ModelPoolPolicy().AgentOverride("worker"); ok || got != "" {
		t.Fatalf("AgentOverride(worker) after failed agent apply = (%q, %v), want cleared override", got, ok)
	}
	if got := sub.llmClient.PrimaryModelRef(); got != "provider/model-a" {
		t.Fatalf("subagent model after failed agent apply = %q, want provider/model-a", got)
	}
	if a.pendingModelPoolRollback != nil {
		t.Fatal("pendingModelPoolRollback not cleared after partial apply")
	}
	events := drainEventsByType(t, a.Events())
	if events["ErrorEvent"] != 1 {
		t.Fatalf("events = %#v, want one ErrorEvent", events)
	}
}

func TestModelSwitchInvalidatesPreviousClientRetryPlan(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	installPoolPolicyForTest(t, a)

	oldCfg := llm.NewProviderConfig("provider", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"model-a": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"test-key"})
	oldProvider := &blockingStreamProvider{calls: []scriptedStreamCall{{err: &llm.APIError{StatusCode: 500, Message: "old pool failed"}}}}
	oldClient := llm.NewClient(oldCfg, oldProvider, "model-a", 1024, "")
	a.swapLLMClientWithRef(oldClient, "model-a", 8192, "provider/model-a")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, err := oldClient.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
		errCh <- err
	}()

	time.Sleep(100 * time.Millisecond)
	if err := a.switchModel("provider/model-b", false); err != nil {
		t.Fatalf("switchModel returned error: %v", err)
	}

	select {
	case err := <-errCh:
		if !llm.IsRoutingInvalidated(err) {
			t.Fatalf("old CompleteStream err = %v, want routing invalidated", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for old client retry plan to be invalidated")
	}
}

func TestBusyCurrentModelPoolSwitchInvalidatesPreviousClientRetryPlan(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	installPoolPolicyForTest(t, a)

	oldCfg := llm.NewProviderConfig("provider", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"model-a": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"test-key"})
	oldProvider := &blockingStreamProvider{calls: []scriptedStreamCall{{err: &llm.APIError{StatusCode: 500, Message: "old pool failed"}}}}
	oldClient := llm.NewClient(oldCfg, oldProvider, "model-a", 1024, "")
	a.swapLLMClientWithRef(oldClient, "model-a", 8192, "provider/model-a")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, err := oldClient.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
		errCh <- err
	}()

	time.Sleep(100 * time.Millisecond)
	a.mainLLMRequestInFlight.Store(true)
	a.SetCurrentModelPool("fast")
	dispatchPendingEvents(t, a)

	if got := a.ModelPoolPolicy().CurrentModelPool(); got != "fast" {
		t.Fatalf("CurrentModelPool after busy switch = %q, want fast", got)
	}
	if !a.pendingMainModelPoolSwitch {
		t.Fatal("pendingMainModelPoolSwitch = false, want true")
	}
	if got := a.ProviderModelRef(); got != "provider/model-a" {
		t.Fatalf("ProviderModelRef before request boundary = %q, want provider/model-a", got)
	}

	select {
	case err := <-errCh:
		if !llm.IsRoutingInvalidated(err) {
			t.Fatalf("old CompleteStream err = %v, want routing invalidated", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for busy model pool switch to invalidate old retry plan")
	}

	a.mainLLMRequestInFlight.Store(false)
	a.applyPendingModelPoolSwitchesAtRequestBoundary()
	if got := a.ProviderModelRef(); got != "provider/model-b" {
		t.Fatalf("ProviderModelRef after request boundary = %q, want provider/model-b", got)
	}
}

func TestSubAgentModelPoolSwitchDefersClientSwapUntilRequestBoundary(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	installPoolPolicyForTest(t, a)
	workerCfg := &config.AgentConfig{
		Name: "worker",
		Mode: config.AgentModeSubAgent,
		Models: map[string][]string{
			"base": {"provider/model-a"},
			"fast": {"provider/model-b"},
		},
	}
	a.agentConfigs["worker"] = workerCfg
	sub := newControllableTestSubAgent(t, a, "task-1")
	oldClient := sub.llmClient
	oldRef := oldClient.PrimaryModelRef()
	drainAgentEvents(a.Events())

	sub.llmRequestInFlight.Store(true)
	a.SetAgentModelPool("worker", "fast")
	dispatchPendingEvents(t, a)

	if got, ok := a.ModelPoolPolicy().AgentOverride("worker"); !ok || got != "fast" {
		t.Fatalf("AgentOverride(worker) = (%q, %v), want (fast, true)", got, ok)
	}
	if sub.llmClient != oldClient {
		t.Fatal("busy sub-agent client was swapped before request boundary")
	}
	if got := sub.llmClient.PrimaryModelRef(); got != oldRef {
		t.Fatalf("busy sub-agent model ref = %q, want %q", got, oldRef)
	}
	if len(a.pendingAgentModelPoolSwitch) != 1 {
		t.Fatalf("pendingAgentModelPoolSwitch = %#v, want worker pending", a.pendingAgentModelPoolSwitch)
	}

	sub.llmRequestInFlight.Store(false)
	a.handleSubAgentRequestBoundary(Event{Type: EventSubAgentRequestBoundary, SourceID: sub.instanceID})

	if sub.llmClient == oldClient {
		t.Fatal("sub-agent client was not swapped at request boundary")
	}
	if got := sub.llmClient.PrimaryModelRef(); got != "provider/model-b" {
		t.Fatalf("sub-agent model ref after boundary = %q, want provider/model-b", got)
	}
	if a.pendingAgentModelPoolSwitch != nil {
		t.Fatalf("pendingAgentModelPoolSwitch after boundary = %#v, want nil", a.pendingAgentModelPoolSwitch)
	}
}

func TestResumeTurnAfterRoutingInvalidationConsumesQueuedUserDraft(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	installPoolPolicyForTest(t, a)
	a.ApplyInitialModel("provider/model-a")
	drainAgentEvents(a.Events())

	a.newTurn()
	turn := a.turn
	if turn == nil {
		t.Fatal("expected active turn")
	}
	a.QueuePendingUserDraft("draft-1", []message.ContentPart{{Type: "text", Text: "queued message"}})
	a.SetCurrentModelPool("fast")
	dispatchPendingEvents(t, a)

	if a.PendingUserMessageCount() != 1 {
		t.Fatalf("PendingUserMessageCount before resume = %d, want 1", a.PendingUserMessageCount())
	}
	if ok := a.resumeTurnAfterRoutingInvalidation(turn.ID); !ok {
		t.Fatal("resumeTurnAfterRoutingInvalidation returned false, want true")
	}
	if a.turn != turn {
		t.Fatal("resumeTurnAfterRoutingInvalidation should keep the active turn")
	}
	if got := a.PendingUserMessageCount(); got != 0 {
		t.Fatalf("PendingUserMessageCount after resume = %d, want 0", got)
	}
	msgs := a.ctxMgr.Snapshot()
	if len(msgs) == 0 || msgs[len(msgs)-1].Role != "user" || msgs[len(msgs)-1].Content != "queued message" {
		t.Fatalf("last ctx message = %#v, want queued user message appended", msgs)
	}
}

// TestHandleModelsCommandBusyDoesNotClearTurn proves the central fix: when an
// active turn is in flight (e.g. LLM 429 retry), /models must not call
// setIdleAndDrainPending. Otherwise a.turn is wiped while the retry is still
// running and esc-cancel becomes a no-op.
func TestHandleModelsCommandBusyDoesNotClearTurn(t *testing.T) {
	cases := []struct {
		name      string
		content   string
		wantEvent string
	}{
		{name: "open selector", content: "/models", wantEvent: "ModelSelectEvent"},
		{name: "status", content: "/models status", wantEvent: "InfoEvent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			installPoolPolicyForTest(t, a)

			a.newTurn()
			turn := a.turn
			if turn == nil {
				t.Fatal("expected active turn")
			}
			drainAgentEvents(a.Events()) // drop RequestCycleStartedEvent

			a.handleModelsCommand(tc.content, true /* busy */)

			if a.turn == nil {
				t.Fatal("a.turn was cleared while busy=true; setIdleAndDrainPending should be skipped")
			}
			if a.turn != turn {
				t.Fatal("a.turn was replaced while busy=true; expected the active turn to be preserved")
			}

			counts := drainEventsByType(t, a.Events())
			if counts[tc.wantEvent] == 0 {
				t.Fatalf("expected %s to be emitted, got %v", tc.wantEvent, counts)
			}
			if counts["IdleEvent"] != 0 {
				t.Fatalf("IdleEvent should not be emitted while busy, got %v", counts)
			}
		})
	}
}

// TestHandleModelsCommandIdleClearsTurn confirms the idle path is unchanged:
// /models on an idle agent still calls setIdleAndDrainPending, leaving a.turn
// nil and emitting IdleEvent so any pending drafts can drain.
func TestHandleModelsCommandIdleClearsTurn(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	installPoolPolicyForTest(t, a)

	if a.turn != nil {
		t.Fatal("expected idle agent to start with a.turn == nil")
	}

	a.handleModelsCommand("/models", false /* busy */)

	if a.turn != nil {
		t.Fatal("a.turn should remain nil after /models on idle agent")
	}
	counts := drainEventsByType(t, a.Events())
	if counts["ModelSelectEvent"] == 0 {
		t.Fatalf("ModelSelectEvent missing, got %v", counts)
	}
	if counts["IdleEvent"] == 0 {
		t.Fatalf("IdleEvent missing on idle path, got %v", counts)
	}
}

func TestHandleUserMessageMCPWhileBusyRunsImmediately(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	controlStarted := make(chan struct{})
	releaseControl := make(chan struct{})
	a.SetMCPControlFunc(func(context.Context, MCPControlRequest) (MCPControlResult, error) {
		close(controlStarted)
		<-releaseControl
		return MCPControlResult{}, nil
	})
	t.Cleanup(func() { close(releaseControl) })
	a.newTurn()
	turn := a.turn
	drainAgentEvents(a.Events())

	a.handleUserMessage(Event{Type: EventUserMessage, Payload: "/mcp enable exa"})
	dispatchPendingEvents(t, a)
	select {
	case <-controlStarted:
	case <-time.After(time.Second):
		t.Fatal("busy /mcp command did not invoke MCP control")
	}

	if got := a.PendingUserMessageCount(); got != 0 {
		t.Fatalf("PendingUserMessageCount = %d, want 0", got)
	}
	if a.turn != turn {
		t.Fatal("busy /mcp should not clear or replace the active turn")
	}
	if !a.mcpTransitionActive.Load() {
		t.Fatal("busy /mcp command should start MCP transition immediately")
	}
}

func TestHandleUserMessageMCPDuringTransitionWarnsInsteadOfQueueing(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.mcpTransitionActive.Store(true)

	a.handleUserMessage(Event{Type: EventUserMessage, Payload: "/mcp enable exa"})
	dispatchPendingEvents(t, a)

	if got := a.PendingUserMessageCount(); got != 0 {
		t.Fatalf("PendingUserMessageCount = %d, want 0", got)
	}
	counts := drainEventsByType(t, a.Events())
	if counts["ToastEvent"] == 0 {
		t.Fatalf("ToastEvent missing, got %v", counts)
	}
	if counts["IdleEvent"] != 0 {
		t.Fatalf("IdleEvent should not be emitted while MCP transition is active, got %v", counts)
	}
}

// TestHandleExportCommandBusyDoesNotClearTurn mirrors the /models test for
// /export — same setIdleAndDrainPending pitfall, same fix.
func TestHandleExportCommandBusyDoesNotClearTurn(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())

	a.newTurn()
	turn := a.turn
	if turn == nil {
		t.Fatal("expected active turn")
	}
	drainAgentEvents(a.Events())

	exportPath := t.TempDir() + "/session.md"
	a.handleExportCommand("/export "+exportPath, true /* busy */)

	if a.turn == nil {
		t.Fatal("a.turn was cleared while busy=true; setIdleAndDrainPending should be skipped")
	}
	if a.turn != turn {
		t.Fatal("a.turn was replaced while busy=true")
	}
	counts := drainEventsByType(t, a.Events())
	if counts["IdleEvent"] != 0 {
		t.Fatalf("IdleEvent should not be emitted while busy, got %v", counts)
	}
}

// TestSendUserMessageRoutesLocalOnlyCommandThroughEventLoop verifies that
// /models is no longer executed inline on the caller's goroutine. The fix
// dispatches it via sendEvent so the event loop's busy-aware handler sees it
// instead. If this regresses, the bug from #cheerful-swinging-seal returns:
// /models during retry would once again null out a.turn from the wrong
// goroutine.
func TestSendUserMessageRoutesLocalOnlyCommandThroughEventLoop(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	installPoolPolicyForTest(t, a)

	// Pretend a turn is in flight so we'd see the bug if SendUserMessage
	// short-circuited inline.
	a.newTurn()
	turn := a.turn
	drainAgentEvents(a.Events())

	a.SendUserMessage("/models")

	// Inline execution would have cleared a.turn before we get here.
	if a.turn == nil || a.turn != turn {
		t.Fatal("/models was executed inline; expected dispatch via sendEvent")
	}

	// The event must be queued on the internal event bus; once we drain and
	// dispatch it under busy=true, a.turn must still survive.
	select {
	case evt := <-a.eventCh:
		if evt.Type != EventUserMessage {
			t.Fatalf("queued event type = %q, want %q", evt.Type, EventUserMessage)
		}
		a.dispatch(evt)
	default:
		t.Fatal("expected EventUserMessage on the event bus")
	}

	if a.turn == nil || a.turn != turn {
		t.Fatal("a.turn cleared after dispatching /models; busy-aware handler should preserve it")
	}
	counts := drainEventsByType(t, a.Events())
	if counts["ModelSelectEvent"] == 0 {
		t.Fatalf("ModelSelectEvent missing after dispatch, got %v", counts)
	}
}

// TestIsTUILocalOnlySlashCommand pins the predicate's surface so accidental
// scope creep (treating other slash commands as local-only) shows up in tests.
func TestIsTUILocalOnlySlashCommand(t *testing.T) {
	cases := []struct {
		content string
		want    bool
	}{
		{"/models", true},
		{"/models status", true},
		{"/models --agent foo bar", true},
		{"/models pool-a", true},
		{"  /models  ", true},
		{"/export", true},
		{"/export ~/out.md", true},
		{"/export --json", true},
		{"/mcp", true},
		{"/mcp enable exa", true},
		{"/tier", true},
		{"  /tier  ", true},
		{"/tier fast", true},
		{"/tier slow", true},
		{"/tier standard", true},
		{"/rename", true},
		{"/rename release review", true},
		{"/compact", true},
		{"/new", false},
		{"/loop", false},
		{"hello", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isTUILocalOnlySlashCommand(tc.content); got != tc.want {
			t.Errorf("isTUILocalOnlySlashCommand(%q) = %v, want %v", tc.content, got, tc.want)
		}
	}
}

func TestExecuteLocalOnlySlashCommandBareTierShowsUsage(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	if !a.executeLocalOnlySlashCommand("/tier", nil, true) {
		t.Fatal("expected bare /tier to be handled as a local-only command")
	}
	toast := waitForToastEvent(t, a.Events(), "Usage: /tier standard | /tier fast | /tier slow")
	if toast.Level != "info" {
		t.Fatalf("toast level = %q, want info", toast.Level)
	}
}
