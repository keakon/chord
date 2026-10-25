package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestHandleTierCommandRejectsEmptyTierArgument(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())

	a.handleTierCommand("/tier", true)
	toast := waitForToastEvent(t, a.Events(), "Usage: /tier standard | /tier fast | /tier slow")
	if toast.Level != "info" {
		t.Fatalf("toast level = %q, want info", toast.Level)
	}
	if got := a.ServiceTier(); got != config.ServiceTierStandard {
		t.Fatalf("expected empty /tier to leave service tier unchanged, got %q", got)
	}
}

func TestHandleTierCommandRejectsUnsupportedTier(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())

	providerCfg := llm.NewProviderConfig("standard-only", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"test-key"})
	a.llmMu.Lock()
	a.llmClient = llm.NewClient(providerCfg, stubProvider{}, "model", 1024, "")
	a.llmMu.Unlock()

	a.handleTierCommand("/tier fast", true)
	toast := waitForToastEvent(t, a.Events(), "Service tier fast is not supported by the current model")
	if toast.Level != "error" {
		t.Fatalf("toast level = %q, want error", toast.Level)
	}
	if got := a.ServiceTier(); got != config.ServiceTierStandard {
		t.Fatalf("service tier = %q, want unchanged standard", got)
	}
}

func TestEffectiveAndSupportedServiceTierUseNextRequestModelRef(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	providerA := llm.NewProviderConfig("slow-provider", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"slow-model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
		SupportedServiceTiers: []config.ServiceTier{config.ServiceTierSlow},
	}, []string{"slow-key"})
	providerB := llm.NewProviderConfig("standard-provider", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"standard-model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"standard-key"})
	client := llm.NewClient(providerA, stubProvider{}, "slow-model", 1024, "")
	client.SetModelPool([]llm.FallbackModel{
		{ProviderConfig: providerA, ProviderImpl: stubProvider{}, ModelID: "slow-model", ContextLimit: 8192, MaxTokens: 1024},
		{ProviderConfig: providerB, ProviderImpl: stubProvider{}, ModelID: "standard-model", ContextLimit: 8192, MaxTokens: 1024},
	}, 0)
	client.SetServiceTier(config.ServiceTierSlow)
	a.swapLLMClientWithRef(client, "slow-model", 8192, "slow-provider/slow-model")

	a.llmMu.Lock()
	a.runningModelRef = "standard-provider/standard-model"
	a.llmMu.Unlock()

	if got := a.ServiceTier(); got != config.ServiceTierSlow {
		t.Fatalf("ServiceTier() = %q, want requested slow", got)
	}
	if got := a.EffectiveServiceTier(); got != config.ServiceTierSlow {
		t.Fatalf("EffectiveServiceTier() = %q, want slow for the next request model", got)
	}
	supported := a.SupportedServiceTiers()
	if !slices.Equal(supported, []config.ServiceTier{config.ServiceTierStandard, config.ServiceTierSlow}) {
		t.Fatalf("SupportedServiceTiers() = %#v, want [standard slow] for the next request model", supported)
	}
}

func TestSyncSubAgentOverlayPreservesSubAgentPermissions(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.activeConfig = &config.AgentConfig{Name: "builder"}
	a.ruleset = permission.Ruleset{{Permission: tools.NameRead, Pattern: "*", Action: permission.ActionAllow}}
	a.agentConfigs = map[string]*config.AgentConfig{
		"worker": {
			Name:       "worker",
			Permission: parsePermissionNode(t, "Write: deny\n"),
		},
	}
	sub := newControllableTestSubAgent(t, a, "adhoc-rules")
	sub.setRuleset(a.buildSubAgentRuleset(a.agentConfigs[sub.agentDefName]))

	if got := sub.currentRuleset().Evaluate(tools.NameWrite, "notes.txt"); got != permission.ActionDeny {
		t.Fatalf("initial subagent Write permission = %q, want deny", got)
	}

	if err := a.AddOverlayRule(permission.Rule{Permission: tools.NameShell, Pattern: "*", Action: permission.ActionAllow}, permission.ScopeSession); err != nil {
		t.Fatalf("AddOverlayRule: %v", err)
	}

	if got := sub.currentRuleset().Evaluate(tools.NameShell, "git status --short"); got != permission.ActionAllow {
		t.Fatalf("subagent overlay Shell permission = %q, want allow", got)
	}
	if got := sub.currentRuleset().Evaluate(tools.NameWrite, "notes.txt"); got != permission.ActionDeny {
		t.Fatalf("subagent Write permission after overlay sync = %q, want deny", got)
	}
}

func TestOverlayRuleChangesRefreshRuntimeSurfaces(t *testing.T) {
	t.Parallel()
	a := newReadyTestMainAgent(t)
	a.activeConfig = &config.AgentConfig{Name: "builder"}
	a.initOverlay()
	a.sessionBuilt.Store(true)
	a.surfaceDirty.Store(false)

	if err := a.AddOverlayRule(permission.Rule{Permission: tools.NameGrep, Pattern: "*", Action: permission.ActionDeny}, permission.ScopeSession); err != nil {
		t.Fatalf("AddOverlayRule: %v", err)
	}
	if a.sessionBuilt.Load() || !a.surfaceDirty.Load() {
		t.Fatalf("runtime surface state after add: sessionBuilt=%v surfaceDirty=%v", a.sessionBuilt.Load(), a.surfaceDirty.Load())
	}
	waitForEnvStatusUpdateEvent(t, a.Events())

	a.sessionBuilt.Store(true)
	a.surfaceDirty.Store(false)
	if err := a.RemoveOverlayAddedRule(0); err != nil {
		t.Fatalf("RemoveOverlayAddedRule: %v", err)
	}
	if a.sessionBuilt.Load() || !a.surfaceDirty.Load() {
		t.Fatalf("runtime surface state after remove: sessionBuilt=%v surfaceDirty=%v", a.sessionBuilt.Load(), a.surfaceDirty.Load())
	}
	waitForEnvStatusUpdateEvent(t, a.Events())
}

func TestSubAgentRuleIntentRefreshPreservesSubAgentPermissions(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.activeConfig = &config.AgentConfig{Name: "builder"}
	a.agentConfigs = map[string]*config.AgentConfig{
		"worker": {
			Name:       "worker",
			Permission: parsePermissionNode(t, "Write: deny\n"),
		},
	}
	sub := newControllableTestSubAgent(t, a, "adhoc-rule-intent")
	sub.setRuleset(a.buildSubAgentRuleset(a.agentConfigs[sub.agentDefName]))

	pipeline := sub.toolExecutionPipeline()
	refreshed := pipeline.refreshRulesetAfterRuleIntent(tools.NameShell, &ConfirmRuleIntent{
		Rules: []ConfirmRule{{Pattern: "*"}},
		Scope: int(permission.ScopeSession),
	})

	if got := refreshed.Evaluate(tools.NameShell, "git status --short"); got != permission.ActionAllow {
		t.Fatalf("subagent rule-intent Shell permission = %q, want allow", got)
	}
	if got := refreshed.Evaluate(tools.NameWrite, "notes.txt"); got != permission.ActionDeny {
		t.Fatalf("subagent Write permission after rule-intent refresh = %q, want deny", got)
	}
	if got := sub.currentRuleset().Evaluate(tools.NameWrite, "notes.txt"); got != permission.ActionDeny {
		t.Fatalf("stored subagent Write permission after rule-intent refresh = %q, want deny", got)
	}
}

// TestSubAgentRuleIntentProjectRuleArchivedToSubAgentRole reproduces the bug
// where a project-scoped permission rule triggered from a SubAgent confirm
// picker was persisted to the MainAgent's role file instead of the SubAgent's
// own role file, so it never took effect after reload.
func TestSubAgentRuleIntentProjectRuleArchivedToSubAgentRole(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.activeConfig = &config.AgentConfig{Name: "builder"}
	a.agentConfigs = map[string]*config.AgentConfig{
		"worker": {
			Name:       "worker",
			Permission: parsePermissionNode(t, "Write: deny\n"),
		},
	}
	sub := newControllableTestSubAgent(t, a, "adhoc-archive")
	sub.setRuleset(a.buildSubAgentRuleset(a.agentConfigs[sub.agentDefName]))

	pipeline := sub.toolExecutionPipeline()
	pipeline.refreshRulesetAfterRuleIntent(tools.NameShell, &ConfirmRuleIntent{
		Rules: []ConfirmRule{{Pattern: "*"}},
		Scope: int(permission.ScopeProject),
	})

	workerPath := filepath.Join(projectRoot, ".chord", "agents", "worker.yaml")
	builderPath := filepath.Join(projectRoot, ".chord", "agents", "builder.yaml")
	data, err := os.ReadFile(workerPath)
	if err != nil {
		var builderInfo string
		if _, berr := os.Stat(builderPath); berr == nil {
			builderInfo = "builder file WAS written instead of worker"
		} else {
			builderInfo = "neither worker nor builder written"
		}
		t.Fatalf("expected worker agent file to hold the rule: %v\n%s", err, builderInfo)
	}
	if !strings.Contains(string(data), tools.NameShell) || !strings.Contains(string(data), string(permission.ActionAllow)) {
		t.Fatalf("worker agent file missing Shell allow rule:\n%s", data)
	}
	if bcontent, berr := os.ReadFile(builderPath); berr == nil {
		t.Fatalf("builder agent file should not have been written, got content:\n%s", bcontent)
	}
}

// TestSubAgentRuleIntentSessionRuleSurvivesMainRoleSwitch reproduces the bug
// where a session-scoped permission rule triggered from a SubAgent was bucketed
// under the MainAgent's active role; switching the MainAgent role dropped that
// bucket from the merged ruleset and the SubAgent lost its rule.
func TestSubAgentRuleIntentSessionRuleSurvivesMainRoleSwitch(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.activeConfig = &config.AgentConfig{Name: "builder"}
	a.agentConfigs = map[string]*config.AgentConfig{
		"worker":  {Name: "worker", Permission: parsePermissionNode(t, "Write: deny\n")},
		"planner": {Name: "planner"},
	}
	sub := newControllableTestSubAgent(t, a, "adhoc-switch")
	sub.setRuleset(a.buildSubAgentRuleset(a.agentConfigs[sub.agentDefName]))

	pipeline := sub.toolExecutionPipeline()
	refreshed := pipeline.refreshRulesetAfterRuleIntent(tools.NameShell, &ConfirmRuleIntent{
		Rules: []ConfirmRule{{Pattern: "*"}},
		Scope: int(permission.ScopeSession),
	})
	if got := refreshed.Evaluate(tools.NameShell, "git status --short"); got != permission.ActionAllow {
		t.Fatalf("subagent Shell permission = %q, want allow", got)
	}
	if got := refreshed.Evaluate(tools.NameWrite, "notes.txt"); got != permission.ActionDeny {
		t.Fatalf("subagent Write permission = %q, want deny", got)
	}

	if err := a.switchRole("planner", false); err != nil {
		t.Fatalf("switchRole: %v", err)
	}

	if got := sub.currentRuleset().Evaluate(tools.NameShell, "git status --short"); got != permission.ActionAllow {
		t.Fatalf("subagent Shell permission after role switch = %q, want allow", got)
	}
	if got := sub.currentRuleset().Evaluate(tools.NameWrite, "notes.txt"); got != permission.ActionDeny {
		t.Fatalf("subagent Write permission after role switch = %q, want deny", got)
	}
}

func TestCreateSubAgentInheritsServiceTier(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 1)
	client, _, _, _ := a.llmSnapshot()
	if client == nil {
		t.Fatal("expected llm client")
	}
	client.SetServiceTier(config.ServiceTierSlow)

	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{Description: "child work", AgentType: "worker"})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	child := a.subAgentByTaskID(handle.TaskID)
	if child == nil {
		t.Fatal("expected child SubAgent to exist")
	}
	childClient, _ := child.llmSnapshot()
	if childClient == nil {
		t.Fatal("expected child SubAgent client")
	}
	if got := childClient.ServiceTier(); got != config.ServiceTierSlow {
		t.Fatalf("child service tier = %q, want %q", got, config.ServiceTierSlow)
	}
	foundStarted := false
	for len(a.outputCh) > 0 {
		if started, ok := (<-a.outputCh).(AgentStartedEvent); ok {
			if started.AgentID != child.instanceID || started.TaskID != child.taskID || started.AgentType != "worker" || started.ParentAgentID != "main" || started.Description != "child work" {
				t.Fatalf("started event = %#v", started)
			}
			foundStarted = true
		}
	}
	if !foundStarted {
		t.Fatal("expected AgentStartedEvent")
	}
}

func TestHandleTierCommandSyncsExistingSubAgents(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-tier")
	subClient, _ := sub.llmSnapshot()
	if subClient == nil {
		t.Fatal("expected SubAgent client")
	}

	a.handleTierCommand("/tier fast", true)
	if got := subClient.ServiceTier(); got != config.ServiceTierFast {
		t.Fatalf("subagent service tier = %q, want %q", got, config.ServiceTierFast)
	}

	a.handleTierCommand("/tier slow", true)
	if got := subClient.ServiceTier(); got != config.ServiceTierSlow {
		t.Fatalf("subagent service tier = %q, want %q", got, config.ServiceTierSlow)
	}
}
