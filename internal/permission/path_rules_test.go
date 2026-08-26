package permission

import (
	"os"
	"path/filepath"
	"testing"
)

const testCWD = "/repo/session"

func TestEvaluatePathCWDInsideCollapsesToRelative(t *testing.T) {
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionDeny},
		{Permission: "delete", Pattern: "**", Action: ActionAllow},
	}
	// Relative, dot-prefixed, and absolute spellings of one in-cwd file all
	// normalize to the same relative form.
	for _, p := range []string{"src/a.ts", "./src/a.ts", filepath.Join(testCWD, "src/a.ts")} {
		if got := rs.EvaluatePath("delete", p, testCWD); got != ActionAllow {
			t.Errorf("EvaluatePath(%q) = %q, want allow", p, got)
		}
	}
	// An out-of-cwd path stays absolute and must not be allowed by a relative rule.
	for _, p := range []string{"/Users/me/other/a.ts", "../other/a.ts"} {
		if got := rs.EvaluatePath("delete", p, testCWD); got != ActionDeny {
			t.Errorf("EvaluatePath(%q) = %q, want deny (relative rule must not cover outside cwd)", p, got)
		}
	}
}

func TestEvaluatePathStarMatchesEverySpelling(t *testing.T) {
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionDeny},
		{Permission: "read", Pattern: "*", Action: ActionAllow},
	}
	for _, p := range []string{"foo.go", filepath.Join(testCWD, "foo.go"), "/Users/me/other/foo.go", "../x/foo.go"} {
		if got := rs.EvaluatePath("read", p, testCWD); got != ActionAllow {
			t.Errorf("EvaluatePath(%q) = %q, want allow (bare '*' matches any path)", p, got)
		}
	}
}

func TestEvaluatePathAbsoluteRuleOnlyMatchesOutsideCWD(t *testing.T) {
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionDeny},
		{Permission: "write", Pattern: "/Users/me/other/**", Action: ActionAllow},
	}
	if got := rs.EvaluatePath("write", "/Users/me/other/plan.md", testCWD); got != ActionAllow {
		t.Errorf("absolute rule = %q, want allow for outside-cwd path", got)
	}
	if got := rs.EvaluatePath("write", "plan.md", testCWD); got != ActionDeny {
		t.Errorf("absolute rule = %q, want deny for in-cwd path (normalized relative)", got)
	}
}

func TestEvaluatePathSlashStarMatchesAllAbsolutePaths(t *testing.T) {
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionDeny},
		{Permission: "write", Pattern: "/**", Action: ActionAllow},
	}
	for _, p := range []string{"/Users/me/other/plan.md", "../x/plan.md"} {
		if got := rs.EvaluatePath("write", p, testCWD); got != ActionAllow {
			t.Errorf("EvaluatePath(%q) = %q, want allow for outside-cwd path under '/**'", p, got)
		}
	}
	if got := rs.EvaluatePath("write", "plan.md", testCWD); got != ActionDeny {
		t.Errorf("EvaluatePath(in-cwd) = %q, want deny ('/**' is absolute-scoped, not current dir)", got)
	}
}

func TestEvaluatePathDotSlashPrefixIsRelative(t *testing.T) {
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionDeny},
		{Permission: "delete", Pattern: "./gen/*", Action: ActionAllow},
	}
	if got := rs.EvaluatePath("delete", "gen/client_old.go", testCWD); got != ActionAllow {
		t.Errorf("'./gen/*' = %q, want allow (dot prefix is redundant)", got)
	}
	if got := rs.EvaluatePath("delete", "other/client.go", testCWD); got != ActionDeny {
		t.Errorf("'./gen/*' = %q, want deny outside the gen/ subtree", got)
	}
}

func TestEvaluatePathTildeRuleExpandsToAbsolute(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionDeny},
		{Permission: "read", Pattern: "~/notes/**", Action: ActionAllow},
	}
	want := filepath.Join(home, "notes", "todo.md")
	if got := rs.EvaluatePath("read", want, testCWD); got != ActionAllow {
		t.Errorf("'~/notes/**' = %q, want allow for %q", got, want)
	}
	if got := rs.EvaluatePath("read", "notes/todo.md", testCWD); got != ActionDeny {
		t.Errorf("'~/notes/**' = %q, want deny for cwd-relative path", got)
	}
}

func TestEvaluatePathEmptyCWDFallsBackToLexical(t *testing.T) {
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionDeny},
		{Permission: "delete", Pattern: "tmp/*", Action: ActionAsk},
	}
	if got := rs.EvaluatePath("delete", "tmp/build.out", ""); got != ActionAsk {
		t.Errorf("EvaluatePath without cwd = %q, want lexical ask", got)
	}
}

func TestEvaluatePathAbsoluteSpellingCannotBypassRelativeDeny(t *testing.T) {
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionDeny},
		{Permission: "delete", Pattern: "secret/*", Action: ActionDeny},
	}
	// The in-cwd absolute spelling normalizes to "secret/x" and must hit the
	// deny rule, exactly like the plain relative spelling.
	for _, p := range []string{"secret/plan.txt", filepath.Join(testCWD, "secret/plan.txt")} {
		if got := rs.EvaluatePath("delete", p, testCWD); got != ActionDeny {
			t.Errorf("EvaluatePath(%q) = %q, want deny", p, got)
		}
	}
}

func TestEvaluatePathTraversalNormalizesBeforeMatching(t *testing.T) {
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionDeny},
		{Permission: "write", Pattern: "secret/*", Action: ActionDeny},
	}
	// "src/../secret/x" cleans to "secret/x" inside cwd and must be denied;
	// a traversal that leaves cwd ("secret/../../../etc/x") becomes absolute
	// and stays denied because no absolute allow rule exists.
	for _, p := range []string{"src/../secret/plan.txt", "src/../../secret/plan.txt"} {
		if got := rs.EvaluatePath("write", p, testCWD); got != ActionDeny {
			t.Errorf("EvaluatePath(%q) = %q, want deny", p, got)
		}
	}
}

func TestEvaluatePathEditPatchFamilyFallbackStillApplies(t *testing.T) {
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionDeny},
		{Permission: "edit", Pattern: "src/**", Action: ActionAllow},
	}
	// apply_patch has no explicit rule, so it inherits the edit-family rule.
	if got := rs.EvaluatePath("apply_patch", "src/main.go", testCWD); got != ActionAllow {
		t.Errorf("apply_patch inherited edit rule = %q, want allow", got)
	}
	if got := rs.EvaluatePath("apply_patch", "vendor/x.go", testCWD); got != ActionDeny {
		t.Errorf("apply_patch outside edit scope = %q, want deny", got)
	}
}

func TestLastSpecificToolMatchPath(t *testing.T) {
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionAllow},
		{Permission: "delete", Pattern: "tmp/*", Action: ActionAsk},
	}
	match := rs.LastSpecificToolMatchPath("delete", filepath.Join(testCWD, "tmp/x.txt"), testCWD)
	if !match.Found || match.Rule.Action != ActionAsk {
		t.Fatalf("specific delete match = %+v, want tmp/* ask rule", match)
	}
	if match := rs.LastSpecificToolMatchPath("read", "x.txt", testCWD); match.Found {
		t.Fatalf("unexpected specific match for read: %+v", match)
	}
}

func TestEvaluatePathRelativeSubdirRuleScope(t *testing.T) {
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionDeny},
		{Permission: "write", Pattern: "gen/*", Action: ActionAllow},
	}
	if got := rs.EvaluatePath("write", "gen/client.go", testCWD); got != ActionAllow {
		t.Errorf("gen/* = %q, want allow", got)
	}
	if got := rs.EvaluatePath("write", "other/client.go", testCWD); got != ActionDeny {
		t.Errorf("gen/* = %q, want deny outside the gen/ subtree", got)
	}
}

func TestClassifyPathRuleWindowsHomeBackslashIsAbsolute(t *testing.T) {
	kind, pattern := classifyPathRuleForOS(`~\notes\**`, true)
	if kind != pathRuleAbsolute {
		t.Fatalf("classifyPathRuleForOS(`~\\notes\\**`, windows) kind = %v, want absolute", kind)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	want := filepath.ToSlash(filepath.Clean(filepath.Join(home, `notes\**`)))
	if pattern != want {
		t.Fatalf("normalized pattern = %q, want %q", pattern, want)
	}
}

func TestClassifyPathRuleUnixHomeBackslashStaysRelative(t *testing.T) {
	kind, _ := classifyPathRuleForOS(`~\notes\**`, false)
	if kind != pathRuleRelative {
		t.Fatalf("classifyPathRuleForOS(`~\\notes\\**`, unix) kind = %v, want relative", kind)
	}
}

func TestEvaluatePathViewImageCWDScoped(t *testing.T) {
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionDeny},
		{Permission: "view_image", Pattern: "**", Action: ActionAllow},
	}
	if got := rs.EvaluatePath("view_image", "img/logo.png", testCWD); got != ActionAllow {
		t.Errorf("in-cwd view_image = %q, want allow under '**'", got)
	}
	if got := rs.EvaluatePath("view_image", "/Users/me/other/logo.png", testCWD); got != ActionDeny {
		t.Errorf("out-of-cwd view_image = %q, want deny under '**'", got)
	}
}
