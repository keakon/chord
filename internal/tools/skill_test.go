package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/skill"
)

type skillProviderStub struct {
	list   []*skill.Meta
	loaded map[string]*skill.Skill
}

func (s skillProviderStub) ListSkills() []*skill.Meta {
	return s.list
}

func (s skillProviderStub) LoadSkill(name string) (*skill.Skill, error) {
	if sk, ok := s.loaded[name]; ok {
		return sk, nil
	}
	return nil, context.Canceled
}

func (s skillProviderStub) InvokedSkills() []*skill.Meta {
	return nil
}

func (s skillProviderStub) MarkSkillInvoked(meta *skill.Meta) {}

func TestSkillToolDescriptionForToolsListsAvailableSkills(t *testing.T) {
	tool := NewSkillTool(skillProviderStub{
		list: []*skill.Meta{{Name: "go-expert", Description: "Go language development expert", Discovered: true}},
	})

	desc := tool.DescriptionForTools(nil)
	for _, want := range []string{
		"Load a skill's full instructions on demand",
		"## Available Skills",
		"go-expert",
		"Go language development expert",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description missing %q in %q", want, desc)
		}
	}
}

func TestSkillToolExecuteSubstitutesSkillPlaceholders(t *testing.T) {
	tool := NewSkillTool(skillProviderStub{
		list: []*skill.Meta{{Name: "go-expert", Description: "Go language development expert", Location: "/tmp/go-expert/SKILL.md", RootDir: "/tmp/go-expert", Discovered: true}},
		loaded: map[string]*skill.Skill{
			"go-expert": {
				Meta: skill.Meta{
					Name:        "go-expert",
					Description: "Go language development expert",
					Location:    "/tmp/go-expert/SKILL.md",
					RootDir:     "/tmp/go-expert",
				},
				Content: "Run `${CHORD_SKILL_DIR}/scripts/check.sh` with `${CHORD_SKILL_ARGS}`.",
			},
		},
	})

	got, err := tool.Execute(context.Background(), []byte(`{"name":"go-expert","args":"--fast"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{
		"<name>go-expert</name>",
		"<path>/tmp/go-expert/SKILL.md</path>",
		"<root>/tmp/go-expert</root>",
		"<relative_paths_base>/tmp/go-expert</relative_paths_base>",
		"<args>--fast</args>",
		"<notes>Relative paths from the skill content resolve against <root>. Read referenced files only when needed; do not guess other entry points if the skill already provides one.</notes>",
		"/tmp/go-expert/scripts/check.sh",
		"--fast",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "${CHORD_SKILL_DIR}") || strings.Contains(got, "${CHORD_SKILL_ARGS}") {
		t.Fatalf("placeholders should be substituted, got %q", got)
	}
}

func TestSkillToolDescriptionTruncatesLongDescription(t *testing.T) {
	longDesc := strings.Repeat("A", 300)
	tool := NewSkillTool(skillProviderStub{
		list: []*skill.Meta{{Name: "long-skill", Description: longDesc, Discovered: true}},
	})

	desc := tool.DescriptionForTools(nil)
	if !strings.Contains(desc, "long-skill") {
		t.Fatal("skill name should be present")
	}
	if strings.Contains(desc, strings.Repeat("A", 200)) {
		t.Fatal("description should be truncated")
	}
	if !strings.Contains(desc, strings.Repeat("A", 157)+"...") {
		t.Fatalf("expected truncated description ending with ..., got:\n%s", desc)
	}
}

func TestSkillToolDescriptionCapsAt32Entries(t *testing.T) {
	list := make([]*skill.Meta, 40)
	for i := range list {
		list[i] = &skill.Meta{
			Name:        fmt.Sprintf("skill-%02d", i),
			Description: fmt.Sprintf("Description for skill %d", i),
			Discovered:  true,
		}
	}
	tool := NewSkillTool(skillProviderStub{list: list})

	desc := tool.DescriptionForTools(nil)
	if !strings.Contains(desc, "+8 more skills available") {
		t.Fatalf("expected overflow summary, got:\n%s", desc)
	}
	// The first 32 skills should be shown.
	if !strings.Contains(desc, "skill-00") {
		t.Fatal("first skill should be listed")
	}
	if !strings.Contains(desc, "skill-31") {
		t.Fatal("32nd skill should be listed")
	}
	if strings.Contains(desc, "skill-32") {
		t.Fatal("33rd skill should NOT be listed")
	}
}

func TestSkillToolDescriptionRespectsTotalBudget(t *testing.T) {
	// Create skills with very long descriptions so total exceeds 4000 chars.
	list := make([]*skill.Meta, 100)
	for i := range list {
		list[i] = &skill.Meta{
			Name:        fmt.Sprintf("skill-%03d", i),
			Description: strings.Repeat("X", 200), // each truncated to 160 chars
			Discovered:  true,
		}
	}
	tool := NewSkillTool(skillProviderStub{list: list})

	desc := tool.DescriptionForTools(nil)
	// The Available Skills listing part should not exceed budget + header overhead.
	headerIdx := strings.Index(desc, "## Available Skills\n")
	if headerIdx < 0 {
		t.Fatal("missing Available Skills header")
	}
	listingPart := desc[headerIdx:]
	if len(listingPart) > SkillListingMaxTotal+100 { // small tolerance for header text
		t.Fatalf("listing section too large: %d chars", len(listingPart))
	}
}

func TestSkillToolDescriptionSkipsUndiscovered(t *testing.T) {
	tool := NewSkillTool(skillProviderStub{
		list: []*skill.Meta{
			{Name: "visible", Description: "Visible skill", Discovered: true},
			{Name: "hidden", Description: "Hidden skill", Discovered: false},
		},
	})
	desc := tool.DescriptionForTools(nil)
	if !strings.Contains(desc, "visible") {
		t.Fatal("visible skill should be listed")
	}
	if strings.Contains(desc, "hidden") {
		t.Fatal("undiscovered skill should NOT be listed")
	}
}

func TestSkillToolDescriptionNoSkillsAvailable(t *testing.T) {
	tool := NewSkillTool(skillProviderStub{list: nil})
	desc := tool.DescriptionForTools(nil)
	if !strings.Contains(desc, "No skills are currently available") {
		t.Fatal("should report no skills available")
	}
}
