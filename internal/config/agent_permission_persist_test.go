package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/permission"
)

func TestUpsertAgentPermissionRuleConvertsScalarToolRule(t *testing.T) {
	path := filepath.Join(t.TempDir(), "planner.yaml")
	body := `# planner agent
name: planner
permission:
  "*": deny
  write: ask
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	changed, err := UpsertAgentPermissionRule(path, permission.Rule{
		Permission: "write",
		Pattern:    ".chord/plans/*",
		Action:     permission.ActionAllow,
	})
	if err != nil {
		t.Fatalf("UpsertAgentPermissionRule: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read updated file: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"# planner agent",
		"name: planner",
		"write:",
		".chord/plans/*: allow",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("updated agent config missing %q in:\n%s", want, text)
		}
	}
	if !strings.Contains(text, `"*": ask`) && !strings.Contains(text, `'*': ask`) {
		t.Fatalf("fallback ask rule missing in:\n%s", text)
	}
}

func TestUpsertAgentPermissionRuleDeduplicatesAndUpdatesExistingPattern(t *testing.T) {
	path := filepath.Join(t.TempDir(), "planner.yaml")
	body := `name: planner
permission:
  write:
    .chord/plans/*: ask
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	rule := permission.Rule{Permission: "write", Pattern: ".chord/plans/*", Action: permission.ActionAllow}
	changed, err := UpsertAgentPermissionRule(path, rule)
	if err != nil {
		t.Fatalf("UpsertAgentPermissionRule(update): %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	changed, err = UpsertAgentPermissionRule(path, rule)
	if err != nil {
		t.Fatalf("UpsertAgentPermissionRule(dedup): %v", err)
	}
	if changed {
		t.Fatal("changed = true for duplicate unchanged rule, want false")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read updated file: %v", err)
	}
	if got := strings.Count(string(data), ".chord/plans/*"); got != 1 {
		t.Fatalf("pattern count = %d, want 1 in:\n%s", got, data)
	}
	if !strings.Contains(string(data), ".chord/plans/*: allow") {
		t.Fatalf("updated action missing in:\n%s", data)
	}
}

func TestUpsertAgentPermissionRuleCreatesMissingFileFromBaseAgent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "planner.yaml")
	base := DefaultPlannerAgent()
	rule := permission.Rule{Permission: "write", Pattern: "docs/*", Action: permission.ActionAllow}

	changed, err := UpsertAgentPermissionRuleForAgent(path, base, rule)
	if err != nil {
		t.Fatalf("UpsertAgentPermissionRuleForAgent: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read created file: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"name: planner",
		"description: Planning agent",
		"mode: main",
		"permission:",
		"read: allow",
		".chord/plans/*: allow",
		"docs/*: allow",
		"handoff: allow",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("created agent config missing %q in:\n%s", want, text)
		}
	}
}

func TestRemoveAgentPermissionRule(t *testing.T) {
	path := filepath.Join(t.TempDir(), "planner.yaml")
	body := `name: planner
permission:
  write:
    "*": ask
    .chord/plans/*: allow
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	changed, err := RemoveAgentPermissionRule(path, permission.Rule{
		Permission: "write",
		Pattern:    ".chord/plans/*",
		Action:     permission.ActionAllow,
	})
	if err != nil {
		t.Fatalf("RemoveAgentPermissionRule: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read updated file: %v", err)
	}
	text := string(data)
	if strings.Contains(text, ".chord/plans/*") {
		t.Fatalf("removed pattern still present in:\n%s", text)
	}
	if !strings.Contains(text, `"*": ask`) {
		t.Fatalf("fallback rule missing in:\n%s", text)
	}
}
