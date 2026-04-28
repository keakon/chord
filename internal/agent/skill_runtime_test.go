package agent

import (
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/skill"
)

func TestRebuildInvokedSkillsFromMessagesRestoresVisibleAndCachedSkills(t *testing.T) {
	msgs := []message.Message{
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "skill-1", Name: "Skill", Args: mustToolArgs(t, map[string]any{"name": "go-expert"})}}},
		{Role: "tool", ToolCallID: "skill-1", Content: "<skill>...</skill>"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "skill-2", Name: "Skill", Args: mustToolArgs(t, map[string]any{"name": "legacy-skill"})}}},
	}
	visible := []*skill.Meta{{Name: "go-expert", Description: "Go language development expert", Location: "/tmp/go-expert/SKILL.md", RootDir: "/tmp/go-expert"}}

	got := rebuildInvokedSkillsFromMessages(msgs, visible)
	if len(got) != 2 {
		t.Fatalf("len(rebuildInvokedSkillsFromMessages) = %d, want 2", len(got))
	}
	if got[0].Name != "go-expert" || !got[0].Invoked || !got[0].Discovered {
		t.Fatalf("first restored skill = %#v, want invoked discovered visible skill", got[0])
	}
	if got[1].Name != "legacy-skill" || !got[1].Invoked || got[1].Discovered {
		t.Fatalf("second restored skill = %#v, want invoked cached-only skill", got[1])
	}
}

func TestMainAgentListSkillsFiltersDeniedSkills(t *testing.T) {
	a := &MainAgent{}
	a.loadedSkills = []*skill.Meta{{Name: "go-expert", Description: "Go language development expert"}, {Name: "secret-skill", Description: "Hidden"}}
	a.ruleset = permission.Ruleset{{Permission: "Skill", Pattern: "*", Action: permission.ActionAllow}, {Permission: "Skill", Pattern: "secret-*", Action: permission.ActionDeny}}

	got := a.ListSkills()
	if len(got) != 1 {
		t.Fatalf("len(ListSkills) = %d, want 1", len(got))
	}
	if got[0].Name != "go-expert" {
		t.Fatalf("visible skill = %q, want go-expert", got[0].Name)
	}
}
