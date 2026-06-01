package agent

import (
	"context"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestSubAgentHidesDelegationControlToolsFromBaseRegistry(t *testing.T) {
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
		Ruleset: permission.Ruleset{
			{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
			{Permission: "delegate", Pattern: "*", Action: permission.ActionDeny},
			{Permission: "notify", Pattern: "*", Action: permission.ActionAllow},
		},
		Depth:      1,
		Delegation: config.DelegationConfig{MaxDepth: 2},
		WorkDir:    t.TempDir(),
		SessionDir: parent.sessionDir,
		ModelName:  "test-model",
	})

	if _, ok := sub.tools.Get("cancel"); ok {
		t.Fatal("sub.tools should hide Cancel when Delegate is denied")
	}
	tool, ok := sub.tools.Get("notify")
	if !ok {
		t.Fatal("sub.tools should retain owner-only Notify when Delegate is denied")
	}
	params := tool.Parameters()
	properties, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("Notify properties type = %T", params["properties"])
	}
	if _, ok := properties["target_task_id"]; ok {
		t.Fatalf("owner-only Notify should not expose target_task_id when Delegate is denied: %#v", properties)
	}
}
