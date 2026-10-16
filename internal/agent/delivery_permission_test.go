package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestPresentPermissionAggregatesEveryTarget(t *testing.T) {
	// Present resolves symlinks, so use the same canonical root for path rules.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"allow.md", "deny.md"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("text"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	args := json.RawMessage(`{"files":[{"path":"allow.md"},{"path":"deny.md"}]}`)
	rules := permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}, {Permission: tools.NamePresent, Pattern: "deny.md", Action: permission.ActionDeny}}
	result := evaluateToolPermissionInDir(rules, tools.NamePresent, args, permission.PathScope{Cwd: root})
	if result.Action != permission.ActionDeny {
		t.Fatalf("partial denial ignored: %#v", result)
	}
	rules[1].Action = permission.ActionAsk
	result = evaluateToolPermissionInDir(rules, tools.NamePresent, args, permission.PathScope{Cwd: root})
	if result.Action != permission.ActionAsk || len(result.NeedsApprovalPaths) != 1 {
		t.Fatalf("ask was not aggregated: %#v", result)
	}
	rules = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionDeny}, {Permission: tools.NameRead, Pattern: "*", Action: permission.ActionAllow}}
	result = evaluateToolPermissionInDir(rules, tools.NamePresent, args, permission.PathScope{Cwd: root})
	if result.Action != permission.ActionDeny {
		t.Fatal("read permission authorized present")
	}
}

func TestPresentPermissionDeniesChordInternalByDefault(t *testing.T) {
	// Present resolves symlinks, so use the same canonical root for path rules.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"report.md", ".chord/notes/task.md", ".chord/plans/20260101-slug.md"} {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("text"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	scope := permission.PathScope{Cwd: root}
	allowAll := permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}

	ordinary := json.RawMessage(`{"files":[{"path":"report.md"}]}`)
	if result := evaluateToolPermissionInDir(allowAll, tools.NamePresent, ordinary, scope); result.Action != permission.ActionAllow {
		t.Fatalf("ordinary file denied under allow-all: %#v", result)
	}

	for _, tc := range []struct {
		name string
		args json.RawMessage
	}{
		{"notes", json.RawMessage(`{"files":[{"path":".chord/notes/task.md"}]}`)},
		{"plans", json.RawMessage(`{"files":[{"path":".chord/plans/20260101-slug.md"}]}`)},
		{"mixed batch", json.RawMessage(`{"files":[{"path":"report.md"},{"path":".chord/notes/task.md"}]}`)},
	} {
		if result := evaluateToolPermissionInDir(allowAll, tools.NamePresent, tc.args, scope); result.Action != permission.ActionDeny {
			t.Fatalf("%s: action = %q, want deny", tc.name, result.Action)
		}
	}

	// A present-specific rule still wins, so a plan file the user explicitly
	// asked to receive keeps working. The allowance must not leak to notes,
	// for which no specific rule exists.
	explicitPlans := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
		{Permission: tools.NamePresent, Pattern: ".chord/plans/*", Action: permission.ActionAllow},
	}
	plans := json.RawMessage(`{"files":[{"path":".chord/plans/20260101-slug.md"}]}`)
	if result := evaluateToolPermissionInDir(explicitPlans, tools.NamePresent, plans, scope); result.Action != permission.ActionAllow {
		t.Fatalf("explicitly allowed plan denied: %#v", result)
	}
	notes := json.RawMessage(`{"files":[{"path":".chord/notes/task.md"}]}`)
	if result := evaluateToolPermissionInDir(explicitPlans, tools.NamePresent, notes, scope); result.Action != permission.ActionDeny {
		t.Fatalf("notes leaked through a plans-only allowance: %#v", result)
	}
}

func TestPresentPermissionIgnoresChordInternalAboveWorkspaceRoot(t *testing.T) {
	// worktree.root: .chord/worktrees puts the checkout itself under a .chord
	// directory. Where the session works must not decide what it may deliver,
	// so an ordinary file stays deliverable while the checkout's own working
	// state stays denied.
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, ".chord", "worktrees", "wt")
	for _, name := range []string{"report.md", ".chord/notes/task.md"} {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("text"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	scope := permission.PathScope{Cwd: root}
	allowAll := permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}
	if result := evaluateToolPermissionInDir(allowAll, tools.NamePresent, json.RawMessage(`{"files":[{"path":"report.md"}]}`), scope); result.Action != permission.ActionAllow {
		t.Fatalf("ordinary file denied in a checkout under .chord: %#v", result)
	}
	if result := evaluateToolPermissionInDir(allowAll, tools.NamePresent, json.RawMessage(`{"files":[{"path":".chord/notes/task.md"}]}`), scope); result.Action != permission.ActionDeny {
		t.Fatalf("checkout's own working state delivered: %#v", result)
	}
}

// The deliverable checkout is the one that contains the file, not the one the
// agent works in: the main agent delivering a linked worktree's ordinary file
// must not see the .chord segment of the worktree's own location.
func TestPresentPermissionDeliversWorktreeFileFromMainCheckout(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mainRoot := filepath.Join(base, "repo")
	worktree := filepath.Join(mainRoot, ".chord", "worktrees", "wt")
	for _, p := range []string{
		filepath.Join(worktree, "report.md"),
		filepath.Join(worktree, ".chord", "notes", "task.md"),
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("text"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	scope := permission.PathScope{Cwd: mainRoot, Roots: []string{worktree, mainRoot}}
	allowAll := permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}
	report := json.RawMessage(`{"files":[{"path":".chord/worktrees/wt/report.md"}]}`)
	if result := evaluateToolPermissionInDir(allowAll, tools.NamePresent, report, scope); result.Action != permission.ActionAllow {
		t.Fatalf("worktree's ordinary file denied for the main agent: %#v", result)
	}
	notes := json.RawMessage(`{"files":[{"path":".chord/worktrees/wt/.chord/notes/task.md"}]}`)
	if result := evaluateToolPermissionInDir(allowAll, tools.NamePresent, notes, scope); result.Action != permission.ActionDeny {
		t.Fatalf("worktree's working state delivered: %#v", result)
	}
}

// A blanket `present: allow` — whether its path pattern is `*` or `**/*` —
// names the tool but no path, and must not lift the .chord default denial;
// naming the file's path does.
func TestPresentPermissionRequiresANamedPathToLiftChordGate(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, ".chord", "notes", "task.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("text"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"files":[{"path":".chord/notes/task.md"}]}`)
	scope := permission.PathScope{Cwd: root}
	for _, blanketPattern := range []string{"*", "**/*"} {
		blanket := permission.Ruleset{
			{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
			{Permission: tools.NamePresent, Pattern: blanketPattern, Action: permission.ActionAllow},
		}
		if result := evaluateToolPermissionInDir(blanket, tools.NamePresent, args, scope); result.Action != permission.ActionDeny {
			t.Fatalf("blanket present pattern %q lifted the .chord gate: %#v", blanketPattern, result)
		}
	}
	named := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
		{Permission: tools.NamePresent, Pattern: ".chord/notes/*", Action: permission.ActionAllow},
	}
	if result := evaluateToolPermissionInDir(named, tools.NamePresent, args, scope); result.Action != permission.ActionAllow {
		t.Fatalf("named path did not lift the .chord gate: %#v", result)
	}
}
