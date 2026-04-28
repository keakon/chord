package command

import (
	"os"
	"path/filepath"
	"testing"
)

// writeMD writes a .md file under dir/rel, creating parent directories.
func writeMD(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanMDDir_basic(t *testing.T) {
	dir := t.TempDir()
	writeMD(t, dir, "review.md", "---\ndescription: do a review\n---\nPlease review the code.")
	writeMD(t, dir, "foo/bar.md", "Do foo bar.")

	defs, warns := scanMDDir(dir, "project-md")
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	if len(defs) != 2 {
		t.Fatalf("want 2 defs, got %d", len(defs))
	}
	byName := map[string]*Definition{}
	for _, d := range defs {
		byName[d.Name] = d
	}
	if d := byName["review"]; d == nil {
		t.Error("missing review")
	} else {
		if d.Description != "do a review" {
			t.Errorf("description: want %q got %q", "do a review", d.Description)
		}
		if d.Template != "Please review the code." {
			t.Errorf("template: want %q got %q", "Please review the code.", d.Template)
		}
		if d.Source != "project-md" {
			t.Errorf("source: want project-md got %q", d.Source)
		}
	}
	if d := byName["foo/bar"]; d == nil {
		t.Error("missing foo/bar")
	}
}

func TestScanMDDir_emptyBody(t *testing.T) {
	dir := t.TempDir()
	writeMD(t, dir, "empty.md", "---\ndescription: test\n---\n")

	defs, warns := scanMDDir(dir, "project-md")
	if len(defs) != 0 {
		t.Errorf("want 0 defs, got %d", len(defs))
	}
	if len(warns) == 0 {
		t.Error("want warning for empty body")
	}
}

func TestScanMDDir_noFrontmatter(t *testing.T) {
	dir := t.TempDir()
	writeMD(t, dir, "plain.md", "Just a plain body.")

	defs, warns := scanMDDir(dir, "global-md")
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	if len(defs) != 1 {
		t.Fatalf("want 1 def, got %d", len(defs))
	}
	if defs[0].Template != "Just a plain body." {
		t.Errorf("template: got %q", defs[0].Template)
	}
}

func TestScanMDDir_notExist(t *testing.T) {
	defs, warns := scanMDDir("/nonexistent/path/xyz", "project-md")
	if len(defs) != 0 || len(warns) != 0 {
		t.Errorf("want empty results for missing dir, got defs=%d warns=%d", len(defs), len(warns))
	}
}

func TestFromYAML(t *testing.T) {
	cmds := map[string]string{
		"/review": "Please review.",
		"daily":   "Daily standup.",
		"/empty":  "   ",
	}
	defs, warns := fromYAML(cmds, "/fake/config.yaml", "global-yaml")
	if len(warns) == 0 {
		t.Error("want warning for empty command")
	}
	byName := map[string]*Definition{}
	for _, d := range defs {
		byName[d.Name] = d
	}
	if _, ok := byName["review"]; !ok {
		t.Error("missing review")
	}
	if _, ok := byName["daily"]; !ok {
		t.Error("missing daily")
	}
	if _, ok := byName["empty"]; ok {
		t.Error("empty command should be skipped")
	}
}

func TestMerge_priority(t *testing.T) {
	projectMD := []*Definition{{Name: "review", Template: "project-md", Source: "project-md"}}
	globalMD := []*Definition{{Name: "review", Template: "global-md", Source: "global-md"}}
	projectYAML := []*Definition{{Name: "review", Template: "project-yaml", Source: "project-yaml"}}
	globalYAML := []*Definition{{Name: "review", Template: "global-yaml", Source: "global-yaml"}}

	defs, warns := Merge(projectMD, globalMD, projectYAML, globalYAML)
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	if len(defs) != 1 {
		t.Fatalf("want 1 merged def, got %d", len(defs))
	}
	if defs[0].Source != "project-md" {
		t.Errorf("project-md should win, got %q", defs[0].Source)
	}
}

func TestMerge_builtinReserved(t *testing.T) {
	defs := []*Definition{
		{Name: "resume", Template: "resume custom"},
		{Name: "help", Template: "help custom"},
		{Name: "myreview", Template: "ok"},
	}
	out, warns := Merge(defs)
	if len(warns) != 2 {
		t.Errorf("want 2 warnings for reserved names, got %d: %v", len(warns), warns)
	}
	if len(out) != 1 || out[0].Name != "myreview" {
		t.Errorf("only myreview should survive, got %+v", out)
	}
}

func TestMerge_MDOverridesYAML(t *testing.T) {
	md := []*Definition{{Name: "daily", Template: "from-md", Source: "project-md"}}
	yaml := []*Definition{{Name: "daily", Template: "from-yaml", Source: "project-yaml"}}

	out, _ := Merge(md, yaml)
	if len(out) != 1 || out[0].Template != "from-md" {
		t.Errorf("md should override yaml, got %+v", out)
	}
}

func TestParseInput(t *testing.T) {
	cases := []struct {
		input    string
		wantName string
		wantArgs string
	}{
		{"/review", "review", ""},
		{"/review fix nil", "review", "fix nil"},
		{"/foo/bar aaa bbb", "foo/bar", "aaa bbb"},
		{"not a slash", "", ""},
		{"/reviewer", "reviewer", ""},
	}
	for _, c := range cases {
		name, args := ParseInput(c.input)
		if name != c.wantName || args != c.wantArgs {
			t.Errorf("ParseInput(%q) = (%q, %q), want (%q, %q)",
				c.input, name, args, c.wantName, c.wantArgs)
		}
	}
}

func TestExpand(t *testing.T) {
	cases := []struct {
		tmpl string
		args string
		want string
	}{
		{"do $ARGUMENTS now", "fix nil", "do fix nil now"},
		{"do $ARGUMENTS now", "", "do  now"},
		{"no placeholder", "extra args", "no placeholder\n\nextra args"},
		{"no placeholder", "", "no placeholder"},
	}
	for _, c := range cases {
		got := Expand(c.tmpl, c.args)
		if got != c.want {
			t.Errorf("Expand(%q, %q) = %q, want %q", c.tmpl, c.args, got, c.want)
		}
	}
}

func TestLoad_integration(t *testing.T) {
	projectRoot := t.TempDir()
	chordHome := t.TempDir()

	// Project MD (highest priority)
	writeMD(t, filepath.Join(projectRoot, ".chord", "commands"), "review.md",
		"---\ndescription: project review\n---\nProject review prompt.")
	// Global MD (lower priority than project MD)
	writeMD(t, filepath.Join(chordHome, "commands"), "review.md",
		"---\ndescription: global review\n---\nGlobal review prompt.")
	writeMD(t, filepath.Join(chordHome, "commands"), "daily.md",
		"Daily standup.")

	defs, warns := Load(LoadOptions{
		ProjectRoot:    projectRoot,
		ConfigHome:     chordHome,
		ProjectCfg:     map[string]string{"standup": "standup via yaml"},
		ProjectCfgPath: filepath.Join(projectRoot, ".chord", "config.yaml"),
		GlobalCfg:      map[string]string{"daily": "daily via yaml"},
		GlobalCfgPath:  filepath.Join(chordHome, "config.yaml"),
	})
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	byName := map[string]*Definition{}
	for _, d := range defs {
		byName[d.Name] = d
	}
	// Project MD beats global MD for "review"
	if d := byName["review"]; d == nil {
		t.Error("missing review")
	} else if d.Source != "project-md" {
		t.Errorf("review source: want project-md, got %q", d.Source)
	}
	// Global MD provides "daily" (beats global-yaml)
	if d := byName["daily"]; d == nil {
		t.Error("missing daily")
	} else if d.Source != "global-md" {
		t.Errorf("daily source: want global-md, got %q", d.Source)
	}
	// Project YAML provides "standup"
	if d := byName["standup"]; d == nil {
		t.Error("missing standup")
	} else if d.Source != "project-yaml" {
		t.Errorf("standup source: want project-yaml, got %q", d.Source)
	}
}
