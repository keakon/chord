package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ---------- parseFrontmatter tests ----------

func TestParseFrontmatter_Valid(t *testing.T) {
	input := `---
name: "go-expert"
description: "Go language development expert"
---
## Go Development Guidelines
- Follow Effective Go
`
	fm, body, err := parseFrontmatter(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fm.Name != "go-expert" {
		t.Errorf("name: got %q, want %q", fm.Name, "go-expert")
	}
	if fm.Description != "Go language development expert" {
		t.Errorf("description: got %q, want %q", fm.Description, "Go language development expert")
	}
	wantBody := "## Go Development Guidelines\n- Follow Effective Go\n"
	if body != wantBody {
		t.Errorf("body:\n got: %q\nwant: %q", body, wantBody)
	}
}

func TestParseFrontmatter_NoQuotes(t *testing.T) {
	input := `---
name: go-expert
description: Go language development expert
---
Body content.
`
	fm, body, err := parseFrontmatter(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fm.Name != "go-expert" {
		t.Errorf("name: got %q, want %q", fm.Name, "go-expert")
	}
	if fm.Description != "Go language development expert" {
		t.Errorf("description: got %q", fm.Description)
	}
	if body != "Body content.\n" {
		t.Errorf("body: got %q", body)
	}
}

func TestParseFrontmatter_NoFrontmatter(t *testing.T) {
	input := "Just some markdown content\n"
	fm, body, err := parseFrontmatter(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fm.Name != "" || fm.Description != "" {
		t.Errorf("expected empty frontmatter, got name=%q desc=%q", fm.Name, fm.Description)
	}
	if body != input {
		t.Errorf("body should be unchanged: got %q", body)
	}
}

func TestParseFrontmatter_NoClosingDelimiter(t *testing.T) {
	input := "---\nname: test\nno closing delimiter\n"
	fm, body, err := parseFrontmatter(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No closing delimiter → treated as no frontmatter.
	if fm.Name != "" {
		t.Errorf("expected empty frontmatter, got name=%q", fm.Name)
	}
	if body != input {
		t.Errorf("body should be unchanged")
	}
}

func TestParseFrontmatter_InvalidYAML(t *testing.T) {
	input := "---\n: invalid yaml [\n---\nbody\n"
	_, _, err := parseFrontmatter(input)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestParseFrontmatter_EmptyBody(t *testing.T) {
	input := "---\nname: test\ndescription: desc\n---\n"
	fm, body, err := parseFrontmatter(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fm.Name != "test" {
		t.Errorf("name: got %q", fm.Name)
	}
	if body != "" {
		t.Errorf("body should be empty, got %q", body)
	}
}

func TestParseFrontmatter_ExtraFields(t *testing.T) {
	// Extra fields in frontmatter are silently ignored.
	input := "---\nname: test\ndescription: desc\nauthor: someone\n---\nbody\n"
	fm, _, err := parseFrontmatter(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fm.Name != "test" {
		t.Errorf("name: got %q", fm.Name)
	}
}

// ---------- LoadSkill tests ----------

func TestLoadSkill_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	content := `---
name: "code-review"
description: "Code review best practices"
---
## Review Checklist
- Check error handling
- Verify test coverage
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	skill, err := LoadSkill(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skill.Name != "code-review" {
		t.Errorf("name: got %q", skill.Name)
	}
	if skill.Description != "Code review best practices" {
		t.Errorf("description: got %q", skill.Description)
	}
	wantContent := "## Review Checklist\n- Check error handling\n- Verify test coverage\n"
	if skill.Content != wantContent {
		t.Errorf("content:\n got: %q\nwant: %q", skill.Content, wantContent)
	}
	if skill.Location == "" {
		t.Error("location should not be empty")
	}
}

func TestLoadSkill_MissingName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	content := "---\ndescription: \"has desc but no name\"\n---\nbody\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadSkill(path)
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestLoadSkill_MissingDescription(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	content := "---\nname: \"has-name\"\n---\nbody\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadSkill(path)
	if err == nil {
		t.Fatal("expected error for missing description")
	}
}

func TestLoadSkill_NoFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	content := "Just plain markdown without frontmatter.\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadSkill(path)
	if err == nil {
		t.Fatal("expected error for missing name (no frontmatter)")
	}
}

func TestLoadSkill_FileNotFound(t *testing.T) {
	_, err := LoadSkill("/nonexistent/SKILL.md")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

// ---------- Loader.Scan tests ----------

func TestScan_SingleDirectory(t *testing.T) {
	dir := t.TempDir()
	createSkillFile(t, dir, "go-expert", "Go expert", "## Go rules\n")

	loader := NewLoader([]string{dir})
	skills, err := loader.ScanMeta()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Name != "go-expert" {
		t.Errorf("name: got %q", skills[0].Name)
	}
}

func TestScan_MultipleDirectories(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	createSkillFile(t, dir1, "skill-a", "Skill A", "Content A\n")
	createSkillFile(t, dir2, "skill-b", "Skill B", "Content B\n")

	loader := NewLoader([]string{dir1, dir2})
	skills, err := loader.ScanMeta()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(skills))
	}

	names := make(map[string]bool)
	for _, s := range skills {
		names[s.Name] = true
	}
	if !names["skill-a"] || !names["skill-b"] {
		t.Errorf("expected skill-a and skill-b, got %v", names)
	}
}

func TestScan_DeduplicateByName(t *testing.T) {
	// Two directories with the same skill name — first wins.
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	createSkillFile(t, dir1, "go-expert", "Go expert (project)", "Project content\n")
	createSkillFile(t, dir2, "go-expert", "Go expert (global)", "Global content\n")

	loader := NewLoader([]string{dir1, dir2})
	skills, err := loader.ScanMeta()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill (deduplicated), got %d", len(skills))
	}
	if skills[0].Description != "Go expert (project)" {
		t.Errorf("first occurrence should win; got description %q", skills[0].Description)
	}
	if skills[0].Location == "" {
		t.Error("location should not be empty")
	}
	if skills[0].RootDir == "" {
		t.Error("root dir should not be empty")
	}
}

func TestScan_NestedDirectories(t *testing.T) {
	dir := t.TempDir()
	// Create a nested skill: dir/nested/sub/SKILL.md
	nestedDir := filepath.Join(dir, "nested", "sub")
	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeSkillMD(t, filepath.Join(nestedDir, "SKILL.md"), "nested-skill", "Nested skill", "Nested content\n")

	loader := NewLoader([]string{dir})
	skills, err := loader.ScanMeta()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill from nested dir, got %d", len(skills))
	}
	if skills[0].Name != "nested-skill" {
		t.Errorf("name: got %q", skills[0].Name)
	}
}

func TestScan_NonexistentDirectory(t *testing.T) {
	loader := NewLoader([]string{"/nonexistent/directory"})
	skills, err := loader.ScanMeta()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(skills))
	}
}

func TestScan_EmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	loader := NewLoader([]string{dir})
	skills, err := loader.ScanMeta()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(skills))
	}
}

func TestScan_SkipsInvalidFiles(t *testing.T) {
	dir := t.TempDir()
	// Create one valid and one invalid skill.
	createSkillFile(t, dir, "valid-skill", "Valid skill", "Valid content\n")

	invalidDir := filepath.Join(dir, "broken")
	if err := os.MkdirAll(invalidDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Invalid: missing name.
	writeSkillMD(t, filepath.Join(invalidDir, "SKILL.md"), "", "Missing name", "Content\n")

	loader := NewLoader([]string{dir})
	skills, err := loader.ScanMeta()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 valid skill, got %d", len(skills))
	}
	if skills[0].Name != "valid-skill" {
		t.Errorf("name: got %q", skills[0].Name)
	}
}

func TestScan_ScanOrder(t *testing.T) {
	// Verify scan order: project → global → extra.
	projectDir := t.TempDir()
	globalDir := t.TempDir()
	extraDir := t.TempDir()

	createSkillFile(t, projectDir, "shared", "Project version", "Project\n")
	createSkillFile(t, globalDir, "shared", "Global version", "Global\n")
	createSkillFile(t, extraDir, "shared", "Extra version", "Extra\n")
	createSkillFile(t, globalDir, "global-only", "Global only", "Global only content\n")
	createSkillFile(t, extraDir, "extra-only", "Extra only", "Extra only content\n")

	loader := NewLoader([]string{projectDir, globalDir, extraDir})
	skills, err := loader.ScanMeta()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should get 3 unique skills: shared (project), global-only, extra-only.
	if len(skills) != 3 {
		t.Fatalf("expected 3 skills, got %d", len(skills))
	}

	byName := make(map[string]*Meta)
	for _, s := range skills {
		byName[s.Name] = s
	}

	// "shared" should be the project version.
	if s, ok := byName["shared"]; !ok {
		t.Error("missing 'shared' skill")
	} else if s.Description != "Project version" {
		t.Errorf("shared: expected project version, got %q", s.Description)
	}

	if _, ok := byName["global-only"]; !ok {
		t.Error("missing 'global-only' skill")
	}
	if _, ok := byName["extra-only"]; !ok {
		t.Error("missing 'extra-only' skill")
	}
}

func TestScan_NoDirs(t *testing.T) {
	loader := NewLoader(nil)
	skills, err := loader.ScanMeta()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(skills))
	}
}

func TestScan_MultipleSkillsSameDir(t *testing.T) {
	dir := t.TempDir()
	createSkillFile(t, dir, "skill-a", "Skill A", "A\n")
	createSkillFile(t, dir, "skill-b", "Skill B", "B\n")
	createSkillFile(t, dir, "skill-c", "Skill C", "C\n")

	loader := NewLoader([]string{dir})
	skills, err := loader.ScanMeta()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skills) != 3 {
		t.Fatalf("expected 3 skills, got %d", len(skills))
	}
}

func TestLoad_ByName(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	createSkillFile(t, dir1, "go-expert", "Go expert (project)", "Project content\n")
	createSkillFile(t, dir2, "go-expert", "Go expert (global)", "Global content\n")

	loader := NewLoader([]string{dir1, dir2})
	sk, err := loader.Load("go-expert")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if sk.Description != "Go expert (project)" {
		t.Fatalf("description = %q, want project version", sk.Description)
	}
	if sk.Content != "Project content\n" {
		t.Fatalf("content = %q, want project content", sk.Content)
	}
}

// ---------- Helpers ----------

// createSkillFile creates a skill directory with a SKILL.md inside the parent dir.
// The directory name is the skill name for filesystem organisation.
func createSkillFile(t *testing.T, parentDir, name, description, body string) {
	t.Helper()
	skillDir := filepath.Join(parentDir, name)
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", skillDir, err)
	}
	writeSkillMD(t, filepath.Join(skillDir, "SKILL.md"), name, description, body)
}

// writeSkillMD writes a SKILL.md file with frontmatter.
func writeSkillMD(t *testing.T, path, name, description, body string) {
	t.Helper()
	var content string
	if name != "" || description != "" {
		content = "---\n"
		if name != "" {
			content += "name: " + quote(name) + "\n"
		}
		if description != "" {
			content += "description: " + quote(description) + "\n"
		}
		content += "---\n"
	}
	content += body
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// quote wraps a string in double quotes for YAML.
func quote(s string) string {
	return `"` + s + `"`
}

// writeSkillMDWithFM writes a SKILL.md with arbitrary frontmatter fields.
func writeSkillMDWithFM(t *testing.T, path string, fm map[string]any, body string) {
	t.Helper()
	if fm == nil {
		fm = make(map[string]any)
	}
	data := "---\n"
	for k, v := range fm {
		switch val := v.(type) {
		case string:
			data += fmt.Sprintf("%s: %q\n", k, val)
		case []string:
			data += k + ":\n"
			for _, item := range val {
				data += fmt.Sprintf("  - %q\n", item)
			}
		default:
			data += fmt.Sprintf("%s: %v\n", k, v)
		}
	}
	data += "---\n"
	data += body
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// ---------- Frontmatter extensions ----------

func TestParseFrontmatter_WhenToUse(t *testing.T) {
	input := `---
name: "code-review"
description: "Code review"
when_to_use: "When reviewing pull requests"
---
Body
`
	fm, _, err := parseFrontmatter(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fm.WhenToUse != "When reviewing pull requests" {
		t.Errorf("when_to_use: got %q", fm.WhenToUse)
	}
}

func TestParseFrontmatter_ArgumentHint(t *testing.T) {
	input := `---
name: "test"
description: "Test skill"
argument_hint: "--flag=value"
---
Body
`
	fm, _, err := parseFrontmatter(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fm.ArgsHint != "--flag=value" {
		t.Errorf("argument_hint: got %q", fm.ArgsHint)
	}
}

func TestParseFrontmatter_ContextFork(t *testing.T) {
	input := `---
name: "fork-skill"
description: "Runs in fork context"
context: "fork"
---
Body
`
	fm, _, err := parseFrontmatter(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fm.Context != "fork" {
		t.Errorf("context: got %q, want 'fork'", fm.Context)
	}
}

func TestParseFrontmatter_ContextInline(t *testing.T) {
	input := `---
name: "inline-skill"
description: "Runs inline"
context: "inline"
---
Body
`
	fm, _, err := parseFrontmatter(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fm.Context != "inline" {
		t.Errorf("context: got %q, want 'inline'", fm.Context)
	}
}

func TestParseFrontmatter_ContextDefaultsToInline(t *testing.T) {
	input := `---
name: "default-skill"
description: "Default context"
---
Body
`
	fm, _, err := parseFrontmatter(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fm.Context != "" {
		t.Errorf("context: got %q, want empty", fm.Context)
	}
}

func TestNormalizeContext(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"fork", "fork"},
		{"Fork", "fork"},
		{"FORK", "fork"},
		{"fork ", "fork"},
		{"inline", "inline"},
		{"", "inline"},
		{"other", "inline"},
	}
	for _, tt := range tests {
		got := normalizeContext(tt.input)
		if got != tt.want {
			t.Errorf("normalizeContext(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseFrontmatter_AllowedTools(t *testing.T) {
	input := `---
name: "tool-skill"
description: "Skill with allowed tools"
allowed_tools:
  - "Bash"
  - "Read"
  - "Write"
---
Body
`
	fm, _, err := parseFrontmatter(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fm.AllowedTools) != 3 {
		t.Fatalf("allowed_tools: got %d items, want 3", len(fm.AllowedTools))
	}
	if fm.AllowedTools[0] != "Bash" || fm.AllowedTools[1] != "Read" || fm.AllowedTools[2] != "Write" {
		t.Errorf("allowed_tools: got %v", fm.AllowedTools)
	}
}

func TestParseFrontmatter_ModelAndEffort(t *testing.T) {
	input := `---
name: "model-skill"
description: "Skill with model override"
model: "claude-opus-4.7"
effort: "high"
---
Body
`
	fm, _, err := parseFrontmatter(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fm.Model != "claude-opus-4.7" {
		t.Errorf("model: got %q", fm.Model)
	}
	if fm.Effort != "high" {
		t.Errorf("effort: got %q", fm.Effort)
	}
}

func TestLoadMeta_ExtendedFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	writeSkillMDWithFM(t, path, map[string]any{
		"name":          "test-skill",
		"description":   "Test",
		"when_to_use":   "For testing",
		"argument_hint": "--test",
		"context":       "fork",
		"model":         "sonnet",
		"effort":        "medium",
		"allowed_tools": []string{"Bash"},
		"paths":         []string{"**/*.go"},
	}, "Body\n")

	meta, err := LoadMeta(path)
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	if meta.WhenToUse != "For testing" {
		t.Errorf("when_to_use: got %q", meta.WhenToUse)
	}
	if meta.ArgsHint != "--test" {
		t.Errorf("argument_hint: got %q", meta.ArgsHint)
	}
	if meta.Context != "fork" {
		t.Errorf("context: got %q", meta.Context)
	}
	if meta.Model != "sonnet" {
		t.Errorf("model: got %q", meta.Model)
	}
	if meta.Effort != "medium" {
		t.Errorf("effort: got %q", meta.Effort)
	}
	if len(meta.AllowedTools) != 1 || meta.AllowedTools[0] != "Bash" {
		t.Errorf("allowed_tools: got %v", meta.AllowedTools)
	}
	if len(meta.Paths) != 1 || meta.Paths[0] != "**/*.go" {
		t.Errorf("paths: got %v", meta.Paths)
	}
}

// ---------- Sidecar metadata ----------

func TestSidecarMetadata_ChordYaml(t *testing.T) {
	dir := t.TempDir()
	content := `---
name: "my-skill"
description: "My skill"
---
Body
`
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	// Write sidecar chord.yaml
	sidecar := `when_to_use: "Sidecar when to use"
context: "fork"
model: "gpt-5.5"
effort: "high"
allowed_tools:
  - "Bash"
`
	if err := os.WriteFile(filepath.Join(dir, "chord.yaml"), []byte(sidecar), 0644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	meta, err := LoadMeta(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	if meta.WhenToUse != "Sidecar when to use" {
		t.Errorf("when_to_use: got %q", meta.WhenToUse)
	}
	if meta.Context != "fork" {
		t.Errorf("context: got %q", meta.Context)
	}
	if meta.Model != "gpt-5.5" {
		t.Errorf("model: got %q", meta.Model)
	}
	if meta.Effort != "high" {
		t.Errorf("effort: got %q", meta.Effort)
	}
	if len(meta.AllowedTools) != 1 || meta.AllowedTools[0] != "Bash" {
		t.Errorf("allowed_tools: got %v", meta.AllowedTools)
	}
}

func TestSidecarMetadata_AgentYaml(t *testing.T) {
	dir := t.TempDir()
	content := `---
name: "my-skill"
description: "My skill"
---
Body
`
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	// Write sidecar agent.yaml
	sidecar := `when_to_use: "Agent sidecar when to use"
argument_hint: "--flag"
`
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(sidecar), 0644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	meta, err := LoadMeta(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	if meta.WhenToUse != "Agent sidecar when to use" {
		t.Errorf("when_to_use: got %q", meta.WhenToUse)
	}
	if meta.ArgsHint != "--flag" {
		t.Errorf("argument_hint: got %q", meta.ArgsHint)
	}
}

func TestSidecarOverridesFrontmatter(t *testing.T) {
	dir := t.TempDir()
	writeSkillMDWithFM(t, filepath.Join(dir, "SKILL.md"), map[string]any{
		"name":        "override-skill",
		"description": "Original",
		"when_to_use": "Original when",
		"context":     "inline",
	}, "Body\n")
	sidecar := `when_to_use: "Sidecar when"
context: "fork"
`
	if err := os.WriteFile(filepath.Join(dir, "chord.yaml"), []byte(sidecar), 0644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	meta, err := LoadMeta(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	if meta.WhenToUse != "Sidecar when" {
		t.Errorf("sidecar should override frontmatter; got when_to_use=%q", meta.WhenToUse)
	}
	if meta.Context != "fork" {
		t.Errorf("sidecar should override frontmatter; got context=%q", meta.Context)
	}
}

func TestSidecarChordTakesPrecedence(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "my-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	skillContent := "---\nname: my-skill\ndescription: My skill\n---\nBody\n"
	if err := os.WriteFile(skillPath, []byte(skillContent), 0644); err != nil {
		t.Fatal(err)
	}
	// Both chord.yaml and agent.yaml present; chord.yaml wins.
	if err := os.WriteFile(filepath.Join(skillDir, "chord.yaml"), []byte(`when_to_use: "chord"`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "agent.yaml"), []byte(`when_to_use: "agent"`), 0644); err != nil {
		t.Fatal(err)
	}

	meta, err := LoadMeta(skillPath)
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	if meta.WhenToUse != "chord" {
		t.Errorf("chord.yaml should take precedence; got %q", meta.WhenToUse)
	}
}

func TestNoSidecarWhenMissing(t *testing.T) {
	dir := t.TempDir()
	writeSkillMDWithFM(t, filepath.Join(dir, "SKILL.md"), map[string]any{
		"name":        "no-sidecar",
		"description": "No sidecar file",
		"when_to_use": "Original",
	}, "Body\n")

	meta, err := LoadMeta(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	if meta.WhenToUse != "Original" {
		t.Errorf("when_to_use should remain from frontmatter; got %q", meta.WhenToUse)
	}
}

// ---------- WorkDir chain ----------

// ---------- LazyWatcher ----------

func TestLazyWatcher_StartStop(t *testing.T) {
	dir := t.TempDir()
	createSkillFile(t, dir, "watch-skill", "Watched skill", "Body\n")
	loader := NewLoader([]string{dir})
	refreshed := make(chan struct{}, 1)
	w := NewLazyWatcher(loader, 100*time.Millisecond, func() {
		select {
		case refreshed <- struct{}{}:
		default:
		}
	})
	w.Start()
	defer w.Stop()

	// Wait for at least one refresh cycle.
	select {
	case <-refreshed:
		// ok
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected at least one refresh cycle")
	}
}

func TestLazyWatcher_StopIsIdempotent(t *testing.T) {
	loader := NewLoader([]string{})
	w := NewLazyWatcher(loader, time.Hour, nil)
	w.Start()
	w.Stop()
	w.Stop() // should not panic
}

func TestLazyWatcherRecheckOnlyFiresOnChange(t *testing.T) {
	dir := t.TempDir()
	createSkillFile(t, dir, "watch-skill", "Watched skill", "Body\n")
	skillPath := filepath.Join(dir, "watch-skill", "SKILL.md")
	loader := NewLoader([]string{dir})
	calls := 0
	w := NewLazyWatcher(loader, 0, func() { calls++ })

	w.recheck()
	if calls != 1 {
		t.Fatalf("first recheck calls = %d, want 1", calls)
	}

	w.recheck()
	if calls != 1 {
		t.Fatalf("unchanged recheck calls = %d, want 1", calls)
	}

	updated := `---
name: watch-skill
description: Watched skill updated
---
Body
`
	if err := os.WriteFile(skillPath, []byte(updated), 0644); err != nil {
		t.Fatalf("write updated skill: %v", err)
	}
	w.recheck()
	if calls != 2 {
		t.Fatalf("changed recheck calls = %d, want 2", calls)
	}
}
