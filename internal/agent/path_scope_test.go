package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func writePathArgs(t *testing.T, path string) json.RawMessage {
	t.Helper()
	return json.RawMessage(fmt.Sprintf(`{"path":%q}`, path))
}

// TestEffectivePathScopeUsesResolverRoots covers the resolver-root wiring: the
// resolver injected by cmd/chord lands in the scope path-taking tools evaluate
// against, so a targeted relative deny catches the same repository file even
// when it lives in another checkout.
func TestEffectivePathScopeUsesResolverRoots(t *testing.T) {
	root := t.TempDir()
	a := newTestMainAgent(t, root)

	mainCheckout := filepath.Join(root, "main")
	wtCheckout := filepath.Join(mainCheckout, ".chord", "worktrees", "x")
	a.SetPathRootsResolver(func() ([]string, []string) {
		return []string{wtCheckout, mainCheckout}, []string{filepath.Join(root, "state", "worktrees", "repo-1")}
	})
	a.refreshPathRoots()

	scope := a.effectivePathScope()
	if scope.Cwd != a.effectiveToolBaseDir() {
		t.Fatalf("scope cwd = %q, want tool base dir %q", scope.Cwd, a.effectiveToolBaseDir())
	}
	if len(scope.Roots) != 2 || len(scope.Containers) != 1 {
		t.Fatalf("scope = %+v, want 2 roots and 1 container", scope)
	}

	a.ruleset = permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
		{Permission: "write", Pattern: "src/gen/**", Action: permission.ActionDeny},
	}
	got := evaluateToolPermissionInDir(a.effectiveRuleset(), tools.NameWrite,
		writePathArgs(t, filepath.Join(mainCheckout, "src", "gen", "x.go")), a.effectivePathScope())
	if got.Action != permission.ActionDeny {
		t.Errorf("write to another checkout's src/gen = %q, want deny", got.Action)
	}
}

// TestEffectivePathScopeContainerCoversNewCheckout covers the container rule end
// to end: a checkout created after the roots snapshot was taken still maps onto
// its own root because the container rule derives it from the configured
// worktree root.
func TestEffectivePathScopeContainerCoversNewCheckout(t *testing.T) {
	root := t.TempDir()
	a := newTestMainAgent(t, root)

	mainCheckout := filepath.Join(root, "main")
	container := filepath.Join(root, "state", "worktrees", "repo-1")
	// The snapshot deliberately omits the checkout created after it was taken.
	a.SetPathRootsResolver(func() ([]string, []string) {
		return []string{mainCheckout}, []string{container}
	})
	a.refreshPathRoots()

	a.ruleset = permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
		{Permission: "write", Pattern: "src/gen/**", Action: permission.ActionDeny},
	}
	createdLater := filepath.Join(container, "created-later", "src", "gen", "y.go")
	got := evaluateToolPermissionInDir(a.effectiveRuleset(), tools.NameWrite,
		writePathArgs(t, createdLater), a.effectivePathScope())
	if got.Action != permission.ActionDeny {
		t.Errorf("write into a checkout missing from the snapshot = %q, want deny via the container rule", got.Action)
	}
}

// TestPathScopeAllowsAnotherCheckoutWhenRulesAllow pins the accepted tradeoff:
// merged policy roots make per-checkout limits inexpressible, so a
// broad allow reaches every checkout of the repository.
func TestPathScopeAllowsAnotherCheckoutWhenRulesAllow(t *testing.T) {
	root := t.TempDir()
	a := newTestMainAgent(t, root)

	mainCheckout := filepath.Join(root, "main")
	wtCheckout := filepath.Join(mainCheckout, ".chord", "worktrees", "x")
	a.SetPathRootsResolver(func() ([]string, []string) {
		return []string{mainCheckout, wtCheckout}, nil
	})
	a.refreshPathRoots()
	a.cachedWorkDir = wtCheckout
	a.ruleset = permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionDeny},
		{Permission: "write", Pattern: "src/**", Action: permission.ActionAllow},
	}

	got := evaluateToolPermissionInDir(a.effectiveRuleset(), tools.NameWrite,
		writePathArgs(t, filepath.Join(mainCheckout, "src", "x.go")), a.effectivePathScope())
	if got.Action != permission.ActionAllow {
		t.Errorf("write to the main checkout from a worktree session = %q, want allow (isolation is not a goal)", got.Action)
	}
}
