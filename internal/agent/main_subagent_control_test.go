package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func newControllableTestSubAgent(t *testing.T, parent *MainAgent, taskID string) *SubAgent {
	t.Helper()
	ctx, cancel := context.WithCancel(parent.parentCtx)
	sub := NewSubAgent(SubAgentConfig{
		InstanceID:   "worker-1",
		TaskID:       taskID,
		AgentDefName: "worker",
		TaskDesc:     "do work",
		LLMClient:    newTestLLMClient(),
		Recovery:     parent.recoveryManager(),
		Parent:       parent,
		ParentCtx:    ctx,
		Cancel:       cancel,
		BaseTools:    parent.tools,
		WorkDir:      parent.projectRoot,
		SessionDir:   parent.sessionDir,
		ModelName:    "test-model",
	})
	parent.subs.mu.Lock()
	parent.subs.subAgents[sub.instanceID] = sub
	parent.subs.mu.Unlock()
	parent.syncTaskRecordFromSub(sub, "")
	return sub
}

func configureNestedDelegationTestRuntime(a *MainAgent, maxDepth int) {
	a.llmFactory = func(systemPrompt string, agentModels []string, variant string) *llm.Client {
		return newTestLLMClient()
	}
	a.agentConfigs = map[string]*config.AgentConfig{
		"worker": {
			Name:        "worker",
			Mode:        "subagent",
			Models:      map[string][]string{"default": {"sample/test-model"}},
			Delegation:  config.DelegationConfig{MaxChildren: 10, MaxDepth: maxDepth},
			Description: "Nested worker",
		},
	}
	a.activeConfig = &config.AgentConfig{
		Name:       "builder",
		Delegation: config.DelegationConfig{MaxChildren: 10, MaxDepth: maxDepth},
	}
}

func TestHandleTierCommandRejectsEmptyTierArgument(t *testing.T) {
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

func TestEffectiveAndSupportedServiceTierUseAgentRunningModelRef(t *testing.T) {
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
	if got := a.EffectiveServiceTier(); got != config.ServiceTierStandard {
		t.Fatalf("EffectiveServiceTier() = %q, want standard for unsupported running model", got)
	}
	supported := a.SupportedServiceTiers()
	if len(supported) != 1 || supported[0] != config.ServiceTierStandard {
		t.Fatalf("SupportedServiceTiers() = %#v, want [standard] for running model", supported)
	}
}

func TestSyncSubAgentOverlayPreservesSubAgentPermissions(t *testing.T) {
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
	a := newReadyTestMainAgent(t)
	a.activeConfig = &config.AgentConfig{Name: "builder"}
	a.initOverlay()
	a.sessionBuilt.Store(true)
	a.surfaceDirty.Store(false)

	if err := a.AddOverlayRule(permission.Rule{Permission: tools.NameLsp, Pattern: "*", Action: permission.ActionDeny}, permission.ScopeSession); err != nil {
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

func waitForEnvStatusUpdateEvent(t *testing.T, events <-chan AgentEvent) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if _, ok := event.(EnvStatusUpdateEvent); ok {
				return
			}
		case <-timer.C:
			t.Fatal("timed out waiting for environment refresh")
		}
	}
}

func TestSubAgentRuleIntentRefreshPreservesSubAgentPermissions(t *testing.T) {
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
		Patterns: []string{"*"},
		Scope:    int(permission.ScopeSession),
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
		Patterns: []string{"*"},
		Scope:    int(permission.ScopeProject),
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
		Patterns: []string{"*"},
		Scope:    int(permission.ScopeSession),
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
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 1)
	client, _, _, _ := a.llmSnapshot()
	if client == nil {
		t.Fatal("expected llm client")
	}
	client.SetServiceTier(config.ServiceTierSlow)

	handle, err := a.CreateSubAgent(context.Background(), "child work", "worker", "", "", tools.WriteScope{})
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

func startMainAgentLoopForTest(t *testing.T, a *MainAgent) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = a.Run(ctx)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !a.started.Load() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for MainAgent event loop to start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() {
		cancel()
		<-a.done
	})
	return cancel
}

func TestManualMainContinueBatchesCompletedMailboxesIntoSingleTurn(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.subAgentInbox.urgent = []SubAgentMailboxMessage{
		{MessageID: "a-1", AgentID: "worker-a", TaskID: "task-a", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent, Summary: "done a"},
		{MessageID: "b-1", AgentID: "worker-b", TaskID: "task-b", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent, Summary: "done b"},
	}

	a.handleContinueFromContext(Event{Type: EventContinue, Payload: manualContinueEvent{}})
	if a.turn == nil {
		t.Fatal("expected manual continue to start a main turn")
	}
	if got := len(a.pendingSubAgentMailboxes); got != 2 {
		t.Fatalf("pending mailbox batch len = %d, want 2", got)
	}
	if got := len(a.activeSubAgentMailboxes); got != 2 {
		t.Fatalf("active mailbox batch len = %d, want 2", got)
	}
	if a.pendingSubAgentMailboxes[0].MessageID != "a-1" || a.pendingSubAgentMailboxes[1].MessageID != "b-1" {
		t.Fatalf("unexpected pending mailbox batch order: %#v", a.pendingSubAgentMailboxes)
	}
	if got := len(a.subAgentInbox.urgent); got != 0 {
		t.Fatalf("len(urgent inbox) = %d, want 0 after batching", got)
	}
}

func TestManualMainUserMessageStagesMailboxBeforeTurn(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.subAgentInbox.urgent = []SubAgentMailboxMessage{{
		MessageID: "done-1",
		AgentID:   "worker-a",
		TaskID:    "task-a",
		Kind:      SubAgentMailboxKindCompleted,
		Priority:  SubAgentMailboxPriorityUrgent,
		Summary:   "done",
	}}

	a.handleUserMessage(Event{Type: EventUserMessage, Payload: "review the result"})

	if a.turn == nil {
		t.Fatal("manual user message did not start a main turn")
	}
	if got := len(a.pendingSubAgentMailboxes); got != 1 {
		t.Fatalf("pending mailbox batch len = %d, want 1", got)
	}
	if got := len(a.subAgentInbox.urgent); got != 0 {
		t.Fatalf("urgent inbox len = %d, want 0 after manual input", got)
	}
}

func TestPrepareSubAgentMailboxBatchForTurnContinuationStagesDecisionMailbox(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.subAgentInbox.urgent = []SubAgentMailboxMessage{{
		MessageID:   "wake-1",
		AgentID:     "worker-a",
		TaskID:      "task-a",
		Kind:        SubAgentMailboxKindDecisionRequired,
		Priority:    SubAgentMailboxPriorityInterrupt,
		Summary:     "need main decision",
		RequiresAck: true,
	}}
	a.newTurn()
	turnID := a.turn.ID

	if !a.prepareSubAgentMailboxBatchForTurnContinuation() {
		t.Fatal("expected busy-turn mailbox staging to succeed")
	}
	if a.turn == nil || a.turn.ID != turnID {
		t.Fatalf("turn changed during mailbox staging, got %#v want turn %d", a.turn, turnID)
	}
	if got := len(a.pendingSubAgentMailboxes); got != 1 {
		t.Fatalf("pending mailbox batch len = %d, want 1", got)
	}
	if got := len(a.activeSubAgentMailboxes); got != 1 {
		t.Fatalf("active mailbox batch len = %d, want 1", got)
	}
	if a.activeSubAgentMailbox == nil || a.activeSubAgentMailbox.MessageID != "wake-1" {
		t.Fatalf("activeSubAgentMailbox = %#v, want wake-1", a.activeSubAgentMailbox)
	}
	if got := len(a.subAgentInbox.urgent); got != 0 {
		t.Fatalf("len(urgent inbox) = %d, want 0 after staging", got)
	}
}

func TestParkedMainOwnedCompletedMailboxStillQueuesForMain(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-root")
	sub.agentDefName = "worker"
	a.newTurn()

	a.handleAgentDone(Event{
		Type:     EventAgentDone,
		SourceID: sub.instanceID,
		Payload:  &AgentResult{Summary: "root task done"},
	})

	if got := a.subAgentByID(sub.instanceID); got != nil {
		t.Fatalf("subAgentByID(%q) = %#v, want parked worker", sub.instanceID, got)
	}
	if rec := a.taskRecordByTaskID(sub.taskID); rec == nil || !rec.RuntimeParked {
		t.Fatalf("task record = %#v, want parked durable task", rec)
	}
	foundDone := false
	for len(a.outputCh) > 0 {
		if done, ok := (<-a.outputCh).(AgentDoneEvent); ok {
			if done.AgentID != sub.instanceID || done.TaskID != sub.taskID || done.AgentType != sub.agentDefName || done.ParentAgentID != "main" || done.Summary != "root task done" {
				t.Fatalf("done event = %#v", done)
			}
			foundDone = true
		}
	}
	if !foundDone {
		t.Fatal("expected AgentDoneEvent")
	}

	var evt Event
	select {
	case evt = <-a.eventCh:
	default:
		t.Fatal("expected completion mailbox event queued on eventCh")
	}
	if evt.Type != EventSubAgentMailbox {
		t.Fatalf("queued event type = %q, want %q", evt.Type, EventSubAgentMailbox)
	}

	a.dispatch(evt)

	if got := len(a.subAgentInbox.urgent); got != 1 {
		t.Fatalf("len(urgent inbox) = %d, want 1 completed mailbox", got)
	}
	msg := a.subAgentInbox.urgent[0]
	if msg.AgentID != sub.instanceID {
		t.Fatalf("mailbox AgentID = %q, want %q", msg.AgentID, sub.instanceID)
	}
	if msg.TaskID != sub.taskID {
		t.Fatalf("mailbox TaskID = %q, want %q", msg.TaskID, sub.taskID)
	}
	if msg.OwnerAgentID != "" || msg.OwnerTaskID != "" {
		t.Fatalf("mailbox owner = (%q,%q), want main-owned empty owner", msg.OwnerAgentID, msg.OwnerTaskID)
	}
	if msg.Kind != SubAgentMailboxKindCompleted {
		t.Fatalf("mailbox Kind = %q, want completed", msg.Kind)
	}
}

func TestClosedUnknownCompletedMailboxIsDropped(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()

	a.handleSubAgentMailboxEvent(Event{
		Type:     EventSubAgentMailbox,
		SourceID: "worker-missing",
		Payload: &SubAgentMailboxMessage{
			MessageID: "missing-1",
			AgentID:   "worker-missing",
			TaskID:    "adhoc-missing",
			Kind:      SubAgentMailboxKindCompleted,
			Priority:  SubAgentMailboxPriorityUrgent,
			Summary:   "done",
		},
	})

	if got := len(a.subAgentInbox.urgent) + len(a.subAgentInbox.normal) + len(a.subAgentInbox.progress); got != 0 {
		t.Fatalf("unexpected mailbox accepted for unknown closed worker, total=%d", got)
	}
}

func TestSetIdleAndDrainPendingConsumesAllActiveCompletedMailboxes(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.activeSubAgentMailboxes = []*SubAgentMailboxMessage{
		{MessageID: "a-1", AgentID: "worker-a", TaskID: "task-a", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent, Summary: "done a"},
		{MessageID: "b-1", AgentID: "worker-b", TaskID: "task-b", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent, Summary: "done b"},
	}
	a.activeSubAgentMailbox = a.activeSubAgentMailboxes[0]
	a.activeSubAgentMailboxAck = true
	a.ctxMgr.Append(message.Message{Role: "assistant", Content: "Handled both completed workers."})

	a.setIdleAndDrainPending()

	acks, err := loadSubAgentMailboxAcks(a.sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxAcks: %v", err)
	}
	for _, messageID := range []string{"a-1", "b-1"} {
		ack, ok := acks[messageID]
		if !ok {
			t.Fatalf("ack for %q not found", messageID)
		}
		if ack.Outcome != "consumed" {
			t.Fatalf("ack[%s].Outcome = %q, want consumed", messageID, ack.Outcome)
		}
	}
}

func TestTakeOutstandingMailboxForSubPreservesSiblingCompletedMailboxes(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	subA := newControllableTestSubAgent(t, a, "task-a")
	subA.instanceID = "worker-a"
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[subA.instanceID] = subA
	a.subs.mu.Unlock()

	subB := newControllableTestSubAgent(t, a, "task-b")
	subB.instanceID = "worker-b"
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[subB.instanceID] = subB
	a.subs.mu.Unlock()

	a.activeSubAgentMailboxes = []*SubAgentMailboxMessage{
		{MessageID: "a-1", AgentID: "worker-a", TaskID: "task-a", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent, Summary: "done a"},
		{MessageID: "b-1", AgentID: "worker-b", TaskID: "task-b", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent, Summary: "done b"},
	}
	a.activeSubAgentMailbox = a.activeSubAgentMailboxes[0]
	a.pendingSubAgentMailboxes = append([]*SubAgentMailboxMessage(nil), a.activeSubAgentMailboxes...)

	got := a.takeOutstandingMailboxForSub(subA)
	if got == nil || got.MessageID != "a-1" {
		t.Fatalf("takeOutstandingMailboxForSub() = %#v, want mailbox a-1", got)
	}
	if len(a.activeSubAgentMailboxes) != 1 || a.activeSubAgentMailboxes[0].MessageID != "b-1" {
		t.Fatalf("active mailbox batch = %#v, want only b-1 remaining", a.activeSubAgentMailboxes)
	}
	if a.activeSubAgentMailbox == nil || a.activeSubAgentMailbox.MessageID != "b-1" {
		t.Fatalf("activeSubAgentMailbox = %#v, want b-1", a.activeSubAgentMailbox)
	}
	if len(a.pendingSubAgentMailboxes) != 1 || a.pendingSubAgentMailboxes[0].MessageID != "b-1" {
		t.Fatalf("pending mailbox batch = %#v, want only b-1 remaining", a.pendingSubAgentMailboxes)
	}
}

func TestSendMessageToWaitingMainWorkerResumesExecution(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-1")
	sub.setState(SubAgentStateWaitingMain, "need approval")
	a.enqueueSubAgentMailbox(SubAgentMailboxMessage{
		MessageID:   "worker-1-1",
		AgentID:     sub.instanceID,
		TaskID:      sub.taskID,
		Kind:        SubAgentMailboxKindDecisionRequired,
		Priority:    SubAgentMailboxPriorityInterrupt,
		Summary:     "need approval",
		RequiresAck: true,
	})

	handle, err := a.NotifySubAgent(context.Background(), "adhoc-1", "continue with option B", "reply")
	if err != nil {
		t.Fatalf("NotifySubAgent: %v", err)
	}
	if handle.Status != "resumed" {
		t.Fatalf("handle.Status = %q, want resumed", handle.Status)
	}
	foundNotify := false
	for len(a.outputCh) > 0 {
		if notify, ok := (<-a.outputCh).(AgentNotifyEvent); ok {
			if notify.AgentID != "main" || notify.TargetAgentID != sub.instanceID || notify.TargetTaskID != sub.taskID || notify.Kind != "reply" || notify.Message != "continue with option B" {
				t.Fatalf("notify event = %#v", notify)
			}
			foundNotify = true
		}
	}
	if !foundNotify {
		t.Fatal("expected targeted AgentNotifyEvent")
	}
	if sub.State() != SubAgentStateRunning {
		t.Fatalf("sub.State() = %q, want running", sub.State())
	}
	if !sub.semHeld {
		t.Fatal("expected resumed worker to hold semaphore slot")
	}
	select {
	case msg := <-sub.inputCh:
		if got := pendingUserMessageText(msg); got != "[reply] continue with option B" {
			t.Fatalf("queued message = %q, want %q", got, "[reply] continue with option B")
		}
	default:
		t.Fatal("expected resumed worker to receive queued follow-up message")
	}
	if got := len(a.subAgentInbox.urgent); got != 0 {
		t.Fatalf("len(urgent) = %d, want 0 after direct reply consumes mailbox", got)
	}
	acks, err := loadSubAgentMailboxAcks(a.sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxAcks: %v", err)
	}
	ack, ok := acks["worker-1-1"]
	if !ok {
		t.Fatal("expected ack for consumed mailbox")
	}
	if ack.ReplyKind != "reply" {
		t.Fatalf("ack.ReplyKind = %q, want reply", ack.ReplyKind)
	}
	if ack.ReplyToMailboxID != "worker-1-1" {
		t.Fatalf("ack.ReplyToMailboxID = %q, want worker-1-1", ack.ReplyToMailboxID)
	}
	if ack.ReplyMessageID == "" {
		t.Fatal("expected reply_message_id to be recorded")
	}
}

func TestNotifySubAgentConsumedMessageCarriesMailboxMetadata(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-notify")
	sub.setState(SubAgentStateWaitingMain, "need approval")

	handle, err := a.NotifySubAgent(context.Background(), sub.taskID, "continue with option B", "follow_up")
	if err != nil {
		t.Fatalf("NotifySubAgent: %v", err)
	}
	if handle.Status != "resumed" {
		t.Fatalf("handle.Status = %q, want resumed", handle.Status)
	}
	var input pendingUserMessage
	select {
	case input = <-sub.inputCh:
	default:
		t.Fatal("expected resumed worker to receive the queued notify message")
	}
	if got := pendingUserMessageText(input); got != "[follow_up] continue with option B" {
		t.Fatalf("queued message = %q, want %q", got, "[follow_up] continue with option B")
	}
	if input.Mailbox == nil {
		t.Fatal("expected the queued notify message to carry mailbox metadata")
	}
	if input.Mailbox.AgentID != "main" || input.Mailbox.TaskID != "" || input.Mailbox.Kind != "follow_up" || input.Mailbox.MessageID != "" {
		t.Fatalf("queued mailbox metadata = %#v, want main/follow_up without a message id", input.Mailbox)
	}
	sub.appendPendingUserMessage(input)
	msgs := sub.GetMessages()
	if len(msgs) == 0 {
		t.Fatal("expected the consumed notify message to be appended to the worker context")
	}
	last := msgs[len(msgs)-1]
	if last.Kind != message.KindSubAgentMailbox {
		t.Fatalf("consumed row kind = %q, want %q", last.Kind, message.KindSubAgentMailbox)
	}
	if last.Content != "[follow_up] continue with option B" {
		t.Fatalf("consumed row content = %q, want the notify text with its kind prefix", last.Content)
	}
	if last.Mailbox == nil || last.Mailbox.AgentID != "main" || last.Mailbox.TaskID != "" || last.Mailbox.Kind != "follow_up" || last.Mailbox.MessageID != "" {
		t.Fatalf("consumed row mailbox = %#v, want main/follow_up without a message id", last.Mailbox)
	}
}

func TestNotifySubAgentDoesNotAckMailboxWhenSlotUnavailable(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-slot")
	sub.setState(SubAgentStateWaitingMain, "need approval")
	a.enqueueSubAgentMailbox(SubAgentMailboxMessage{
		MessageID:   "worker-1-2",
		AgentID:     sub.instanceID,
		TaskID:      sub.taskID,
		Kind:        SubAgentMailboxKindDecisionRequired,
		Priority:    SubAgentMailboxPriorityInterrupt,
		Summary:     "need approval",
		RequiresAck: true,
	})
	for i := 0; i < cap(a.sem); i++ {
		a.sem <- struct{}{}
	}

	if _, err := a.NotifySubAgent(context.Background(), "adhoc-slot", "continue with option C", "reply"); err == nil {
		t.Fatal("expected NotifySubAgent to fail when semaphore is exhausted")
	}
	if got := len(a.subAgentInbox.urgent); got != 1 {
		t.Fatalf("len(urgent) = %d, want 1 after failed resume", got)
	}
	acks, err := loadSubAgentMailboxAcks(a.sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxAcks: %v", err)
	}
	if _, ok := acks["worker-1-2"]; ok {
		t.Fatal("unexpected mailbox ack recorded when resume failed before worker restart")
	}
}

func TestNotifySubAgentQueueRejectionLeavesWaitingWorkerAndMailboxUnchanged(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-queue-reject")
	sub.setState(SubAgentStateWaitingMain, "need approval")
	sub.queueByteLimit = 1
	a.enqueueSubAgentMailbox(SubAgentMailboxMessage{
		MessageID:   "worker-1-queue-reject",
		AgentID:     sub.instanceID,
		TaskID:      sub.taskID,
		Kind:        SubAgentMailboxKindDecisionRequired,
		Priority:    SubAgentMailboxPriorityInterrupt,
		Summary:     "need approval",
		RequiresAck: true,
	})

	if _, err := a.NotifySubAgent(context.Background(), sub.taskID, "continue with option D", "reply"); err == nil {
		t.Fatal("expected NotifySubAgent to reject the oversized resume message")
	}
	if sub.State() != SubAgentStateWaitingMain {
		t.Fatalf("sub.State() = %q, want waiting_main", sub.State())
	}
	if held, _ := sub.slotState(); held {
		t.Fatal("queue rejection left the waiting worker holding a runtime slot")
	}
	if got := len(a.sem); got != 0 {
		t.Fatalf("runtime slots in use = %d, want 0", got)
	}
	if got := len(a.subAgentInbox.urgent); got != 1 || a.subAgentInbox.urgent[0].MessageID != "worker-1-queue-reject" {
		t.Fatalf("urgent inbox = %#v, want the original mailbox message", a.subAgentInbox.urgent)
	}
	acks, err := loadSubAgentMailboxAcks(a.sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxAcks: %v", err)
	}
	if _, ok := acks["worker-1-queue-reject"]; ok {
		t.Fatal("queue rejection persisted a mailbox acknowledgement")
	}
}

func TestOwnedMailboxQueueRejectionRollsBackReactivation(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	owner := newControllableTestSubAgent(t, a, "adhoc-owner-queue-reject")
	owner.setState(SubAgentStateWaitingMain, "waiting")
	owner.queueByteLimit = 1

	if a.routeOwnedSubAgentMailbox(SubAgentMailboxMessage{
		MessageID:    "child-queue-reject",
		AgentID:      "child",
		TaskID:       "adhoc-child",
		OwnerAgentID: owner.instanceID,
		OwnerTaskID:  owner.taskID,
		Kind:         SubAgentMailboxKindDecisionRequired,
		Summary:      "needs a decision",
	}) {
		t.Fatal("oversized owned mailbox unexpectedly delivered")
	}
	if owner.State() != SubAgentStateWaitingMain {
		t.Fatalf("owner state = %q, want waiting_main", owner.State())
	}
	if held, _ := owner.slotState(); held {
		t.Fatal("rejected owned mailbox left the owner holding a runtime slot")
	}
	if got := len(a.sem); got != 0 {
		t.Fatalf("runtime slots in use = %d, want 0", got)
	}
}

func TestManualDeliveryRoutesOwnedMailboxesBeforeManualMessage(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-drain-order")
	a.ownedSubAgentMailboxes = map[string][]SubAgentMailboxMessage{
		sub.instanceID: {
			{
				MessageID:    "child-progress-1",
				AgentID:      "child-1",
				TaskID:       "adhoc-child",
				OwnerAgentID: sub.instanceID,
				OwnerTaskID:  sub.taskID,
				Kind:         SubAgentMailboxKindProgress,
				Priority:     SubAgentMailboxPriorityNotify,
				Summary:      "child halfway done",
				Payload:      "child halfway done",
				CreatedAt:    time.Now(),
			},
			{
				MessageID:    "child-report-1",
				AgentID:      "child-1",
				TaskID:       "adhoc-child",
				OwnerAgentID: sub.instanceID,
				OwnerTaskID:  sub.taskID,
				Kind:         SubAgentMailboxKindCompleted,
				Priority:     SubAgentMailboxPriorityUrgent,
				Summary:      "child finished the refactor",
				Payload:      "child finished the refactor",
				CreatedAt:    time.Now(),
			},
		},
	}

	status, _, err := a.deliverManualMessageToSubAgent(sub, "please integrate the child result", "reply")
	if err != nil {
		t.Fatalf("deliverManualMessageToSubAgent: %v", err)
	}
	if status != "queued" {
		t.Fatalf("status = %q, want queued", status)
	}
	if got := len(a.ownedSubAgentMailboxes[sub.instanceID]); got != 0 {
		t.Fatalf("owned mailboxes left undrained = %d, want 0", got)
	}
	ctxMsg, ok := sub.tryReceiveContextAppend()
	if !ok || !strings.Contains(ctxMsg.Content, "child halfway done") {
		t.Fatalf("context append = (%q, %v), want the queued progress report", ctxMsg.Content, ok)
	}
	first, ok := sub.tryReceiveUserInput()
	if !ok || first.MailboxAckID != "child-report-1" || !strings.Contains(first.Content, "child finished the refactor") {
		t.Fatalf("first queued input = (%q, ack:%q, %v), want the child completion report", first.Content, first.MailboxAckID, ok)
	}
	second, ok := sub.tryReceiveUserInput()
	if !ok || !strings.Contains(second.Content, "please integrate the child result") {
		t.Fatalf("second queued input = (%q, %v), want the manual message", second.Content, ok)
	}
}

func TestReservedInputBlocksParking(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-reserve-park")
	sub.setState(SubAgentStateIdle, "waiting")
	if !sub.canPark() {
		t.Fatal("expected an idle SubAgent with empty queues to be parkable")
	}

	reservation := sub.reserveUserMessage(pendingUserMessage{Content: "queued follow-up"})
	if reservation == nil {
		t.Fatal("reserveUserMessage rejected a message within limits")
	}
	if sub.canPark() {
		t.Fatal("outstanding input reservation must block parking")
	}

	reservation.Cancel()
	if !sub.canPark() {
		t.Fatal("cancelled reservation should make the SubAgent parkable again")
	}
}

func TestReservationCommitFailsAfterSubAgentShutdown(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-reserve-shutdown")

	reservation := sub.reserveUserMessage(pendingUserMessage{Content: "late delivery"})
	if reservation == nil {
		t.Fatal("reserveUserMessage rejected a message within limits")
	}
	sub.cancel()

	if reservation.Commit() {
		t.Fatal("Commit should fail once the SubAgent context is cancelled")
	}
	if sub.hasPendingUserInput() {
		t.Fatal("failed commit must not leave queued input or reservations behind")
	}
	sub.inputQueueMu.Lock()
	reserved := sub.inputQueueReservedMessages
	reservedBytes := sub.inputQueueReservedBytes
	queueBytes := sub.inputQueueBytes
	sub.inputQueueMu.Unlock()
	if reserved != 0 || reservedBytes != 0 || queueBytes != 0 {
		t.Fatalf("queue accounting after failed commit = reserved:%d reservedBytes:%d queueBytes:%d, want zeros", reserved, reservedBytes, queueBytes)
	}
}

func TestResponseMailboxReservationIsIdempotent(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-response-idempotent")
	input := pendingUserMessage{Content: "decision", Mailbox: &message.MailboxMetadata{
		MessageID: "response-corr-1", MessageType: string(AgentMessageTypeResponse),
	}}
	first := sub.reserveUserMessage(input)
	if first == nil || !first.Commit() {
		t.Fatal("first response reservation was not committed")
	}
	second := sub.reserveUserMessage(input)
	if second == nil || !second.duplicate || !second.Commit() {
		t.Fatalf("duplicate reservation = %#v, want successful no-op", second)
	}
	if got := len(sub.inputCh) + len(sub.inputOverflow); got != 1 {
		t.Fatalf("queued response count = %d, want 1", got)
	}
}

func TestRestoreMessagesRebuildsResponseMailboxIdempotency(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-response-restore")
	sub.RestoreMessages([]message.Message{{Role: "user", Content: "decision", Mailbox: &message.MailboxMetadata{
		MessageID: "response-corr-1", MessageType: string(AgentMessageTypeResponse),
	}}})
	if !sub.hasAcceptedMailbox("response-corr-1") {
		t.Fatal("restored response mailbox ID was not indexed")
	}
	if sub.hasAcceptedMailbox("other") {
		t.Fatal("unexpected mailbox ID was indexed")
	}
}

func TestCreateSubAgentFromSubAgentContextSetsOwnerAndDepth(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 2, MaxDepth: 2}

	handle, err := a.CreateSubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID), "child work", "worker", "", "", tools.WriteScope{})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	if handle.Status != "started" {
		t.Fatalf("handle.Status = %q, want started", handle.Status)
	}
	child := a.subAgentByTaskID(handle.TaskID)
	if child == nil {
		t.Fatal("expected child SubAgent to exist")
	}
	if child.ownerAgentID != parent.instanceID {
		t.Fatalf("child.ownerAgentID = %q, want %q", child.ownerAgentID, parent.instanceID)
	}
	if child.ownerTaskID != parent.taskID {
		t.Fatalf("child.ownerTaskID = %q, want %q", child.ownerTaskID, parent.taskID)
	}
	if child.depth != 2 {
		t.Fatalf("child.depth = %d, want 2", child.depth)
	}
	rec := a.taskRecordByTaskID(handle.TaskID)
	if rec == nil {
		t.Fatal("expected task record for child")
	}
	if rec.OwnerAgentID != parent.instanceID || rec.OwnerTaskID != parent.taskID {
		t.Fatalf("task record owner = (%q,%q), want (%q,%q)", rec.OwnerAgentID, rec.OwnerTaskID, parent.instanceID, parent.taskID)
	}
	if !rec.JoinToOwner {
		t.Fatal("expected child task record to default to join-to-owner for SubAgent caller")
	}
}

func TestCreateSubAgentRejectsTargetDeniedForCaller(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	a.activeConfig.Permission = parsePermissionNode(t, `
"*": allow
delegate:
  "*": deny
  reviewer: allow
`)
	a.rebuildRuleset()

	_, err := a.CreateSubAgent(context.Background(), "child work", "worker", "", "", tools.WriteScope{})
	if err == nil || !strings.Contains(err.Error(), "denied by Delegate permission policy") {
		t.Fatalf("CreateSubAgent() err = %v, want Delegate target denial", err)
	}
	if got := len(a.sem); got != 0 {
		t.Fatalf("CreateSubAgent() consumed %d semaphore slots for denied target", got)
	}
}

func TestCreateSubAgentReturnsChildLimitReachedForDirectOwner(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 1, MaxDepth: 2}

	first, err := a.CreateSubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID), "child one", "worker", "", "", tools.WriteScope{})
	if err != nil {
		t.Fatalf("CreateSubAgent(first): %v", err)
	}
	if first.Status != "started" {
		t.Fatalf("first.Status = %q, want started", first.Status)
	}
	second, err := a.CreateSubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID), "child two", "worker", "", "", tools.WriteScope{})
	if err != nil {
		t.Fatalf("CreateSubAgent(second): %v", err)
	}
	if second.Status != "child_limit_reached" {
		t.Fatalf("second.Status = %q, want child_limit_reached", second.Status)
	}
}

func TestCreateSubAgentCountsNonTerminalDirectChildrenForLimit(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 1, MaxDepth: 2}

	first, err := a.CreateSubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID), "child one", "worker", "", "", tools.WriteScope{})
	if err != nil {
		t.Fatalf("CreateSubAgent(first): %v", err)
	}
	child := a.subAgentByTaskID(first.TaskID)
	if child == nil {
		t.Fatal("expected first child SubAgent to exist")
	}
	child.setState(SubAgentStateWaitingMain, "need decision")
	a.noteSubAgentStateTransition(child, SubAgentStateWaitingMain)
	a.syncTaskRecordFromSub(child, "")

	second, err := a.CreateSubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID), "child two", "worker", "", "", tools.WriteScope{})
	if err != nil {
		t.Fatalf("CreateSubAgent(second): %v", err)
	}
	if second.Status != "child_limit_reached" {
		t.Fatalf("second.Status = %q, want child_limit_reached", second.Status)
	}
}

func TestConcurrentCreateSubAgentRespectsDirectChildLimit(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 1, MaxDepth: 2}
	ctx := tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID)

	start := make(chan struct{})
	results := make(chan tools.TaskHandle, 2)
	errs := make(chan error, 2)
	for i := range 2 {
		go func() {
			<-start
			handle, err := a.CreateSubAgent(ctx, fmt.Sprintf("child %d", i), "worker", "", "", tools.WriteScope{})
			results <- handle
			errs <- err
		}()
	}
	close(start)

	statuses := map[string]int{}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("CreateSubAgent: %v", err)
		}
		statuses[(<-results).Status]++
	}
	if statuses["started"] != 1 || statuses["child_limit_reached"] != 1 {
		t.Fatalf("statuses = %#v, want one started and one child_limit_reached", statuses)
	}
}

func TestCreateSubAgentRejectsBeforeLLMFactoryWhenChildLimitReached(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 1, MaxDepth: 2}
	ctx := tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID)

	first, err := a.CreateSubAgent(ctx, "child one", "worker", "", "", tools.WriteScope{})
	if err != nil || first.Status != "started" {
		t.Fatalf("CreateSubAgent(first) = (%#v, %v), want started", first, err)
	}
	var factoryCalls atomic.Int32
	a.llmFactory = func(string, []string, string) *llm.Client {
		factoryCalls.Add(1)
		return newTestLLMClient()
	}

	second, err := a.CreateSubAgent(ctx, "child two", "worker", "", "", tools.WriteScope{})
	if err != nil {
		t.Fatalf("CreateSubAgent(second): %v", err)
	}
	if second.Status != "child_limit_reached" {
		t.Fatalf("second.Status = %q, want child_limit_reached", second.Status)
	}
	if got := factoryCalls.Load(); got != 0 {
		t.Fatalf("LLM factory calls = %d, want 0 on rejected admission", got)
	}
}

func TestConcurrentDuplicateCreateSharesAdmissionResult(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var factoryCalls atomic.Int32
	a.llmFactory = func(string, []string, string) *llm.Client {
		factoryCalls.Add(1)
		entered <- struct{}{}
		<-release
		return newTestLLMClient()
	}

	results := make(chan tools.TaskHandle, 2)
	errs := make(chan error, 2)
	create := func() {
		handle, err := a.CreateSubAgent(context.Background(), "same task", "worker", "plan-1", "semantic-1", tools.WriteScope{})
		results <- handle
		errs <- err
	}
	go create()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first admission did not reach LLM factory")
	}
	go create()
	time.Sleep(20 * time.Millisecond)
	if got := factoryCalls.Load(); got != 1 {
		t.Fatalf("LLM factory calls before release = %d, want 1", got)
	}
	close(release)

	var handles []tools.TaskHandle
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("CreateSubAgent: %v", err)
		}
		handles = append(handles, <-results)
	}
	if handles[0].Status != "started" || handles[1].Status != "started" || handles[0].TaskID != handles[1].TaskID || handles[0].AgentID != handles[1].AgentID {
		t.Fatalf("shared admission handles = %#v, want identical started result", handles)
	}
	if got := factoryCalls.Load(); got != 1 {
		t.Fatalf("LLM factory calls = %d, want 1", got)
	}
}

func TestCreateSubAgentPersistenceFailureDoesNotStartRuntime(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	blockedRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile(blockedRoot): %v", err)
	}
	a.sessionDir = blockedRoot

	handle, err := a.CreateSubAgent(context.Background(), "must persist", "worker", "plan-persist", "semantic-persist", tools.WriteScope{})
	if err == nil || !strings.Contains(err.Error(), "persist initial durable task registration") {
		t.Fatalf("CreateSubAgent() = (%#v, %v), want persistence failure", handle, err)
	}
	if got := len(a.subs.snapshotSubAgents()); got != 0 {
		t.Fatalf("live SubAgents = %d, want 0 after failed registration", got)
	}
	if got := len(a.sem); got != 0 {
		t.Fatalf("semaphore use = %d, want 0 after failed registration", got)
	}
	if got := len(a.subs.admissions); got != 0 {
		t.Fatalf("pending admissions = %d, want 0 after failed registration", got)
	}
	if rec := a.taskRecordByTaskID("adhoc-1"); rec != nil {
		t.Fatalf("task record = %#v, want nil after failed registration", rec)
	}
}

func TestCreateSubAgentCancellationDuringPersistenceDoesNotStartRuntime(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	entered := make(chan struct{})
	release := make(chan struct{})
	a.taskRegistryPersistHook = func() {
		close(entered)
		<-release
	}
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		handle tools.TaskHandle
		err    error
	}
	result := make(chan outcome, 1)
	go func() {
		handle, err := a.CreateSubAgent(ctx, "cancel during persistence", "worker", "plan-cancel", "semantic-cancel", tools.WriteScope{})
		result <- outcome{handle: handle, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("admission did not reach durable persistence")
	}
	cancel()
	close(release)
	got := <-result
	if got.err == nil || !strings.Contains(got.err.Error(), context.Canceled.Error()) {
		t.Fatalf("CreateSubAgent() = (%#v, %v), want cancellation", got.handle, got.err)
	}
	if live := len(a.subs.snapshotSubAgents()); live != 0 {
		t.Fatalf("live SubAgents = %d, want 0 after cancellation", live)
	}
	if got := len(a.sem); got != 0 {
		t.Fatalf("semaphore use = %d, want 0 after cancellation", got)
	}
	if got := len(a.subs.admissions); got != 0 {
		t.Fatalf("pending admissions = %d, want 0 after cancellation", got)
	}
	if rec := a.taskRecordByTaskID("adhoc-1"); rec != nil {
		t.Fatalf("task record = %#v, want nil after cancellation", rec)
	}
}

func TestSubAgentRegistrationMergesWithLatestTaskRegistry(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	sub := newControllableTestSubAgent(t, a, "new-task")
	registration := buildTaskRecordFromSub(sub, nil, "", 0, time.Now())
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"existing": {TaskID: "existing", State: string(SubAgentStateCompleted)},
	})
	if err := a.persistSubAgentRegistration(a.sessionDir, sub, registration); err != nil {
		t.Fatalf("persistSubAgentRegistration: %v", err)
	}
	records, err := loadDurableTaskRecords(a.sessionDir)
	if err != nil {
		t.Fatalf("loadDurableTaskRecords: %v", err)
	}
	if got := records["existing"]; got == nil || got.State != string(SubAgentStateCompleted) {
		t.Fatalf("existing task = %#v, want latest completed state", got)
	}
	if got := records[sub.taskID]; got == nil || got.LatestInstanceID != sub.instanceID {
		t.Fatalf("registered task = %#v, want worker registration", got)
	}
}

func TestRehydratePersistenceFailureDoesNotRegisterRuntime(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	record := &DurableTaskRecord{
		TaskID:           "adhoc-rehydrate-persist",
		AgentDefName:     "worker",
		TaskDesc:         "resume",
		State:            string(SubAgentStateIdle),
		RuntimeParked:    true,
		ResumePolicy:     taskResumePolicyNotify,
		LatestInstanceID: "worker-old",
		// A rehydratable record must carry the write boundary it was admitted
		// under (rehydrateTask refuses scope-less records outright), even
		// though this test only exercises the persistence-failure path.
		ExpectedWriteScope: tools.WriteScope{PathPrefix: []string{"internal/agent"}},
	}
	a.setTaskRecords(map[string]*DurableTaskRecord{record.TaskID: record})
	blockedRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile(blockedRoot): %v", err)
	}
	a.sessionDir = blockedRoot

	_, _, err := a.rehydrateTask(record)
	if err == nil || !strings.Contains(err.Error(), "persist rehydrated durable task registration") {
		t.Fatalf("rehydrateTask() error = %v, want persistence failure", err)
	}
	if got := len(a.subs.snapshotSubAgents()); got != 0 {
		t.Fatalf("live SubAgents = %d, want 0 after failed rehydrate", got)
	}
	if got := len(a.sem); got != 0 {
		t.Fatalf("semaphore use = %d, want 0 after failed rehydrate", got)
	}
}

func TestCreateSubAgentInitializesConcurrentlyBeforeAdmission(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	a.llmFactory = func(string, []string, string) *llm.Client {
		entered <- struct{}{}
		<-release
		return newTestLLMClient()
	}

	results := make(chan tools.TaskHandle, 2)
	errs := make(chan error, 2)
	for i := range 2 {
		go func() {
			handle, err := a.CreateSubAgent(context.Background(), fmt.Sprintf("child %d", i), "worker", "", "", tools.WriteScope{PathPrefix: []string{fmt.Sprintf("module-%d", i)}})
			results <- handle
			errs <- err
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("SubAgent initialization was serialized by admission locking")
		}
	}
	close(release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("CreateSubAgent: %v", err)
		}
		if handle := <-results; handle.Status != "started" {
			t.Fatalf("handle.Status = %q, want started", handle.Status)
		}
	}
}

func TestCreateSubAgentDoesNotHoldAdmissionLockDuringReliableOutput(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	for len(a.outputCh) < cap(a.outputCh) {
		a.outputCh <- InfoEvent{Message: "fill"}
	}

	createDone := make(chan error, 1)
	go func() {
		_, err := a.CreateSubAgent(context.Background(), "work", "worker", "", "", tools.WriteScope{})
		createDone <- err
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(a.subs.snapshotSubAgents()) == 0 {
		time.Sleep(time.Millisecond)
	}
	if got := len(a.subs.snapshotSubAgents()); got != 1 {
		t.Fatalf("live SubAgents = %d, want registered worker", got)
	}

	acquired := make(chan struct{})
	go func() {
		a.admissionMu.Lock()
		close(acquired)
		a.admissionMu.Unlock()
	}()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("admission lock remained held while reliable output was blocked")
	}

	deadline = time.Now().Add(time.Second)
	for {
		select {
		case err := <-createDone:
			if err != nil {
				t.Fatalf("CreateSubAgent: %v", err)
			}
			a.cancelActiveWork()
			return
		case <-a.outputCh:
		case <-time.After(time.Until(deadline)):
			t.Fatal("CreateSubAgent remained blocked while output was drained")
		}
	}
}

func TestCreateSubAgentRejectsRegistrationAfterSessionSwitch(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	entered := make(chan struct{})
	release := make(chan struct{})
	a.llmFactory = func(string, []string, string) *llm.Client {
		close(entered)
		<-release
		return newTestLLMClient()
	}

	result := make(chan error, 1)
	go func() {
		_, err := a.CreateSubAgent(context.Background(), "work", "worker", "", "", tools.WriteScope{})
		result <- err
	}()
	<-entered
	a.prepareSessionSwitch()
	close(release)

	if err := <-result; err == nil || !strings.Contains(err.Error(), "invalidated") {
		t.Fatalf("CreateSubAgent error = %v, want lifecycle invalidation", err)
	}
	if got := len(a.subs.snapshotSubAgents()); got != 0 {
		t.Fatalf("live SubAgents = %d, want 0 after session switch", got)
	}
}

func TestCreateSubAgentRejectsWhileSessionTransitionIsPaused(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	a.prepareSessionSwitch()

	if _, err := a.CreateSubAgent(context.Background(), "work", "worker", "", "", tools.WriteScope{}); err == nil || !strings.Contains(err.Error(), "session transition") {
		t.Fatalf("CreateSubAgent error = %v, want transition rejection", err)
	}
	a.finishSessionSwitch()
	handle, err := a.CreateSubAgent(context.Background(), "work", "worker", "", "", tools.WriteScope{})
	if err != nil {
		t.Fatalf("CreateSubAgent after transition: %v", err)
	}
	if handle.Status != "started" {
		t.Fatalf("handle.Status = %q, want started", handle.Status)
	}
}

func TestRehydrateTaskRejectsWhileSessionTransitionIsPaused(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	record := &DurableTaskRecord{
		TaskID:           "parked-task",
		AgentDefName:     "worker",
		TaskDesc:         "resume work",
		State:            string(SubAgentStateCompleted),
		ResumePolicy:     taskResumePolicyNotify,
		LatestInstanceID: "worker-old",
		RuntimeParked:    true,
	}
	a.setTaskRecords(map[string]*DurableTaskRecord{record.TaskID: record})
	a.prepareSessionSwitch()

	sub, _, _, err := a.getOrRehydrateTask(cloneDurableTaskRecord(record))
	if err == nil || sub != nil {
		t.Fatalf("getOrRehydrateTask during transition = (sub=%v, err=%v), want rejection", sub != nil, err)
	}
	if got := len(a.subs.snapshotSubAgents()); got != 0 {
		t.Fatalf("live SubAgents = %d, want 0", got)
	}
}

func TestCreateSubAgentRejectsRegistrationAfterOwnerCompletes(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 2, MaxDepth: 2}
	entered := make(chan struct{})
	release := make(chan struct{})
	a.llmFactory = func(string, []string, string) *llm.Client {
		close(entered)
		<-release
		return newTestLLMClient()
	}

	ctx := tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID)
	result := make(chan error, 1)
	go func() {
		_, err := a.CreateSubAgent(ctx, "child work", "worker", "", "", tools.WriteScope{})
		result <- err
	}()
	<-entered
	parent.setState(SubAgentStateCompleted, "done")
	close(release)

	if err := <-result; err == nil || !strings.Contains(err.Error(), "owner task is no longer active") {
		t.Fatalf("CreateSubAgent error = %v, want inactive owner rejection", err)
	}
	if child := a.subAgentByTaskID("adhoc-1"); child != nil {
		t.Fatalf("unexpected child registered after owner completion: %s", child.instanceID)
	}
	if got := len(a.subs.admissions); got != 0 {
		t.Fatalf("pending admissions = %d, want 0 after owner completion", got)
	}
	if got := len(a.sem); got != 0 {
		t.Fatalf("semaphore use = %d, want 0 after owner completion", got)
	}
}

func TestNestedCreateSubAgentRejectsBroaderWriteScope(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 2, MaxDepth: 2}
	parent.writeScope = tools.WriteScope{PathPrefix: []string{"internal/agent"}}
	ctx := tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID)

	_, err := a.CreateSubAgent(ctx, "child work", "worker", "", "", tools.WriteScope{PathPrefix: []string{"internal"}})
	if err == nil || !strings.Contains(err.Error(), "must not be broader") {
		t.Fatalf("CreateSubAgent error = %v, want scope inheritance rejection", err)
	}
	if got := len(a.subs.snapshotSubAgents()); got != 1 {
		t.Fatalf("live SubAgents = %d, want only parent", got)
	}
}

func TestNestedCreateSubAgentAllowsNarrowerWriteScope(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 2, MaxDepth: 2}
	parent.writeScope = tools.WriteScope{PathPrefix: []string{"internal"}}
	ctx := tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID)

	handle, err := a.CreateSubAgent(ctx, "child work", "worker", "", "", tools.WriteScope{Files: []string{"internal/agent/main.go"}})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	if handle.Status != "started" {
		t.Fatalf("handle.Status = %q, want started", handle.Status)
	}
}

func TestNestedCreateSubAgentDoesNotExpandExactFileIntoPrefix(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 2, MaxDepth: 2}
	parent.writeScope = tools.WriteScope{Files: []string{"internal/agent/main.go"}}
	ctx := tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID)

	_, err := a.CreateSubAgent(ctx, "child work", "worker", "", "", tools.WriteScope{PathPrefix: []string{"internal/agent/main.go"}})
	if err == nil || !strings.Contains(err.Error(), "must not be broader") {
		t.Fatalf("CreateSubAgent error = %v, want exact-file expansion rejection", err)
	}
}

func TestCreateSubAgentCapsActiveChildrenAtTen(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.depth = 1
	// MaxChildren is honoured as configured rather than silently clamped to the
	// default. Set it to 10 so the cap fires at ten, and give each child a
	// distinct description so the semantic-key duplicate guard does not flag
	// the fan-out as a re-delegation of the same deliverable.
	parent.delegation = config.DelegationConfig{MaxChildren: 10, MaxDepth: 2}

	ctx := tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID)
	for i := range 10 {
		handle, err := a.CreateSubAgent(ctx, fmt.Sprintf("child work %d", i), "worker", "", "", tools.WriteScope{PathPrefix: []string{fmt.Sprintf("module-%d", i)}})
		if err != nil {
			t.Fatalf("CreateSubAgent(%d): %v", i, err)
		}
		if handle.Status != "started" {
			t.Fatalf("handle.Status[%d] = %q, want started", i, handle.Status)
		}
	}

	overflow, err := a.CreateSubAgent(ctx, "child overflow", "worker", "", "", tools.WriteScope{PathPrefix: []string{"overflow"}})
	if err != nil {
		t.Fatalf("CreateSubAgent(overflow): %v", err)
	}
	if overflow.Status != "child_limit_reached" {
		t.Fatalf("overflow.Status = %q, want child_limit_reached", overflow.Status)
	}
}

func TestDirectOwnerOnlyControlAppliesToLiveChildAndCompletedRehydrate(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 2, MaxDepth: 2}

	handle, err := a.CreateSubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID), "child work", "worker", "", "", tools.WriteScope{PathPrefix: []string{"internal/agent/main.go"}})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	child := a.subAgentByTaskID(handle.TaskID)
	if child == nil {
		t.Fatal("expected child SubAgent to exist")
	}

	liveHandle, err := a.NotifySubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID), child.taskID, "continue", "follow_up")
	if err != nil {
		t.Fatalf("NotifySubAgent(live direct owner): %v", err)
	}
	if liveHandle.TaskID != child.taskID {
		t.Fatalf("liveHandle.TaskID = %q, want %q", liveHandle.TaskID, child.taskID)
	}
	if _, err := a.NotifySubAgent(context.Background(), child.taskID, "ancestor override", "follow_up"); err == nil {
		t.Fatal("expected ancestor/main caller to be rejected for descendant control")
	}

	child.setState(SubAgentStateCompleted, "done")
	a.noteSubAgentStateTransition(child, SubAgentStateCompleted)
	a.persistSubAgentMeta(child)
	a.syncTaskRecordFromSub(child, "task completed")
	a.closeSubAgent(child.instanceID)

	rehydrated, err := a.NotifySubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID), handle.TaskID, "follow up", "follow_up")
	if err != nil {
		t.Fatalf("NotifySubAgent(completed direct owner): %v", err)
	}
	if !rehydrated.Rehydrated {
		t.Fatalf("rehydrated.Rehydrated = false, want true")
	}
	if _, err := a.NotifySubAgent(context.Background(), handle.TaskID, "ancestor override", "follow_up"); err == nil {
		t.Fatal("expected ancestor/main caller to be rejected for completed-task rehydrate control")
	}
}

func TestDirectOwnerOnlyStopRejectsAncestorCaller(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 2, MaxDepth: 2}

	handle, err := a.CreateSubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID), "child work", "worker", "", "", tools.WriteScope{})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	if _, err := a.CancelSubAgent(context.Background(), handle.TaskID, "ancestor override"); err == nil {
		t.Fatal("expected ancestor/main caller to be rejected for descendant stop")
	}
	if _, err := a.CancelSubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID), handle.TaskID, "stop child"); err != nil {
		t.Fatalf("CancelSubAgent(direct owner): %v", err)
	}
}

func TestCancelSubAgentCancelsDirectDescendantsRecursively(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 3)

	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.instanceID = "worker-parent"
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 2, MaxDepth: 3}
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[parent.instanceID] = parent
	a.subs.mu.Unlock()
	a.syncTaskRecordFromSub(parent, "")

	child := newControllableTestSubAgent(t, a, "adhoc-child")
	child.instanceID = "worker-child"
	child.ownerAgentID = parent.instanceID
	child.ownerTaskID = parent.taskID
	child.depth = 2
	child.joinToOwner = true
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[child.instanceID] = child
	a.subs.mu.Unlock()
	a.syncTaskRecordFromSub(child, "")

	grand := newControllableTestSubAgent(t, a, "adhoc-grand")
	grand.instanceID = "worker-grand"
	grand.ownerAgentID = child.instanceID
	grand.ownerTaskID = child.taskID
	grand.depth = 3
	grand.joinToOwner = true
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[grand.instanceID] = grand
	a.subs.mu.Unlock()
	a.syncTaskRecordFromSub(grand, "")

	if _, err := a.CancelSubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID), child.taskID, "stop subtree"); err != nil {
		t.Fatalf("CancelSubAgent: %v", err)
	}
	if child.State() != SubAgentStateCancelled {
		t.Fatalf("child.State() = %q, want %q", child.State(), SubAgentStateCancelled)
	}
	record := a.taskRecordByTaskID(grand.taskID)
	if record == nil || record.State != string(SubAgentStateCancelled) {
		t.Fatalf("grandchild record state = %#v, want cancelled", record)
	}
}

func TestOwnedCompletedMailboxReactivatesWaitingDescendantParent(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.instanceID = "worker-parent"
	parent.setState(SubAgentStateWaitingDescendant, "waiting for child")
	parent.setPendingCompleteIntent(&AgentResult{Summary: "final summary"})
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[parent.instanceID] = parent
	a.subs.mu.Unlock()
	a.syncTaskRecordFromSub(parent, "")

	child := newControllableTestSubAgent(t, a, "adhoc-child")
	child.instanceID = "worker-child"
	child.ownerAgentID = parent.instanceID
	child.ownerTaskID = parent.taskID
	child.depth = 2
	child.joinToOwner = true
	child.setState(SubAgentStateCompleted, "child done")
	child.semHeld = true
	a.sem <- struct{}{}
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[child.instanceID] = child
	a.subs.mu.Unlock()
	a.syncTaskRecordFromSub(child, "")

	ok := a.routeOwnedSubAgentMailbox(SubAgentMailboxMessage{
		AgentID:      child.instanceID,
		TaskID:       child.taskID,
		OwnerAgentID: parent.instanceID,
		OwnerTaskID:  parent.taskID,
		Kind:         SubAgentMailboxKindCompleted,
		Summary:      "child done",
		Payload:      "child done",
	})
	if !ok {
		t.Fatal("routeOwnedSubAgentMailbox() = false, want true")
	}
	if parent.State() != SubAgentStateRunning {
		t.Fatalf("parent.State() = %q, want running", parent.State())
	}
	if !parent.semHeld {
		t.Fatal("expected parent to hold transferred slot after child completion")
	}
	if child.semHeld {
		t.Fatal("expected child slot to be transferred away on final joined completion")
	}
	pending := parent.PendingCompleteIntent()
	if pending != nil {
		t.Fatal("expected pending complete intent to be cleared after reactivation")
	}
	select {
	case msg := <-parent.inputCh:
		text := pendingUserMessageText(msg)
		if !strings.Contains(text, "Parent pending completion intent:") || !strings.Contains(text, "child done") {
			t.Fatalf("parent queued message = %q, want pending summary and child completion details", text)
		}
	default:
		t.Fatal("expected parent to receive a resumed child-completion message")
	}
}

func TestOwnedCompletedMailboxQueueRejectionReturnsTransferredSlot(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	parent := newControllableTestSubAgent(t, a, "adhoc-parent-transfer-reject")
	parent.instanceID = "worker-parent-transfer-reject"
	parent.setState(SubAgentStateWaitingDescendant, "waiting for child")
	parent.queueByteLimit = 1
	child := newControllableTestSubAgent(t, a, "adhoc-child-transfer-reject")
	child.instanceID = "worker-child-transfer-reject"
	child.ownerAgentID = parent.instanceID
	child.ownerTaskID = parent.taskID
	child.joinToOwner = true
	child.setState(SubAgentStateCompleted, "done")
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[parent.instanceID] = parent
	a.subs.subAgents[child.instanceID] = child
	a.subs.taskRecords[child.taskID] = buildTaskRecordFromSub(child, nil, "task completed", 0, time.Now())
	a.subs.mu.Unlock()
	if err := a.acquireSubAgentSlot(child); err != nil {
		t.Fatalf("acquire child slot: %v", err)
	}

	if a.routeOwnedSubAgentMailbox(SubAgentMailboxMessage{
		MessageID:    "child-complete-transfer-reject",
		AgentID:      child.instanceID,
		TaskID:       child.taskID,
		OwnerAgentID: parent.instanceID,
		OwnerTaskID:  parent.taskID,
		Kind:         SubAgentMailboxKindCompleted,
		Summary:      "child completed with a payload too large for the parent queue",
	}) {
		t.Fatal("oversized child completion unexpectedly delivered")
	}
	if held, _ := parent.slotState(); held {
		t.Fatal("parent retained the transferred slot after queue rejection")
	}
	if held, _ := child.slotState(); !held {
		t.Fatal("child did not recover the slot after parent queue rejection")
	}
}

func TestWaitingDescendantDirectOwnerResumeReacquiresSemaphore(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 2, MaxDepth: 2}
	parent.setState(SubAgentStateWaitingDescendant, "waiting for child")
	a.releaseSubAgentSlot(parent)

	handle, err := a.NotifySubAgent(context.Background(), parent.taskID, "resume after child event", "follow_up")
	if err != nil {
		t.Fatalf("NotifySubAgent: %v", err)
	}
	if handle.Status != "resumed" {
		t.Fatalf("handle.Status = %q, want resumed", handle.Status)
	}
	if !parent.semHeld {
		t.Fatal("expected waiting_descendant owner to reacquire semaphore slot on manual resume")
	}
}

func TestOwnerRoutedMailboxIsAckedConsumed(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.instanceID = "worker-parent"
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[parent.instanceID] = parent
	a.subs.mu.Unlock()
	a.syncTaskRecordFromSub(parent, "")

	childMsg := SubAgentMailboxMessage{
		MessageID:    "worker-child-1",
		AgentID:      "worker-child",
		TaskID:       "adhoc-child",
		OwnerAgentID: parent.instanceID,
		OwnerTaskID:  parent.taskID,
		Kind:         SubAgentMailboxKindProgress,
		Priority:     SubAgentMailboxPriorityNotify,
		Summary:      "child progress",
	}
	a.enqueueSubAgentMailbox(childMsg)

	select {
	case msg := <-parent.ctxAppendCh:
		parent.appendContextOnly(msg)
	default:
		t.Fatal("expected owner-routed mailbox to enqueue a context append for direct parent")
	}

	msgs, err := loadSubAgentMailboxMessages(a.sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxMessages: %v", err)
	}
	if len(msgs) == 0 || msgs[len(msgs)-1].MessageID != "worker-child-1" {
		t.Fatalf("mailbox messages = %#v, want worker-child-1 persisted", msgs)
	}
	acks, err := loadSubAgentMailboxAcks(a.sessionDir)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err != nil {
			t.Fatalf("loadSubAgentMailboxAcks: %v", err)
		}
		if ack, ok := acks["worker-child-1"]; ok && ack.Outcome == "consumed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ack = %#v, want consumed ack for owner-routed mailbox", acks["worker-child-1"])
		}
		time.Sleep(10 * time.Millisecond)
		acks, err = loadSubAgentMailboxAcks(a.sessionDir)
	}
}

func TestBusyOwnerMailboxQueuesForNextRequest(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	owner := newControllableTestSubAgent(t, a, "adhoc-parent-busy")
	owner.instanceID = "worker-parent-busy"
	owner.setState(SubAgentStateRunning, "working")
	owner.llmRequestInFlight.Store(true)
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[owner.instanceID] = owner
	a.subs.mu.Unlock()

	if !a.routeOwnedSubAgentMailbox(SubAgentMailboxMessage{
		MessageID:    "worker-child-complete",
		AgentID:      "worker-child",
		TaskID:       "adhoc-child",
		OwnerAgentID: owner.instanceID,
		OwnerTaskID:  owner.taskID,
		Kind:         SubAgentMailboxKindCompleted,
		Priority:     SubAgentMailboxPriorityUrgent,
		Summary:      "child completed",
	}) {
		t.Fatal("routeOwnedSubAgentMailbox() = false")
	}
	select {
	case pending := <-owner.inputCh:
		if text := pendingUserMessageText(pending); !strings.Contains(text, "child completed") {
			t.Fatalf("queued owner message = %q, want child completion", text)
		}
	default:
		t.Fatal("expected busy owner mailbox to queue for the next request")
	}
	select {
	case msg := <-owner.ctxAppendCh:
		t.Fatalf("unexpected context-only mailbox while owner busy: %#v", msg)
	default:
	}
}

func TestOwnerRoutedWakeBypassesSemaphoreWhenOwnerMustResume(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.instanceID = "worker-parent"
	parent.setState(SubAgentStateWaitingDescendant, "waiting for child")
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[parent.instanceID] = parent
	a.subs.mu.Unlock()
	a.syncTaskRecordFromSub(parent, "")
	for i := 0; i < cap(a.sem); i++ {
		a.sem <- struct{}{}
	}

	msg := SubAgentMailboxMessage{
		MessageID:    "worker-child-2",
		AgentID:      "worker-child",
		TaskID:       "adhoc-child",
		OwnerAgentID: parent.instanceID,
		OwnerTaskID:  parent.taskID,
		Kind:         SubAgentMailboxKindDecisionRequired,
		Priority:     SubAgentMailboxPriorityInterrupt,
		Summary:      "need owner decision",
	}
	a.enqueueSubAgentMailbox(msg)

	if got := len(a.subAgentInbox.urgent) + len(a.subAgentInbox.normal) + len(a.subAgentInbox.progress); got != 0 {
		t.Fatalf("main inbox unexpectedly received owner-routed mailbox, total=%d", got)
	}
	if parent.State() != SubAgentStateRunning {
		t.Fatalf("parent.State() = %q, want %q", parent.State(), SubAgentStateRunning)
	}
	if !parent.semHeld || !parent.semBorrowed || parent.semBypassed {
		t.Fatalf("parent slot flags = held:%v borrowed:%v bypassed:%v, want borrowed grant", parent.semHeld, parent.semBorrowed, parent.semBypassed)
	}
	if queued := a.ownedSubAgentMailboxes[parent.instanceID]; len(queued) != 0 {
		t.Fatalf("ownedSubAgentMailboxes = %#v, want empty after wake bypass", queued)
	}
	select {
	case pending := <-parent.inputCh:
		if text := pendingUserMessageText(pending); !strings.Contains(text, "need owner decision") {
			t.Fatalf("parent queued message = %q, want owner decision text", text)
		}
	default:
		t.Fatal("expected owner-routed wake to resume parent immediately")
	}
}

func TestCloseSubAgentReleasesWakeBypassWithoutDrainingSemaphore(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.semHeld = true
	parent.semBypassed = true
	parent.instanceID = "worker-parent"
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[parent.instanceID] = parent
	a.subs.mu.Unlock()
	for i := 0; i < cap(a.sem); i++ {
		a.sem <- struct{}{}
	}

	a.closeSubAgent(parent.instanceID)

	if got := len(a.sem); got != cap(a.sem) {
		t.Fatalf("len(a.sem) = %d, want %d", got, cap(a.sem))
	}
}

func TestCloseSubAgentRemovesOwnedPendingMailboxState(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.instanceID = "worker-parent"
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[parent.instanceID] = parent
	a.subs.mu.Unlock()
	a.syncTaskRecordFromSub(parent, "")

	a.enqueueOwnedSubAgentMailbox(SubAgentMailboxMessage{
		MessageID:    "worker-child-queued",
		AgentID:      "worker-child",
		TaskID:       "adhoc-child",
		OwnerAgentID: parent.instanceID,
		OwnerTaskID:  parent.taskID,
		Kind:         SubAgentMailboxKindDecisionRequired,
		Priority:     SubAgentMailboxPriorityInterrupt,
		Summary:      "need decision",
	})
	if got := len(a.ownedSubAgentMailboxes[parent.instanceID]); got != 1 {
		t.Fatalf("len(ownedSubAgentMailboxes[parent]) = %d, want 1 before close", got)
	}

	a.closeSubAgent(parent.instanceID)

	if got := len(a.ownedSubAgentMailboxes[parent.instanceID]); got != 0 {
		t.Fatalf("len(ownedSubAgentMailboxes[parent]) = %d, want 0 after close", got)
	}
}

func TestOwnedMailboxNotifyDoesNotInflateUrgentCount(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.instanceID = "worker-parent"
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[parent.instanceID] = parent
	a.subs.mu.Unlock()
	a.syncTaskRecordFromSub(parent, "")

	a.enqueueOwnedSubAgentMailbox(SubAgentMailboxMessage{
		MessageID:    "worker-child-progress",
		AgentID:      "worker-child",
		TaskID:       "adhoc-child",
		OwnerAgentID: parent.instanceID,
		OwnerTaskID:  parent.taskID,
		Kind:         SubAgentMailboxKindProgress,
		Priority:     SubAgentMailboxPriorityNotify,
		Summary:      "child progress",
	})
	a.refreshSubAgentInboxSummary()
	if got := a.subAgentUrgentInboxCountLocked(parent.instanceID); got != 0 {
		t.Fatalf("subAgentUrgentInboxCountLocked(parent) = %d, want 0 for notify/progress owned mailbox", got)
	}
}

func TestOwnerReactivationSavesFreshSnapshot(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.instanceID = "worker-parent"
	parent.setState(SubAgentStateWaitingDescendant, "waiting for child")
	parent.setPendingCompleteIntent(&AgentResult{Summary: "final summary"})
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[parent.instanceID] = parent
	a.subs.mu.Unlock()
	a.syncTaskRecordFromSub(parent, "")

	child := newControllableTestSubAgent(t, a, "adhoc-child")
	child.instanceID = "worker-child"
	child.ownerAgentID = parent.instanceID
	child.ownerTaskID = parent.taskID
	child.depth = 2
	child.joinToOwner = true
	child.setState(SubAgentStateCompleted, "child done")
	child.semHeld = true
	a.sem <- struct{}{}
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[child.instanceID] = child
	a.subs.mu.Unlock()
	a.syncTaskRecordFromSub(child, "")

	ok := a.routeOwnedSubAgentMailbox(SubAgentMailboxMessage{
		MessageID:    "worker-child-3",
		AgentID:      child.instanceID,
		TaskID:       child.taskID,
		OwnerAgentID: parent.instanceID,
		OwnerTaskID:  parent.taskID,
		Kind:         SubAgentMailboxKindCompleted,
		Summary:      "child done",
		Payload:      "child done",
	})
	if !ok {
		t.Fatal("routeOwnedSubAgentMailbox() = false, want true")
	}

	snap, err := a.recoveryManager().Recover()
	if err != nil {
		t.Fatalf("Recover(): %v", err)
	}
	if snap == nil {
		t.Fatal("expected recovery snapshot")
	}
	found := false
	for _, as := range snap.ActiveAgents {
		if as.InstanceID != parent.instanceID {
			continue
		}
		found = true
		if as.State != string(SubAgentStateRunning) {
			t.Fatalf("snapshot state = %q, want %q", as.State, SubAgentStateRunning)
		}
		break
	}
	if !found {
		t.Fatalf("expected parent agent in snapshot: %#v", snap.ActiveAgents)
	}
}

func TestCallerTaskIDAllowsRehydratedParentToControlChild(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)

	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.instanceID = "worker-parent-1"
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 2, MaxDepth: 2}
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[parent.instanceID] = parent
	a.subs.mu.Unlock()
	a.syncTaskRecordFromSub(parent, "")

	child := newControllableTestSubAgent(t, a, "adhoc-child")
	child.instanceID = "worker-child-1"
	child.ownerAgentID = parent.instanceID
	child.ownerTaskID = parent.taskID
	child.depth = 2
	child.joinToOwner = true
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[child.instanceID] = child
	a.subs.mu.Unlock()
	a.syncTaskRecordFromSub(child, "")

	parent2 := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent2.instanceID = "worker-parent-2"
	parent2.depth = 1
	parent2.delegation = parent.delegation
	a.subs.mu.Lock()
	delete(a.subs.subAgents, parent.instanceID)
	a.subs.subAgents[parent2.instanceID] = parent2
	a.subs.mu.Unlock()
	a.syncTaskRecordFromSub(parent2, "")

	handle, err := a.NotifySubAgent(tools.WithTaskID(tools.WithAgentID(context.Background(), parent2.instanceID), parent2.taskID), child.taskID, "continue from rehydrated parent", "follow_up")
	if err != nil {
		t.Fatalf("NotifySubAgent: %v", err)
	}
	if handle.TaskID != child.taskID {
		t.Fatalf("handle.TaskID = %q, want %q", handle.TaskID, child.taskID)
	}
}

func TestNotifySubAgentUsesEventLoopWhenStarted(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	startMainAgentLoopForTest(t, a)
	sub := newControllableTestSubAgent(t, a, "adhoc-loop")
	sub.setState(SubAgentStateWaitingMain, "need approval")
	a.enqueueSubAgentMailbox(SubAgentMailboxMessage{
		MessageID:   "worker-1-3",
		AgentID:     sub.instanceID,
		TaskID:      sub.taskID,
		Kind:        SubAgentMailboxKindDecisionRequired,
		Priority:    SubAgentMailboxPriorityInterrupt,
		Summary:     "need approval",
		RequiresAck: true,
	})

	handle, err := a.NotifySubAgent(context.Background(), "adhoc-loop", "continue with option D", "reply")
	if err != nil {
		t.Fatalf("NotifySubAgent: %v", err)
	}
	if handle.Status != "resumed" {
		t.Fatalf("handle.Status = %q, want resumed", handle.Status)
	}
	acks, err := loadSubAgentMailboxAcks(a.sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxAcks: %v", err)
	}
	if _, ok := acks["worker-1-3"]; !ok {
		t.Fatal("expected mailbox ack recorded through event-loop path")
	}
}

func TestSendMessageToCompletedWorkerCreatesFollowupWithoutNewTaskID(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-3")
	sub.setState(SubAgentStateCompleted, "finished initial pass")
	// Keep the durable record in step with the runtime: a settled task that
	// still has a live worker attached is admitted for a follow-up attempt
	// from the record's terminal state, not from the stale running snapshot
	// the helper's initial sync left behind.
	a.syncTaskRecordFromSub(sub, "task completed")
	sub.setLastMailboxID("worker-1-9")

	handle, err := a.NotifySubAgent(context.Background(), "adhoc-3", "follow up on edge cases", "follow_up")
	if err != nil {
		t.Fatalf("NotifySubAgent: %v", err)
	}
	if handle.AgentID != sub.instanceID {
		t.Fatalf("handle.AgentID = %q, want %q", handle.AgentID, sub.instanceID)
	}
	if handle.TaskID != sub.taskID {
		t.Fatalf("handle.TaskID = %q, want %q", handle.TaskID, sub.taskID)
	}
	if sub.State() != SubAgentStateRunning {
		t.Fatalf("sub.State() = %q, want running", sub.State())
	}
	if !sub.semHeld {
		t.Fatal("expected completed worker follow-up to reacquire semaphore")
	}
}

func TestFocusedCompletedWorkerDirectInputResumesSameWorker(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-7")
	sub.setState(SubAgentStateCompleted, "finished initial pass")
	sub.ownerAgentID = "main-owner"
	sub.ownerTaskID = "main-task"
	a.syncTaskRecordFromSub(sub, "")
	a.SwitchFocus(sub.instanceID)

	a.SendUserMessage("follow up on edge cases")

	if sub.State() != SubAgentStateRunning {
		t.Fatalf("sub.State() = %q, want running", sub.State())
	}
	if !sub.semHeld {
		t.Fatal("expected focused completed worker direct input to reacquire semaphore")
	}
	select {
	case msg := <-sub.inputCh:
		if got := pendingUserMessageText(msg); got != "[follow_up] follow up on edge cases" {
			t.Fatalf("queued message = %q, want %q", got, "[follow_up] follow up on edge cases")
		}
	default:
		t.Fatal("expected focused completed worker to receive direct follow-up input")
	}
}

func TestCancelSubAgentCancelsPendingToolAndReleasesSlot(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-2")
	sub.ctxMgr.Append(message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{{
			ID:   "call-1",
			Name: "read",
			Args: []byte(`{"path":"README.md"}`),
		}},
	})
	sub.turn = &Turn{
		ID:              1,
		Ctx:             context.Background(),
		Cancel:          func() {},
		PendingToolMeta: map[string]PendingToolCall{"call-1": {CallID: "call-1", Name: "read", ArgsJSON: `{"path":"README.md"}`, AgentID: sub.instanceID}},
	}
	sub.turn.PendingToolCalls.Store(1)
	sub.semHeld = true
	a.sem <- struct{}{}
	a.focusedAgent.Store(sub)

	handle, err := a.CancelSubAgent(context.Background(), "adhoc-2", "task superseded")
	if err != nil {
		t.Fatalf("CancelSubAgent: %v", err)
	}
	if handle.Status != "cancelled" {
		t.Fatalf("handle.Status = %q, want cancelled", handle.Status)
	}
	if sub.State() != SubAgentStateCancelled {
		t.Fatalf("sub.State() = %q, want cancelled", sub.State())
	}
	if sub.semHeld {
		t.Fatal("expected stopped worker to release semaphore slot")
	}
	if got := len(a.sem); got != 0 {
		t.Fatalf("len(a.sem) = %d, want 0", got)
	}
	if focused := a.focusedAgent.Load(); focused != nil {
		t.Fatalf("focusedAgent = %v, want nil", focused.instanceID)
	}
	msgs := sub.ctxMgr.Snapshot()
	last := msgs[len(msgs)-1]
	if last.Role != "tool" || last.ToolCallID != "call-1" || last.Content != "Cancelled" {
		t.Fatalf("last message = %#v, want cancelled tool result for call-1", last)
	}
}

func TestInvalidTaskIDReturnsErrorWithoutImplicitSpawn(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	if _, err := a.NotifySubAgent(context.Background(), "missing-task", "hello", "reply"); err == nil {
		t.Fatal("expected NotifySubAgent to fail for missing task_id")
	}
	if _, err := a.CancelSubAgent(context.Background(), "missing-task", "stop"); err == nil {
		t.Fatal("expected CancelSubAgent to fail for missing task_id")
	}
}

func TestSessionSwitchInvalidatesOldTaskID(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	_ = newControllableTestSubAgent(t, a, "adhoc-4")
	if abandoned := a.abandonSubAgentsForSessionSwitch(); abandoned != 1 {
		t.Fatalf("abandonSubAgentsForSessionSwitch() = %d, want 1", abandoned)
	}
	if _, err := a.NotifySubAgent(context.Background(), "adhoc-4", "continue", "reply"); err == nil {
		t.Fatal("expected old task_id to be invalid after session switch abandonment")
	}
}

func TestCompletedWorkerParksAndPersistsTaskRecord(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-5")
	a.handleAgentDone(Event{
		SourceID: sub.instanceID,
		Payload:  &AgentResult{Summary: "done"},
	})

	if got := a.subAgentByID(sub.instanceID); got != nil {
		t.Fatal("expected completed worker runtime to be parked")
	}
	record := a.taskRecordByTaskID(sub.taskID)
	if record == nil {
		t.Fatal("expected durable task record for completed worker")
	}
	if record.State != string(SubAgentStateCompleted) {
		t.Fatalf("record.State = %q, want %q", record.State, SubAgentStateCompleted)
	}
	if !record.RuntimeParked {
		t.Fatal("record.RuntimeParked = false, want true")
	}
}

func TestCompletedChildReleasesSlotWhileParentWaitsForManualContinue(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.instanceID = "worker-parent"
	parent.setState(SubAgentStateWaitingDescendant, "waiting for child")
	parent.semHeld = false

	child := newControllableTestSubAgent(t, a, "adhoc-child")
	child.instanceID = "worker-child"
	child.ownerAgentID = parent.instanceID
	child.ownerTaskID = parent.taskID
	child.joinToOwner = true
	child.semHeld = true
	a.sem <- struct{}{}

	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[parent.instanceID] = parent
	a.subs.subAgents[child.instanceID] = child
	a.subs.mu.Unlock()

	a.handleAgentDone(Event{SourceID: child.instanceID, Payload: &AgentResult{Summary: "done"}})

	if child.semHeld {
		t.Fatal("completed child still holds a semaphore slot")
	}
	if parent.semHeld {
		t.Fatal("waiting parent acquired a slot before manual continue")
	}
	if got := len(a.sem); got != 0 {
		t.Fatalf("len(a.sem) = %d, want 0 after child completion", got)
	}
}

func TestSendMessageToCompletedTaskRehydratesParkedWorker(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"restorer": {
			Name:   "restorer",
			Mode:   "subagent",
			Models: map[string][]string{"default": {"test/test-model"}},
		},
	})
	a.SetLLMFactory(func(systemPrompt string, agentModels []string, variant string) *llm.Client {
		return newTestLLMClient()
	})
	sub := newControllableTestSubAgent(t, a, "adhoc-13")
	sub.agentDefName = "restorer"
	sub.taskDesc = "Investigate issue"
	sub.planTaskRef = "plan-item-13"
	sub.semanticTaskKey = "investigate-issue"
	sub.writeScope = tools.WriteScope{Files: []string{"internal/agent/main_subagent_control.go"}}
	sub.ctxMgr.Append(message.Message{Role: "user", Content: "Investigate issue"})
	if err := a.recoveryManager().PersistMessage(sub.instanceID, message.Message{Role: "user", Content: "Investigate issue"}); err != nil {
		t.Fatalf("PersistMessage(sub): %v", err)
	}
	oldInstanceID := sub.instanceID
	a.handleAgentDone(Event{
		SourceID: sub.instanceID,
		Payload:  &AgentResult{Summary: "done"},
	})

	handle, err := a.NotifySubAgent(context.Background(), "adhoc-13", "follow up on edge cases", "follow_up")
	if err != nil {
		t.Fatalf("NotifySubAgent: %v", err)
	}
	if !handle.Rehydrated {
		t.Fatal("completed parked worker should rehydrate")
	}
	if handle.PreviousAgentID != oldInstanceID {
		t.Fatalf("handle.PreviousAgentID = %q, want %q", handle.PreviousAgentID, oldInstanceID)
	}
	if handle.AgentID == oldInstanceID {
		t.Fatalf("handle.AgentID = %q, want a new runtime instance", handle.AgentID)
	}
	restored := a.subAgentByTaskID("adhoc-13")
	if restored == nil {
		t.Fatal("expected rehydrated live worker")
	}
	if restored.instanceID != handle.AgentID {
		t.Fatalf("restored.instanceID = %q, want %q", restored.instanceID, handle.AgentID)
	}
	if restored.State() != SubAgentStateRunning {
		t.Fatalf("restored.State() = %q, want running", restored.State())
	}
	record := a.taskRecordByTaskID("adhoc-13")
	if record == nil {
		t.Fatal("expected durable task record")
	}
	if record.LatestInstanceID != handle.AgentID {
		t.Fatalf("record.LatestInstanceID = %q, want %q", record.LatestInstanceID, handle.AgentID)
	}
	if len(record.InstanceHistory) != 2 {
		t.Fatalf("len(record.InstanceHistory) = %d, want 2", len(record.InstanceHistory))
	}
	if record.PlanTaskRef != sub.planTaskRef || record.SemanticTaskKey != sub.semanticTaskKey {
		t.Fatalf("rehydrated task identity = (%q, %q), want (%q, %q)", record.PlanTaskRef, record.SemanticTaskKey, sub.planTaskRef, sub.semanticTaskKey)
	}
	if len(record.ExpectedWriteScope.Files) != 1 || record.ExpectedWriteScope.Files[0] != "internal/agent/main_subagent_control.go" {
		t.Fatalf("rehydrated write scope = %#v, want original file scope", record.ExpectedWriteScope)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !restored.hasActiveTurn() {
		time.Sleep(time.Millisecond)
	}
	if !restored.hasActiveTurn() {
		t.Fatal("rehydrated worker did not consume the delivered message and create a turn")
	}
}

func TestRehydratePreservesConfiguredOrchestrationAndWorkDir(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{Orchestration: config.OrchestrationConfig{
		SubAgentQueueMessages: 7,
		SubAgentQueueBytes:    12345,
		SubAgentCompactUsage:  0.65,
	}}
	wantWorkDir := filepath.Join(a.projectRoot, "workspace")
	a.cachedWorkDir = wantWorkDir
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"restorer": {
			Name:   "restorer",
			Mode:   config.AgentModeSubAgent,
			Models: map[string][]string{"default": {"test/test-model"}},
		},
	})
	a.SetLLMFactory(func(string, []string, string) *llm.Client { return newTestLLMClient() })
	record := &DurableTaskRecord{
		TaskID:           "adhoc-rehydrate-config",
		AgentDefName:     "restorer",
		TaskDesc:         "resume with configured limits",
		State:            string(SubAgentStateCompleted),
		ResumePolicy:     taskResumePolicyNotify,
		LatestInstanceID: "restorer-old",
		InstanceHistory:  []string{"restorer-old"},
		RuntimeParked:    true,
		// The completed worker may only be revived under the write boundary
		// its admission recorded; this test pins the orchestration/limits it
		// resumes with, not the scope itself.
		ExpectedWriteScope: tools.WriteScope{PathPrefix: []string{"internal/agent"}},
	}
	a.setTaskRecords(map[string]*DurableTaskRecord{record.TaskID: record})

	sub, _, err := a.rehydrateTask(record)
	if err != nil {
		t.Fatalf("rehydrateTask: %v", err)
	}
	if sub.queueMessageLimit != 7 || sub.queueByteLimit != 12345 || sub.compactUsage != 0.65 {
		t.Fatalf("rehydrated orchestration = messages:%d bytes:%d compact:%v", sub.queueMessageLimit, sub.queueByteLimit, sub.compactUsage)
	}
	if sub.workDir != wantWorkDir {
		t.Fatalf("rehydrated workDir = %q, want %q", sub.workDir, wantWorkDir)
	}
}

func TestCreateSubAgentUsesCachedWorkDir(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 1)
	wantWorkDir := filepath.Join(a.projectRoot, "workspace")
	a.cachedWorkDir = wantWorkDir

	handle, err := a.CreateSubAgent(context.Background(), "use stable workspace", "worker", "", "", tools.WriteScope{})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	sub := a.subAgentByTaskID(handle.TaskID)
	if sub == nil {
		t.Fatal("expected live SubAgent")
	}
	if sub.workDir != wantWorkDir {
		t.Fatalf("created workDir = %q, want %q", sub.workDir, wantWorkDir)
	}
}

func TestConcurrentTaskRehydratePublishesOneRuntime(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"restorer": {
			Name:   "restorer",
			Mode:   "subagent",
			Models: map[string][]string{"default": {"test/test-model"}},
		},
	})
	var factoryCalls atomic.Int32
	factoryStarted := make(chan struct{})
	releaseFactory := make(chan struct{})
	a.SetLLMFactory(func(string, []string, string) *llm.Client {
		if factoryCalls.Add(1) == 1 {
			close(factoryStarted)
		}
		<-releaseFactory
		return newTestLLMClient()
	})
	record := &DurableTaskRecord{
		TaskID:           "adhoc-concurrent-rehydrate",
		AgentDefName:     "restorer",
		TaskDesc:         "resume once",
		State:            string(SubAgentStateCompleted),
		ResumePolicy:     taskResumePolicyNotify,
		LatestInstanceID: "restorer-40",
		InstanceHistory:  []string{"restorer-40"},
		RuntimeParked:    true,
		// Scope-less records are refused outright by rehydrateTask, so the
		// concurrent-activation fixture must carry the boundary a real
		// delegated task would have been admitted under.
		ExpectedWriteScope: tools.WriteScope{PathPrefix: []string{"internal/agent"}},
	}
	a.setTaskRecords(map[string]*DurableTaskRecord{record.TaskID: record})

	type result struct {
		sub        *SubAgent
		rehydrated bool
		err        error
	}
	results := make(chan result, 2)
	var callers sync.WaitGroup
	for range 2 {
		callers.Go(func() {
			sub, _, rehydrated, err := a.getOrRehydrateTask(cloneDurableTaskRecord(record))
			results <- result{sub: sub, rehydrated: rehydrated, err: err}
		})
	}
	<-factoryStarted
	time.Sleep(20 * time.Millisecond)
	if got := factoryCalls.Load(); got != 1 {
		t.Fatalf("LLM factory calls while activation is in flight = %d, want 1", got)
	}
	close(releaseFactory)
	callers.Wait()
	close(results)

	var first *SubAgent
	rehydratedCount := 0
	for got := range results {
		if got.err != nil {
			t.Fatalf("getOrRehydrateTask: %v", got.err)
		}
		if first == nil {
			first = got.sub
		} else if got.sub != first {
			t.Fatalf("concurrent callers received different runtimes: %p and %p", first, got.sub)
		}
		if got.rehydrated {
			rehydratedCount++
		}
	}
	if rehydratedCount != 1 {
		t.Fatalf("rehydrated result count = %d, want one activation leader", rehydratedCount)
	}
	live := 0
	for _, sub := range a.subs.snapshotSubAgents() {
		if sub.taskID == record.TaskID {
			live++
		}
	}
	if live != 1 || len(a.sem) != 1 {
		t.Fatalf("live runtimes = %d, semaphore slots = %d; want 1 and 1", live, len(a.sem))
	}
}

func newRevivalRaceTestMainAgent(t *testing.T) (*MainAgent, *DurableTaskRecord) {
	t.Helper()
	a := newTestMainAgent(t, t.TempDir())
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"restorer": {
			Name:   "restorer",
			Mode:   config.AgentModeSubAgent,
			Models: map[string][]string{"default": {"test/test-model"}},
		},
	})
	a.SetLLMFactory(func(string, []string, string) *llm.Client { return newTestLLMClient() })
	record := &DurableTaskRecord{
		TaskID:           "revival-race",
		AgentDefName:     "restorer",
		TaskDesc:         "reply after parking",
		State:            string(SubAgentStateWaitingMain),
		Attempt:          3,
		ResumePolicy:     taskResumePolicyNotify,
		LatestInstanceID: "restorer-9",
		InstanceHistory:  []string{"restorer-9"},
		RuntimeParked:    true,
		// Rehydration refuses scope-less records, and both revival-race tests
		// pin the attempt/settlement handling around rehydration — not the
		// write boundary itself — so the fixture carries a plausible one.
		ExpectedWriteScope: tools.WriteScope{PathPrefix: []string{"internal/agent"}},
	}
	a.setTaskRecords(map[string]*DurableTaskRecord{record.TaskID: cloneDurableTaskRecord(record)})
	return a, record
}

func TestRehydrateDecidesAttemptFromCurrentRecordNotStaleSnapshot(t *testing.T) {
	a, record := newRevivalRaceTestMainAgent(t)
	// The sweep settles the attempt after the caller took its snapshot but
	// before rehydration begins: the current record is terminal even though
	// the snapshot still says waiting_main.
	if got := a.settleDetachedTerminalTask(record.TaskID, SubAgentStateCancelled, "expired waiting for main reply", "expired"); got != SubAgentStateCancelled {
		t.Fatalf("settleDetachedTerminalTask = %q, want cancelled", got)
	}

	sub, _, err := a.rehydrateTask(cloneDurableTaskRecord(record))
	if err != nil {
		t.Fatalf("rehydrateTask: %v", err)
	}
	if sub == nil {
		t.Fatal("expected revived runtime")
	}
	rec := a.taskRecordByTaskID(record.TaskID)
	if rec.Attempt != 4 {
		t.Fatalf("revived attempt = %d, want 4 (bumped past the settled attempt)", rec.Attempt)
	}
	if rec.LatestSettlement != nil || rec.SettlementDurable {
		t.Fatalf("revived record kept the settled attempt's settlement: %+v", rec.LatestSettlement)
	}
}

func TestRehydrateBacksOffWhenTaskSettlesDuringCommit(t *testing.T) {
	a, record := newRevivalRaceTestMainAgent(t)
	// The sweep wins the race inside the rehydration window: after the attempt
	// decision was made from a live record, but before the runtime is published.
	a.rehydrateCommitHook = func() {
		a.rehydrateCommitHook = nil
		if got := a.settleDetachedTerminalTask(record.TaskID, SubAgentStateCancelled, "expired waiting for main reply", "expired"); got != SubAgentStateCancelled {
			t.Errorf("settleDetachedTerminalTask = %q, want cancelled", got)
		}
	}

	sub, _, err := a.rehydrateTask(cloneDurableTaskRecord(record))
	if err == nil || !strings.Contains(err.Error(), "settled") {
		t.Fatalf("rehydrateTask error = %v, want settled-while-reactivating conflict", err)
	}
	if sub != nil {
		t.Fatal("conflicting revival must not publish a runtime")
	}
	rec := a.taskRecordByTaskID(record.TaskID)
	if rec.State != string(SubAgentStateCancelled) || rec.Attempt != 3 {
		t.Fatalf("record after lost race = state:%s attempt:%d, want the settled cancelled attempt 3", rec.State, rec.Attempt)
	}
	if rec.LatestSettlement == nil || rec.LatestSettlement.Outcome != string(SubAgentStateCancelled) {
		t.Fatalf("settlement after lost race = %+v, want cancelled", rec.LatestSettlement)
	}
	if live := a.subAgentByTaskID(record.TaskID); live != nil {
		t.Fatal("no runtime should remain registered for the settled attempt")
	}

	// Retrying from the now-terminal record starts a clean next attempt.
	retried, _, err := a.rehydrateTask(a.taskRecordByTaskID(record.TaskID))
	if err != nil {
		t.Fatalf("retry rehydrateTask: %v", err)
	}
	if retried == nil {
		t.Fatal("expected revived runtime on retry")
	}
	if rec := a.taskRecordByTaskID(record.TaskID); rec.Attempt != 4 || rec.LatestSettlement != nil {
		t.Fatalf("retried record = attempt:%d settlement:%v, want fresh attempt 4", rec.Attempt, rec.LatestSettlement)
	}
}

func TestSubAgentWakeReevaluatesInputAfterRunningTransition(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-wake-transition")
	sub.setState(SubAgentStateWaitingMain, "waiting")
	sub.startRunLoop()
	t.Cleanup(sub.cancel)

	// Give runLoop time to enter select with its state-gated input channel
	// disabled, reproducing the rehydrate ordering that previously lost wakeups.
	time.Sleep(20 * time.Millisecond)
	sub.setState(SubAgentStateRunning, "resumed")
	if !sub.InjectUserMessage("resume this task") {
		t.Fatal("InjectUserMessage() rejected resumed input")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, msg := range sub.ctxMgr.Snapshot() {
			if msg.Role == message.RoleUser && strings.Contains(msg.Content, "resume this task") {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("resumed input remained queued after wake")
}

func TestSubAgentStartupWatchdogRetriesWakeThenReportsError(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-startup-watchdog")
	sub.startupTimeout = 10 * time.Millisecond
	if !sub.InjectUserMessage("queued without a running loop") {
		t.Fatal("InjectUserMessage() rejected queued input")
	}
	sub.armStartupWatchdog()

	select {
	case evt := <-a.eventCh:
		if evt.Type != EventAgentError || evt.SourceID != sub.instanceID {
			t.Fatalf("watchdog event = %#v, want SubAgent agent_error", evt)
		}
		err, ok := evt.Payload.(error)
		if !ok || !strings.Contains(err.Error(), "automatic wake retry") {
			t.Fatalf("watchdog error = %#v, want automatic wake retry failure", evt.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("startup watchdog did not report stalled queued input")
	}
}

func TestSubAgentTerminalErrorQueuesRiskAlertAndWakesMain(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-terminal-error")

	a.handleAgentError(Event{Type: EventAgentError, SourceID: sub.instanceID, Payload: context.DeadlineExceeded})

	select {
	case evt := <-a.eventCh:
		if evt.Type != EventSubAgentMailbox {
			t.Fatalf("event = %#v, want SubAgent mailbox", evt)
		}
		msg, ok := evt.Payload.(*SubAgentMailboxMessage)
		if !ok || msg.Kind != SubAgentMailboxKindRiskAlert || msg.Priority != SubAgentMailboxPriorityInterrupt {
			t.Fatalf("mailbox = %#v, want interrupt risk alert", evt.Payload)
		}
		a.dispatch(evt)
	case <-time.After(time.Second):
		t.Fatal("terminal SubAgent error did not queue a mailbox")
	}

	if a.turn == nil {
		t.Fatal("risk alert mailbox did not wake MainAgent")
	}
	if len(a.pendingSubAgentMailboxes) != 1 || a.pendingSubAgentMailboxes[0].Kind != SubAgentMailboxKindRiskAlert {
		t.Fatalf("pending mailboxes = %#v, want one risk alert", a.pendingSubAgentMailboxes)
	}
	if rec := a.taskRecordByTaskID(sub.taskID); rec == nil || rec.State != string(SubAgentStateFailed) {
		t.Fatalf("task record = %#v, want failed", rec)
	}
	foundBlockedNotice := false
	for {
		select {
		case output := <-a.outputCh:
			notify, ok := output.(AgentNotifyEvent)
			if ok && notify.AgentID == sub.instanceID && notify.TargetAgentID == "main" && notify.Kind == string(SubAgentMailboxKindRiskAlert) && strings.Contains(notify.Message, "failed") {
				foundBlockedNotice = true
			}
		default:
			if !foundBlockedNotice {
				t.Fatal("terminal SubAgent error did not emit owner-visible risk notification")
			}
			return
		}
	}
}

func TestOwnerMailboxQueuesDurablyWhenOwnerParksDuringDelivery(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	owner := newControllableTestSubAgent(t, a, "adhoc-owner-park-mailbox")
	owner.setState(SubAgentStateWaitingDescendant, "waiting for child")
	a.releaseSubAgentSlot(owner)
	a.syncTaskRecordFromSub(owner, "")

	barrierReached := make(chan struct{})
	releaseBarrier := make(chan struct{})
	a.subAgentParkBarrierHook = func(sub *SubAgent) {
		if sub == owner {
			close(barrierReached)
			<-releaseBarrier
		}
	}
	parked := make(chan bool, 1)
	go func() { parked <- a.parkSubAgent(owner.instanceID) }()
	<-barrierReached

	msg := SubAgentMailboxMessage{
		MessageID:    "child-risk-1",
		AgentID:      "child-1",
		TaskID:       "adhoc-child-risk",
		OwnerAgentID: owner.instanceID,
		OwnerTaskID:  owner.taskID,
		Kind:         SubAgentMailboxKindRiskAlert,
		Priority:     SubAgentMailboxPriorityInterrupt,
		Summary:      "child failed",
		Payload:      "inspect child failure",
	}
	enqueued := make(chan struct{})
	go func() {
		a.enqueueSubAgentMailbox(msg)
		close(enqueued)
	}()
	close(releaseBarrier)
	if !<-parked {
		t.Fatal("parkSubAgent() = false")
	}
	<-enqueued

	queued := a.ownedSubAgentMailboxes[owner.instanceID]
	if len(queued) != 1 || queued[0].MessageID != msg.MessageID {
		t.Fatalf("durable owner mailbox queue = %#v, want retained risk alert", queued)
	}
}

func TestWaitingMainLifecycleExpiresAfterUserTurns(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	// The turn budget only expires a wait once the minimum wall-clock wait has
	// also elapsed; this case exercises the turn clock, so the wall-clock guard
	// is disabled rather than slept through.
	a.waitingMainExpiry = waitingMainExpiryPolicy{turns: config.DefaultWaitingMainExpiryTurns, minWait: 0, maxWait: time.Hour}
	sub := newControllableTestSubAgent(t, a, "adhoc-6")
	sub.setState(SubAgentStateWaitingMain, "need answer")
	a.noteSubAgentStateTransition(sub, SubAgentStateWaitingMain)

	for range a.waitingMainExpiry.turns {
		a.explicitUserTurnCount.Add(1)
	}
	a.sweepSubAgentLifecycle()

	if got := a.subAgentByID(sub.instanceID); got != nil {
		t.Fatal("expected expired waiting_main worker to be parked")
	}
	rec := a.taskRecordByTaskID(sub.taskID)
	if rec == nil || rec.State != string(SubAgentStateCancelled) || !rec.RuntimeParked {
		t.Fatalf("task record = %#v, want parked cancelled task", rec)
	}
	if !strings.Contains(rec.LastSummary, "user turns") && !strings.Contains(rec.ClosedReason, "user turns") {
		t.Fatalf("task record summary/reason = (%q, %q), want the turn clock named", rec.LastSummary, rec.ClosedReason)
	}
}

func TestWaitingMainLifecycleKeepsWorkerBeforeMinimumWait(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	// A user holding several quick exchanges with the main agent must not
	// cancel a worker that escalated moments ago.
	a.waitingMainExpiry = waitingMainExpiryPolicy{turns: 2, minWait: time.Hour, maxWait: 24 * time.Hour}
	sub := newControllableTestSubAgent(t, a, "adhoc-min-wait")
	sub.setState(SubAgentStateWaitingMain, "need answer")
	a.noteSubAgentStateTransition(sub, SubAgentStateWaitingMain)

	a.explicitUserTurnCount.Add(10)
	a.sweepSubAgentLifecycle()

	if got := a.subAgentByID(sub.instanceID); got == nil {
		t.Fatal("waiting_main worker was cancelled before the minimum wall-clock wait elapsed")
	}
}

func TestWaitingMainLifecycleExpiresOnMaxWaitWithoutUserTurns(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	// Headless runs and an absent user produce no explicit turns at all, so the
	// unconditional clock is the only thing that can collect the wait.
	a.waitingMainExpiry = waitingMainExpiryPolicy{turns: 1 << 30, minWait: time.Hour, maxWait: time.Nanosecond}
	sub := newControllableTestSubAgent(t, a, "adhoc-max-wait")
	sub.setState(SubAgentStateWaitingMain, "need answer")
	a.noteSubAgentStateTransition(sub, SubAgentStateWaitingMain)

	a.sweepSubAgentLifecycle()

	if got := a.subAgentByID(sub.instanceID); got != nil {
		t.Fatal("waiting_main worker outlived the unconditional maximum wait")
	}
	if rec := a.taskRecordByTaskID(sub.taskID); rec == nil || rec.State != string(SubAgentStateCancelled) {
		t.Fatalf("task record = %#v, want cancelled task", rec)
	}
}

func TestTargetedNotifyRejectsParkedCancelledTask(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-cancelled-notify")
	sub.setState(SubAgentStateCancelled, "stopped by user")
	a.syncTaskRecordFromSub(sub, "stopped by user")
	if !a.parkSubAgent(sub.instanceID) {
		t.Fatal("parkSubAgent() = false, want parked cancelled worker")
	}

	if _, err := a.NotifySubAgent(context.Background(), sub.taskID, "resume", "follow_up"); err == nil {
		t.Fatal("NotifySubAgent succeeded for cancelled parked task")
	}
}

func TestParkedCancelledTaskAllowsExplicitUserResume(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"restorer": {Name: "restorer", Mode: "subagent", Models: map[string][]string{"default": {"test/test-model"}}},
	})
	a.SetLLMFactory(func(string, []string, string) *llm.Client { return newTestLLMClient() })
	sub := newControllableTestSubAgent(t, a, "adhoc-cancelled-user")
	sub.agentDefName = "restorer"
	// The explicit user resume revives the parked record through rehydration,
	// which refuses scope-less records; carry the boundary a real task had.
	sub.writeScope = tools.WriteScope{PathPrefix: []string{"internal/agent"}}
	sub.setState(SubAgentStateCancelled, "stopped by user")
	a.syncTaskRecordFromSub(sub, "stopped by user")
	if !a.parkSubAgent(sub.instanceID) {
		t.Fatal("parkSubAgent() = false, want parked cancelled worker")
	}
	a.SwitchFocus(sub.instanceID)
	a.SendUserMessage("resume explicitly")

	restored := a.subAgentByTaskID(sub.taskID)
	if restored == nil || restored.State() != SubAgentStateRunning {
		t.Fatalf("restored worker = %#v, want running", restored)
	}
}

func TestDescendantMailboxRoutesThroughRehydratedOwnerAlias(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"worker": {Name: "worker", Mode: "subagent", Models: map[string][]string{"default": {"test/test-model"}}},
	})
	a.SetLLMFactory(func(string, []string, string) *llm.Client { return newTestLLMClient() })
	owner := newControllableTestSubAgent(t, a, "adhoc-owner-alias")
	// The owner parks and is rehydrated from its durable record later in this
	// test; give the record the write boundary a rehydrated runtime requires.
	owner.writeScope = tools.WriteScope{PathPrefix: []string{"internal/agent"}}
	owner.setState(SubAgentStateWaitingDescendant, "waiting")
	a.syncTaskRecordFromSub(owner, "")
	oldOwnerID := owner.instanceID
	if !a.parkSubAgent(oldOwnerID) {
		t.Fatal("parkSubAgent() = false, want parked owner")
	}
	rec := a.taskRecordByTaskID(owner.taskID)
	rehydrated, _, err := a.rehydrateTask(rec)
	if err != nil {
		t.Fatalf("rehydrateTask: %v", err)
	}
	rehydrated.setState(SubAgentStateRunning, "resumed")

	msg := SubAgentMailboxMessage{
		MessageID:    "child-alias-1",
		AgentID:      "child-1",
		TaskID:       "adhoc-child-alias",
		OwnerAgentID: oldOwnerID,
		OwnerTaskID:  owner.taskID,
		Kind:         SubAgentMailboxKindProgress,
		Summary:      "still working",
	}
	if !a.routeOwnedSubAgentMailbox(msg) {
		t.Fatal("routeOwnedSubAgentMailbox() = false for historical owner runtime ID")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, persisted := range rehydrated.ctxMgr.Snapshot() {
			if strings.Contains(persisted.Content, "still working") {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("rehydrated owner did not append mailbox context")
}

func TestOwnedProgressMailboxPersistsDurableMetadata(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	owner := newControllableTestSubAgent(t, a, "adhoc-owner")
	owner.setState(SubAgentStateRunning, "running")
	mailbox := SubAgentMailboxMessage{
		MessageID:    "worker-child-1",
		AgentID:      "worker-child",
		TaskID:       "adhoc-child",
		OwnerAgentID: owner.instanceID,
		OwnerTaskID:  owner.taskID,
		Kind:         SubAgentMailboxKindProgress,
		Summary:      "halfway",
	}
	if !a.routeOwnedSubAgentMailbox(mailbox) {
		t.Fatal("routeOwnedSubAgentMailbox() = false")
	}
	owner.drainContextAppendsBeforeTurn()
	a.flushPersist()
	msgs := owner.ctxMgr.Snapshot()
	if len(msgs) != 1 || msgs[0].Kind != message.KindSubAgentMailbox || msgs[0].Mailbox == nil {
		t.Fatalf("owner context = %#v, want durable mailbox message", msgs)
	}
	if msgs[0].Mailbox.MessageID != mailbox.MessageID || msgs[0].Mailbox.Kind != string(SubAgentMailboxKindProgress) {
		t.Fatalf("mailbox metadata = %#v", msgs[0].Mailbox)
	}
	persisted, err := a.recoveryManager().LoadMessages(owner.instanceID)
	if err != nil {
		t.Fatalf("LoadMessages(owner): %v", err)
	}
	if len(persisted) != 1 || persisted[0].Mailbox == nil || persisted[0].Mailbox.MessageID != mailbox.MessageID {
		t.Fatalf("persisted owner context = %#v", persisted)
	}
}

func TestParkSubAgentWaitsForPendingTranscriptPersistence(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-park-persist")
	sub.setState(SubAgentStateCompleted, "done")
	sub.ctxMgr.Append(message.Message{Role: "assistant", Content: "final persisted reply"})
	sub.persistMessageAsync(message.Message{Role: "assistant", Content: "final persisted reply"}, "test reply", nil)
	a.syncTaskRecordFromSub(sub, "")

	if !a.parkSubAgent(sub.instanceID) {
		t.Fatal("parkSubAgent() = false")
	}
	rec := a.taskRecordByTaskID(sub.taskID)
	msgs, err := loadTaskHistoryMessages(a.recoveryManager(), rec, nil)
	if err != nil {
		t.Fatalf("loadTaskHistoryMessages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "final persisted reply" {
		t.Fatalf("persisted messages = %#v, want final reply", msgs)
	}
}

func TestParkSubAgentPersistenceFailureKeepsRuntimeLive(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-park-failure")
	sub.setState(SubAgentStateCompleted, "done")
	a.syncTaskRecordFromSub(sub, "")

	blockedRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile(blockedRoot): %v", err)
	}
	a.sessionDir = blockedRoot
	if parked := a.parkSubAgent(sub.instanceID); parked {
		t.Fatal("parkSubAgent() = true, want false after persistence failure")
	}
	if got := a.subAgentByID(sub.instanceID); got != sub {
		t.Fatalf("live SubAgent = %#v, want original runtime", got)
	}
	if rec := a.taskRecordByTaskID(sub.taskID); rec == nil || rec.RuntimeParked {
		t.Fatalf("task record = %#v, want live non-parked record", rec)
	}
}

func TestParkSubAgentRecoversDegradedTranscriptWithCheckpoint(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-park-recover")
	sub.setState(SubAgentStateCompleted, "done")
	sub.ctxMgr.Append(message.Message{Role: "assistant", Content: "checkpointed reply"})
	sub.notePersistenceFailure(fmt.Errorf("temporary disk error"))

	if got := sub.PersistenceHealth().State; got != PersistenceDegraded {
		t.Fatalf("persistence state = %q, want degraded", got)
	}
	if !a.parkSubAgent(sub.instanceID) {
		t.Fatal("parkSubAgent() = false after checkpoint recovery")
	}
	client, _ := sub.llmSnapshot()
	if client == nil {
		t.Fatal("parked SubAgent lost model metadata client")
	}
	if _, err := client.CompleteStream(context.Background(), nil, nil, nil); err == nil || err.Error() != "llm client is closed" {
		t.Fatalf("parked SubAgent client error = %v, want closed", err)
	}
	rec := a.taskRecordByTaskID(sub.taskID)
	if rec == nil || rec.Persistence.State != PersistenceHealthy {
		t.Fatalf("task persistence = %#v, want healthy", rec)
	}
	msgs, err := loadTaskHistoryMessages(a.recoveryManager(), rec, nil)
	if err != nil {
		t.Fatalf("loadTaskHistoryMessages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "checkpointed reply" {
		t.Fatalf("checkpointed messages = %#v", msgs)
	}
}

func TestParkedSubAgentRemoveLastMessageDoesNotRewriteMainTranscript(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	mainMsgs := []message.Message{{Role: "user", Content: "keep main"}}
	a.ctxMgr.RestoreMessages(mainMsgs)
	if err := a.recoveryManager().RewriteLog("main", mainMsgs); err != nil {
		t.Fatalf("RewriteLog(main): %v", err)
	}

	sub := newControllableTestSubAgent(t, a, "adhoc-park-remove")
	subMsgs := []message.Message{{Role: "user", Content: "worker prompt"}, {Role: "assistant", Content: "remove worker reply"}}
	sub.ctxMgr.RestoreMessages(subMsgs)
	if err := a.recoveryManager().RewriteLog(sub.instanceID, subMsgs); err != nil {
		t.Fatalf("RewriteLog(sub): %v", err)
	}
	sub.setState(SubAgentStateCompleted, "done")
	a.syncTaskRecordFromSub(sub, "")
	a.SwitchFocus(sub.instanceID)
	if !a.parkSubAgent(sub.instanceID) {
		t.Fatal("parkSubAgent() = false")
	}

	a.RemoveLastMessage()

	gotMain, err := a.recoveryManager().LoadMessages("main")
	if err != nil {
		t.Fatalf("LoadMessages(main): %v", err)
	}
	if len(gotMain) != 1 || gotMain[0].Content != "keep main" {
		t.Fatalf("main transcript = %#v, want unchanged", gotMain)
	}
	gotWorker := a.GetMessages()
	if len(gotWorker) != 1 || gotWorker[0].Content != "worker prompt" {
		t.Fatalf("parked worker transcript = %#v, want last worker message removed", gotWorker)
	}
}

func TestParkedSubAgentFocusedStatsAndPoolDoNotFallBackToMain(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.modelPoolPolicy = NewRuntimeModelPoolPolicy()
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder": {Name: "builder", Mode: config.AgentModeMain, Models: map[string][]string{"main": {"test/main"}}},
		"worker":  {Name: "worker", Mode: config.AgentModeSubAgent, Models: map[string][]string{"base": {"test/worker"}, "fast": {"test/worker-fast"}}},
	})
	a.activeConfig = a.agentConfigs["builder"]
	a.usageTracker.RecordForAgent(identity.MainAgentID, "test/main", nil, message.TokenUsage{InputTokens: 100})

	sub := newControllableTestSubAgent(t, a, "adhoc-park-focus")
	sub.agentDefName = "worker"
	a.usageTracker.RecordForAgent(sub.instanceID, "test/worker", nil, message.TokenUsage{InputTokens: 7, OutputTokens: 3})
	sub.setState(SubAgentStateCompleted, "done")
	a.syncTaskRecordFromSub(sub, "")
	a.SwitchFocus(sub.instanceID)
	if !a.parkSubAgent(sub.instanceID) {
		t.Fatal("parkSubAgent() = false")
	}

	stats := a.GetSidebarUsageStats()
	if stats.InputTokens != 7 || stats.OutputTokens != 3 {
		t.Fatalf("parked usage = %#v, want worker-only stats", stats)
	}
	if current, limit := a.GetContextStats(); current != 0 || limit != 0 {
		t.Fatalf("parked context stats = (%d, %d), want unavailable zeros", current, limit)
	}
	if got := a.CurrentPoolName(); got != "base" {
		t.Fatalf("CurrentPoolName() = %q, want worker base pool", got)
	}
	if err := a.SetCurrentModelPool("fast"); err != nil {
		t.Fatalf("SetCurrentModelPool(fast): %v", err)
	}
	a.dispatch(<-a.eventCh)
	if got, ok := a.AgentOverridePoolName("worker"); !ok || got != "fast" {
		t.Fatalf("worker pool override = %q, %v; want fast, true", got, ok)
	}
	if got := a.modelPoolPolicy.CurrentModelPool(); got != "" {
		t.Fatalf("main current pool = %q, want unchanged", got)
	}
}

func TestParkBarrierConcurrentFocusedInputPreventsParkingAndPreservesMessage(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"worker": {Name: "worker", Mode: config.AgentModeSubAgent, Models: map[string][]string{"default": {"test/test-model"}}},
	})
	a.SetLLMFactory(func(string, []string, string) *llm.Client { return newTestLLMClient() })
	sub := newControllableTestSubAgent(t, a, "adhoc-park-input-race")
	// The message racing the park barrier is delivered through a rehydrated
	// runtime, which requires the record to carry a write boundary.
	sub.writeScope = tools.WriteScope{PathPrefix: []string{"internal/agent"}}
	sub.setState(SubAgentStateIdle, "idle")
	a.syncTaskRecordFromSub(sub, "")
	a.SwitchFocus(sub.instanceID)

	barrierReached := make(chan struct{})
	releaseBarrier := make(chan struct{})
	a.subAgentParkBarrierHook = func(*SubAgent) {
		close(barrierReached)
		<-releaseBarrier
	}
	parked := make(chan bool, 1)
	go func() { parked <- a.parkSubAgent(sub.instanceID) }()
	<-barrierReached

	inputDone := make(chan struct{})
	go func() {
		a.SendUserMessage("preserve during park")
		close(inputDone)
	}()
	close(releaseBarrier)
	wasParked := <-parked
	<-inputDone

	rec := a.taskRecordByTaskID(sub.taskID)
	if wasParked && (rec == nil || rec.LatestInstanceID == sub.instanceID) {
		t.Fatalf("task record = %#v, want input delivered through a rehydrated runtime", rec)
	}
	live := a.subAgentByTaskID(sub.taskID)
	if live == nil {
		t.Fatal("focused input was lost without a live runtime")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, msg := range live.ctxMgr.Snapshot() {
			if msg.Role == message.RoleUser && strings.Contains(msg.Content, "preserve during park") {
				return
			}
		}
		if len(live.inputCh)+len(live.inputOverflow) > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("concurrent focused input was neither queued nor appended")
}

func TestParkReactivatesWaitingSubAgentWhenInputArrivesBeforeFinalCheck(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-park-waiting-input")
	sub.setState(SubAgentStateWaitingMain, "waiting")
	a.releaseSubAgentSlot(sub)
	a.syncTaskRecordFromSub(sub, "")

	barrierReached := make(chan struct{})
	releaseBarrier := make(chan struct{})
	a.subAgentParkBarrierHook = func(*SubAgent) {
		close(barrierReached)
		<-releaseBarrier
	}
	parked := make(chan bool, 1)
	go func() { parked <- a.parkSubAgent(sub.instanceID) }()
	<-barrierReached
	if !sub.InjectUserMessage("arrived before park") {
		t.Fatal("InjectUserMessage() = false")
	}
	close(releaseBarrier)

	if <-parked {
		t.Fatal("parkSubAgent() = true with queued input")
	}
	if sub.State() != SubAgentStateRunning {
		t.Fatalf("sub.State() = %q, want running", sub.State())
	}
	if !sub.semHeld {
		t.Fatal("reactivated SubAgent did not reacquire a slot")
	}
}

func TestSubAgentPersistencePumpPreservesMessageOrder(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-persist-order")
	for _, content := range []string{"first", "second", "third"} {
		sub.persistMessageAsync(message.Message{Role: "user", Content: content}, "ordered test message", nil)
	}
	a.flushPersist()

	msgs, err := a.recoveryManager().LoadMessages(sub.instanceID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 3 || msgs[0].Content != "first" || msgs[1].Content != "second" || msgs[2].Content != "third" {
		t.Fatalf("persisted messages = %#v, want enqueue order", msgs)
	}
}

func TestSubAgentControlToolPersistenceKeepsResultAfterRestore(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-control-persist")
	callID := "call-escalate-order"
	sub.persistMessageAsync(message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{{
			ID:   callID,
			Name: tools.NameEscalate,
			Args: []byte(`{"reason":"need owner input"}`),
		}},
	}, "assistant control call", nil)
	sub.persistMessageAsync(message.Message{
		Role:       "tool",
		ToolCallID: callID,
		Content:    "Escalation sent: need owner input",
	}, "control tool result", nil)
	a.flushPersist()

	msgs, err := a.recoveryManager().LoadMessages(sub.instanceID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Role != message.RoleAssistant || msgs[1].Role != message.RoleTool {
		t.Fatalf("persisted control messages = %#v, want assistant then tool", msgs)
	}

	restored := newControllableTestSubAgent(t, a, "adhoc-control-restore")
	restored.RestoreMessages(msgs)
	got := restored.GetMessages()
	if len(got) != 2 || got[1].ToolCallID != callID {
		t.Fatalf("restored control messages = %#v, want settled tool result retained", got)
	}
}

func TestPersistencePumpCloseDrainsAcceptedEntriesAndRejectsLaterEnqueues(t *testing.T) {
	pump := newPersistencePump(1)
	var got []string
	pump.start(func(entry persistEntry) { got = append(got, entry.msg.Content) })
	if !pump.enqueue(persistEntry{msg: message.Message{Content: "accepted"}}, make(chan struct{})) {
		t.Fatal("initial enqueue rejected")
	}
	pump.close()
	<-pump.done
	if len(got) != 1 || got[0] != "accepted" {
		t.Fatalf("drained entries = %#v, want accepted entry", got)
	}
	if pump.enqueue(persistEntry{msg: message.Message{Content: "late"}}, make(chan struct{})) {
		t.Fatal("enqueue succeeded after close")
	}
}

func TestTerminalSubAgentsRemainAvailableAfterLifecycleSweep(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	failed := newControllableTestSubAgent(t, a, "adhoc-failed")
	failed.instanceID = "worker-failed"
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[failed.instanceID] = failed
	a.subs.mu.Unlock()
	failed.setState(SubAgentStateFailed, "request failed")
	a.noteSubAgentStateTransition(failed, SubAgentStateFailed)
	cancelled := newControllableTestSubAgent(t, a, "adhoc-cancelled")
	cancelled.instanceID = "worker-cancelled"
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[cancelled.instanceID] = cancelled
	a.subs.mu.Unlock()
	cancelled.setState(SubAgentStateCancelled, "cancelled by user")
	a.noteSubAgentStateTransition(cancelled, SubAgentStateCancelled)

	a.explicitUserTurnCount.Add(config.DefaultWaitingMainExpiryTurns + 1)
	a.sweepSubAgentLifecycle()

	if got := a.subAgentByID(failed.instanceID); got != failed {
		t.Fatal("failed SubAgent was removed by lifecycle sweep")
	}
	if got := a.subAgentByID(cancelled.instanceID); got != cancelled {
		t.Fatal("cancelled SubAgent was removed by lifecycle sweep")
	}
}

// installSnapshotBlockingRecoveryManager installs a real recovery manager whose
// snapshot.json path is already a directory, so every SaveSnapshot fails with a
// filesystem error while message, meta, and task-registry writes keep working.
// It is the deterministic injection point for the recovery-snapshot failure
// branch of CreateSubAgent and rehydrateTaskAsActivationLeader.
func installSnapshotBlockingRecoveryManager(t *testing.T, a *MainAgent) {
	t.Helper()
	blocker := filepath.Join(a.sessionDir, "snapshot.json")
	if err := os.Mkdir(blocker, 0o755); err != nil {
		t.Fatalf("mkdir snapshot blocker: %v", err)
	}
	a.installRecoveryManager(recovery.NewRecoveryManager(a.sessionDir))
}

func TestCreateSubAgentSnapshotPersistFailureDoesNotLeakSlot(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	installSnapshotBlockingRecoveryManager(t, a)

	// The snapshot write after the commit must be non-fatal: the registration
	// (task registry + instance meta) is already durable and the next
	// saveRecoverySnapshot rewrites the snapshot from live state. Failing the
	// creation here was the historical path that leaked the runtime slot.
	handle, err := a.CreateSubAgent(context.Background(), "snapshot write fails", "worker", "", "", tools.WriteScope{})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	if handle.Status != "started" {
		t.Fatalf("handle.Status = %q, want started despite snapshot failure", handle.Status)
	}
	sub := a.subAgentByTaskID(handle.TaskID)
	if sub == nil {
		t.Fatal("expected the published SubAgent to stay live after a non-fatal snapshot failure")
	}
	if got := len(a.subs.snapshotSubAgents()); got != 1 {
		t.Fatalf("live SubAgents = %d, want 1", got)
	}
	snap := a.runtimeGovernorSnapshot()
	if snap.RuntimeInUse != 1 || snap.RuntimeHolders != 1 || snap.RuntimeSlotDrift != 0 {
		t.Fatalf("governor after publish = in_use:%d holders:%d drift:%d, want 1/1/0", snap.RuntimeInUse, snap.RuntimeHolders, snap.RuntimeSlotDrift)
	}
	if rec := a.taskRecordByTaskID(handle.TaskID); rec == nil || rec.LatestInstanceID != sub.instanceID {
		t.Fatalf("task record after publish = %#v, want the live instance", rec)
	}

	a.closeSubAgent(sub.instanceID)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := sub.waitDone(ctx); err != nil {
		t.Fatalf("wait for SubAgent shutdown: %v", err)
	}
	snap = a.runtimeGovernorSnapshot()
	if snap.RuntimeInUse != 0 || snap.RuntimeHolders != 0 || snap.RuntimeSlotDrift != 0 {
		t.Fatalf("governor after close = in_use:%d holders:%d drift:%d, want 0/0/0", snap.RuntimeInUse, snap.RuntimeHolders, snap.RuntimeSlotDrift)
	}
}

func TestRehydrateSnapshotPersistFailureDoesNotLeakSlot(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	installSnapshotBlockingRecoveryManager(t, a)
	record := &DurableTaskRecord{
		TaskID:           "adhoc-rehydrate-snap",
		AgentDefName:     "worker",
		TaskDesc:         "resume after snapshot failure",
		State:            string(SubAgentStateIdle),
		RuntimeParked:    true,
		ResumePolicy:     taskResumePolicyNotify,
		LatestInstanceID: "worker-old",
		// This test pins slot governance around a refused snapshot persist,
		// so the rehydratable record carries the boundary real tasks ship with.
		ExpectedWriteScope: tools.WriteScope{PathPrefix: []string{"internal/agent"}},
	}
	a.setTaskRecords(map[string]*DurableTaskRecord{record.TaskID: cloneDurableTaskRecord(record)})

	sub, _, err := a.rehydrateTask(cloneDurableTaskRecord(record))
	if err != nil {
		t.Fatalf("rehydrateTask: %v", err)
	}
	if sub == nil {
		t.Fatal("rehydrateTask returned no SubAgent")
	}
	snap := a.runtimeGovernorSnapshot()
	if snap.RuntimeInUse != 1 || snap.RuntimeHolders != 1 || snap.RuntimeSlotDrift != 0 {
		t.Fatalf("governor after rehydrate = in_use:%d holders:%d drift:%d, want 1/1/0", snap.RuntimeInUse, snap.RuntimeHolders, snap.RuntimeSlotDrift)
	}
	if rec := a.taskRecordByTaskID(record.TaskID); rec == nil || rec.LatestInstanceID != sub.instanceID {
		t.Fatalf("task record after rehydrate = %#v, want the new live instance", rec)
	}

	a.closeSubAgent(sub.instanceID)
	if snap := a.runtimeGovernorSnapshot(); snap.RuntimeInUse != 0 || snap.RuntimeHolders != 0 || snap.RuntimeSlotDrift != 0 {
		t.Fatalf("governor after close = %+v, want runtime 0/0/0", snap)
	}
}

func TestCreateSubAgentMCPServerFailureReleasesAdmissionSlot(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	// Agent-scoped MCP servers cannot be enabled at runtime, so a manual entry
	// makes getOrCreateAgentMCP fail after the admission already holds a slot.
	a.agentConfigs["worker"].MCP = config.MCPConfig{"runtime-only": {Manual: true}}

	_, err := a.CreateSubAgent(context.Background(), "mcp must fail", "worker", "", "", tools.WriteScope{})
	if err == nil || !strings.Contains(err.Error(), "manual") {
		t.Fatalf("CreateSubAgent() err = %v, want agent-scoped MCP manual rejection", err)
	}
	if got := len(a.subs.snapshotSubAgents()); got != 0 {
		t.Fatalf("live SubAgents = %d, want 0 after MCP failure", got)
	}
	if got := len(a.subs.admissions); got != 0 {
		t.Fatalf("pending admissions = %d, want 0 after MCP failure", got)
	}
	if got := a.governor.snapshot().RuntimeInUse; got != 0 {
		t.Fatalf("runtime in use = %d, want 0 after MCP failure", got)
	}
}

func TestCancelSubAgentAdmissionsDuringCreateReleasesSlotOnce(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	entered := make(chan struct{})
	release := make(chan struct{})
	a.taskRegistryPersistHook = func() {
		close(entered)
		<-release
	}
	type outcome struct {
		handle tools.TaskHandle
		err    error
	}
	result := make(chan outcome, 1)
	go func() {
		handle, err := a.CreateSubAgent(context.Background(), "cancel vs create", "worker", "plan-race", "semantic-race", tools.WriteScope{})
		result <- outcome{handle: handle, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("create did not reach durable persistence")
	}
	// Cancel while the create is inside registration persistence: the admission
	// is still recorded as holding the slot, so the canceller releases it once.
	// The create's post-persistence recheck then backs off without a second
	// release (the admission is already gone from the registry).
	a.cancelSubAgentAdmissions()
	close(release)
	got := <-result
	if got.err == nil || !strings.Contains(got.err.Error(), "admission was cancelled after persistence") {
		t.Fatalf("CreateSubAgent() = (%#v, %v), want post-persistence cancellation", got.handle, got.err)
	}
	if pending := len(a.subs.admissions); pending != 0 {
		t.Fatalf("pending admissions = %d, want 0", pending)
	}
	if live := len(a.subs.snapshotSubAgents()); live != 0 {
		t.Fatalf("live SubAgents = %d, want 0", live)
	}
	if rec := a.taskRecordByTaskID("adhoc-1"); rec != nil {
		t.Fatalf("task record = %#v, want nil after cancellation", rec)
	}
	if got := a.governor.snapshot().RuntimeInUse; got != 0 {
		t.Fatalf("runtime in use = %d, want 0: the slot must be released exactly once across cancel and create rollback", got)
	}
}

// ---------------------------------------------------------------------------
// WaitingMain expiry trigger, owner notification, and request-expired ledger
// (regression: expiry previously depended on user speech)
// ---------------------------------------------------------------------------

// pendingAgentRequestForSource returns the durable escalation request a source
// task owns in the sweep tests below, cloned under the registry lock.
func pendingAgentRequestForSource(t *testing.T, a *MainAgent, taskID string) *DurableAgentRequest {
	t.Helper()
	a.subs.mu.RLock()
	defer a.subs.mu.RUnlock()
	for _, request := range a.subs.agentRequests {
		if request != nil && strings.TrimSpace(request.SourceTaskID) == taskID {
			return cloneDurableAgentRequest(request)
		}
	}
	return nil
}

// dispatchQueuedEvents runs every already-queued event through dispatch until
// the queues are empty. It is the test-side stand-in for the Run loop; new
// events produced during dispatch are drained as well, so callers must keep
// mailbox delivery paused unless a real turn is wanted.
func dispatchQueuedEvents(t *testing.T, a *MainAgent) {
	t.Helper()
	for {
		evt, ok := a.popQueuedEvent()
		if !ok {
			return
		}
		a.dispatch(evt)
	}
}

// findRiskAlertForTask locates the expiry risk_alert mailbox in the main-agent
// inbox (memory or spool) for the given task.
func findRiskAlertForTask(a *MainAgent, taskID string) *SubAgentMailboxMessage {
	collect := func() []SubAgentMailboxMessage {
		out := append([]SubAgentMailboxMessage{}, a.subAgentInbox.urgent...)
		out = append(out, a.subAgentInbox.normal...)
		for _, id := range a.subAgentInbox.spoolUrgent {
			if msg, found, err := a.loadSpooledMailbox(id); err == nil && found {
				out = append(out, *msg)
			}
		}
		for _, id := range a.subAgentInbox.spoolNormal {
			if msg, found, err := a.loadSpooledMailbox(id); err == nil && found {
				out = append(out, *msg)
			}
		}
		return out
	}
	for _, msg := range collect() {
		if msg.Kind == SubAgentMailboxKindRiskAlert && strings.TrimSpace(msg.TaskID) == taskID {
			duplicate := msg
			return &duplicate
		}
	}
	return nil
}

// sawRiskAlertNotify drains the TUI output channel looking for the
// control-plane AgentNotifyEvent that accompanies the expiry mailbox.
func sawRiskAlertNotify(a *MainAgent, taskID string) bool {
	for {
		select {
		case evt := <-a.outputCh:
			notify, ok := evt.(AgentNotifyEvent)
			if ok && strings.TrimSpace(notify.TaskID) == taskID && notify.Kind == string(SubAgentMailboxKindRiskAlert) {
				return true
			}
		default:
			return false
		}
	}
}

// TestWaitingMainLifecycleSweepReclaimsParkedWorkerNotifiesOwnerAndExpiresRequest
// is the end-to-end regression for the "expiry depends on user speech" defect:
// a delegated worker parks waiting for the main agent, wall clock advances past
// maxWait with no user message, and the independent lifecycle-sweep event must
// reclaim the task, deliver a risk_alert mailbox to the owner, and move the
// durable escalation request to expired.
func TestWaitingMainLifecycleSweepReclaimsParkedWorkerNotifiesOwnerAndExpiresRequest(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-expiry-e2e")
	sub.agentDefName = "worker"

	// A delegated worker asks its owner (the main agent) for a decision through
	// the production escalate path, which parks the worker in waiting_main and
	// writes a pending durable agent request.
	a.waitingMainExpiry = waitingMainExpiryPolicy{turns: config.DefaultWaitingMainExpiryTurns, minWait: 5 * time.Minute, maxWait: time.Hour}
	a.handleEscalate(Event{
		Type:     EventEscalate,
		SourceID: sub.instanceID,
		Payload:  tools.AgentRequestPayload{Reason: "which API should I use"},
	})

	rec := a.taskRecordByTaskID(sub.taskID)
	if rec == nil || SubAgentState(rec.State) != SubAgentStateWaitingMain || !rec.RuntimeParked {
		t.Fatalf("after escalate task record = %#v, want parked waiting_main worker", rec)
	}
	if live := a.subAgentByTaskID(sub.taskID); live != nil {
		t.Fatal("worker must be parked (no live runtime) while waiting for main")
	}
	request := pendingAgentRequestForSource(t, a, sub.taskID)
	if request == nil || request.State != "pending" {
		t.Fatalf("escalation request = %#v, want pending", request)
	}

	// Wall clock advances past maxWait while the user stays silent, then the
	// periodic trigger fires its sweep event. No EventUserMessage is involved.
	a.mailboxDeliveryPaused.Store(true)
	a.waitingMainExpiry = waitingMainExpiryPolicy{turns: 1 << 30, minWait: time.Hour, maxWait: time.Nanosecond}
	a.dispatch(Event{Type: EventSubAgentLifecycleSweep})

	rec = a.taskRecordByTaskID(sub.taskID)
	if rec == nil || SubAgentState(rec.State) != SubAgentStateCancelled || !rec.RuntimeParked {
		t.Fatalf("after expiry task record = %#v, want parked cancelled worker", rec)
	}
	request = pendingAgentRequestForSource(t, a, sub.taskID)
	if request == nil || request.State != "expired" {
		t.Fatalf("escalation request after expiry = %#v, want expired", request)
	}
	loaded, err := loadAgentRequests(a.sessionDir)
	if err != nil {
		t.Fatalf("loadAgentRequests: %v", err)
	}
	if got := loaded[request.CorrelationID]; got == nil || got.State != "expired" {
		t.Fatalf("persisted request = %#v, want expired on disk too", got)
	}
	if !sawRiskAlertNotify(a, sub.taskID) {
		t.Fatal("owner did not receive the expiry AgentNotifyEvent")
	}

	// Drain the queued mailbox events (the escalate decision request and the
	// new expiry risk alert) so the owner-facing mailbox lands in the inbox.
	dispatchQueuedEvents(t, a)
	risk := findRiskAlertForTask(a, sub.taskID)
	if risk == nil {
		t.Fatalf("expected a risk_alert mailbox for task %s in the owner inbox, urgent=%v normal=%v spool=%v/%v",
			sub.taskID, a.subAgentInbox.urgent, a.subAgentInbox.normal, a.subAgentInbox.spoolUrgent, a.subAgentInbox.spoolNormal)
	}
	if risk.OwnerAgentID != "" || risk.OwnerTaskID != "" {
		t.Fatalf("expiry mailbox owner = (%q,%q), want main-owned empty owner", risk.OwnerAgentID, risk.OwnerTaskID)
	}
	if !strings.Contains(risk.Summary, "expired") {
		t.Fatalf("expiry mailbox summary = %q, want the expiry reason", risk.Summary)
	}
}

// TestWaitingMainLifecycleSweepNotifiesAndExpiresForLiveWaitingMainWorker covers
// the same notification contract for the live-worker branch of the sweep: a
// worker still registered in waiting_main (park refused or never attempted)
// must alert its owner and expire its pending request when collected.
func TestWaitingMainLifecycleSweepNotifiesAndExpiresForLiveWaitingMainWorker(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-live-expiry")
	sub.agentDefName = "worker"
	if _, err := a.createAgentRequest(sub, tools.AgentRequestPayload{Reason: "need a decision"}); err != nil {
		t.Fatalf("createAgentRequest: %v", err)
	}
	a.handleSubAgentStateChangedEvent(Event{
		Type:     EventSubAgentStateChanged,
		SourceID: sub.instanceID,
		Payload:  &SubAgentStateChangedPayload{State: SubAgentStateWaitingMain, Summary: "need a decision"},
	})
	if live := a.subAgentByTaskID(sub.taskID); live == nil {
		t.Fatal("worker must stay live for the live-waiting_main sweep branch")
	}

	a.mailboxDeliveryPaused.Store(true)
	a.waitingMainExpiry = waitingMainExpiryPolicy{turns: 1 << 30, minWait: time.Hour, maxWait: time.Nanosecond}
	a.dispatch(Event{Type: EventSubAgentLifecycleSweep})

	if live := a.subAgentByTaskID(sub.taskID); live != nil {
		t.Fatal("live waiting_main worker survived the lifecycle sweep")
	}
	rec := a.taskRecordByTaskID(sub.taskID)
	if rec == nil || SubAgentState(rec.State) != SubAgentStateCancelled || !rec.RuntimeParked {
		t.Fatalf("task record after live expiry = %#v, want parked cancelled worker", rec)
	}
	request := pendingAgentRequestForSource(t, a, sub.taskID)
	if request == nil || request.State != "expired" {
		t.Fatalf("escalation request after live expiry = %#v, want expired", request)
	}
	if !sawRiskAlertNotify(a, sub.taskID) {
		t.Fatal("owner did not receive the expiry AgentNotifyEvent")
	}
	dispatchQueuedEvents(t, a)
	risk := findRiskAlertForTask(a, sub.taskID)
	if risk == nil {
		t.Fatalf("expected a risk_alert mailbox for task %s in the owner inbox", sub.taskID)
	}
	if risk.TaskID != request.SourceTaskID {
		t.Fatalf("expiry mailbox task = %q, want %q", risk.TaskID, request.SourceTaskID)
	}
}

// TestWaitingMainLifecycleSweepCandidatesPinsLiveAndParkedWaits pins the gate
// the periodic trigger relies on: only live or parked waiting_main state counts
// as a candidate, so the trigger stays silent for sessions that never delegate.
func TestWaitingMainLifecycleSweepCandidatesPinsLiveAndParkedWaits(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	if a.hasWaitingMainExpiryCandidates() {
		t.Fatal("empty session must not produce expiry candidates")
	}
	live := newControllableTestSubAgent(t, a, "adhoc-candidate-live")
	live.setState(SubAgentStateWaitingMain, "waiting")
	if !a.hasWaitingMainExpiryCandidates() {
		t.Fatal("live waiting_main worker not detected as candidate")
	}
	live.setState(SubAgentStateRunning, "")
	rec := &DurableTaskRecord{
		TaskID:        "adhoc-candidate-parked",
		AgentDefName:  "worker",
		State:         string(SubAgentStateWaitingMain),
		RuntimeParked: true,
		Attempt:       1,
	}
	a.subs.mu.Lock()
	a.subs.taskRecords[rec.TaskID] = rec
	a.subs.mu.Unlock()
	if !a.hasWaitingMainExpiryCandidates() {
		t.Fatal("parked waiting_main record not detected as candidate")
	}
	a.subs.mu.Lock()
	a.subs.taskRecords[rec.TaskID].State = string(SubAgentStateCancelled)
	a.subs.mu.Unlock()
	if a.hasWaitingMainExpiryCandidates() {
		t.Fatal("terminal parked record must not remain an expiry candidate")
	}
}

// TestExpiredAgentRequestRejectsLateNotify pins the consumer-facing effect of
// the new "expired" ledger write: a reply to an expired request is refused with
// an explicit state instead of being treated as pending.
func TestExpiredAgentRequestRejectsLateNotify(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-expired-notify")
	request, err := a.createAgentRequest(sub, tools.AgentRequestPayload{Reason: "need a decision"})
	if err != nil {
		t.Fatalf("createAgentRequest: %v", err)
	}
	// Stand in for the expiry sweep's terminal settle (commitTerminalTask with
	// SubAgentStateCancelled), which is the only state under which the request
	// ledger moves to expired.
	a.handleSubAgentCloseRequestedEvent(Event{
		Type:     EventSubAgentCloseRequested,
		SourceID: sub.instanceID,
		Payload: &SubAgentCloseRequestedPayload{
			Reason:       "expired waiting for main reply",
			ClosedReason: "expired waiting for main reply",
			FinalState:   SubAgentStateCancelled,
		},
	})
	a.expireAgentRequestsAfterCancellation(sub.taskID)

	_, err = a.NotifySubAgentMessage(context.Background(), tools.AgentResponseRequest{
		TargetTaskID: sub.taskID, CorrelationID: request.CorrelationID, Message: "too late",
	})
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("late NotifySubAgentMessage error = %v, want expired-state rejection", err)
	}
}

func completedMailboxMessage(msgs []SubAgentMailboxMessage, taskID string) *SubAgentMailboxMessage {
	for i := range msgs {
		msg := &msgs[i]
		if msg.Kind != SubAgentMailboxKindCompleted || strings.TrimSpace(msg.TaskID) != taskID {
			continue
		}
		return msg
	}
	return nil
}

// TestHandleAgentDonePersistsCompletionMailboxBeforeTerminalCommit pins the
// durable ordering fix for the "settled but never notified" crash window: the
// completion mailbox must be persisted before the terminal commit, because a
// crash after the commit but before the mailbox delivery would otherwise leave
// a durable completed task whose owner never receives the completion. After
// handleAgentDone returns, the completion mailbox must already be in the
// mailbox log and the task record already terminal, even though the queued
// mailbox delivery event has not been dispatched yet. The later event only
// delivers the already-durable message and must not write a second copy.
func TestHandleAgentDonePersistsCompletionMailboxBeforeTerminalCommit(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(a.sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	sub := newControllableTestSubAgent(t, a, "adhoc-durable-first")
	a.newTurn() // keep a busy turn so dispatching delivery does not start an LLM turn

	a.handleAgentDone(Event{
		Type:     EventAgentDone,
		SourceID: sub.instanceID,
		Payload:  &AgentResult{Summary: "durable-first done"},
	})

	msgs, err := loadSubAgentMailboxMessages(a.sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxMessages: %v", err)
	}
	mailbox := completedMailboxMessage(msgs, sub.taskID)
	if mailbox == nil || mailbox.Summary != "durable-first done" || mailbox.Completion == nil {
		t.Fatalf("completion mailbox not durable after handleAgentDone (before delivery), log=%#v", msgs)
	}
	rec := a.taskRecordByTaskID(sub.taskID)
	if rec == nil || rec.State != string(SubAgentStateCompleted) {
		t.Fatalf("task record after handleAgentDone = %#v, want terminal completed", rec)
	}

	dispatchQueuedEvents(t, a)

	rec = a.taskRecordByTaskID(sub.taskID)
	if rec == nil || rec.State != string(SubAgentStateCompleted) || rec.LastMailboxID != mailbox.MessageID {
		t.Fatalf("task record after delivery = %#v, want completed with LastMailboxID %q", rec, mailbox.MessageID)
	}
	if got := len(a.subAgentInbox.urgent); got != 1 {
		t.Fatalf("len(urgent inbox) after delivery = %d, want one completed mailbox", got)
	}
	if a.subAgentInbox.urgent[0].MessageID != mailbox.MessageID {
		t.Fatalf("delivered mailbox id = %q, want %q", a.subAgentInbox.urgent[0].MessageID, mailbox.MessageID)
	}
	raw, err := os.ReadFile(filepath.Join(a.sessionDir, "subagents", "mailbox.jsonl"))
	if err != nil {
		t.Fatalf("read mailbox log: %v", err)
	}
	entryCount := 0
	for line := range strings.SplitSeq(string(raw), "\n") {
		if strings.Contains(line, `"message_id":`) {
			entryCount++
		}
	}
	if entryCount != 1 {
		t.Fatalf("mailbox log entries = %d, want exactly one (delivery must not rewrite the message)", entryCount)
	}
}

// TestHandleAgentDoneTerminalCommitSurvivesMailboxPersistFailure pins the
// best-effort constraint of the ordering fix: when the completion mailbox
// cannot be persisted (mailbox.jsonl is blocked here), the terminal commit
// must still succeed — persistence failure never leaves the task stuck in a
// non-terminal state.
func TestHandleAgentDoneTerminalCommitSurvivesMailboxPersistFailure(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-best-effort")
	subagentsDir := filepath.Join(a.sessionDir, "subagents")
	if err := os.MkdirAll(subagentsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	blockedMailboxPath := filepath.Join(subagentsDir, "mailbox.jsonl")
	if err := os.Mkdir(blockedMailboxPath, 0o755); err != nil {
		t.Fatalf("block mailbox log with a directory: %v", err)
	}

	a.handleAgentDone(Event{
		Type:     EventAgentDone,
		SourceID: sub.instanceID,
		Payload:  &AgentResult{Summary: "best-effort done"},
	})

	rec := a.taskRecordByTaskID(sub.taskID)
	if rec == nil || rec.State != string(SubAgentStateCompleted) || !rec.RuntimeParked {
		t.Fatalf("task record after degraded mailbox persist = %#v, want parked completed task", rec)
	}
	onDisk, err := loadDurableTaskRecords(a.sessionDir)
	if err != nil {
		t.Fatalf("loadDurableTaskRecords: %v", err)
	}
	if got := onDisk[sub.taskID]; got == nil || got.State != string(SubAgentStateCompleted) {
		t.Fatalf("durable task record after degraded mailbox persist = %#v, want completed", got)
	}
}

// TestAgentDoneCompletionMailboxEventDeliversExactlyOnceWithoutRewriting pins
// the delivery semantics of the completion event queued by handleAgentDone
// after it persisted the completion mailbox ahead of the terminal commit:
// dispatching that event must deliver the already-durable message exactly once
// without persisting it a second time (the mailbox log keeps a single entry),
// and a repeated dispatch of the same event must not queue a second copy for
// the owner. The duplicate-restore side of the same discriminator (a message
// already queued by restore is dropped) is pinned by
// TestRestoredMailboxEventDeduplicatesQueuedMessageID.
func TestAgentDoneCompletionMailboxEventDeliversExactlyOnceWithoutRewriting(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(a.sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	sub := newControllableTestSubAgent(t, a, "adhoc-deliver-once")
	a.newTurn() // keep a busy turn so dispatch does not auto-drain into a new LLM turn

	a.handleAgentDone(Event{
		Type:     EventAgentDone,
		SourceID: sub.instanceID,
		Payload:  &AgentResult{Summary: "deliver once done"},
	})

	var evt Event
	for {
		select {
		case queued := <-a.eventCh:
			evt = queued
		default:
			t.Fatal("expected completion mailbox event queued on eventCh")
		}
		if evt.Type == EventSubAgentMailbox {
			break
		}
	}
	mailbox, ok := evt.Payload.(*SubAgentMailboxMessage)
	if !ok || mailbox == nil {
		t.Fatalf("queued mailbox payload = %#v, want *SubAgentMailboxMessage", evt.Payload)
	}
	messageID := strings.TrimSpace(mailbox.MessageID)
	if messageID == "" {
		t.Fatal("completion mailbox event carries no MessageID")
	}
	// Precondition of the deliver-only dispatch: the message is already
	// durable (persisted by handleAgentDone) but not yet queued anywhere.
	if a.hasQueuedMailboxMessage(messageID) {
		t.Fatalf("completion message %q unexpectedly already queued before dispatch", messageID)
	}

	a.dispatch(evt)
	if got := len(a.subAgentInbox.urgent); got != 1 {
		t.Fatalf("len(urgent) after first dispatch = %d, want 1", got)
	}
	if got := a.subAgentInbox.urgent[0].MessageID; got != messageID {
		t.Fatalf("delivered mailbox id = %q, want %q", got, messageID)
	}

	// A repeated dispatch of the same event must not double-deliver: the
	// message is now queued, so the duplicate is dropped.
	a.dispatch(evt)
	if got := len(a.subAgentInbox.urgent); got != 1 {
		t.Fatalf("len(urgent) after re-dispatch = %d, want 1 (deliver-only dispatch must be exactly-once)", got)
	}

	raw, err := os.ReadFile(filepath.Join(a.sessionDir, "subagents", "mailbox.jsonl"))
	if err != nil {
		t.Fatalf("read mailbox log: %v", err)
	}
	entryCount := 0
	for line := range strings.SplitSeq(string(raw), "\n") {
		if strings.Contains(line, `"message_id":`) {
			entryCount++
		}
	}
	if entryCount != 1 {
		t.Fatalf("mailbox log entries = %d, want exactly one (neither dispatch may rewrite the message)", entryCount)
	}
}

// TestHandleAgentErrorPersistsRiskAlertMailboxBeforeTerminalCommit pins the
// durable ordering fix for the failure half of the "settled but never
// notified" crash window: the risk_alert mailbox must be persisted before the
// terminal commit, because a crash after the Failed commit but before the
// mailbox delivery would otherwise leave a durable failed task whose owner
// never receives the failure notification. After handleAgentError returns, the
// risk_alert mailbox must already be in the mailbox log and the task record
// already terminal Failed, and the queued mailbox delivery event must not
// write a second copy.
func TestHandleAgentErrorPersistsRiskAlertMailboxBeforeTerminalCommit(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(a.sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	sub := newControllableTestSubAgent(t, a, "adhoc-risk-durable-first")
	a.newTurn() // keep a busy turn so dispatching delivery does not start an LLM turn

	a.handleAgentError(Event{Type: EventAgentError, SourceID: sub.instanceID, Payload: context.DeadlineExceeded})

	msgs, err := loadSubAgentMailboxMessages(a.sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxMessages: %v", err)
	}
	var alert *SubAgentMailboxMessage
	for i := range msgs {
		if msgs[i].Kind == SubAgentMailboxKindRiskAlert && strings.TrimSpace(msgs[i].TaskID) == sub.taskID {
			alert = &msgs[i]
		}
	}
	if alert == nil {
		t.Fatalf("risk_alert mailbox not durable after handleAgentError (before delivery), log=%#v", msgs)
	}
	rec := a.taskRecordByTaskID(sub.taskID)
	if rec == nil || rec.State != string(SubAgentStateFailed) {
		t.Fatalf("task record after handleAgentError = %#v, want terminal failed", rec)
	}
	if rec.LastMailboxID != alert.MessageID {
		t.Fatalf("task record LastMailboxID = %q, want risk_alert %q (mailbox must be persisted before the terminal commit)", rec.LastMailboxID, alert.MessageID)
	}

	dispatchQueuedEvents(t, a)

	raw, err := os.ReadFile(filepath.Join(a.sessionDir, "subagents", "mailbox.jsonl"))
	if err != nil {
		t.Fatalf("read mailbox log: %v", err)
	}
	entryCount := 0
	for line := range strings.SplitSeq(string(raw), "\n") {
		if strings.Contains(line, `"risk_alert"`) {
			entryCount++
		}
	}
	if entryCount != 1 {
		t.Fatalf("risk_alert mailbox log entries = %d, want exactly one (delivery must not rewrite the message)", entryCount)
	}
}

// TestWaitingMainExpiryPersistsRiskAlertMailboxBeforeTerminalCommit pins the
// same durable ordering for the live-worker branch of the WaitingMain expiry
// sweep: the sweep must persist the expiry risk_alert mailbox before its
// terminal Cancelled commit, so the owner notification cannot be lost to a
// crash between the commit and the queued mailbox delivery.
func TestWaitingMainExpiryPersistsRiskAlertMailboxBeforeTerminalCommit(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(a.sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	sub := newControllableTestSubAgent(t, a, "adhoc-expiry-durable")
	sub.setState(SubAgentStateWaitingMain, "need answer")
	a.noteSubAgentStateTransition(sub, SubAgentStateWaitingMain)
	a.waitingMainExpiry = waitingMainExpiryPolicy{turns: 1 << 30, minWait: time.Hour, maxWait: time.Nanosecond}
	a.mailboxDeliveryPaused.Store(true) // keep the expiry mailbox out of a live turn

	a.sweepSubAgentLifecycle()

	msgs, err := loadSubAgentMailboxMessages(a.sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxMessages: %v", err)
	}
	var alert *SubAgentMailboxMessage
	for i := range msgs {
		if msgs[i].Kind == SubAgentMailboxKindRiskAlert && strings.TrimSpace(msgs[i].TaskID) == sub.taskID {
			alert = &msgs[i]
		}
	}
	if alert == nil {
		t.Fatalf("expiry risk_alert mailbox not durable after the sweep (before delivery), log=%#v", msgs)
	}
	if !strings.Contains(alert.Summary, "expired waiting for main reply") {
		t.Fatalf("expiry risk_alert summary = %q, want the expiry reason", alert.Summary)
	}
	rec := a.taskRecordByTaskID(sub.taskID)
	if rec == nil || rec.State != string(SubAgentStateCancelled) {
		t.Fatalf("task record after expiry sweep = %#v, want terminal cancelled", rec)
	}
	if rec.LastMailboxID != alert.MessageID {
		t.Fatalf("task record LastMailboxID = %q, want expiry risk_alert %q (mailbox must be persisted before the terminal commit)", rec.LastMailboxID, alert.MessageID)
	}
}

// A worker that failed still holds the transcript that produced the failure, so
// telling it what went wrong resumes that run instead of making a fresh worker
// rediscover the whole context. This is the path a wrap-up rejection needs: the
// deliverable is already in that worker's history.
func TestSendMessageToFailedTaskRehydratesWorkerForAnotherAttempt(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"fixer": {
			Name:   "fixer",
			Mode:   "subagent",
			Models: map[string][]string{"default": {"test/test-model"}},
		},
	})
	a.SetLLMFactory(func(systemPrompt string, agentModels []string, variant string) *llm.Client {
		return newTestLLMClient()
	})
	sub := newControllableTestSubAgent(t, a, "adhoc-failed-resume")
	sub.agentDefName = "fixer"
	sub.taskDesc = "Fix the parser"
	sub.writeScope = tools.WriteScope{Files: []string{"internal/parser/parse.go"}}
	sub.ctxMgr.Append(message.Message{Role: "user", Content: "Fix the parser"})
	if err := a.recoveryManager().PersistMessage(sub.instanceID, message.Message{Role: "user", Content: "Fix the parser"}); err != nil {
		t.Fatalf("PersistMessage(sub): %v", err)
	}
	oldInstanceID := sub.instanceID
	a.handleAgentError(Event{Type: EventAgentError, SourceID: sub.instanceID, Payload: context.DeadlineExceeded})

	record := a.taskRecordByTaskID("adhoc-failed-resume")
	if record == nil || SubAgentState(record.State) != SubAgentStateFailed {
		t.Fatalf("record state = %#v, want a failed task", record)
	}
	priorAttempt := record.Attempt

	handle, err := a.NotifySubAgent(context.Background(), "adhoc-failed-resume", "the wrap-up call was malformed; re-send it", "correction")
	if err != nil {
		t.Fatalf("NotifySubAgent on a failed task: %v", err)
	}
	if !handle.Rehydrated {
		t.Fatal("failed worker should rehydrate for another attempt")
	}
	if handle.PreviousAgentID != oldInstanceID {
		t.Fatalf("handle.PreviousAgentID = %q, want %q", handle.PreviousAgentID, oldInstanceID)
	}

	restored := a.subAgentByTaskID("adhoc-failed-resume")
	if restored == nil {
		t.Fatal("expected a live worker after rehydrating the failed task")
	}
	if restored.State() == SubAgentStateFailed {
		t.Fatal("restored.State() = failed; a resumed worker must not stay terminal")
	}
	// The transcript that produced the failure is what makes resuming cheaper
	// than re-delegating, so it has to come back with the worker.
	restoredMsgs := restored.ctxMgr.Snapshot()
	if len(restoredMsgs) == 0 || restoredMsgs[0].Content != "Fix the parser" {
		t.Fatalf("restored transcript = %#v, want the original task history", restoredMsgs)
	}
	record = a.taskRecordByTaskID("adhoc-failed-resume")
	if record.Attempt != priorAttempt+1 {
		t.Fatalf("record.Attempt = %d, want %d (a resumed terminal task starts a new attempt)", record.Attempt, priorAttempt+1)
	}
	if record.LatestSettlement != nil {
		t.Fatalf("record.LatestSettlement = %#v, want the previous attempt's settlement cleared", record.LatestSettlement)
	}
	if record.ExpectedWriteScope.Files[0] != "internal/parser/parse.go" {
		t.Fatalf("rehydrated write scope = %#v, want the original file scope", record.ExpectedWriteScope)
	}
}

// Cancellation is a decision someone made. A follow-up message must not quietly
// reverse it, and the rejection has to point at the way forward.
func TestSendMessageToCancelledTaskIsRejectedWithDelegateGuidance(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"adhoc-cancelled": {
			TaskID:            "adhoc-cancelled",
			AgentDefName:      "fixer",
			State:             string(SubAgentStateCancelled),
			ResumePolicy:      taskResumePolicyExplicitOnly,
			RuntimeParked:     true,
			SettlementDurable: true,
		},
	})

	_, err := a.NotifySubAgent(context.Background(), "adhoc-cancelled", "please continue", "follow_up")
	if err == nil {
		t.Fatal("NotifySubAgent on a cancelled task = nil error, want a rejection")
	}
	if !strings.Contains(err.Error(), "cancelled") || !strings.Contains(err.Error(), "delegate the work again") {
		t.Fatalf("error = %q, want it to name the cancellation and point at re-delegation", err.Error())
	}
}

// The status bar reads one number for the focused agent's prompt side, and it
// has to mean the same thing before and after that agent parks. Reading a live
// agent from its context manager broke that: a context manager belongs to one
// runtime instance, so a task resumed for a second attempt reported only the
// current attempt while live and every attempt once parked — the number jumped
// on parking without any work happening. Both sides now read the ledger, which
// accounts per task.
func TestFocusedTokenUsageCoversEveryAttemptWhileLive(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"worker": {Name: "worker", Mode: config.AgentModeSubAgent, Models: map[string][]string{"default": {"test/worker"}}},
	})
	a.usageTracker.RecordForAgent(identity.MainAgentID, "test/main", nil, message.TokenUsage{InputTokens: 900, OutputTokens: 90})

	sub := newControllableTestSubAgent(t, a, "adhoc-usage-frame")
	sub.agentDefName = "worker"
	// A first attempt that already ended, and the live attempt that replaced it.
	a.usageTracker.RecordForAgent("worker-earlier", "test/worker", nil, message.TokenUsage{InputTokens: 300, OutputTokens: 30})
	current := message.TokenUsage{InputTokens: 1000, OutputTokens: 40, CacheReadTokens: 800, CacheWriteTokens: 50}
	a.usageTracker.RecordForAgent(sub.instanceID, "test/worker", nil, current)
	sub.ctxMgr.UpdateFromUsage(current)
	sub.setState(SubAgentStateCompleted, "done")
	a.syncTaskRecordFromSub(sub, "")
	record := a.taskRecordByTaskID("adhoc-usage-frame")
	record.InstanceHistory = []string{"worker-earlier", sub.instanceID}
	a.setTaskRecords(map[string]*DurableTaskRecord{"adhoc-usage-frame": record})

	a.SwitchFocus(sub.instanceID)
	live := a.GetTokenUsage()
	if live.OutputTokens != 70 {
		t.Fatalf("live focused OutputTokens = %d, want 70 (both attempts of this task, not just the live instance)", live.OutputTokens)
	}
	if live.InputTokens != 1300 {
		t.Fatalf("live focused InputTokens = %d, want 1300 (both attempts, prompt side including the cached prefix)", live.InputTokens)
	}

	if !a.parkSubAgent(sub.instanceID) {
		t.Fatal("parkSubAgent() = false")
	}
	if parked := a.GetTokenUsage(); parked != live {
		t.Fatalf("parked focused usage = %#v, want the live reading %#v unchanged by parking", parked, live)
	}
}

// Discovering a missing path used to cost the whole worker: a running task's
// scope is fixed at delegation time, so the owner had to cancel and re-delegate
// with a corrected scope, throwing away everything the worker had done.
func TestNotifyWithScopeGrantWidensALiveWorkersScope(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	root := t.TempDir()
	a.projectRoot = root
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"worker": {Name: "worker", Mode: config.AgentModeSubAgent, Models: map[string][]string{"default": {"test/test-model"}}},
	})
	a.SetLLMFactory(func(string, []string, string) *llm.Client { return newTestLLMClient() })
	sub := newControllableTestSubAgent(t, a, "adhoc-scope-grant")
	sub.agentDefName = "worker"
	sub.workDir = root
	sub.writeScope = tools.WriteScope{PathPrefix: []string{"internal/agent"}}
	sub.tools.Register(tools.WriteTool{BaseDir: root})
	sub.setState(SubAgentStateIdle, "idle")
	a.syncTaskRecordFromSub(sub, "")

	blocked, _ := json.Marshal(map[string]string{"path": "internal/tools/task.go", "content": "x"})
	if _, err := sub.executeToolCall(context.Background(), message.ToolCall{ID: "before", Name: tools.NameWrite, Args: blocked}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("pre-grant write error = %v, want a scope rejection", err)
	}

	if _, err := a.NotifySubAgentWithScopeGrant(context.Background(), "adhoc-scope-grant", "you also need internal/tools", "constraint_update",
		tools.WriteScope{PathPrefix: []string{"internal/tools"}}); err != nil {
		t.Fatalf("NotifySubAgentWithScopeGrant: %v", err)
	}

	if _, err := sub.executeToolCall(context.Background(), message.ToolCall{ID: "after", Name: tools.NameWrite, Args: blocked}); err != nil {
		t.Fatalf("post-grant write error = %v, want the granted path allowed", err)
	}
	// The original path stays writable: a grant adds, it does not replace.
	original, _ := json.Marshal(map[string]string{"path": "internal/agent/sub.go", "content": "y"})
	if _, err := sub.executeToolCall(context.Background(), message.ToolCall{ID: "original", Name: tools.NameWrite, Args: original}); err != nil {
		t.Fatalf("originally-scoped write error = %v, want it still allowed", err)
	}
	record := a.taskRecordByTaskID("adhoc-scope-grant")
	if len(record.ExpectedWriteScope.PathPrefix) != 2 {
		t.Fatalf("record scope = %#v, want both path prefixes so a rehydrate keeps the grant", record.ExpectedWriteScope)
	}
}

func TestNotifyScopeGrantRejectsNoOpAndReadOnlyTargets(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"adhoc-writer": {
			TaskID: "adhoc-writer", AgentDefName: "worker", State: string(SubAgentStateIdle),
			ExpectedWriteScope: tools.WriteScope{PathPrefix: []string{"internal/agent"}},
		},
		"adhoc-reader": {
			TaskID: "adhoc-reader", AgentDefName: "worker", State: string(SubAgentStateIdle),
			ExpectedWriteScope: tools.WriteScope{ReadOnly: true},
		},
	})

	_, err := a.NotifySubAgentWithScopeGrant(context.Background(), "adhoc-writer", "msg", "", tools.WriteScope{PathPrefix: []string{"internal/agent"}})
	if err == nil || !strings.Contains(err.Error(), "already covers every path") {
		t.Fatalf("no-op grant error = %v, want the caller told it changes nothing", err)
	}
	_, err = a.NotifySubAgentWithScopeGrant(context.Background(), "adhoc-reader", "msg", "", tools.WriteScope{PathPrefix: []string{"internal/agent"}})
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("read-only grant error = %v, want it refused with a re-delegation hint", err)
	}
}

// TestHandleAgentNotifyIgnoresLateNotifyForTerminalRuntime pins the terminal
// barrier for queued/late progress notices: once a durable terminal outcome is
// committed, the runtime mirrors it (commitTerminalTask flips the live runtime
// before updating the record), so a notify event dispatched afterwards must not
// resurrect the cancelled/completed attempt. Legal terminal reuse opens a new
// attempt through resetForAttempt/rehydration instead of a plain Running
// transition, so dropping here cannot block legitimate follow-up work.
func TestHandleAgentNotifyIgnoresLateNotifyForTerminalRuntime(t *testing.T) {
	for _, terminal := range []SubAgentState{SubAgentStateCancelled, SubAgentStateCompleted, SubAgentStateFailed} {
		t.Run(string(terminal), func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			sub := newControllableTestSubAgent(t, a, "adhoc-terminal-notify")
			sub.setState(terminal, "settled")
			a.syncTaskRecordFromSub(sub, "settled")
			if !isTerminalSubAgentState(sub.State()) {
				t.Fatalf("runtime state = %q, want terminal %q", sub.State(), terminal)
			}

			a.handleAgentNotify(Event{SourceID: sub.instanceID, Payload: tools.AgentNotifyPayload{Message: "late progress update"}})

			if got := sub.State(); got != terminal {
				t.Fatalf("runtime state after late notify = %q, want unchanged terminal %q", got, terminal)
			}
			select {
			case evt := <-a.eventCh:
				t.Fatalf("late notify queued a mailbox event for the settled runtime: %v", evt.Type)
			default:
			}
			if rec := a.taskRecordByTaskID(sub.taskID); rec == nil || SubAgentState(rec.State) != terminal {
				t.Fatalf("task record = %#v, want terminal %q", rec, terminal)
			}
		})
	}
}

// TestHandleAgentNotifyResumesWaitingForMainRuntime pins the legal side of the
// terminal barrier: a worker parked in waiting_main (or quiescent in idle)
// that reports continued progress must still resume to Running and queue its
// progress mailbox for the main inbox.
func TestHandleAgentNotifyResumesWaitingForMainRuntime(t *testing.T) {
	for _, resumed := range []SubAgentState{SubAgentStateWaitingMain, SubAgentStateIdle} {
		t.Run(string(resumed), func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			sub := newControllableTestSubAgent(t, a, "adhoc-resume-notify")
			sub.setState(resumed, "waiting on an owner decision")
			a.syncTaskRecordFromSub(sub, "")

			a.handleAgentNotify(Event{SourceID: sub.instanceID, Payload: tools.AgentNotifyPayload{Message: "owner approved; continuing"}})

			if got := sub.State(); got != SubAgentStateRunning {
				t.Fatalf("runtime state after notify = %q, want running", got)
			}
			deadline := time.After(time.Second)
			for {
				select {
				case evt := <-a.eventCh:
					mailbox, ok := evt.Payload.(*SubAgentMailboxMessage)
					if !ok || evt.Type != EventSubAgentMailbox {
						continue
					}
					if mailbox.Kind != SubAgentMailboxKindProgress || mailbox.Summary != "owner approved; continuing" {
						t.Fatalf("mailbox = %#v, want a progress update carrying the notify", mailbox)
					}
					return
				case <-deadline:
					t.Fatal("timed out waiting for the queued progress mailbox")
				}
			}
		})
	}
}

// TestMainInboxProgressIsRunnableAndStagesAsOneBatch pins the progress-wake
// gate: while an idle main holds per-agent progress snapshots, they count as
// runnable mailbox work, and a single drain stages every snapshot into one
// batch instead of waking the main once per update.
func TestMainInboxProgressIsRunnableAndStagesAsOneBatch(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.subAgentInbox.progress["worker-1"] = SubAgentMailboxMessage{MessageID: "p-1", AgentID: "worker-1", TaskID: "task-a", Kind: SubAgentMailboxKindProgress, Summary: "still working"}
	a.subAgentInbox.progress["worker-2"] = SubAgentMailboxMessage{MessageID: "p-2", AgentID: "worker-2", TaskID: "task-b", Kind: SubAgentMailboxKindProgress, Summary: "almost done"}

	if !a.hasRunnableMailboxWork() {
		t.Fatal("hasRunnableMailboxWork() = false while progress snapshots are pending for an idle main")
	}
	if !a.stageNextSubAgentMailboxBatch() {
		t.Fatal("stageNextSubAgentMailboxBatch() = false, want progress staged")
	}
	if len(a.pendingSubAgentMailboxes) != 2 {
		t.Fatalf("pending batch = %d messages, want both snapshots merged into one batch", len(a.pendingSubAgentMailboxes))
	}
	if len(a.subAgentInbox.progress) != 0 {
		t.Fatalf("progress snapshot map = %#v, want it consumed by staging", a.subAgentInbox.progress)
	}
	if a.stageNextSubAgentMailboxBatch() {
		t.Fatal("stageNextSubAgentMailboxBatch() = true with nothing left to stage")
	}
	// Once the staged batch is taken by the request (the turn-overlay consumer)
	// and no new snapshots arrived, the work is gone and the main may go idle.
	a.pendingSubAgentMailboxes = nil
	a.activeSubAgentMailboxes = nil
	a.activeSubAgentMailbox = nil
	if a.hasRunnableMailboxWork() {
		t.Fatal("hasRunnableMailboxWork() = true after the staged batch was consumed")
	}
}

// TestProgressArrivalMergesIntoQueuedMailboxBatch pins the one-cycle merge for
// a real arrival: a progress snapshot and a queued urgent mailbox that drain
// together land in the same staged batch, so the wake handles all routable
// main-inbox messages at once.
func TestProgressArrivalMergesIntoQueuedMailboxBatch(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.subAgentInbox.progress["worker-1"] = SubAgentMailboxMessage{MessageID: "p-1", AgentID: "worker-1", TaskID: "task-a", Kind: SubAgentMailboxKindProgress, Summary: "still working"}
	a.subAgentInbox.urgent = []SubAgentMailboxMessage{{
		MessageID: "u-1",
		AgentID:   "worker-2",
		TaskID:    "task-b",
		Kind:      SubAgentMailboxKindDecisionRequired,
		Priority:  SubAgentMailboxPriorityInterrupt,
		Summary:   "needs a decision",
	}}

	if !a.stageNextSubAgentMailboxBatch() {
		t.Fatal("stageNextSubAgentMailboxBatch() = false")
	}
	if len(a.pendingSubAgentMailboxes) != 2 {
		t.Fatalf("pending batch = %d messages, want the urgent mailbox and the progress snapshot merged", len(a.pendingSubAgentMailboxes))
	}
	first := a.pendingSubAgentMailboxes[0]
	if first == nil || first.MessageID != "u-1" {
		t.Fatalf("first pending mailbox = %#v, want the urgent mailbox staged first", first)
	}
}

// TestMainInboxProgressDoesNotInterruptActiveTurn pins the anti-interruption
// constraint: progress snapshots are only staged by a drain that starts while
// the main is idle; an active turn leaves both the snapshot and the staging
// pipeline untouched.
func TestMainInboxProgressDoesNotInterruptActiveTurn(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.subAgentInbox.progress["worker-1"] = SubAgentMailboxMessage{MessageID: "p-1", AgentID: "worker-1", TaskID: "task-a", Kind: SubAgentMailboxKindProgress, Summary: "update"}
	a.newTurn()
	if a.currentTurn() == nil {
		t.Fatal("active turn was not created")
	}

	a.drainSubAgentInbox()

	if len(a.pendingSubAgentMailboxes) != 0 || len(a.activeSubAgentMailboxes) != 0 {
		t.Fatalf("progress staged while a turn was running: pending=%v active=%v", a.pendingSubAgentMailboxes, a.activeSubAgentMailboxes)
	}
	if got := a.subAgentInbox.progress["worker-1"]; got.MessageID != "p-1" {
		t.Fatalf("progress snapshot = %#v, want it untouched by an active-turn drain", got)
	}
	if a.currentTurn() == nil {
		t.Fatal("active turn was replaced by the drain")
	}
}

// TestTurnContinuationStagingExcludesPendingProgress pins the same boundary on
// the mid-turn continuation path: an active turn's next-request staging must
// still deliver actionable mailbox heads, but must leave pending progress
// snapshots queued until the turn ends and the main goes idle again.
func TestTurnContinuationStagingExcludesPendingProgress(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.subAgentInbox.progress["worker-1"] = SubAgentMailboxMessage{MessageID: "p-1", AgentID: "worker-1", TaskID: "task-a", Kind: SubAgentMailboxKindProgress, Summary: "still working"}
	a.subAgentInbox.urgent = []SubAgentMailboxMessage{{
		MessageID: "u-1",
		AgentID:   "worker-2",
		TaskID:    "task-b",
		Kind:      SubAgentMailboxKindDecisionRequired,
		Priority:  SubAgentMailboxPriorityInterrupt,
		Summary:   "needs a decision",
	}}
	a.newTurn()
	if a.currentTurn() == nil {
		t.Fatal("active turn was not created")
	}

	if !a.prepareSubAgentMailboxBatchForTurnContinuation() {
		t.Fatal("turn-continuation staging returned false with an actionable mailbox queued")
	}
	if len(a.pendingSubAgentMailboxes) != 1 || a.pendingSubAgentMailboxes[0] == nil || a.pendingSubAgentMailboxes[0].MessageID != "u-1" {
		t.Fatalf("pending batch = %#v, want only the actionable mailbox staged mid-turn", a.pendingSubAgentMailboxes)
	}
	if got := a.subAgentInbox.progress["worker-1"]; got.MessageID != "p-1" {
		t.Fatalf("progress snapshot = %#v, want it still queued while the turn runs", got)
	}
	if len(a.pendingSubAgentMailboxes) != 1 {
		t.Fatalf("progress was merged into the mid-turn continuation batch: %#v", a.pendingSubAgentMailboxes)
	}
}

// TestStageNextCompletedBatchRequeuesQueueResidentProgress pins the P3-2
// strictness gate: a progress message that somehow sits in the deliverable
// queue behind a completed head (progress normally only lives in the per-agent
// snapshot map, which staging claims only between turns) must be requeued into
// that map instead of being folded into the staged batch. Folding it would let
// a progress update ride a mid-turn completed batch, bypassing the
// between-turns gate the head applies to snapshot claims.
func TestStageNextCompletedBatchRequeuesQueueResidentProgress(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.subAgentInbox.urgent = []SubAgentMailboxMessage{{
		MessageID: "c-1",
		AgentID:   "worker-1",
		TaskID:    "task-a",
		Kind:      SubAgentMailboxKindCompleted,
		Priority:  SubAgentMailboxPriorityUrgent,
		Summary:   "child finished",
	}}
	a.subAgentInbox.normal = []SubAgentMailboxMessage{{
		MessageID: "p-1",
		AgentID:   "worker-2",
		TaskID:    "task-b",
		Kind:      SubAgentMailboxKindProgress,
		Summary:   "still working",
	}}

	if !a.stageNextSubAgentMailboxBatch() {
		t.Fatal("stageNextSubAgentMailboxBatch() = false, want the completed head staged")
	}
	if len(a.pendingSubAgentMailboxes) != 1 || a.pendingSubAgentMailboxes[0] == nil || a.pendingSubAgentMailboxes[0].MessageID != "c-1" {
		t.Fatalf("pending batch = %#v, want only the completed head (no progress fold-in)", a.pendingSubAgentMailboxes)
	}
	if got := a.subAgentInbox.progress["worker-2"]; got.MessageID != "p-1" {
		t.Fatalf("progress snapshot = %#v, want the dequeued progress requeued into the snapshot map", a.subAgentInbox.progress)
	}
	if len(a.subAgentInbox.urgent)+len(a.subAgentInbox.normal) != 0 {
		t.Fatalf("queue still holds messages after staging: urgent=%d normal=%d", len(a.subAgentInbox.urgent), len(a.subAgentInbox.normal))
	}
}

// TestConcurrentMailboxQueueDeliveryKeepsStateConsistent hammers the shared
// main-inbox queue from the two goroutine roles that race in production — the
// event-loop delivery/drain side and the TUI-facing manual-delivery side that
// claims messages for a worker — and checks that the queue contents and the
// memory-byte counter stay consistent. Run under -race this pins the
// subAgentMailboxIDsMu discipline across store/dequeue/takeOutstanding;
// without it the counter and slice state can tear.
func TestConcurrentMailboxQueueDeliveryKeepsStateConsistent(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "race-task")

	const messagesPerProducer = 300
	const producerAgents = 3

	var stored, removed atomic.Int64
	var wg sync.WaitGroup
	enqueue := func(group int) {
		defer wg.Done()
		agentID := fmt.Sprintf("other-%d", group)
		if group == 0 {
			// This producer feeds the claim side: takeOutstandingMailboxForSub
			// matches the worker's instance ID the way a manual follow-up does.
			agentID = sub.instanceID
		}
		for i := 0; i < messagesPerProducer; i++ {
			msg := SubAgentMailboxMessage{
				MessageID: fmt.Sprintf("%s-%d", agentID, group*messagesPerProducer+i),
				AgentID:   agentID,
				TaskID:    "race-task",
				Kind:      SubAgentMailboxKindDecisionRequired,
				Priority:  SubAgentMailboxPriorityInterrupt,
				Summary:   "update",
			}
			if a.storeMailboxInMemory(msg, false) {
				stored.Add(1)
			}
		}
	}
	for group := 0; group < producerAgents; group++ {
		wg.Add(1)
		go enqueue(group)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < messagesPerProducer*producerAgents; i++ {
			if msg := a.takeOutstandingMailboxForSub(sub); msg != nil {
				removed.Add(1)
			}
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < messagesPerProducer*producerAgents; i++ {
			if msg := a.dequeueNextSubAgentMailbox(); msg != nil {
				removed.Add(1)
			}
		}
	}()
	wg.Wait()

	a.subAgentMailboxIDsMu.Lock()
	remaining := len(a.subAgentInbox.urgent) + len(a.subAgentInbox.normal)
	var wantBytes int
	for _, msg := range a.subAgentInbox.urgent {
		wantBytes += mailboxMessageBytes(msg)
	}
	for _, msg := range a.subAgentInbox.normal {
		wantBytes += mailboxMessageBytes(msg)
	}
	gotBytes := a.subAgentInbox.memoryBytes
	a.subAgentMailboxIDsMu.Unlock()
	if got := stored.Load() - removed.Load(); got != int64(remaining) {
		t.Fatalf("queue accounting drifted: stored=%d removed=%d remaining=%d, want %d", stored.Load(), removed.Load(), remaining, got)
	}
	if gotBytes != wantBytes {
		t.Fatalf("mailbox memory counter = %d bytes, want %d (queue held %d messages)", gotBytes, wantBytes, remaining)
	}
}

// TestRequeueActiveSubAgentMailboxSkipsClaimedMessages pins the P3-1 fix: the
// active-batch requeue runs in one critical section over the live batch, so a
// message already claimed by the manual-delivery path
// (takeOutstandingMailboxForSub) is not reinserted into the queue from a stale
// snapshot. Reinserting it would deliver the same message twice — once to the
// main turn whose teardown is requeueing it, and once more after the claiming
// manual delivery replies.
func TestRequeueActiveSubAgentMailboxSkipsClaimedMessages(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "task-1") // instance ID worker-1
	m1 := &SubAgentMailboxMessage{MessageID: "m1", AgentID: "worker-1", TaskID: "task-1", Kind: SubAgentMailboxKindDecisionRequired, Priority: SubAgentMailboxPriorityInterrupt, Summary: "one"}
	m2 := &SubAgentMailboxMessage{MessageID: "m2", AgentID: "worker-2", TaskID: "task-2", Kind: SubAgentMailboxKindDecisionRequired, Priority: SubAgentMailboxPriorityInterrupt, Summary: "two"}
	a.activeSubAgentMailboxes = []*SubAgentMailboxMessage{m1, m2}
	a.activeSubAgentMailbox = m1
	a.pendingSubAgentMailboxes = []*SubAgentMailboxMessage{m1, m2}
	a.activeSubAgentMailboxAck = false

	// The manual-delivery path claims m1 for the worker's follow-up reply.
	if got := a.takeOutstandingMailboxForSub(sub); got != m1 {
		t.Fatalf("takeOutstandingMailboxForSub() = %#v, want m1", got)
	}

	a.requeueActiveSubAgentMailbox()

	queued := append([]SubAgentMailboxMessage{}, a.subAgentInbox.urgent...)
	queued = append(queued, a.subAgentInbox.normal...)
	if len(queued) != 1 || queued[0].MessageID != "m2" {
		t.Fatalf("requeued mailbox queue = %#v, want only m2 (m1 was claimed)", queued)
	}
	if len(a.subAgentInbox.progress) != 0 {
		t.Fatalf("claimed m1 was requeued into the progress map: %#v", a.subAgentInbox.progress)
	}
	if got, want := a.subAgentInbox.memoryBytes, mailboxMessageBytes(*m2); got != want {
		t.Fatalf("mailbox memory counter = %d, want %d (only m2 requeued)", got, want)
	}
}

// TestTurnContinuationStagingRequeuesQueueResidentProgressHead pins the P3-2
// gate on the queue-head shape: a progress message dequeued as the batch head
// mid-turn must return to the per-agent snapshot map instead of being
// delivered, matching the turn==nil gate applied to the snapshot claims and to
// the completed-batch inner loop.
func TestTurnContinuationStagingRequeuesQueueResidentProgressHead(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.subAgentInbox.urgent = []SubAgentMailboxMessage{{
		MessageID: "p-1",
		AgentID:   "worker-1",
		TaskID:    "task-a",
		Kind:      SubAgentMailboxKindProgress,
		Summary:   "still working",
	}}
	a.newTurn()
	if a.currentTurn() == nil {
		t.Fatal("active turn was not created")
	}

	if a.prepareSubAgentMailboxBatchForTurnContinuation() {
		t.Fatal("mid-turn staging delivered a progress queue head")
	}
	if len(a.pendingSubAgentMailboxes) != 0 || len(a.activeSubAgentMailboxes) != 0 {
		t.Fatalf("batch staged mid-turn: pending=%v active=%v", a.pendingSubAgentMailboxes, a.activeSubAgentMailboxes)
	}
	if got := a.subAgentInbox.progress["worker-1"]; got.MessageID != "p-1" {
		t.Fatalf("progress snapshot = %#v, want the queue head requeued into the snapshot map", got)
	}
	if len(a.subAgentInbox.urgent) != 0 {
		t.Fatalf("progress queue head not consumed from the queue: %#v", a.subAgentInbox.urgent)
	}
}
