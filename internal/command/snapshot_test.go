package command

import (
	"path/filepath"
	"testing"
)

func TestLoadCapturedProjectFiles(t *testing.T) {
	root, configHome := t.TempDir(), t.TempDir()
	dir := filepath.Join(root, ".chord", "commands")
	writeMD(t, dir, "review.md", "Changed instruction.")
	writeMD(t, dir, "added.md", "New instruction.")
	writeMD(t, filepath.Join(configHome, "commands"), "review.md", "Global instruction.")
	writeMD(t, filepath.Join(configHome, "commands"), "daily.md", "Daily instruction.")
	files := map[string][]byte{
		filepath.Join(dir, "review.md"):     []byte("---\ndescription: Captured description\n---\nCaptured instruction."),
		filepath.Join(dir, "nested/run.md"): []byte("Nested instruction."),
	}
	defs, warnings := Load(LoadOptions{ContentRoot: root, ConfigHome: configHome, ProjectFiles: files})
	if len(warnings) != 0 {
		t.Fatalf("warnings=%v", warnings)
	}
	byName := map[string]*Definition{}
	for _, def := range defs {
		byName[def.Name] = def
	}
	if len(defs) != 3 || byName["added"] != nil {
		t.Fatalf("unexpected definitions: %+v", defs)
	}
	if def := byName["review"]; def.Template != "Captured instruction." || def.Description != "Captured description" {
		t.Fatalf("review=%+v", def)
	}
	if byName["nested/run"] == nil || byName["daily"] == nil {
		t.Fatal("nested project or global definition was lost")
	}

	defs, warnings = Load(LoadOptions{ContentRoot: root, ConfigHome: configHome, ProjectFiles: map[string][]byte{}})
	if len(warnings) != 0 || len(defs) != 2 {
		t.Fatalf("empty snapshot definitions=%+v warnings=%v", defs, warnings)
	}
	for _, def := range defs {
		if def.Source != "global-md" {
			t.Fatalf("empty snapshot read project files: %+v", def)
		}
	}
}
