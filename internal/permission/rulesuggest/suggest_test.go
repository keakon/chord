package rulesuggest

import (
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/toolname"
)

// cwdScope is the cwd-only scope equivalent of the pre-PathScope cwd argument:
// no checkout roots, so path evaluation stays cwd-relative.
func cwdScope(cwd string) permission.PathScope {
	return permission.PathScope{Cwd: cwd}
}

func TestSuggestRulePatterns_BashSimple(t *testing.T) {
	candidates := Suggest("shell", `{"command":"git log --oneline"}`, nil, nil, cwdScope("/home/user/project"))
	if len(candidates) == 0 {
		t.Fatal("expected candidates for simple shell command")
	}

	// The two-word prefix is the narrowest rule that matches the command as
	// written, so it leads and is default; no exact-command candidate repeats
	// it.
	if candidates[0].Pattern != "git log *" || !candidates[0].Default {
		t.Errorf("expected default 'git log *' first, got %#v", candidates[0])
	}
	for _, c := range candidates {
		if c.Pattern == "git log --oneline" {
			t.Errorf("unexpected exact-command candidate: %#v", candidates)
		}
	}
}

func TestSuggestRulePatterns_BashNeedsApproval(t *testing.T) {
	candidates := Suggest("shell", `{"command":"git log --oneline && git status"}`, []string{"git status"}, nil, cwdScope("/home/user/project"))
	if len(candidates) == 0 {
		t.Fatal("expected candidates")
	}
	// The seed subcommand is generalized to its prefix, which is the narrowest
	// rule that matches it as written.
	if candidates[0].Pattern != "git status *" || !candidates[0].Default {
		t.Errorf("expected default 'git status *' first when needsApproval is set, got %#v", candidates[0])
	}
}

func TestSuggestRulePatterns_BashDestructiveSingleTarget(t *testing.T) {
	candidates := Suggest("shell", `{"command":"rm -rf /tmp/foo"}`, nil, nil, cwdScope("/home/user/project"))
	if len(candidates) == 0 {
		t.Fatal("expected candidates for a destructive command")
	}
	// A destructive command gets the same ladder as any other: the prefix that
	// matches it as written is default, then the command word and the catch-all.
	if got := candidates[0].Pattern; got != "rm -rf *" || !candidates[0].Default || !candidates[0].Broad {
		t.Fatalf("first candidate = %#v, want default rm -rf *", candidates[0])
	}
	for _, want := range []string{"rm *", "*"} {
		found := false
		for _, c := range candidates {
			if c.Pattern == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected %q among %#v", want, candidates)
		}
	}
	for _, c := range candidates {
		if c.Pattern == "rm -rf /tmp/foo" {
			t.Fatalf("unexpected exact-command candidate: %#v", candidates)
		}
	}
}

func TestSuggestRulePatterns_BashBatchUsesCommandPrefix(t *testing.T) {
	for _, command := range []string{"rm -f /tmp/a /tmp/b", "rm -f /tmp/a /var/log/b /var/log/c"} {
		args, err := json.Marshal(map[string]string{"command": command})
		if err != nil {
			t.Fatal(err)
		}
		candidates := Suggest("shell", string(args), nil, nil, cwdScope(""))
		if len(candidates) != 3 || candidates[0].Pattern != "rm -f *" || !candidates[0].Default {
			t.Fatalf("command %q: candidates = %#v", command, candidates)
		}
		if !candidates[0].Broad || candidates[0].Summary != "same command prefix, any paths or arguments" {
			t.Fatalf("misleading scope: %#v", candidates[0])
		}
		for _, candidate := range candidates {
			rules := permission.Ruleset{{Permission: "shell", Pattern: candidate.Pattern, Action: permission.ActionAllow}}
			if rules.Evaluate("shell", command) != permission.ActionAllow {
				t.Fatalf("candidate %#v does not cover %q", candidate, command)
			}
		}
	}
}

func TestSuggestRulePatterns_BashQuotedBatchFallsBackToPrefix(t *testing.T) {
	candidates := Suggest("shell",
		`{"command":"rm -f \"/tmp/a b.txt\" /tmp/c.txt"}`,
		nil, nil, cwdScope(""))
	if len(candidates) == 0 {
		t.Fatal("expected candidates for quoted path batch")
	}
	// Splitting the quoted text with the ordinary whitespace rules would not
	// spell the command, so no directory rule is derived; the prefix that
	// matches the command as written leads instead.
	if got := candidates[0].Pattern; got != "rm -f *" || !candidates[0].Default {
		t.Fatalf("first candidate = %#v, want default rm -f *", candidates[0])
	}
}

func TestSuggestRulePatterns_BashComplex(t *testing.T) {
	candidates := Suggest("shell", `{"command":"git log | grep foo"}`, nil, nil, cwdScope("/home/user/project"))
	if len(candidates) == 0 {
		t.Fatal("expected candidates for complex command")
	}
	// Complex command: should only have literal + very broad
	if len(candidates) > 2 {
		t.Errorf("expected at most 2 candidates for complex command, got %d", len(candidates))
	}
	if candidates[0].Pattern != "git log | grep foo" {
		t.Errorf("expected literal pattern first, got %q", candidates[0].Pattern)
	}
}

func TestSuggestRulePatterns_WriteFile(t *testing.T) {
	candidates := Suggest("write", `{"path":"internal/tui/app.go","content":"..."}`, nil, nil, cwdScope("/home/user/project"))
	if len(candidates) == 0 {
		t.Fatal("expected candidates for Write tool")
	}

	// First should be literal
	if candidates[0].Pattern != "internal/tui/app.go" {
		t.Errorf("expected literal path first, got %q", candidates[0].Pattern)
	}

	// Should have dir/*
	found := false
	for _, c := range candidates {
		if c.Pattern == "internal/tui/*" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'internal/tui/*' candidate")
	}
}

func TestSuggestRulePatterns_EditFile(t *testing.T) {
	candidates := Suggest(toolname.Edit, `{"path":"docs/README.md","patch":"@@\n-old\n+new\n"}`, nil, nil, cwdScope("/home/user/project"))
	if len(candidates) == 0 {
		t.Fatal("expected candidates for Edit tool")
	}

	// Should have *.md pattern
	found := false
	for _, c := range candidates {
		if c.Pattern == "**/*.md" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected '**/*.md' candidate")
	}
}

func TestSuggestRulePatterns_WebFetch(t *testing.T) {
	candidates := Suggest("web_fetch", `{"url":"https://example.com/api/v1/data"}`, nil, nil, cwdScope(""))
	if len(candidates) == 0 {
		t.Fatal("expected candidates for WebFetch")
	}

	// First should be literal URL
	if candidates[0].Pattern != "https://example.com/api/v1/data" {
		t.Errorf("expected literal URL first, got %q", candidates[0].Pattern)
	}

	// Should have path/* and host/*
	foundPath := false
	foundHost := false
	for _, c := range candidates {
		if c.Pattern == "https://example.com/api/v1/*" {
			foundPath = true
		}
		if c.Pattern == "https://example.com/*" {
			foundHost = true
		}
	}
	if !foundPath {
		t.Error("expected 'https://example.com/api/v1/*' candidate")
	}
	if !foundHost {
		t.Error("expected 'https://example.com/*' candidate")
	}
}

func TestSuggestRulePatterns_WebFetchWithPortAndQuery(t *testing.T) {
	candidates := Suggest("web_fetch", `{"url":"https://example.com:8443/api/v1/data?q=ok"}`, nil, nil, cwdScope(""))
	foundPath := false
	foundHost := false
	for _, c := range candidates {
		if c.Pattern == "https://example.com:8443/api/v1/*" {
			foundPath = true
		}
		if c.Pattern == "https://example.com:8443/*" {
			foundHost = true
		}
	}
	if !foundPath || !foundHost {
		t.Fatalf("expected path and host candidates, got %#v", candidates)
	}
}

func TestSuggestRulePatterns_Delete(t *testing.T) {
	candidates := Suggest("delete", `{"paths":["tmp/foo.log"]}`, []string{"tmp/foo.log"}, nil, cwdScope(""))
	if len(candidates) == 0 {
		t.Fatal("expected directory candidates for Delete tool")
	}
	if candidates[0].Pattern != "tmp/*" || !candidates[0].Default {
		t.Fatalf("first candidate = %+v, want parent directory default", candidates[0])
	}
	for _, candidate := range candidates {
		if candidate.Pattern == "tmp/foo.log" {
			t.Fatalf("delete candidates should omit exact paths: %+v", candidates)
		}
	}
	// A global wildcard must always be available so the user can add a broad
	// allow rule instead of re-confirming per subfolder.
	foundWildcard := false
	for _, c := range candidates {
		if c.Pattern == "*" {
			foundWildcard = true
			break
		}
	}
	if !foundWildcard {
		t.Fatalf("delete candidates must include a global '*' catch-all: %+v", candidates)
	}
}

func TestSuggestRulePatterns_DeleteWithinCWDOffersCurDirCandidateNotDefault(t *testing.T) {
	candidates := Suggest("delete", `{"paths":["nested/deep/tmp.go"]}`, []string{"nested/deep/tmp.go"}, nil, cwdScope("/home/user/project"))
	foundCwd := false
	for _, c := range candidates {
		if c.Pattern == "**" {
			if c.Default {
				t.Fatalf("cwd-scoped candidate must not be pre-selected by default: %+v", c)
			}
			foundCwd = true
			break
		}
	}
	if !foundCwd {
		t.Fatalf("expected cwd-scoped candidate for in-cwd delete: %+v", candidates)
	}
}

func TestSuggestRulePatterns_DeleteOutsideCWDNoCurDirCandidate(t *testing.T) {
	candidates := Suggest("delete", `{"paths":["../shared/tmp.go"]}`, []string{"../shared/tmp.go"}, nil, cwdScope("/home/user/project"))
	for _, c := range candidates {
		if c.Pattern == "**" {
			t.Fatalf("unexpected cwd-scoped candidate for outside-cwd delete: %+v", candidates)
		}
	}
}

func TestSuggestRulePatterns_DeleteAbsoluteInCWDCollapsesToRelative(t *testing.T) {
	candidates := Suggest("delete", `{"paths":["/home/user/project/tmp/foo.log"]}`, []string{"/home/user/project/tmp/foo.log"}, nil, cwdScope("/home/user/project"))
	if len(candidates) == 0 {
		t.Fatal("expected candidates")
	}
	if candidates[0].Pattern != "tmp/*" || !candidates[0].Default {
		t.Fatalf("first candidate = %+v, want cwd-relative parent directory", candidates[0])
	}
}

func TestSuggestRulePatterns_DeleteOutsideCWDStaysAbsolute(t *testing.T) {
	candidates := Suggest("delete", `{"paths":["/Users/me/shared/tmp.go"]}`, []string{"/Users/me/shared/tmp.go"}, nil, cwdScope("/home/user/project"))
	if len(candidates) == 0 || candidates[0].Pattern != "/Users/me/shared/*" {
		t.Fatalf("first candidate = %+v, want absolute parent directory", candidates)
	}
}

func TestSuggestRulePatterns_DeleteRanksDirectoriesByPathCount(t *testing.T) {
	candidates := Suggest("delete", `{"paths":["artifacts/report-1.json","artifacts/report-2.json","artifacts/report-3.json","/tmp/old-report.json"]}`, []string{"/tmp/old-report.json", "artifacts/report-1.json", "artifacts/report-2.json", "artifacts/report-3.json"}, nil, cwdScope("/home/user/project"))
	want := []string{"artifacts/*", "/tmp/*", "*"}
	if len(candidates) != len(want) {
		t.Fatalf("candidate count = %d, want %d: %+v", len(candidates), len(want), candidates)
	}
	for i, pattern := range want {
		if candidates[i].Pattern != pattern {
			t.Fatalf("candidate[%d] = %q, want %q: %+v", i, candidates[i].Pattern, pattern, candidates)
		}
	}
	if !candidates[0].Default {
		t.Fatalf("most common directory should be the default: %+v", candidates)
	}
}

func TestSuggestRulePatterns_DeleteReservesCWDAndGlobalCandidates(t *testing.T) {
	paths := []string{
		"alpha/one.txt",
		"bravo/two.txt",
		"charlie/three.txt",
		"delta/four.txt",
		"echo/five.txt",
		"foxtrot/six.txt",
	}
	candidates := Suggest("delete", `{"paths":["fallback.txt"]}`, paths, nil, cwdScope("/home/user/project"))
	if len(candidates) != maxPatternCandidates {
		t.Fatalf("candidate count = %d, want %d: %+v", len(candidates), maxPatternCandidates, candidates)
	}
	if got := candidates[len(candidates)-2].Pattern; got != "**" {
		t.Fatalf("penultimate candidate = %q, want cwd-wide '**': %+v", got, candidates)
	}
	if got := candidates[len(candidates)-1].Pattern; got != "*" {
		t.Fatalf("last candidate = %q, want global '*': %+v", got, candidates)
	}
	for _, candidate := range candidates[len(candidates)-2:] {
		if candidate.Default {
			t.Fatalf("reserved broad candidate should not be the default: %+v", candidates)
		}
	}
}

func TestSuggestRulePatterns_DeleteMixedCWDTargetsOmitsCWDWildcard(t *testing.T) {
	candidates := Suggest("delete", `{"paths":["artifacts/report.json","/tmp/old-report.json"]}`, []string{"artifacts/report.json", "/tmp/old-report.json"}, nil, cwdScope("/home/user/project"))
	for _, candidate := range candidates {
		if candidate.Pattern == "**" {
			t.Fatalf("mixed in-cwd and out-of-cwd targets should omit '**': %+v", candidates)
		}
	}
}

func TestSuggestRulePatterns_DeleteAllowedOutsideTargetStillOmitsCWDWildcard(t *testing.T) {
	candidates := Suggest("delete", `{"paths":["artifacts/report.json","/tmp/already-allowed.json"]}`, []string{"artifacts/report.json"}, nil, cwdScope("/home/user/project"))
	for _, candidate := range candidates {
		if candidate.Pattern == "**" {
			t.Fatalf("an out-of-cwd requested target should omit '**' even when already allowed: %+v", candidates)
		}
	}
}

func TestSuggestRulePatterns_DeleteCWDRootFileDoesNotPreselectBroadRule(t *testing.T) {
	candidates := Suggest("delete", `{"paths":["obsolete.txt"]}`, []string{"obsolete.txt"}, nil, cwdScope("/home/user/project"))
	want := []string{"**", "*"}
	if len(candidates) != len(want) {
		t.Fatalf("candidate count = %d, want %d: %+v", len(candidates), len(want), candidates)
	}
	for i, pattern := range want {
		if candidates[i].Pattern != pattern {
			t.Fatalf("candidate[%d] = %q, want %q: %+v", i, candidates[i].Pattern, pattern, candidates)
		}
		if candidates[i].Default {
			t.Fatalf("broad candidate should not be preselected: %+v", candidates)
		}
	}
}

func TestSuggestRulePatterns_Read(t *testing.T) {
	candidates := Suggest("read", `{"path":"foo.go"}`, nil, nil, cwdScope(""))
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate for Read, got %d", len(candidates))
	}
	if candidates[0].Pattern != "*" {
		t.Errorf("expected '*' pattern for Read, got %q", candidates[0].Pattern)
	}
}

func TestSuggestRulePatterns_Grep(t *testing.T) {
	candidates := Suggest("grep", `{"pattern":"TODO"}`, nil, nil, cwdScope(""))
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate for Grep, got %d", len(candidates))
	}
	if candidates[0].Pattern != "*" {
		t.Errorf("expected '*' pattern for Grep, got %q", candidates[0].Pattern)
	}
}

func TestSuggestRulePatterns_BashSudo(t *testing.T) {
	candidates := Suggest("shell", `{"command":"sudo apt install foo"}`, nil, nil, cwdScope(""))
	if len(candidates) == 0 {
		t.Fatal("expected candidates for sudo command")
	}
	// sudo is not withheld from reusable rules: the two-word prefix leads and
	// is default, matching the command as written.
	if got := candidates[0].Pattern; got != "sudo apt *" || !candidates[0].Default {
		t.Errorf("expected default 'sudo apt *' first, got %#v", candidates[0])
	}
}

func TestSuggestRulePatterns_BashComplexCommandFallsBackToLiteral(t *testing.T) {
	candidates := Suggest("shell", `{"command":"cat <<'EOF'\nhello\nEOF"}`, nil, nil, cwdScope(""))
	if len(candidates) < 1 {
		t.Fatal("expected candidates for heredoc command")
	}
	if got := candidates[0].Pattern; got != "cat <<'EOF'\nhello\nEOF" {
		t.Fatalf("first pattern = %q, want literal heredoc", got)
	}
	if !candidates[0].Default {
		t.Fatal("expected literal heredoc candidate to be default")
	}
}

func TestSuggestRulePatterns_BashComplexCommandPrefersMatchedAskRules(t *testing.T) {
	candidates := Suggest(
		"shell",
		`{"command":"git reset HEAD^ && git add CHANGELOG.md && git commit -m fix"}`,
		[]string{"git reset HEAD^", "git add CHANGELOG.md", "git commit -m fix"},
		[]string{"git reset *", "git add *", "git commit *"},
		cwdScope(""),
	)
	if len(candidates) < 4 {
		t.Fatalf("candidates = %#v, want matched ask rules", candidates)
	}
	for i, want := range []string{"git reset *", "git add *", "git commit *"} {
		if got := candidates[i].Pattern; got != want {
			t.Fatalf("candidate[%d] = %q, want %q", i, got, want)
		}
	}
	for i := range 3 {
		if !candidates[i].Default {
			t.Fatalf("expected matched ask rule candidate[%d] to be default, got %#v", i, candidates[i])
		}
	}
	foundGitWildcard := false
	var gitWildcard Candidate
	for _, c := range candidates {
		if c.Pattern == "git *" {
			foundGitWildcard = true
			gitWildcard = c
			break
		}
	}
	if !foundGitWildcard {
		t.Fatalf("expected broader git * candidate in %#v", candidates)
	}
	if gitWildcard.Default {
		t.Fatalf("expected broad git * candidate to stay unselected by default: %#v", gitWildcard)
	}
}

func TestSuggestRulePatterns_BashMultilineFallsBackToLiteral(t *testing.T) {
	candidates := Suggest("shell", `{"command":"echo one\necho two"}`, nil, nil, cwdScope(""))
	if len(candidates) < 1 {
		t.Fatal("expected candidates for multiline command")
	}
	if got := candidates[0].Pattern; got != "echo one\necho two" {
		t.Fatalf("first pattern = %q, want multiline literal", got)
	}
	if !candidates[0].Default {
		t.Fatal("expected multiline literal candidate to be default")
	}
}

func TestSuggestRulePatterns_BashSingleWord(t *testing.T) {
	candidates := Suggest("shell", `{"command":"ls"}`, nil, nil, cwdScope(""))
	if len(candidates) == 0 {
		t.Fatal("expected candidates for single-word command")
	}
	// The command word with a wildcard is the narrowest rule that matches the
	// bare command, so it leads and is default.
	if candidates[0].Pattern != "ls *" || !candidates[0].Default {
		t.Errorf("expected default 'ls *' first, got %#v", candidates[0])
	}
}

func TestSuggestRulePatterns_WriteRelativePathDefaultsToDirScope(t *testing.T) {
	candidates := Suggest("write", `{"path":"docs/guide.md","content":"..."}`, nil, nil, cwdScope("/home/user/project"))
	foundDefaultDir := false
	for _, c := range candidates {
		if c.Pattern == "docs/*" && c.Default {
			foundDefaultDir = true
			break
		}
	}
	if !foundDefaultDir {
		t.Error("expected docs/* to be default for cwd-relative write path")
	}
}

func TestSuggestRulePatterns_WriteParentPathDoesNotDefaultToDirScope(t *testing.T) {
	candidates := Suggest("write", `{"path":"../secret/plan.md","content":"..."}`, nil, nil, cwdScope("/home/user/project"))
	for _, c := range candidates {
		if c.Pattern == "../secret/*" && c.Default {
			t.Fatalf("unexpected default dir candidate for parent path: %+v", c)
		}
	}
}

func TestSuggestRulePatterns_OutsideCWDHasNoRelativeExtCandidate(t *testing.T) {
	// A relative "**/*.md" rule is scoped to the working directory and could
	// never match this out-of-cwd absolute path; offering it would save a rule
	// that has no effect on future calls.
	candidates := Suggest("write", `{"path":"/home/shared/plan.md","content":"..."}`, nil, nil, cwdScope("/home/user/project"))
	for _, c := range candidates {
		if c.Pattern == "**/*.md" {
			t.Fatalf("unexpected cwd-scoped ext candidate for outside-cwd path: %+v", c)
		}
	}
	foundExact := false
	for _, c := range candidates {
		if c.Pattern == "/home/shared/plan.md" {
			foundExact = true
			break
		}
	}
	if !foundExact {
		t.Fatalf("expected absolute literal candidate in %#v", candidates)
	}
}

// TestSuggestRulePatterns_DeleteRanksByRecursiveCoverage guards that a
// directory candidate ranks by how many pending paths it covers under the
// rule engine's semantics (a "dir/*" rule matches any depth), so a common
// ancestor is not crowded out by its own subdirectory.
func TestSuggestRulePatterns_DeleteRanksByRecursiveCoverage(t *testing.T) {
	candidates := Suggest("delete", `{"paths":["artifacts/2024/q1.log","artifacts/2024/q2.log","artifacts/root.log"]}`, []string{"artifacts/2024/q1.log", "artifacts/2024/q2.log", "artifacts/root.log"}, nil, cwdScope("/home/user/project"))
	if len(candidates) == 0 || candidates[0].Pattern != "artifacts/*" {
		t.Fatalf("first candidate = %+v, want the common ancestor covering all three paths", candidates)
	}
	for _, candidate := range candidates {
		if candidate.Pattern == "artifacts/*" || candidate.Pattern == "artifacts/2024/*" {
			if candidate.Broad {
				t.Fatalf("scoped directory candidate %q must not be marked broad: %+v", candidate.Pattern, candidates)
			}
		}
	}
}

// TestSuggestRulePatterns_SubdirectorySessionUsesCheckoutRelativeSpelling
// guards that a session running in a subdirectory suggests the
// repository-relative rule the permission engine matches, not a cwd-relative
// spelling that would never apply to the same call.
func TestSuggestRulePatterns_SubdirectorySessionUsesCheckoutRelativeSpelling(t *testing.T) {
	scope := permission.PathScope{
		Cwd:   "/repo/internal",
		Roots: []string{"/repo"},
	}
	candidates := Suggest("delete", `{"paths":["tools/delete.go"]}`, []string{"tools/delete.go"}, nil, scope)
	if len(candidates) == 0 || candidates[0].Pattern != "internal/tools/*" {
		t.Fatalf("first candidate = %+v, want checkout-relative internal/tools/*", candidates)
	}
	if !candidates[0].Default {
		t.Fatalf("in-scope directory candidate should be the default: %+v", candidates)
	}
	rs := permission.Ruleset{{Permission: toolname.Delete, Pattern: candidates[0].Pattern, Action: permission.ActionAllow}}
	if got := rs.EvaluatePath(toolname.Delete, "tools/delete.go", scope); got != permission.ActionAllow {
		t.Fatalf("suggested rule %q does not match the confirmed call (got %q)", candidates[0].Pattern, got)
	}
}

// TestSuggestRulePatterns_WorktreeSessionUsesMainCheckoutSpelling guards that
// a linked-worktree session suggests the same repository-relative spelling as
// the main checkout, so one rule governs the file everywhere.
func TestSuggestRulePatterns_WorktreeSessionUsesMainCheckoutSpelling(t *testing.T) {
	scope := permission.PathScope{
		Cwd:   "/worktrees/feature",
		Roots: []string{"/repo", "/worktrees/feature"},
	}
	candidates := Suggest("write", `{"path":"/repo/src/main.go","content":"..."}`, nil, nil, scope)
	if len(candidates) == 0 || candidates[0].Pattern != "src/main.go" {
		t.Fatalf("first candidate = %+v, want src/main.go", candidates)
	}
	rs := permission.Ruleset{{Permission: toolname.Write, Pattern: candidates[0].Pattern, Action: permission.ActionAllow}}
	for _, spelled := range []string{"/repo/src/main.go", "src/main.go"} {
		if got := rs.EvaluatePath(toolname.Write, spelled, scope); got != permission.ActionAllow {
			t.Fatalf("suggested rule %q does not match %q (got %q)", candidates[0].Pattern, spelled, got)
		}
	}
}

// TestSuggestRulePatterns_WildcardCandidateNamesRepositoryScope guards the
// "**" candidate in a subdirectory session: it covers the whole repository, so
// it must be offered and described as such, and it must match a file outside
// the working directory.
func TestSuggestRulePatterns_WildcardCandidateNamesRepositoryScope(t *testing.T) {
	scope := permission.PathScope{
		Cwd:   "/repo/internal",
		Roots: []string{"/repo"},
	}
	candidates := Suggest("write", `{"path":"docs/guide.md","content":"..."}`, nil, nil, scope)
	found := false
	for _, c := range candidates {
		if c.Pattern != "**" {
			continue
		}
		found = true
		if want := "any path in this repository"; c.Summary != want {
			t.Errorf("** summary = %q, want %q", c.Summary, want)
		}
	}
	if !found {
		t.Fatalf("expected a '**' candidate for an in-repository path: %+v", candidates)
	}
	rs := permission.Ruleset{{Permission: toolname.Write, Pattern: "**", Action: permission.ActionAllow}}
	if got := rs.EvaluatePath(toolname.Write, "/repo/src/other.go", scope); got != permission.ActionAllow {
		t.Fatalf("'**' must match a repository file outside the working directory (got %q)", got)
	}
}

// TestSuggestRulePatterns_DeleteRepoWideTargetsOfferWildcard guards that the
// delete "**" candidate is gated on the repository scope, not the working
// directory: targets spread across the repository still qualify.
func TestSuggestRulePatterns_DeleteRepoWideTargetsOfferWildcard(t *testing.T) {
	scope := permission.PathScope{
		Cwd:   "/repo/internal",
		Roots: []string{"/repo"},
	}
	paths := []string{"tools/a.go", "../src/b.go"}
	candidates := Suggest("delete", `{"paths":["tools/a.go","../src/b.go"]}`, paths, nil, scope)
	for _, c := range candidates {
		if c.Pattern == "**" {
			if c.Summary != "any path in this repository" {
				t.Fatalf("** summary = %q, want repository wording: %+v", c.Summary, candidates)
			}
			return
		}
	}
	t.Fatalf("expected a repository-wide '**' candidate: %+v", candidates)
}
