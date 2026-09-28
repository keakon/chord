package permission

import (
	"path/filepath"
	"testing"
)

func TestNormalizeRulePathCollapsesToCheckoutRelative(t *testing.T) {
	scope := PathScope{
		Cwd:   filepath.Join("/repo", "internal"),
		Roots: []string{"/repo"},
	}
	cases := []struct {
		in   string
		want string
	}{
		// The session runs in a subdirectory: a relative spelling must keep
		// the repository-relative prefix so the rule matches the same file
		// from every checkout and from the repository root.
		{"tools/delete.go", "internal/tools/delete.go"},
		{filepath.Join("/repo", "internal", "tools", "delete.go"), "internal/tools/delete.go"},
		{filepath.Join("/repo", "src", "main.go"), "src/main.go"},
	}
	for _, tc := range cases {
		if got := NormalizeRulePath(tc.in, scope); got != tc.want {
			t.Errorf("NormalizeRulePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeRulePathWorktreeCheckoutUsesSameSpelling(t *testing.T) {
	scope := PathScope{
		Cwd:   "/worktrees/feature",
		Roots: []string{"/repo", "/worktrees/feature"},
	}
	got := NormalizeRulePath(filepath.Join("/worktrees", "feature", "src", "main.go"), scope)
	if want := "src/main.go"; got != want {
		t.Fatalf("NormalizeRulePath(worktree file) = %q, want %q", got, want)
	}
	// The same file spelled through the main checkout resolves to the same
	// repository-relative rule spelling.
	gotMain := NormalizeRulePath(filepath.Join("/repo", "src", "main.go"), scope)
	if gotMain != got {
		t.Fatalf("main checkout spelling = %q, want %q", gotMain, got)
	}
}

func TestNormalizeRulePathNestedWorktreeRootWins(t *testing.T) {
	scope := PathScope{
		Cwd:   "/repo/.worktrees/feature",
		Roots: []string{"/repo", "/repo/.worktrees/feature"},
	}
	got := NormalizeRulePath("/repo/.worktrees/feature/src/main.go", scope)
	if want := "src/main.go"; got != want {
		t.Fatalf("NormalizeRulePath(nested worktree file) = %q, want %q", got, want)
	}
}

func TestNormalizeRulePathWithoutRootsFallsBackToCwd(t *testing.T) {
	scope := PathScope{Cwd: filepath.Join("/repo", "internal")}
	cases := []struct {
		in   string
		want string
	}{
		{"tools/delete.go", "tools/delete.go"},
		{filepath.Join("/repo", "internal", "tools", "delete.go"), "tools/delete.go"},
		{"/tmp/outside.log", "/tmp/outside.log"},
	}
	for _, tc := range cases {
		if got := NormalizeRulePath(tc.in, scope); got != tc.want {
			t.Errorf("NormalizeRulePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeRulePathContainerCheckout(t *testing.T) {
	scope := PathScope{
		Cwd:        "/worktrees/feature",
		Roots:      []string{"/repo"},
		Containers: []string{"/worktrees"},
	}
	got := NormalizeRulePath("/worktrees/feature/src/main.go", scope)
	if want := "src/main.go"; got != want {
		t.Fatalf("NormalizeRulePath(container checkout file) = %q, want %q", got, want)
	}
}

func TestRulePathInScope(t *testing.T) {
	scope := PathScope{
		Cwd:   filepath.Join("/repo", "internal"),
		Roots: []string{"/repo"},
	}
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"relative under cwd", "tools/delete.go", true},
		{"relative escaping cwd but inside root", "../src/main.go", true},
		{"absolute inside root", filepath.Join("/repo", "src", "main.go"), true},
		{"absolute outside roots", "/tmp/scratch.log", false},
		{"scope root itself", "/repo", false},
		{"escaping the root", "/repo/../elsewhere/file.go", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		if got := RulePathInScope(tc.in, scope); got != tc.want {
			t.Errorf("%s: RulePathInScope(%q) = %v, want %v", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestRulePathInScopeDegenerateScopeIsLexical(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"internal/tools/delete.go", true},
		{"./internal/tools/delete.go", true},
		{"..", false},
		{"../sibling/file.go", false},
		{"/tmp/scratch.log", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := RulePathInScope(tc.in, PathScope{}); got != tc.want {
			t.Errorf("RulePathInScope(%q, empty scope) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
