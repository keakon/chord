package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

// installApplyPatchPoolPolicyForTest wires two pools whose models sit on
// opposite sides of the per-model file-tool selection: the base pool is
// patch-native (apply_patch, write/delete hidden) and the fast pool is
// edit-only. Switching pools therefore also flips the file-tool surface.
func installApplyPatchPoolPolicyForTest(t *testing.T, a *MainAgent) {
	t.Helper()
	policy := NewRuntimeModelPoolPolicy()
	policy.SetCurrentModelPool("base")
	a.SetModelPoolPolicy(policy, "")
	a.SetModelSwitchFactory(func(providerModel string, _ []string, _ string) (*llm.Client, string, int, error) {
		providerCfg := llm.NewProviderConfig("provider", config.ProviderConfig{
			Type: config.ProviderTypeChatCompletions,
			Models: map[string]config.ModelConfig{
				"gpt-5.5":       {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
				"claude-opus-4": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
			},
		}, []string{"test-key"})
		modelID := strings.TrimPrefix(providerModel, "provider/")
		return llm.NewClient(providerCfg, stubProvider{}, modelID, 1024, ""), modelID, 8192, nil
	})
	cfg := &config.AgentConfig{
		Name: "test",
		Mode: config.AgentModeMain,
		Models: map[string][]string{
			"base": {"provider/gpt-5.5"},
			"fast": {"provider/claude-opus-4"},
		},
	}
	a.agentConfigs = map[string]*config.AgentConfig{"test": cfg}
	a.activeConfig = cfg
}

func newToolPhaseSwitchTestAgent(t *testing.T) *MainAgent {
	t.Helper()
	a := newTestMainAgent(t, t.TempDir())
	installApplyPatchPoolPolicyForTest(t, a)
	if err := a.ApplyInitialModel("provider/gpt-5.5"); err != nil {
		t.Fatalf("ApplyInitialModel: %v", err)
	}
	drainAgentEvents(a.Events())
	a.tools.Register(tools.ApplyPatchTool{})
	a.tools.Register(tools.EditTool{})
	a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}
	return a
}

// A pool switch requested while a request is in flight must not land while the
// tool calls of that request's response are still executing: those calls were
// emitted against the producing model's file-tool surface, so the patch call
// must run before the edit-only model's surface takes effect.
func TestModelPoolSwitchWaitsForResponseToolPhase(t *testing.T) {
	a := newToolPhaseSwitchTestAgent(t)
	a.newTurn()

	a.mainLLMRequestInFlight.Store(true)
	a.SetCurrentModelPool("fast")
	dispatchPendingEvents(t, a)
	if !a.pendingMainModelPoolSwitch {
		t.Fatal("pendingMainModelPoolSwitch = false after in-flight switch, want deferred")
	}

	// The response arrives carrying the apply_patch call the running
	// (patch-native) model emitted.
	a.mainLLMRequestInFlight.Store(false)
	a.handleLLMResponse(Event{
		Type:   EventLLMResponse,
		TurnID: a.turn.ID,
		Payload: &LLMResponsePayload{
			ToolCalls: []message.ToolCall{{
				ID:   "call-patch",
				Name: tools.NameApplyPatch,
				Args: json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: missing.txt\n@@\n-old\n+new\n*** End Patch"}`),
			}},
			StopReason: "tool_calls",
		},
	})

	if !a.pendingMainModelPoolSwitch {
		t.Fatal("pending switch landed during the response tool phase, want it held for the next request")
	}
	if got := a.ProviderModelRef(); got != "provider/gpt-5.5" {
		t.Fatalf("ProviderModelRef during tool phase = %q, want provider/gpt-5.5", got)
	}
	if _, ok := a.mainVisibleLLMToolNames()[tools.NameApplyPatch]; !ok {
		t.Fatal("apply_patch left the visible surface during the tool phase")
	}

	// The next request boundary is where the deferred switch lands.
	a.turn.PendingToolCalls.Store(0)
	a.turn.toolExecutionBatches = nil
	a.turn.nextToolBatch = 0
	a.applyPendingModelPoolSwitchesAtRequestBoundary()

	if a.pendingMainModelPoolSwitch {
		t.Fatal("pendingMainModelPoolSwitch = true after the next request boundary")
	}
	if got := a.ProviderModelRef(); got != "provider/claude-opus-4" {
		t.Fatalf("ProviderModelRef after boundary = %q, want provider/claude-opus-4", got)
	}
	if _, ok := a.mainVisibleLLMToolNames()[tools.NameApplyPatch]; ok {
		t.Fatal("apply_patch still visible after the switch landed")
	}
}

// A switch requested while the response's tool calls are already running defers
// the same way: the running model and its per-model tools stay unchanged until
// the tool phase ends and the next request is prepared.
func TestModelPoolSwitchDuringToolPhaseStaysDeferred(t *testing.T) {
	a := newToolPhaseSwitchTestAgent(t)
	a.newTurn()
	a.turn.PendingToolCalls.Store(1)

	a.SetCurrentModelPool("fast")
	dispatchPendingEvents(t, a)

	if got := a.ProviderModelRef(); got != "provider/gpt-5.5" {
		t.Fatalf("ProviderModelRef during tool phase = %q, want provider/gpt-5.5", got)
	}
	if !a.pendingMainModelPoolSwitch {
		t.Fatal("pendingMainModelPoolSwitch = false, want the switch deferred to the next request")
	}
	// A boundary call that still sees outstanding tool calls must not apply it.
	a.applyPendingModelPoolSwitchesAtRequestBoundary()
	if got := a.ProviderModelRef(); got != "provider/gpt-5.5" {
		t.Fatalf("ProviderModelRef after tool-phase boundary = %q, want provider/gpt-5.5", got)
	}

	a.turn.PendingToolCalls.Store(0)
	a.applyPendingModelPoolSwitchesAtRequestBoundary()
	if got := a.ProviderModelRef(); got != "provider/claude-opus-4" {
		t.Fatalf("ProviderModelRef after the tool phase ended = %q, want provider/claude-opus-4", got)
	}
	if a.pendingMainModelPoolSwitch {
		t.Fatal("pendingMainModelPoolSwitch = true after the tool phase ended")
	}
}

// The same deferral covers the main role switched by name
// (/models --agent <role> <pool>): it reaches the same main client and must
// not land while that client's response tool calls are still executing.
func TestMainRoleAgentModelPoolSwitchDuringToolPhaseStaysDeferred(t *testing.T) {
	a := newToolPhaseSwitchTestAgent(t)
	a.newTurn()
	a.turn.PendingToolCalls.Store(1)

	if err := a.SetAgentModelPool("test", "fast"); err != nil {
		t.Fatalf("SetAgentModelPool: %v", err)
	}
	dispatchPendingEvents(t, a)

	if got := a.ProviderModelRef(); got != "provider/gpt-5.5" {
		t.Fatalf("ProviderModelRef during tool phase = %q, want provider/gpt-5.5", got)
	}
	if !a.pendingMainModelPoolSwitch {
		t.Fatal("pendingMainModelPoolSwitch = false, want the switch deferred to the next request")
	}

	a.turn.PendingToolCalls.Store(0)
	a.applyPendingModelPoolSwitchesAtRequestBoundary()
	if got := a.ProviderModelRef(); got != "provider/claude-opus-4" {
		t.Fatalf("ProviderModelRef after the tool phase ended = %q, want provider/claude-opus-4", got)
	}
	if a.pendingMainModelPoolSwitch {
		t.Fatal("pendingMainModelPoolSwitch = true after the tool phase ended")
	}
}
