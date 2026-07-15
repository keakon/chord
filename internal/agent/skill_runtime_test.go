package agent

import (
	"slices"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/skill"
)

func TestRebuildInvokedSkillsFromMessagesRestoresOnlySuccessfulSkills(t *testing.T) {
	msgs := []message.Message{
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "skill-1", Name: "skill", Args: mustToolArgs(t, map[string]any{"name": "go-expert"})}}},
		{Role: "tool", ToolCallID: "skill-1", Content: "<skill>...</skill>"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "skill-2", Name: "skill", Args: mustToolArgs(t, map[string]any{"name": "legacy-skill"})}}},
		{Role: "tool", ToolCallID: "skill-2", Content: "Error: skill \"legacy-skill\" not found"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "skill-3", Name: "skill", Args: mustToolArgs(t, map[string]any{"name": "later-discovered"})}}},
		{Role: "tool", ToolCallID: "skill-3", Content: "<skill>...</skill>"},
	}
	visible := []*skill.Meta{
		{Name: "go-expert", Description: "Go language development expert", Location: "/tmp/go-expert/SKILL.md", RootDir: "/tmp/go-expert"},
	}

	got := rebuildInvokedSkillsFromMessages(msgs, visible)
	if len(got) != 2 {
		t.Fatalf("len(rebuildInvokedSkillsFromMessages) = %d, want 2", len(got))
	}
	if got[0].Name != "go-expert" || !got[0].Invoked || !got[0].Discovered {
		t.Fatalf("first restored skill = %#v, want successful visible skill", got[0])
	}
	if got[1].Name != "later-discovered" || !got[1].Invoked || got[1].Discovered {
		t.Fatalf("second restored skill = %#v, want successful pending-discovery skill", got[1])
	}
}

func TestSetSkillsHydratesRestoredInvokedSkillMetadata(t *testing.T) {
	a := &MainAgent{invokedSkills: map[string]*skill.Meta{"go-expert": {Name: "go-expert", Invoked: true}}}
	a.SetSkills([]*skill.Meta{{Name: "go-expert", Description: "Go language development expert"}})

	got := a.InvokedSkills()
	if len(got) != 1 {
		t.Fatalf("InvokedSkills() after rediscovery = %#v, want 1", got)
	}
	if got[0].Name != "go-expert" || !got[0].Discovered || got[0].Description != "Go language development expert" {
		t.Fatalf("rediscovered invoked skill = %#v, want discovered go-expert with metadata", got[0])
	}
}

func TestMainAgentListSkillsFiltersDeniedSkills(t *testing.T) {
	a := &MainAgent{}
	a.loadedSkills = []*skill.Meta{{Name: "go-expert", Description: "Go language development expert"}, {Name: "secret-skill", Description: "Hidden"}}
	a.ruleset = permission.Ruleset{{Permission: "skill", Pattern: "*", Action: permission.ActionAllow}, {Permission: "skill", Pattern: "secret-*", Action: permission.ActionDeny}}

	got := a.ListSkills()
	if len(got) != 1 {
		t.Fatalf("len(ListSkills) = %d, want 1", len(got))
	}
	if got[0].Name != "go-expert" {
		t.Fatalf("visible skill = %q, want go-expert", got[0].Name)
	}
}

func TestMainAgentFocusedSkillsUsesFocusedSubAgentPermissions(t *testing.T) {
	a := &MainAgent{subs: newSubAgentRegistry()}
	a.loadedSkills = []*skill.Meta{{Name: "go-expert", Description: "Go language development expert"}}
	a.ruleset = permission.Ruleset{{Permission: "skill", Pattern: "*", Action: permission.ActionDeny}}
	sub := &SubAgent{
		instanceID:   "agent-1",
		loadedSkills: a.loadedSkillsSnapshot(),
		ruleset:      permission.Ruleset{{Permission: "skill", Pattern: "*", Action: permission.ActionAllow}},
	}
	a.subs.subAgents[sub.instanceID] = sub

	if got := a.ListSkills(); len(got) != 0 {
		t.Fatalf("main ListSkills() = %#v, want none while skill is denied", got)
	}
	a.SwitchFocus(sub.instanceID)
	got := a.FocusedSkills()
	if len(got) != 1 || got[0].Name != "go-expert" {
		t.Fatalf("FocusedSkills() = %#v, want focused subagent skill", got)
	}

	a.SwitchFocus("main")
	if got := a.FocusedSkills(); len(got) != 0 {
		t.Fatalf("FocusedSkills() after returning to main = %#v, want none", got)
	}
}

func TestMainAgentInvokedSkillsForFocusedSubAgentDoesNotReenterFocusedRouter(t *testing.T) {
	a := &MainAgent{subs: newSubAgentRegistry()}
	a.loadedSkills = []*skill.Meta{{Name: "visible"}, {Name: "hidden"}}
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"explorer": {
			Name:       "explorer",
			Mode:       config.AgentModeSubAgent,
			Permission: parsePermissionNode(t, "skill:\n  '*': allow\n  hidden: deny\n"),
		},
	})
	a.MarkSkillInvoked(&skill.Meta{Name: "visible"})
	a.MarkSkillInvoked(&skill.Meta{Name: "hidden"})
	sub := &SubAgent{
		parent:       a,
		instanceID:   "agent-1",
		agentDefName: "explorer",
		loadedSkills: a.loadedSkillsSnapshot(),
		invokedSkills: map[string]*skill.Meta{
			"visible": {Name: "visible", Invoked: true},
			"hidden":  {Name: "hidden", Invoked: true},
		},
		ruleset: permission.Ruleset{
			{Permission: "skill", Pattern: "*", Action: permission.ActionAllow},
			{Permission: "skill", Pattern: "hidden", Action: permission.ActionDeny},
		},
	}
	a.subs.subAgents[sub.instanceID] = sub
	a.SwitchFocus(sub.instanceID)

	got := a.InvokedSkills()
	if len(got) != 1 || got[0].Name != "visible" {
		t.Fatalf("focused InvokedSkills() = %#v, want visible skill only", got)
	}
}

func TestSubAgentSkillsUseDynamicWorkspaceCatalogAndIsolatedInvocations(t *testing.T) {
	a := &MainAgent{subs: newSubAgentRegistry(), invokedSkills: make(map[string]*skill.Meta)}
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"explorer": {
			Name:       "explorer",
			Mode:       config.AgentModeSubAgent,
			Permission: parsePermissionNode(t, "skill:\n  '*': allow\n  denied: deny\n"),
		},
	})
	a.SetSkills([]*skill.Meta{{Name: "initial"}})
	sub := &SubAgent{
		parent:        a,
		instanceID:    "agent-1",
		agentDefName:  "explorer",
		invokedSkills: make(map[string]*skill.Meta),
	}
	a.subs.subAgents[sub.instanceID] = sub

	a.SetSkills([]*skill.Meta{{Name: "initial"}, {Name: "later"}, {Name: "denied"}})
	visible := sub.ListSkills()
	if len(visible) != 2 || visible[0].Name != "initial" || visible[1].Name != "later" {
		t.Fatalf("dynamic subagent skills = %#v, want initial and later", visible)
	}

	a.MarkSkillInvoked(&skill.Meta{Name: "initial"})
	if got := sub.InvokedSkills(); len(got) != 0 {
		t.Fatalf("main invocation leaked into subagent: %#v", got)
	}
	sub.MarkSkillInvoked(&skill.Meta{Name: "later"})
	if got := a.invokedSkillsSnapshot(); len(got) != 1 || got[0].Name != "initial" {
		t.Fatalf("subagent invocation polluted main: %#v", got)
	}
	if got := sub.InvokedSkills(); len(got) != 1 || got[0].Name != "later" {
		t.Fatalf("subagent invocation = %#v, want later only", got)
	}
}

func TestSubAgentRestoreMessagesRebuildsOwnInvokedSkills(t *testing.T) {
	a := &MainAgent{}
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"explorer": {
			Name:       "explorer",
			Mode:       config.AgentModeSubAgent,
			Permission: parsePermissionNode(t, "skill:\n  '*': allow\n"),
		},
	})
	a.SetSkills([]*skill.Meta{{Name: "sub-skill"}})
	sub := &SubAgent{
		parent:        a,
		agentDefName:  "explorer",
		ctxMgr:        ctxmgr.NewManager(0, 0),
		invokedSkills: make(map[string]*skill.Meta),
	}
	msgs := []message.Message{
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "skill-1", Name: "skill", Args: mustToolArgs(t, map[string]any{"name": "sub-skill"})}}},
		{Role: "tool", ToolCallID: "skill-1", Content: "<skill>...</skill>"},
	}

	sub.RestoreMessages(msgs)

	if got := sub.InvokedSkills(); len(got) != 1 || got[0].Name != "sub-skill" {
		t.Fatalf("restored subagent invoked skills = %#v", got)
	}
	if got := a.invokedSkillsSnapshot(); len(got) != 0 {
		t.Fatalf("restored subagent invocation polluted main: %#v", got)
	}
}

func TestParkedSubAgentSkillsUseLatestConfigAndTaskInvocations(t *testing.T) {
	a := &MainAgent{subs: newSubAgentRegistry(), invokedSkills: make(map[string]*skill.Meta)}
	a.SetSkills([]*skill.Meta{{Name: "visible"}, {Name: "denied"}})
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"explorer": {
			Name:       "explorer",
			Mode:       config.AgentModeSubAgent,
			Permission: parsePermissionNode(t, "skill:\n  '*': allow\n  denied: deny\n"),
		},
	})
	a.subs.taskRecords["adhoc-1"] = &DurableTaskRecord{
		TaskID:            "adhoc-1",
		AgentDefName:      "explorer",
		LatestInstanceID:  "explorer-6",
		RuntimeParked:     true,
		InvokedSkillNames: []string{"visible", "denied"},
	}
	a.MarkSkillInvoked(&skill.Meta{Name: "denied"})
	a.SwitchFocus("explorer-6")

	visible := a.FocusedSkills()
	if len(visible) != 1 || visible[0].Name != "visible" {
		t.Fatalf("parked visible skills = %#v", visible)
	}
	invoked := a.InvokedSkills()
	if len(invoked) != 1 || invoked[0].Name != "visible" {
		t.Fatalf("parked invoked skills = %#v", invoked)
	}
}

func TestDurableTaskInvokedSkillNamesCloneAndMerge(t *testing.T) {
	original := &DurableTaskRecord{TaskID: "task-1", InvokedSkillNames: []string{"beta", "alpha"}}
	clone := cloneDurableTaskRecord(original)
	clone.InvokedSkillNames[0] = "changed"
	if original.InvokedSkillNames[0] != "beta" {
		t.Fatalf("clone mutated original names: %#v", original.InvokedSkillNames)
	}

	merged := mergeDurableTaskRecords(
		map[string]*DurableTaskRecord{"task-1": {TaskID: "task-1", InvokedSkillNames: []string{"alpha"}}},
		map[string]*DurableTaskRecord{"task-1": {TaskID: "task-1", InvokedSkillNames: []string{"beta", "alpha"}}},
	)
	if got := merged["task-1"].InvokedSkillNames; !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Fatalf("merged invoked names = %#v", got)
	}
}
