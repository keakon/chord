package agent

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

const pathTestCWD = "/repo/session"

// pathTestScope is the cwd-only scope equivalent of the pre-PathScope cwd
// argument: no repository roots, so path rules stay cwd-relative.
var pathTestScope = permission.PathScope{Cwd: pathTestCWD}

func TestEvaluateToolPermissionInDirDeleteAbsoluteSpellingHitsRelativeDeny(t *testing.T) {
	node := parsePermissionNode(t, `
"*": deny
delete:
  "secret/*": deny
`)
	ruleset := permission.ParsePermission(&node)
	// The in-cwd absolute spelling must be caught by the relative deny rule,
	// exactly like the plain relative spelling. MatchArgument reports the
	// spelling the model supplied.
	for _, p := range []string{"secret/plan.txt", filepath.Join(pathTestCWD, "secret/plan.txt")} {
		args := mustDeletePermissionArgs(t, []string{p})
		got := evaluateToolPermissionInDir(ruleset, "delete", args, pathTestScope)
		if got.Action != permission.ActionDeny || got.MatchArgument != p {
			t.Fatalf("decision = %#v, want deny of %q", got, p)
		}
	}
}

func TestEvaluateToolPermissionInDirDeleteRelativeRuleNotCoverOutsideCWD(t *testing.T) {
	node := parsePermissionNode(t, `
"*": deny
delete:
  "**": allow
`)
	ruleset := permission.ParsePermission(&node)
	// "**" scopes to the current directory; a traversal that leaves cwd stays
	// absolute and must fall through to the wildcard deny.
	args := mustDeletePermissionArgs(t, []string{"../shared/tmp.go"})
	got := evaluateToolPermissionInDir(ruleset, "delete", args, pathTestScope)
	if got.Action != permission.ActionDeny {
		t.Fatalf("decision = %#v, want deny for out-of-cwd delete under '**'", got)
	}
}

func TestEvaluateToolPermissionInDirDeleteOutsideCWDAbsoluteRule(t *testing.T) {
	node := parsePermissionNode(t, `
"*": deny
delete:
  "/Users/me/shared/**": allow
`)
	ruleset := permission.ParsePermission(&node)
	args := mustDeletePermissionArgs(t, []string{"/Users/me/shared/tmp.go"})
	got := evaluateToolPermissionInDir(ruleset, "delete", args, pathTestScope)
	if got.Action != permission.ActionAllow {
		t.Fatalf("decision = %#v, want allow for out-of-cwd absolute path", got)
	}
	// The same absolute rule must not leak into the current directory.
	inCWD := mustDeletePermissionArgs(t, []string{filepath.Join(pathTestCWD, "src/a.go")})
	if got := evaluateToolPermissionInDir(ruleset, "delete", inCWD, pathTestScope); got.Action != permission.ActionDeny {
		t.Fatalf("decision = %#v, want deny for in-cwd path (normalized relative)", got)
	}
}

func TestEvaluateToolPermissionInDirReadCWDScoped(t *testing.T) {
	node := parsePermissionNode(t, `
"*": deny
read:
  "**": allow
`)
	ruleset := permission.ParsePermission(&node)
	inCWD := json.RawMessage(`{"path":"foo.go"}`)
	if got := evaluateToolPermissionInDir(ruleset, "read", inCWD, pathTestScope); got.Action != permission.ActionAllow {
		t.Fatalf("in-cwd read = %#v, want allow under '**'", got)
	}
	outCWD := json.RawMessage(`{"path":"/Users/me/other/foo.go"}`)
	if got := evaluateToolPermissionInDir(ruleset, "read", outCWD, pathTestScope); got.Action != permission.ActionDeny {
		t.Fatalf("out-of-cwd read = %#v, want deny under '**'", got)
	}
}

func TestEvaluateToolPermissionInDirEmptyCWDDegradesToLexical(t *testing.T) {
	node := parsePermissionNode(t, `
"*": deny
delete:
  "tmp/*": ask
`)
	ruleset := permission.ParsePermission(&node)
	args := mustDeletePermissionArgs(t, []string{"tmp/build.out"})
	lexical := evaluateToolPermission(ruleset, "delete", args)
	inDir := evaluateToolPermissionInDir(ruleset, "delete", args, permission.PathScope{})
	if lexical.Action != inDir.Action || lexical.MatchArgument != inDir.MatchArgument {
		t.Fatalf("empty-cwd InDir = %#v, want identical to lexical %#v", inDir, lexical)
	}
}

func TestEvaluateToolPermissionInDirApplyPatchDeleteLayerCWD(t *testing.T) {
	node := parsePermissionNode(t, `
"*": deny
apply_patch: allow
delete:
  "tmp/*": deny
`)
	ruleset := permission.ParsePermission(&node)
	for name, tc := range map[string]struct {
		path string
		want string
	}{
		"relative": {path: "tmp/obsolete.txt", want: "tmp/obsolete.txt"},
		"absolute": {path: filepath.Join(pathTestCWD, "tmp/obsolete.txt"), want: filepath.Join(pathTestCWD, "tmp/obsolete.txt")},
	} {
		t.Run(name, func(t *testing.T) {
			patch := "*** Begin Patch\n*** Update File: src/main.go\n@@\n-old\n+new\n*** Delete File: " + tc.path + "\n*** End Patch"
			args, err := json.Marshal(map[string]string{"patch": patch})
			if err != nil {
				t.Fatal(err)
			}
			got := evaluateToolPermissionInDir(ruleset, tools.NameApplyPatch, args, pathTestScope)
			if got.Action != permission.ActionDeny || got.MatchArgument != tc.want {
				t.Fatalf("decision = %#v, want deny of %q", got, tc.want)
			}
		})
	}
}

func TestEvaluateToolPermissionInDirWriteEditCWDScoped(t *testing.T) {
	node := parsePermissionNode(t, `
"*": deny
write:
  "src/**": allow
edit:
  "src/**": allow
`)
	ruleset := permission.ParsePermission(&node)
	writeIn := json.RawMessage(`{"path":"src/gen.go","content":"package gen"}`)
	if got := evaluateToolPermissionInDir(ruleset, "write", writeIn, pathTestScope); got.Action != permission.ActionAllow {
		t.Fatalf("in-cwd write = %#v, want allow under 'src/**'", got)
	}
	writeOut := json.RawMessage(`{"path":"/Users/me/other/gen.go","content":"package gen"}`)
	if got := evaluateToolPermissionInDir(ruleset, "write", writeOut, pathTestScope); got.Action != permission.ActionDeny {
		t.Fatalf("out-of-cwd write = %#v, want deny (relative rule scoped to cwd)", got)
	}
	editIn := json.RawMessage(`{"path":"src/main.go","old_string":"a","new_string":"b"}`)
	if got := evaluateToolPermissionInDir(ruleset, "edit", editIn, pathTestScope); got.Action != permission.ActionAllow {
		t.Fatalf("in-cwd edit = %#v, want allow under 'src/**'", got)
	}
	editOut := json.RawMessage(`{"path":"/Users/me/other/main.go","old_string":"a","new_string":"b"}`)
	if got := evaluateToolPermissionInDir(ruleset, "edit", editOut, pathTestScope); got.Action != permission.ActionDeny {
		t.Fatalf("out-of-cwd edit = %#v, want deny (relative rule scoped to cwd)", got)
	}
}
