package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tools"
)

func skillCardResult(path, body string) string {
	return "<skill>\n<name>sample</name>\n<path>" + path + "</path>\n<root>/tmp/sample</root>\n<notes>notes</notes>\n\n" + body + "\n</skill>"
}

func TestSkillCardWithoutWarningUnchanged(t *testing.T) {
	result := skillCardResult("/tmp/sample/SKILL.md", "# Title\n")
	collapsed := toolCollapsedResultContent(tools.NameSkill, result)
	if strings.Contains(collapsed, "resources:") {
		t.Fatalf("healthy skill should show no resource marker, got %q", collapsed)
	}
	expanded := toolExpandedResultContent(tools.NameSkill, result)
	if !strings.Contains(expanded, "# Title") {
		t.Fatalf("expanded should keep the body, got %q", expanded)
	}
}

func TestSkillCardShowsResourceWarning(t *testing.T) {
	warning := tools.SkillResourceWarningOpen + "\n- references/missing.md: resource_missing: gone\n" + tools.SkillResourceWarningClose
	result := skillCardResult("/tmp/sample/SKILL.md", warning+"\n\n# Title\n")
	collapsed := toolCollapsedResultContent(tools.NameSkill, result)
	if !strings.Contains(collapsed, "resources: 1 issue") {
		t.Fatalf("collapsed should mark the resource warning, got %q", collapsed)
	}
	if strings.Contains(collapsed, "# Title") {
		t.Fatalf("collapsed should still hide the body, got %q", collapsed)
	}
	expanded := toolExpandedResultContent(tools.NameSkill, result)
	if !strings.Contains(expanded, "Skill resources:") || !strings.Contains(expanded, "references/missing.md") {
		t.Fatalf("expanded should show the warning section, got %q", expanded)
	}
	if !strings.Contains(expanded, "# Title") {
		t.Fatalf("expanded should keep the body, got %q", expanded)
	}
	if strings.Contains(expanded, tools.SkillResourceWarningOpen) {
		t.Fatalf("expanded should not leak raw warning tags, got %q", expanded)
	}
}
