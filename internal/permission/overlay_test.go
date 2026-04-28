package permission

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppendRoleOverlayRule_CreatesNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-role.yaml")

	rule := Rule{
		Permission: "Bash",
		Pattern:    "git log *",
		Action:     ActionAllow,
	}

	if err := AppendRoleOverlayRule(path, rule); err != nil {
		t.Fatalf("AppendRoleOverlayRule failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read created file: %v", err)
	}

	content := string(data)
	if content == "" {
		t.Fatal("expected non-empty file content")
	}
	if !containsLine(content, "permission:") {
		t.Fatalf("expected permission root key, got:\n%s", content)
	}
}

func TestAppendRoleOverlayRule_Deduplicates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-role.yaml")

	rule := Rule{
		Permission: "Bash",
		Pattern:    "git log *",
		Action:     ActionAllow,
	}

	if err := AppendRoleOverlayRule(path, rule); err != nil {
		t.Fatalf("first AppendRoleOverlayRule failed: %v", err)
	}

	if err := AppendRoleOverlayRule(path, rule); err != nil {
		t.Fatalf("second AppendRoleOverlayRule failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	// Should contain the rule exactly once
	content := string(data)
	count := 0
	for i := 0; i < len(content); i++ {
		if i+7 <= len(content) && content[i:i+7] == "git log" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected rule to appear exactly once, found %d occurrences in:\n%s", count, content)
	}
}

func TestRemoveRoleOverlayRule(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-role.yaml")

	rule := Rule{
		Permission: "Bash",
		Pattern:    "git log *",
		Action:     ActionAllow,
	}

	if err := AppendRoleOverlayRule(path, rule); err != nil {
		t.Fatalf("AppendRoleOverlayRule failed: %v", err)
	}

	if err := RemoveRoleOverlayRule(path, rule); err != nil {
		t.Fatalf("RemoveRoleOverlayRule failed: %v", err)
	}

	if _, err := os.ReadFile(path); err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	rules, err := loadOverlayFile(path)
	if err != nil {
		t.Fatalf("loadOverlayFile failed: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("expected no rules after removal, got: %+v", rules)
	}
}

func TestRemoveRoleOverlayRule_NoFile(t *testing.T) {
	rule := Rule{
		Permission: "Bash",
		Pattern:    "git log *",
		Action:     ActionAllow,
	}
	// Should not error when file doesn't exist
	if err := RemoveRoleOverlayRule("/nonexistent/path.yaml", rule); err != nil {
		t.Fatalf("RemoveRoleOverlayRule should not error on missing file: %v", err)
	}
}

func TestOverlay_AddAndRemove(t *testing.T) {
	o := NewOverlay()
	o.SetActiveRole("test-role")
	o.SetBase(Ruleset{
		{Permission: "Bash", Pattern: "*", Action: ActionAsk},
	})

	rule := Rule{Permission: "Bash", Pattern: "git log *", Action: ActionAllow}
	o.AddSessionRule("test-role", rule)

	// Check merged ruleset
	merged := o.MergedRuleset()
	result := merged.Evaluate("Bash", "git log --oneline")
	if result != ActionAllow {
		t.Errorf("expected ActionAllow after adding session rule, got %v", result)
	}

	// Remove it
	o.RemoveSessionRule(0)
	merged = o.MergedRuleset()
	result = merged.Evaluate("Bash", "git log --oneline")
	if result != ActionAsk {
		t.Errorf("expected ActionAsk after removing session rule, got %v", result)
	}
}

func TestOverlay_AddedRules(t *testing.T) {
	o := NewOverlay()
	o.SetActiveRole("builder")
	o.SetBase(Ruleset{
		{Permission: "Bash", Pattern: "*", Action: ActionAsk},
	})

	rule := Rule{Permission: "Bash", Pattern: "git *", Action: ActionAllow}
	o.AddSessionRule("builder", rule)

	added := o.AddedRules()
	if len(added) != 1 {
		t.Fatalf("expected 1 added rule, got %d", len(added))
	}
	if added[0].Role != "builder" {
		t.Errorf("expected role 'builder', got %q", added[0].Role)
	}
	if added[0].Scope != ScopeSession {
		t.Errorf("expected ScopeSession, got %v", added[0].Scope)
	}
}

func TestOverlay_AddSessionRule_Deduplicates(t *testing.T) {
	o := NewOverlay()
	o.SetActiveRole("builder")
	rule := Rule{Permission: "Bash", Pattern: "git *", Action: ActionAllow}
	o.AddSessionRule("builder", rule)
	o.AddSessionRule("builder", rule)
	if got := len(o.AddedRules()); got != 1 {
		t.Fatalf("added rules = %d, want 1", got)
	}
	if got := o.SessionRuleCount(); got != 1 {
		t.Fatalf("session rules = %d, want 1", got)
	}
}

func TestOverlay_AddProjectRule_PathRequired(t *testing.T) {
	o := NewOverlay()
	rule := Rule{Permission: "Bash", Pattern: "git *", Action: ActionAllow}
	if err := o.AddProjectRule("builder", rule); err == nil {
		t.Fatal("expected AddProjectRule to fail when project path is empty")
	}
}

func TestOverlay_LoadOverlayFile_RequiresPermissionRoot(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "legacy.yaml")
	permissionPath := filepath.Join(dir, "permission.yaml")

	legacy := "Bash:\n  \"git *\": allow\n"
	if err := os.WriteFile(legacyPath, []byte(legacy), 0o644); err != nil {
		t.Fatalf("write legacy file: %v", err)
	}
	withPermission := "permission:\n  Bash:\n    \"git *\": allow\n"
	if err := os.WriteFile(permissionPath, []byte(withPermission), 0o644); err != nil {
		t.Fatalf("write permission file: %v", err)
	}

	rules, err := loadOverlayFile(permissionPath)
	if err != nil {
		t.Fatalf("loadOverlayFile(permissionPath) failed: %v", err)
	}
	if got := rules.Evaluate("Bash", "git status"); got != ActionAllow {
		t.Fatalf("evaluate loaded rules = %s, want %s", got, ActionAllow)
	}

	if _, err := loadOverlayFile(legacyPath); err == nil {
		t.Fatal("expected legacy overlay file without permission root to fail")
	}
}

func TestAppendRoleOverlayRule_RejectsLegacyRootSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.yaml")
	legacy := "Bash:\n  \"git *\": allow\n"
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatalf("write legacy file: %v", err)
	}
	rule := Rule{
		Permission: "Bash",
		Pattern:    "git status *",
		Action:     ActionAllow,
	}
	if err := AppendRoleOverlayRule(path, rule); err == nil {
		t.Fatal("expected AppendRoleOverlayRule to reject legacy root schema")
	}
}

func TestOverlay_SessionRulesAreRoleScoped(t *testing.T) {
	o := NewOverlay()
	o.SetActiveRole("builder")
	o.SetBase(Ruleset{{Permission: "Bash", Pattern: "*", Action: ActionAsk}})
	builderRule := Rule{Permission: "Bash", Pattern: "git *", Action: ActionAllow}
	o.AddSessionRule("builder", builderRule)

	if got := o.MergedRuleset().Evaluate("Bash", "git status"); got != ActionAllow {
		t.Fatalf("builder merged evaluation = %s, want %s", got, ActionAllow)
	}
	if got := o.SessionRuleCountForRole("builder"); got != 1 {
		t.Fatalf("builder session count = %d, want 1", got)
	}

	o.SetActiveRole("planner")
	if got := o.MergedRuleset().Evaluate("Bash", "git status"); got != ActionAsk {
		t.Fatalf("planner merged evaluation = %s, want %s", got, ActionAsk)
	}
	if got := o.SessionRuleCount(); got != 0 {
		t.Fatalf("active planner session count = %d, want 0", got)
	}
}

func TestOverlay_RemoveAddedRulePersistentFailureKeepsTrackingState(t *testing.T) {
	o := NewOverlay()
	path := filepath.Join(t.TempDir(), "builder.yaml")
	rule := Rule{Permission: "Bash", Pattern: "git *", Action: ActionAllow}
	if err := AppendRoleOverlayRule(path, rule); err != nil {
		t.Fatalf("AppendRoleOverlayRule failed: %v", err)
	}
	o.SetProjectPath(path)
	o.project = Ruleset{rule}
	o.addedRules = []AddedRule{{Role: "builder", Rule: rule, Scope: ScopeProject, Path: path, AddedAt: time.Now()}}

	if err := os.Chmod(filepath.Dir(path), 0o500); err != nil {
		t.Fatalf("chmod dir read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(path), 0o700) })
	if err := o.RemoveAddedRule(0); err == nil {
		t.Fatal("expected RemoveAddedRule to fail when backing file removal fails")
	}
	if got := len(o.AddedRules()); got != 1 {
		t.Fatalf("added rules after failure = %d, want 1", got)
	}
	if got := len(o.project); got != 1 {
		t.Fatalf("project rules after failure = %d, want 1", got)
	}
}
func TestOverlay_AddedRulesPreserveRemovalIndexOrder(t *testing.T) {
	o := NewOverlay()
	o.SetActiveRole("builder")
	older := Rule{Permission: "Bash", Pattern: "git log *", Action: ActionAllow}
	newer := Rule{Permission: "Bash", Pattern: "git status *", Action: ActionAllow}
	o.AddSessionRule("builder", older)
	o.AddSessionRule("builder", newer)

	// Simulate non-monotonic timestamps from restore/manual construction. The UI
	// must see insertion order because RemoveAddedRule accepts the displayed index.
	o.addedRules[0].AddedAt = time.Now().Add(time.Hour)
	o.addedRules[1].AddedAt = time.Now().Add(-time.Hour)

	added := o.AddedRules()
	if got := added[0].Rule.Pattern; got != "git log *" {
		t.Fatalf("first displayed rule = %q, want insertion-order git log *", got)
	}
	if err := o.RemoveAddedRule(0); err != nil {
		t.Fatalf("RemoveAddedRule failed: %v", err)
	}
	remaining := o.AddedRules()
	if got := len(remaining); got != 1 {
		t.Fatalf("remaining added rules = %d, want 1", got)
	}
	if got := remaining[0].Rule.Pattern; got != "git status *" {
		t.Fatalf("remaining rule = %q, want git status *", got)
	}
}

func containsLine(content, line string) bool {
	for _, l := range strings.Split(content, "\n") {
		if strings.TrimSpace(l) == line {
			return true
		}
	}
	return false
}
