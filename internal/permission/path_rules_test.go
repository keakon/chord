package permission

import (
	"os"
	"path/filepath"
	"testing"
)

const testCWD = "/repo/session"

// testScope is the cwd-only scope equivalent of the pre-PathScope cwd
// argument: no roots, so path evaluation stays cwd-relative.
var testScope = PathScope{Cwd: testCWD}

func TestEvaluatePathCWDInsideCollapsesToRelative(t *testing.T) {
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionDeny},
		{Permission: "delete", Pattern: "**", Action: ActionAllow},
	}
	// Relative, dot-prefixed, and absolute spellings of one in-cwd file all
	// normalize to the same relative form.
	for _, p := range []string{"src/a.ts", "./src/a.ts", filepath.Join(testCWD, "src/a.ts")} {
		if got := rs.EvaluatePath("delete", p, testScope); got != ActionAllow {
			t.Errorf("EvaluatePath(%q) = %q, want allow", p, got)
		}
	}
	// An out-of-cwd path stays absolute and must not be allowed by a relative rule.
	for _, p := range []string{"/Users/me/other/a.ts", "../other/a.ts"} {
		if got := rs.EvaluatePath("delete", p, testScope); got != ActionDeny {
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
		if got := rs.EvaluatePath("read", p, testScope); got != ActionAllow {
			t.Errorf("EvaluatePath(%q) = %q, want allow (bare '*' matches any path)", p, got)
		}
	}
}

func TestEvaluatePathAbsoluteRuleOnlyMatchesOutsideCWD(t *testing.T) {
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionDeny},
		{Permission: "write", Pattern: "/Users/me/other/**", Action: ActionAllow},
	}
	if got := rs.EvaluatePath("write", "/Users/me/other/plan.md", testScope); got != ActionAllow {
		t.Errorf("absolute rule = %q, want allow for outside-cwd path", got)
	}
	if got := rs.EvaluatePath("write", "plan.md", testScope); got != ActionDeny {
		t.Errorf("absolute rule = %q, want deny for in-cwd path (normalized relative)", got)
	}
}

func TestEvaluatePathSlashStarMatchesAllAbsolutePaths(t *testing.T) {
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionDeny},
		{Permission: "write", Pattern: "/**", Action: ActionAllow},
	}
	for _, p := range []string{"/Users/me/other/plan.md", "../x/plan.md"} {
		if got := rs.EvaluatePath("write", p, testScope); got != ActionAllow {
			t.Errorf("EvaluatePath(%q) = %q, want allow for outside-cwd path under '/**'", p, got)
		}
	}
	if got := rs.EvaluatePath("write", "plan.md", testScope); got != ActionDeny {
		t.Errorf("EvaluatePath(in-cwd) = %q, want deny ('/**' is absolute-scoped, not current dir)", got)
	}
}

func TestEvaluatePathDotSlashPrefixIsRelative(t *testing.T) {
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionDeny},
		{Permission: "delete", Pattern: "./gen/*", Action: ActionAllow},
	}
	if got := rs.EvaluatePath("delete", "gen/client_old.go", testScope); got != ActionAllow {
		t.Errorf("'./gen/*' = %q, want allow (dot prefix is redundant)", got)
	}
	if got := rs.EvaluatePath("delete", "other/client.go", testScope); got != ActionDeny {
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
	if got := rs.EvaluatePath("read", want, testScope); got != ActionAllow {
		t.Errorf("'~/notes/**' = %q, want allow for %q", got, want)
	}
	if got := rs.EvaluatePath("read", "notes/todo.md", testScope); got != ActionDeny {
		t.Errorf("'~/notes/**' = %q, want deny for cwd-relative path", got)
	}
}

func TestEvaluatePathEmptyCWDFallsBackToLexical(t *testing.T) {
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionDeny},
		{Permission: "delete", Pattern: "tmp/*", Action: ActionAsk},
	}
	if got := rs.EvaluatePath("delete", "tmp/build.out", PathScope{}); got != ActionAsk {
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
		if got := rs.EvaluatePath("delete", p, testScope); got != ActionDeny {
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
		if got := rs.EvaluatePath("write", p, testScope); got != ActionDeny {
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
	if got := rs.EvaluatePath("apply_patch", "src/main.go", testScope); got != ActionAllow {
		t.Errorf("apply_patch inherited edit rule = %q, want allow", got)
	}
	if got := rs.EvaluatePath("apply_patch", "vendor/x.go", testScope); got != ActionDeny {
		t.Errorf("apply_patch outside edit scope = %q, want deny", got)
	}
}

func TestLastSpecificToolMatchPath(t *testing.T) {
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionAllow},
		{Permission: "delete", Pattern: "tmp/*", Action: ActionAsk},
	}
	match := rs.LastSpecificToolMatchPath("delete", filepath.Join(testCWD, "tmp/x.txt"), testScope)
	if !match.Found || match.Rule.Action != ActionAsk {
		t.Fatalf("specific delete match = %+v, want tmp/* ask rule", match)
	}
	if match := rs.LastSpecificToolMatchPath("read", "x.txt", testScope); match.Found {
		t.Fatalf("unexpected specific match for read: %+v", match)
	}
}

func TestLastNamedPathMatchSkipsUniversalPatterns(t *testing.T) {
	path := ".chord/notes/task.md"
	for _, pattern := range []string{"*", "./*", "**", "*/*", "**/*", "**/**"} {
		rs := Ruleset{
			{Permission: "*", Pattern: "*", Action: ActionAllow},
			{Permission: "present", Pattern: pattern, Action: ActionAllow},
		}
		if match := rs.LastNamedPathMatch("present", path, testScope); match.Found {
			t.Errorf("pattern %q lifted the path gate: %+v", pattern, match)
		}
	}
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionAllow},
		{Permission: "present", Pattern: ".chord/plans/*", Action: ActionAllow},
	}
	if match := rs.LastNamedPathMatch("present", ".chord/plans/20260101-slug.md", testScope); !match.Found {
		t.Fatal("a named plan path did not match")
	}
	if match := rs.LastNamedPathMatch("present", ".chord/notes/task.md", testScope); match.Found {
		t.Fatalf("a plans-only rule matched a note: %+v", match)
	}
	if match := rs.LastNamedPathMatch("present", "./.chord/plans/20260101-slug.md", testScope); !match.Found {
		t.Fatal("a ./-spelled plan path did not match")
	}
}

func TestEvaluatePathRelativeSubdirRuleScope(t *testing.T) {
	rs := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionDeny},
		{Permission: "write", Pattern: "gen/*", Action: ActionAllow},
	}
	if got := rs.EvaluatePath("write", "gen/client.go", testScope); got != ActionAllow {
		t.Errorf("gen/* = %q, want allow", got)
	}
	if got := rs.EvaluatePath("write", "other/client.go", testScope); got != ActionDeny {
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
	if got := rs.EvaluatePath("view_image", "img/logo.png", testScope); got != ActionAllow {
		t.Errorf("in-cwd view_image = %q, want allow under '**'", got)
	}
	if got := rs.EvaluatePath("view_image", "/Users/me/other/logo.png", testScope); got != ActionDeny {
		t.Errorf("out-of-cwd view_image = %q, want deny under '**'", got)
	}
}

func TestLastSpecificMatchPathMirrorsEvaluatePathSelection(t *testing.T) {
	rs := Ruleset{
		{Permission: "edit", Pattern: "docs/*", Action: ActionAsk},
		{Permission: "apply_patch", Pattern: "src/*", Action: ActionAllow},
	}
	// Counterpart inheritance: an edit rule governs apply_patch on the same
	// path, the way EvaluatePath selects it.
	match := rs.LastSpecificMatchPath("apply_patch", "docs/a.md", testScope)
	if !match.Found || match.Rule.Permission != "edit" || match.Rule.Action != ActionAsk {
		t.Fatalf("match = %#v, want the edit ask rule", match)
	}
	// A same-tool rule wins over the counterpart.
	match = rs.LastSpecificMatchPath("apply_patch", "src/a.md", testScope)
	if !match.Found || match.Rule.Permission != "apply_patch" {
		t.Fatalf("match = %#v, want the apply_patch rule", match)
	}
	// No specific rule matches: the action would come from a wildcard default.
	if match := rs.LastSpecificMatchPath("apply_patch", "other/a.md", testScope); match.Found {
		t.Fatalf("match = %#v, want not found", match)
	}
	// The degenerate scope degrades to lexical matching, which has no
	// counterpart inheritance.
	if match := rs.LastSpecificMatchPath("apply_patch", "docs/a.md", PathScope{}); match.Found {
		t.Fatalf("match = %#v, want not found under a degenerate scope", match)
	}
}

func TestRuleCoversPathMirrorsPathRuleMatching(t *testing.T) {
	rule := Rule{Permission: "delete", Pattern: ".chord/*", Action: ActionAsk}
	if !rule.CoversPath("delete", ".chord/notes/a.md", testScope) {
		t.Fatal("expected the rule to cover its own relative path")
	}
	// An in-scope absolute spelling normalizes to the same relative form.
	if !rule.CoversPath("delete", filepath.Join(testCWD, ".chord/notes/a.md"), testScope) {
		t.Fatal("expected the rule to cover the absolute in-cwd spelling")
	}
	if rule.CoversPath("write", ".chord/notes/a.md", testScope) {
		t.Fatal("expected a different tool not to be covered")
	}
	// Relative rules never match out-of-scope absolute paths.
	if rule.CoversPath("delete", "/elsewhere/.chord/a.md", testScope) {
		t.Fatal("expected an out-of-scope path not to be covered")
	}
	// The literal "*" path rule matches every spelling, including out-of-scope.
	any := Rule{Permission: "delete", Pattern: "*", Action: ActionAsk}
	if !any.CoversPath("delete", "/elsewhere/a.md", testScope) {
		t.Fatal("expected the wildcard path rule to cover any spelling")
	}
}
