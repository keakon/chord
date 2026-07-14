package agent

import (
	"testing"

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
