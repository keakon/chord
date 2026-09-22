package permission

import (
	"os"
	"path/filepath"
	"testing"
)

const (
	scopeMainRoot = "/repo/main"
	scopeWtRoot   = "/repo/main/.chord/worktrees/x"
)

// TestEvaluatePathScopeMapsEveryCheckoutToSameRule is the core merged-roots
// guarantee: the same repository-relative path resolves to one action wherever
// the agent stands, so a targeted relative deny cannot be bypassed by standing
// in another checkout.
func TestEvaluatePathScopeMapsEveryCheckoutToSameRule(t *testing.T) {
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionAllow},
		{Permission: "write", Pattern: "src/gen/**", Action: ActionDeny},
	}
	scope := PathScope{Cwd: scopeWtRoot, Roots: []string{scopeWtRoot, scopeMainRoot}}
	for _, p := range []string{
		"src/gen/x.go",
		filepath.Join(scopeWtRoot, "src", "gen", "x.go"),
		filepath.Join(scopeMainRoot, "src", "gen", "x.go"),
	} {
		if got := rs.EvaluatePath("write", p, scope); got != ActionDeny {
			t.Errorf("EvaluatePath(%q) = %q, want deny", p, got)
		}
	}
	for _, p := range []string{
		filepath.Join(scopeMainRoot, "other", "x.go"),
		filepath.Join(scopeWtRoot, "other", "x.go"),
		"/tmp/x.go",
	} {
		if got := rs.EvaluatePath("write", p, scope); got != ActionAllow {
			t.Errorf("EvaluatePath(%q) = %q, want allow", p, got)
		}
	}
}

// TestEvaluatePathScopeRootsCoverPathsOutsideCwd covers the reverse direction:
// a session working in the main checkout still has the linked worktree root in
// scope, so its relative rule applies there too.
func TestEvaluatePathScopeRootsCoverPathsOutsideCwd(t *testing.T) {
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionAllow},
		{Permission: "write", Pattern: "src/gen/**", Action: ActionDeny},
	}
	scope := PathScope{Cwd: scopeMainRoot, Roots: []string{scopeWtRoot, scopeMainRoot}}
	if got := rs.EvaluatePath("write", filepath.Join(scopeWtRoot, "src", "gen", "x.go"), scope); got != ActionDeny {
		t.Errorf("worktree path under main-checkout cwd = %q, want deny", got)
	}
}

// TestEvaluatePathScopeContainerRule covers the container rule: a checkout
// created after the roots snapshot was taken still gets its own root because the
// container rule derives it lexically from the configured worktree root.
func TestEvaluatePathScopeContainerRule(t *testing.T) {
	const container = "/state/worktrees/repo-1"
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionAllow},
		{Permission: "write", Pattern: "src/gen/**", Action: ActionDeny},
	}
	scope := PathScope{Cwd: scopeMainRoot, Roots: []string{scopeMainRoot}, Containers: []string{container}}
	for _, tc := range []struct {
		path string
		want Action
	}{
		{path: filepath.Join(container, "brand-new", "src", "gen", "x.go"), want: ActionDeny},
		{path: filepath.Join(container, "brand-new", "src", "ok.go"), want: ActionAllow},
	} {
		if got := rs.EvaluatePath("write", tc.path, scope); got != tc.want {
			t.Errorf("EvaluatePath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// TestNormalizePathInputPrefersCheckoutRootOverCwdSubdir pins the spelling rule
// for a session running from a subdirectory of its checkout: paths must be
// spelled relative to the checkout root, or <checkout>/sub/foo and
// <checkout>/foo both become "foo" and a rule written for one directory applies
// to the other. With no root at all the cwd fallback keeps the old behavior.
func TestNormalizePathInputPrefersCheckoutRootOverCwdSubdir(t *testing.T) {
	scope := PathScope{Cwd: "/repo/sub", Roots: []string{"/repo"}}
	if got := normalizePathInput("/repo/sub/foo.txt", scope); got != "sub/foo.txt" {
		t.Errorf("normalizePathInput = %q, want %q", got, "sub/foo.txt")
	}
	bare := PathScope{Cwd: "/repo/sub"}
	if got := normalizePathInput("/repo/sub/foo.txt", bare); got != "foo.txt" {
		t.Errorf("cwd-only normalizePathInput = %q, want %q", got, "foo.txt")
	}
}

// TestNormalizePathInputContainerDirectChildMustBeADirectory pins the container
// rule's direct-child case: <container>/<name> is a checkout root only when it
// is a directory. A file of that name would otherwise relativize to "." and be
// matched by rules written for the checkout root.
func TestNormalizePathInputContainerDirectChildMustBeADirectory(t *testing.T) {
	container := t.TempDir()
	file := filepath.Join(container, "stray.yaml")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write stray file: %v", err)
	}
	scope := PathScope{Containers: []string{container}}
	if got := normalizePathInput(file, scope); got != filepath.ToSlash(file) {
		t.Errorf("normalizePathInput(%q) = %q, want the absolute spelling kept", file, got)
	}

	checkout := filepath.Join(container, "brand-new")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatalf("mkdir checkout: %v", err)
	}
	if got := normalizePathInput(checkout, scope); got != "." {
		t.Errorf("normalizePathInput(%q) = %q, want %q (a checkout root)", checkout, got, ".")
	}
}

// TestEvaluatePathScopeOutsideRepositoryKeepsAbsoluteSpelling pins the
// boundary: a path outside every root only absolute rules and "*" can match,
// so a relative deny never leaks out of the repository.
func TestEvaluatePathScopeOutsideRepositoryKeepsAbsoluteSpelling(t *testing.T) {
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionAllow},
		{Permission: "write", Pattern: "tmp/**", Action: ActionDeny},
	}
	scope := PathScope{Cwd: scopeMainRoot, Roots: []string{scopeMainRoot}}
	if got := rs.EvaluatePath("write", "/var/tmp/x.go", scope); got != ActionAllow {
		t.Errorf("outside-repository path = %q, want allow (relative rule must not cover it)", got)
	}
}

// TestEvaluatePathScopeResolvesSymlinkedPrefix covers step ④: an absolute path
// spelled through a symlinked prefix still maps onto the root. Skipped where
// the temp dir has no symlinked spelling to exercise.
func TestEvaluatePathScopeResolvesSymlinkedPrefix(t *testing.T) {
	spelled, err := os.MkdirTemp("", "chord-scope")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(spelled) })
	resolved, err := filepath.EvalSymlinks(spelled)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	if resolved == spelled {
		t.Skip("temp dir has no symlinked spelling")
	}
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionAllow},
		{Permission: "write", Pattern: "src/gen/**", Action: ActionDeny},
	}
	scope := PathScope{Cwd: resolved, Roots: []string{resolved}}
	if got := rs.EvaluatePath("write", filepath.Join(spelled, "src", "gen", "x.go"), scope); got != ActionDeny {
		t.Errorf("symlinked spelling = %q, want deny through the resolved root", got)
	}
}
