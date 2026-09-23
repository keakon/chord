package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/skill"
)

func writeSkillResourceFixture(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

func TestSkillResourceWarningBlock_Empty(t *testing.T) {
	if got := SkillResourceWarningBlock(nil); got != "" {
		t.Fatalf("empty entries should produce no block, got %q", got)
	}
	if got := SkillDeclaredResourceWarningBlock(t.TempDir(), nil); got != "" {
		t.Fatalf("no resources should produce no block, got %q", got)
	}
}

func TestSkillResourceWarningBlock_FormatsProblems(t *testing.T) {
	root := t.TempDir()
	writeSkillResourceFixture(t, root, "references/present.md", "content")
	block := SkillDeclaredResourceWarningBlock(root, []string{"references/present.md", "references/missing.md"})
	if !strings.Contains(block, SkillResourceWarningOpen) || !strings.Contains(block, SkillResourceWarningClose) {
		t.Fatalf("block should be delimited, got %q", block)
	}
	if !strings.Contains(block, "references/missing.md") {
		t.Fatalf("block should name the missing resource, got %q", block)
	}
	if strings.Contains(block, "references/present.md") {
		t.Fatalf("passing resources should not appear in the block, got %q", block)
	}
	lines := ExtractSkillResourceWarningLines(block)
	if len(lines) != 1 {
		t.Fatalf("lines = %q, want one", lines)
	}
	if summary := SkillResourceWarningSummary(block); !strings.Contains(summary, "1 issue") {
		t.Fatalf("summary = %q, want single-issue text", summary)
	}
}

func TestSkillToolExecute_PrependsResourceWarningBeforeBody(t *testing.T) {
	root := t.TempDir()
	writeSkillResourceFixture(t, root, "references/present.md", "content")
	body := "# Sample Skill\n\nBody text.\n"
	tool := NewSkillTool(skillProviderStub{
		list: []*skill.Meta{{Name: "sample", Description: "Sample", Location: filepath.Join(root, "SKILL.md"), RootDir: root, Discovered: true}},
		loaded: map[string]*skill.Skill{
			"sample": {
				Meta: skill.Meta{
					Name: "sample", Description: "Sample",
					Location: filepath.Join(root, "SKILL.md"), RootDir: root,
					Resources: []string{"references/missing.md"},
				},
				Content: body,
			},
		},
	})
	got, err := tool.Execute(context.Background(), []byte(`{"name":"sample"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	warningIdx := strings.Index(got, SkillResourceWarningOpen)
	bodyIdx := strings.Index(got, body)
	if warningIdx < 0 {
		t.Fatalf("output should contain a warning block, got %q", got)
	}
	if bodyIdx < 0 {
		t.Fatalf("output should preserve the body, got %q", got)
	}
	if warningIdx > bodyIdx {
		t.Fatalf("warning should precede the body, got %q", got)
	}
	clean := StripSkillResourceWarningBlock(got)
	if !strings.Contains(clean, body) {
		t.Fatalf("stripped output should keep the body, got %q", clean)
	}
	if len(ExtractSkillResourceWarningLines(clean)) != 0 {
		t.Fatalf("stripped output should carry no warning, got %q", clean)
	}
}

func TestSkillToolExecute_NoWarningWhenResourcesHealthy(t *testing.T) {
	root := t.TempDir()
	writeSkillResourceFixture(t, root, "references/present.md", "content")
	body := "# Sample Skill\n\nBody text.\n"
	tool := NewSkillTool(skillProviderStub{
		list: []*skill.Meta{{Name: "sample", Description: "Sample", Location: filepath.Join(root, "SKILL.md"), RootDir: root, Discovered: true}},
		loaded: map[string]*skill.Skill{
			"sample": {
				Meta: skill.Meta{
					Name: "sample", Description: "Sample",
					Location: filepath.Join(root, "SKILL.md"), RootDir: root,
					Resources: []string{"references/present.md"},
				},
				Content: body,
			},
		},
	})
	got, err := tool.Execute(context.Background(), []byte(`{"name":"sample"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(ExtractSkillResourceWarningLines(got)) != 0 {
		t.Fatalf("healthy resources should produce no warning, got %q", got)
	}
	if !strings.Contains(got, body) {
		t.Fatalf("output should preserve the body, got %q", got)
	}
}

func TestFormatSkillBodyForDisplay(t *testing.T) {
	body := SkillResourceWarningOpen + "\n- references/missing.md: resource_missing: gone\n" + SkillResourceWarningClose + "\n\n# Title\n"
	formatted := FormatSkillBodyForDisplay(body)
	if strings.Contains(formatted, SkillResourceWarningOpen) {
		t.Fatalf("display body should not leak raw tags, got %q", formatted)
	}
	if !strings.Contains(formatted, "Skill resources:") || !strings.Contains(formatted, "references/missing.md") || !strings.Contains(formatted, "# Title") {
		t.Fatalf("display body should show the warning and the clean body, got %q", formatted)
	}
	if got := FormatSkillBodyForDisplay("# Plain\n"); got != "# Plain\n" {
		t.Fatalf("bodies without warnings should pass through, got %q", got)
	}
}
