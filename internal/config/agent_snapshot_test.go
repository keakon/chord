package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveAgentConfigsDataUsesCapturedDeclarations(t *testing.T) {
	globalDir, projectDir := t.TempDir(), t.TempDir()
	globalPath := filepath.Join(globalDir, "helper.yaml")
	projectPath := filepath.Join(projectDir, "helper.md")
	if err := os.WriteFile(globalPath, []byte("name: helper\nmode: subagent\nprompt: global\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{projectPath: []byte("---\nname: helper\nmode: subagent\n---\nCaptured prompt.\n")}
	if err := os.WriteFile(projectPath, []byte("---\nname: helper\nmode: subagent\n---\nChanged prompt.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	agents, err := ResolveAgentConfigsData(files, globalDir)
	if err != nil {
		t.Fatal(err)
	}
	if agents["helper"].SystemPrompt != "Captured prompt." {
		t.Fatalf("prompt=%q", agents["helper"].SystemPrompt)
	}
	if agents["builder"] == nil || agents["planner"] == nil {
		t.Fatal("built-in roles were lost")
	}
	agents, err = ResolveAgentConfigsData(map[string][]byte{}, globalDir)
	if err != nil {
		t.Fatal(err)
	}
	if agents["helper"].SystemPrompt != "global" {
		t.Fatalf("empty project snapshot did not preserve global role: %+v", agents["helper"])
	}
}

func TestLoadAgentConfigsDataValidation(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name  string
		files map[string][]byte
		want  string
	}{
		{"invalid YAML", map[string][]byte{filepath.Join(dir, "helper.yaml"): []byte("permission: [")}, "yaml"},
		{"name mismatch", map[string][]byte{filepath.Join(dir, "helper.yaml"): []byte("name: other\n")}, "name"},
		{"duplicate", map[string][]byte{
			filepath.Join(dir, "helper.md"):   []byte("---\nname: helper\n---\nPrompt.\n"),
			filepath.Join(dir, "helper.yaml"): []byte("name: helper\n"),
		}, "duplicate agent name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := LoadAgentConfigsData(tc.files); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
}
