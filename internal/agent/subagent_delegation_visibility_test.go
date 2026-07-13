package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestSubAgentHidesDelegationControlToolsFromBaseRegistry(t *testing.T) {
	parent := newTestMainAgent(t, t.TempDir())
	parent.SetAgentConfigs(map[string]*config.AgentConfig{
		"reviewer": {Name: "reviewer", Mode: config.AgentModeSubAgent},
	})
	reg := tools.NewRegistry()
	reg.Register(tools.NewDelegateTool(parent))
	reg.Register(tools.NewNotifyTool(nil, parent, false, true))
	reg.Register(tools.NewCancelTool(parent))
	reg.Register(tools.ReadTool{})

	sub := NewSubAgent(SubAgentConfig{
		InstanceID:   "worker-1",
		TaskID:       "adhoc-1",
		AgentDefName: "worker",
		TaskDesc:     "inspect code",
		LLMClient:    newTestLLMClient(),
		Recovery:     parent.recovery,
		Parent:       parent,
		ParentCtx:    context.Background(),
		Cancel:       func() {},
		BaseTools:    reg,
		Depth:        1,
		Delegation:   config.DelegationConfig{MaxDepth: 1},
		WorkDir:      t.TempDir(),
		SessionDir:   parent.sessionDir,
		ModelName:    "test-model",
	})

	for _, name := range []string{"delegate", "cancel"} {
		if _, ok := sub.tools.Get(name); ok {
			t.Fatalf("sub.tools unexpectedly exposes %q while nested delegation is hard-disabled", name)
		}
	}
	tool, ok := sub.tools.Get("notify")
	if !ok {
		t.Fatal("sub.tools should still expose owner-only Notify when delegate family is hidden")
	}
	notify, ok := tool.(*tools.NotifyTool)
	if !ok || !notify.VisibleWithRuleset(nil) {
		t.Fatalf("sub.tools should still expose owner-only Notify when delegate family is hidden, got %T", tool)
	}
	if _, ok := sub.tools.Get("read"); !ok {
		t.Fatal("sub.tools should still expose normal non-delegation tools")
	}
}

func TestSubAgentShowsDelegationControlToolsWhenDepthAllows(t *testing.T) {
	parent := newTestMainAgent(t, t.TempDir())
	parent.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder": {Name: "builder", Mode: config.AgentModeMain},
		"worker":  {Name: "worker", Mode: config.AgentModeSubAgent},
	})
	reg := tools.NewRegistry()
	reg.Register(tools.NewDelegateTool(taskCreatorStub{agents: []tools.AgentInfo{{Name: "builder", Description: "General coding"}}}))
	reg.Register(tools.NewNotifyTool(nil, parent, false, true))
	reg.Register(tools.NewCancelTool(parent))

	sub := NewSubAgent(SubAgentConfig{
		InstanceID:   "worker-1",
		TaskID:       "adhoc-1",
		AgentDefName: "worker",
		TaskDesc:     "inspect code",
		LLMClient:    newTestLLMClient(),
		Recovery:     parent.recovery,
		Parent:       parent,
		ParentCtx:    context.Background(),
		Cancel:       func() {},
		BaseTools:    reg,
		Depth:        1,
		Delegation:   config.DelegationConfig{MaxDepth: 2},
		WorkDir:      t.TempDir(),
		SessionDir:   parent.sessionDir,
		ModelName:    "test-model",
	})

	for _, name := range []string{"delegate", "notify", "cancel"} {
		if _, ok := sub.tools.Get(name); !ok {
			t.Fatalf("sub.tools missing %q when depth allows nested delegation", name)
		}
	}
}

func TestSubAgentDelegateTargetsUseSubAgentRuleset(t *testing.T) {
	parent := newTestMainAgent(t, t.TempDir())
	parent.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder":  {Name: "builder", Mode: config.AgentModeMain},
		"reviewer": {Name: "reviewer", Mode: config.AgentModeSubAgent},
		"tester":   {Name: "tester", Mode: config.AgentModeSubAgent},
	})
	reg := tools.NewRegistry()
	reg.Register(tools.NewDelegateTool(parent))
	reg.Register(tools.NewNotifyTool(nil, parent, false, true))
	reg.Register(tools.NewCancelTool(parent))

	sub := NewSubAgent(SubAgentConfig{
		InstanceID:   "worker-1",
		TaskID:       "adhoc-1",
		AgentDefName: "worker",
		TaskDesc:     "inspect code",
		LLMClient:    newTestLLMClient(),
		Recovery:     parent.recovery,
		Parent:       parent,
		ParentCtx:    context.Background(),
		Cancel:       func() {},
		BaseTools:    reg,
		Ruleset: permission.Ruleset{
			{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
			{Permission: tools.NameDelegate, Pattern: "*", Action: permission.ActionDeny},
			{Permission: tools.NameDelegate, Pattern: "reviewer", Action: permission.ActionAllow},
		},
		Depth:      1,
		Delegation: config.DelegationConfig{MaxDepth: 2},
		WorkDir:    t.TempDir(),
		SessionDir: parent.sessionDir,
		ModelName:  "test-model",
	})

	tool, ok := sub.tools.Get(tools.NameDelegate)
	if !ok {
		t.Fatal("sub.tools should expose Delegate when one target is allowed")
	}
	properties := tool.Parameters()["properties"].(map[string]any)
	agentType := properties["agent_type"].(map[string]any)
	enum := agentType["enum"].([]string)
	if len(enum) != 1 || enum[0] != "reviewer" {
		t.Fatalf("nested Delegate agent_type enum = %v, want [reviewer]", enum)
	}
	prompt := sub.delegationPromptBlock()
	if !strings.Contains(prompt, "**reviewer**") || strings.Contains(prompt, "**tester**") {
		t.Fatalf("nested delegation prompt does not match filtered targets: %q", prompt)
	}
}

func TestSubAgentDisableStarKeepsOnlyCompleteAsInternalControlTool(t *testing.T) {
	parent := newTestMainAgent(t, t.TempDir())
	reg := tools.NewRegistry()
	reg.Register(tools.NewDelegateTool(taskCreatorStub{agents: []tools.AgentInfo{{Name: "builder", Description: "General coding"}}}))
	reg.Register(tools.NewNotifyTool(nil, parent, false, true))
	reg.Register(tools.NewCancelTool(parent))
	reg.Register(tools.ReadTool{})

	sub := NewSubAgent(SubAgentConfig{
		InstanceID:   "worker-1",
		TaskID:       "adhoc-1",
		AgentDefName: "worker",
		TaskDesc:     "inspect code",
		LLMClient:    newTestLLMClient(),
		Recovery:     parent.recovery,
		Parent:       parent,
		ParentCtx:    context.Background(),
		Cancel:       func() {},
		BaseTools:    reg,
		Ruleset: permission.Ruleset{{
			Permission: "*",
			Pattern:    "*",
			Action:     permission.ActionDeny,
		}},
		Depth:      1,
		Delegation: config.DelegationConfig{MaxDepth: 2},
		WorkDir:    t.TempDir(),
		SessionDir: parent.sessionDir,
		ModelName:  "test-model",
	})

	if _, ok := sub.tools.Get("complete"); !ok {
		t.Fatal("sub.tools should retain Complete even when disable=*")
	}
	visible := visibleLLMTools(sub.tools, sub.ruleset, isSubAgentInternalTool)
	foundComplete := false
	for _, tool := range visible {
		if tools.NormalizeName(tool.Name()) == tools.NameComplete {
			foundComplete = true
			break
		}
	}
	if !foundComplete {
		t.Fatal("visibleLLMTools should retain Complete even when disable=*")
	}
	for _, name := range []string{"delegate", "notify", "escalate", "cancel", "read"} {
		if _, ok := sub.tools.Get(name); ok {
			t.Fatalf("sub.tools unexpectedly exposes %q when disable=*", name)
		}
	}
}

func TestSubAgentNotifyTargetingDependsOnDelegatePermission(t *testing.T) {
	parent := newTestMainAgent(t, t.TempDir())
	parent.SetAgentConfigs(map[string]*config.AgentConfig{
		"reviewer": {Name: "reviewer", Mode: config.AgentModeSubAgent},
	})
	reg := tools.NewRegistry()
	reg.Register(tools.NewDelegateTool(parent))
	reg.Register(tools.NewNotifyTool(nil, parent, false, true))
	reg.Register(tools.NewCancelTool(parent))

	sub := NewSubAgent(SubAgentConfig{
		InstanceID:   "worker-1",
		TaskID:       "adhoc-1",
		AgentDefName: "worker",
		TaskDesc:     "inspect code",
		LLMClient:    newTestLLMClient(),
		Recovery:     parent.recovery,
		Parent:       parent,
		ParentCtx:    context.Background(),
		Cancel:       func() {},
		BaseTools:    reg,
		Ruleset: permission.Ruleset{
			{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
			{Permission: "delegate", Pattern: "*", Action: permission.ActionDeny},
			{Permission: "delegate", Pattern: "reviewer", Action: permission.ActionAllow},
			{Permission: "notify", Pattern: "*", Action: permission.ActionAllow},
		},
		Depth:      1,
		Delegation: config.DelegationConfig{MaxDepth: 2},
		WorkDir:    t.TempDir(),
		SessionDir: parent.sessionDir,
		ModelName:  "test-model",
	})

	if _, ok := sub.tools.Get("cancel"); !ok {
		t.Fatal("sub.tools should retain Cancel when a Delegate target is allowed")
	}
	tool, ok := sub.tools.Get("notify")
	if !ok {
		t.Fatal("sub.tools should retain Notify when a Delegate target is allowed")
	}
	params := tool.Parameters()
	properties, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("Notify properties type = %T", params["properties"])
	}
	if _, ok := properties["target_task_id"]; !ok {
		t.Fatalf("Notify should expose target_task_id when a Delegate target is allowed: %#v", properties)
	}
}
