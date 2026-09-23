package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tools"
)

func commandShapeContext(t *testing.T, command, content string) requestReductionContext {
	t.Helper()
	policy := defaultContextReductionPolicy()
	return requestReductionContext{
		ToolName:    tools.NameShell,
		Meta:        toolCallMeta{Name: tools.NameShell, Args: fmt.Sprintf(`{"command":%q}`, command)},
		Content:     content,
		ToolStatus:  string(ToolResultStatusSuccess),
		Age:         policy.ShellReadOnlyAgeTurns + 1,
		Policy:      policy,
		ToolResults: policy.MinToolResultsPrune + 1,
	}
}

// gitLogListing is shaped like this repository's own history: conventional
// commit subjects, several of which contain "fail". Two such lines are all the
// build-log heuristic needs, and the log summary would then keep only those and
// drop every other commit.
func gitLogListing() string {
	var b strings.Builder
	for i := range 40 {
		switch i {
		case 7:
			b.WriteString("d20cb9f1 fix(agent): recover from a failed compaction apply\n")
		case 19:
			b.WriteString("e0d22a48 fix(tools): improve match-failure recovery in edit\n")
		default:
			fmt.Fprintf(&b, "%08x feat(agent): bound the checkpoint sections %d\n", i*2654435761, i)
		}
	}
	return b.String()
}

// The command name settles the shape: a commit listing is a plain shell
// success, whatever its subjects happen to say.
func TestGitLogListingIsNotClassifiedAsBuildLog(t *testing.T) {
	content := gitLogListing()
	// Guard the premise: without the command signal the content alone does read
	// as a build log, so this test would be vacuous if the heuristic changed.
	sniffed := commandShapeContext(t, "git log --oneline -40", content)
	sniffed.Meta.Args = `{}`
	if !looksLikeBuildLikeLog(sniffed) {
		t.Fatal("premise broken: the listing no longer trips looksLikeBuildLikeLog, so this test proves nothing")
	}

	ctx := commandShapeContext(t, "git log --oneline -40", content)
	if got := classifyRequestReduction(ctx).Class; got != requestReductionListing {
		t.Fatalf("git log classified as %q, want %q", got, requestReductionListing)
	}
	reduced, rule, ok := reduceRequestToolOutput(requestReductionListing, ctx)
	if !ok || rule != "record_listing" {
		t.Fatalf("reduction = (%q, %q, %v), want a record_listing summary", reduced, rule, ok)
	}
	// A listing's records are peers, so the summary must keep the newest ones
	// rather than the ones whose subjects happen to match a marker word. The
	// first line of the input is the single most relevant record.
	firstRecord, _, _ := strings.Cut(content, "\n")
	if !strings.Contains(reduced, firstRecord) {
		t.Fatalf("listing summary dropped the newest record %q: %q", firstRecord, reduced)
	}
	// And it must say how many it dropped, so the summary is not mistaken for
	// the whole listing.
	if !strings.Contains(reduced, "records omitted") {
		t.Fatalf("listing summary did not disclose the omitted records: %q", reduced)
	}
}

// The marker-based shell summary is the wrong shape for a listing: it scores
// lines by substrings that are ordinary words in commit subjects, so it can
// keep two arbitrary middle records and drop the newest one. This pins why the
// listing shape exists rather than reusing shell_success.
func TestListingSummaryBeatsMarkerSelectionOnCommitSubjects(t *testing.T) {
	content := gitLogListing()
	firstRecord, _, _ := strings.Cut(content, "\n")
	if marker := summarizeShellSuccess(content, 4); len(marker.Lines) > 0 &&
		strings.Contains(strings.Join(marker.Lines, "\n"), firstRecord) {
		t.Skip("marker selection happens to keep the head; the comparison proves nothing here")
	}
	ctx := commandShapeContext(t, "git log --oneline -40", content)
	if !strings.Contains(reduceRecordListingOutputSummary(ctx), firstRecord) {
		t.Fatal("listing summary must keep the newest record where marker selection does not")
	}
}

// git status is a status listing, not a search result: "modified:" lines carry
// a colon and must not be parsed as path:line locations.
func TestGitStatusIsClassifiedAsListing(t *testing.T) {
	var b strings.Builder
	b.WriteString("On branch main\nChanges not staged for commit:\n")
	for i := range 60 {
		fmt.Fprintf(&b, "\tmodified:   internal/agent/file_%d.go\n", i)
	}
	ctx := commandShapeContext(t, "git status", b.String())
	if got := classifyRequestReduction(ctx).Class; got != requestReductionListing {
		t.Fatalf("git status classified as %q, want %q", got, requestReductionListing)
	}
}

// A pipeline reshapes the output, so the command no longer describes it and the
// content heuristics stay in charge.
func TestPipedGitLogFallsBackToContentHeuristics(t *testing.T) {
	if _, ok := shellOutputShapeFromCommandMemo(nil, "", `{"command":"git log --oneline | rg fix"}`); ok {
		t.Fatal("a pipeline must not claim a command-derived shape")
	}
}

// Commands outside the table are left to the heuristics untouched: cat can
// print JSON, source or a log, and the command name says nothing about which.
func TestUnmappedShellCommandsKeepHeuristicShape(t *testing.T) {
	for _, command := range []string{"cat internal/agent/main.go", "go test ./...", "rg pattern internal"} {
		if shape, ok := shellOutputShapeFromCommandMemo(nil, "", fmt.Sprintf(`{"command":%q}`, command)); ok {
			t.Fatalf("%q claimed shape %q; it must fall through to the heuristics", command, shape)
		}
	}
}

// A git diff prints a patch, and the diff rules run ahead of the shell branch,
// so the command table must not intercept it.
func TestGitDiffStillClassifiesAsDiff(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/internal/agent/main.go b/internal/agent/main.go\n")
	b.WriteString("--- a/internal/agent/main.go\n+++ b/internal/agent/main.go\n")
	for i := range 120 {
		fmt.Fprintf(&b, "@@ -%d,3 +%d,3 @@\n-old source line %d in main.go\n+new source line %d in main.go\n", i*10, i*10, i, i)
	}
	ctx := commandShapeContext(t, "git diff", b.String())
	ctx.Age = ctx.Policy.DiffProtectAgeTurns + 1
	if got := classifyRequestReduction(ctx).Class; got != requestReductionDiff {
		t.Fatalf("git diff classified as %q, want %q", got, requestReductionDiff)
	}
}
