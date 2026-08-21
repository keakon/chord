package agent

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tools"
)

func TestLooksLikeBuildLikeLogIncludesPatch(t *testing.T) {
	ctx := requestReductionContext{
		ToolName: tools.NameApplyPatch,
		Content:  "Diagnostics:\nwarning: unused variable\n",
	}
	if !looksLikeBuildLikeLog(ctx) {
		t.Fatal("patch diagnostics output should be treated as build-like log")
	}
}

// A tool result is already age 1 at the first request whose response can react
// to it, so size-based summarization must not fire before age 2: the model has
// to see every fresh payload in full exactly once. Age-1 reduction shipped
// summaries of outputs the model had never seen, which it reported to users as
// "output truncated".
func TestFreshToolOutputsAreNeverSummarizedAtFirstSight(t *testing.T) {
	policy := defaultContextReductionPolicy()
	bigShellSuccess := strings.Repeat("pipeline case line\n", 300)
	bigSearch := func() string {
		var b strings.Builder
		for i := range 200 {
			b.WriteString("internal/agent/file")
			b.WriteString(strconv.Itoa(i))
			b.WriteString(".go:12: match line\n")
		}
		return b.String()
	}()
	diagnostics := "Replaced 1 occurrence\n\nDiagnostics:\n[E] 10:1 [F821] Undefined name `x`\n[E] 11:1 another diagnostic"
	for _, tc := range []struct {
		name string
		ctx  requestReductionContext
	}{
		{name: "shell success", ctx: requestReductionContext{ToolName: tools.NameShell, Content: bigShellSuccess, Policy: policy}},
		{name: "search result", ctx: requestReductionContext{ToolName: tools.NameGrep, Content: bigSearch, Policy: policy}},
		{name: "edit diagnostics", ctx: requestReductionContext{ToolName: tools.NameEdit, Content: diagnostics, Policy: policy}},
	} {
		tc.ctx.Age = 1
		if got := classifyRequestReductionToolOutput(tc.ctx); got != requestReductionNone {
			t.Fatalf("%s at first sight (age 1) classified %q, want none", tc.name, got)
		}
		tc.ctx.Age = 2
		if got := classifyRequestReductionToolOutput(tc.ctx); got == requestReductionNone {
			t.Fatalf("%s at age 2 should be reducible, got none", tc.name)
		}
	}
	// Validity markers are not payload thinning: a stale read renders its
	// marker immediately instead of exposing misleading content for a round.
	staleRead := requestReductionContext{
		ToolName:        tools.NameRead,
		Content:         "READ_RESULT lines=1-400 total=400\n" + strings.Repeat("stale content line\n", 300),
		Policy:          policy,
		Age:             1,
		ReadInvalidated: true,
	}
	if got := classifyRequestReductionToolOutput(staleRead); got != requestReductionReadLike {
		t.Fatalf("invalidated read at age 1 classified %q, want read_like validity marker", got)
	}
}

func TestDiffReductionUsesReviewSummaryInsteadOfLogSummary(t *testing.T) {
	content := "diff --git a/internal/llm/errors.go b/internal/llm/errors.go\n" +
		"--- a/internal/llm/errors.go\n+++ b/internal/llm/errors.go\n" +
		"@@ -10,2 +10,3 @@\n-error: old branch\n+return failedReplayError\n+return nil\n"
	ctx := requestReductionContext{
		ToolName: tools.NameShell,
		Content:  content,
		Policy: func() contextReductionPolicy {
			p := defaultContextReductionPolicy()
			p.ShellSuccessBytes = 1
			p.ReadLikeOutputBytes = 1
			return p
		}(),
	}
	ctx.Age = ctx.Policy.DiffProtectAgeTurns
	if got := classifyRequestReductionToolOutput(ctx); got != requestReductionDiff {
		t.Fatalf("class = %q, want diff", got)
	}
	reduced, rule, ok := reduceRequestToolOutput(requestReductionDiff, ctx)
	if !ok || rule != "diff" {
		t.Fatalf("reduction = (%q, %q, %v), want diff summary", reduced, rule, ok)
	}
	if strings.Contains(reduced, "errors=") || strings.Contains(reduced, "failed=") {
		t.Fatalf("diff was rendered as a log summary: %q", reduced)
	}
	for _, want := range []string{"git diff summarized", "internal/llm/errors.go", "hunks=1", "failedReplayError"} {
		if !strings.Contains(reduced, want) {
			t.Fatalf("diff summary missing %q: %q", want, reduced)
		}
	}
}

func TestDiffReductionProtectsReviewEvidenceUntilDedicatedAge(t *testing.T) {
	content := strings.Repeat("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n", 60)
	policy := defaultContextReductionPolicy()
	for _, tc := range []struct {
		age  int
		want requestReductionClass
	}{
		{age: policy.DiffProtectAgeTurns - 1, want: requestReductionNone},
		{age: policy.DiffProtectAgeTurns, want: requestReductionDiff},
	} {
		ctx := requestReductionContext{ToolName: tools.NameShell, Content: content, Age: tc.age, Policy: policy}
		if got := classifyRequestReductionToolOutput(ctx); got != tc.want {
			t.Fatalf("age %d class = %q, want %q", tc.age, got, tc.want)
		}
	}
}

func TestFailedDiffKeepsToolErrorSemantics(t *testing.T) {
	content := strings.Repeat("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n", 60)
	policy := defaultContextReductionPolicy()
	ctx := requestReductionContext{
		ToolName:   tools.NameShell,
		Content:    content,
		ToolStatus: string(ToolResultStatusError),
		Age:        policy.DiffProtectAgeTurns,
		Policy:     policy,
	}
	if got := classifyRequestReductionToolOutput(ctx); got != requestReductionToolError {
		t.Fatalf("failed diff class = %q, want tool error", got)
	}
}

func TestDiffSummarySupportsApplyPatchAndBoundsChanges(t *testing.T) {
	var content strings.Builder
	content.WriteString("*** Begin Patch\n*** Update File: internal/agent/a.go\n")
	for i := range 100 {
		content.WriteString("@@ -1 +1 @@\n")
		content.WriteString("-old line\n")
		content.WriteString("+new line ")
		content.WriteString(strconv.Itoa(i))
		content.WriteByte('\n')
	}
	content.WriteString("*** End Patch\n")
	reduced := reduceDiffOutputSummary(content.String())
	for _, want := range []string{"internal/agent/a.go", "hunks=100", "changes=200", "changes omitted"} {
		if !strings.Contains(reduced, want) {
			t.Fatalf("ApplyPatch summary missing %q: %q", want, reduced)
		}
	}
	if len(reduced) > 8*1024 {
		t.Fatalf("diff summary is not bounded: %d bytes", len(reduced))
	}
}

func TestDiffSummarySupportsPlainUnifiedDiff(t *testing.T) {
	content := "--- a/old.go\n+++ b/new.go\n@@ -1 +1 @@\n-old\n+new\n" +
		"--- a/second.go\n+++ b/second.go\n@@ -2 +2 @@\n-before\n+after\n"
	reduced := reduceDiffOutputSummary(content)
	if !strings.Contains(reduced, "new.go") || !strings.Contains(reduced, "second.go") || strings.Count(reduced, "hunks=1") != 2 {
		t.Fatalf("unified diff summary lost structure: %q", reduced)
	}
}

func TestDiffSummaryDoesNotDuplicateStandardGitFiles(t *testing.T) {
	var content strings.Builder
	for i := 1; i <= 13; i++ {
		content.WriteString("diff --git a/f")
		content.WriteString(strconv.Itoa(i))
		content.WriteString(".txt b/f")
		content.WriteString(strconv.Itoa(i))
		content.WriteString(".txt\n")
		content.WriteString("index 3367afd..3e75765 100644\n")
		content.WriteString("--- a/f")
		content.WriteString(strconv.Itoa(i))
		content.WriteString(".txt\n")
		content.WriteString("+++ b/f")
		content.WriteString(strconv.Itoa(i))
		content.WriteString(".txt\n")
		content.WriteString("@@ -1 +1 @@\n-old\n+new\n")
	}

	reduced := reduceDiffOutputSummary(content.String())
	for i := 1; i <= 12; i++ {
		name := "f" + strconv.Itoa(i) + ".txt"
		if strings.Count(reduced, name) != 1 {
			t.Fatalf("standard Git diff file %s appears %d times: %q", name, strings.Count(reduced, name), reduced)
		}
	}
	if strings.Contains(reduced, "f13.txt") || !strings.Contains(reduced, "additional files omitted") {
		t.Fatalf("standard Git diff should bound and report omitted files: %q", reduced)
	}
}

func TestDiffRecognitionProtectsBinaryModeAndRenameEvidence(t *testing.T) {
	policy := defaultContextReductionPolicy()
	for _, tc := range []struct {
		content string
		name    string
		kind    string
	}{
		{content: "diff --git a/blob.bin b/blob.bin\nindex 0000000..1111111 100644\nGIT binary patch\nliteral 4\n", name: "blob.bin", kind: "binary"},
		{content: "diff --git a/script b/script\nold mode 100644\nnew mode 100755\n", name: "script", kind: "mode"},
		{content: "diff --git a/old.go b/new.go\nsimilarity index 95%\nrename from old.go\nrename to new.go\n", name: "new.go", kind: "rename"},
	} {
		ctx := requestReductionContext{ToolName: tools.NameShell, Content: tc.content, Age: 1, Policy: policy}
		if got := classifyRequestReductionToolOutput(ctx); got != requestReductionNone {
			t.Fatalf("diff metadata was not protected: class=%q content=%q", got, tc.content)
		}
		summary := reduceDiffOutputSummary(tc.content)
		if !strings.Contains(summary, tc.name) || !strings.Contains(summary, "kind="+tc.kind) {
			t.Fatalf("diff summary = %q, want name=%q kind=%q", summary, tc.name, tc.kind)
		}
	}
}

// A read output that is both repeated (an identical call appears later) and
// invalidated/superseded must classify as read_like so every reduction path
// renders the same validity-marked summary. Returning the repeated marker here
// while the frozen incremental path force-refreshes the same message to the
// truncated=stale/superseded shape made the two renderings alternate across
// requests, rewriting the cached prefix each time.
func TestRepeatedInvalidatedReadClassifiesAsReadLikeNotRepeated(t *testing.T) {
	content := "READ_RESULT lines=1-40 total=40\n" + strings.Repeat("source line\n", 40)
	base := requestReductionContext{
		ToolName: tools.NameRead,
		Content:  content,
		Age:      2,
		Repeated: true,
		Policy:   defaultContextReductionPolicy(),
	}

	for _, tc := range []struct {
		name        string
		invalidated bool
		superseded  bool
		want        requestReductionClass
	}{
		{name: "superseded", superseded: true, want: requestReductionReadLike},
		{name: "invalidated", invalidated: true, want: requestReductionReadLike},
		{name: "still_valid", want: requestReductionRepeated},
	} {
		ctx := base
		ctx.ReadInvalidated = tc.invalidated
		ctx.ReadSuperseded = tc.superseded
		if got := classifyRequestReductionToolOutput(ctx); got != tc.want {
			t.Fatalf("%s: class = %q, want %q", tc.name, got, tc.want)
		}
	}

	// The read_like rendering must be deterministic and carry the truncated=
	// marker stableReductionSurfaceNeedsReview treats as settled.
	ctx := base
	ctx.ReadSuperseded = true
	first, rule, ok := reduceRequestToolOutput(requestReductionReadLike, ctx)
	if !ok || rule != "read_like" {
		t.Fatalf("reduction = (rule=%q, ok=%v), want read_like", rule, ok)
	}
	if !strings.Contains(first, "truncated="+tools.ReadTruncatedSuperseded) {
		t.Fatalf("summary missing superseded marker: %q", first)
	}
	second, _, _ := reduceRequestToolOutput(requestReductionReadLike, ctx)
	if first != second {
		t.Fatalf("read_like rendering is not deterministic:\n%q\nvs\n%q", first, second)
	}
}

func TestSummarizeJSONObjectEntriesKeepsScalarValues(t *testing.T) {
	content := `{"error":"permission denied on /etc/hosts","code":403,"retry_after":30,"nested":{"x":1,"y":[2,3]},"ok":true,"nil":null,"arr":[1,2,3]}`
	var decoded any
	if err := json.Unmarshal([]byte(content), &decoded); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	obj, ok := decoded.(map[string]any)
	if !ok {
		t.Fatalf("fixture type: %T", decoded)
	}
	lines := summarizeJSONObjectEntries(obj)
	joined := strings.Join(lines, "\n")
	// Scalar values must survive: the exact fields a model re-reads. Keys are
	// quoted by summarizeJSONKey; assert on the value content plus quoted key.
	if !strings.Contains(joined, `"error": "permission denied on /etc/hosts"`) {
		t.Fatalf("object summary dropped string value: %q", joined)
	}
	if !strings.Contains(joined, `"code": 403`) {
		t.Fatalf("object summary dropped number value: %q", joined)
	}
	if !strings.Contains(joined, `"retry_after": 30`) {
		t.Fatalf("object summary dropped number value: %q", joined)
	}
	if !strings.Contains(joined, `"ok": true`) {
		t.Fatalf("object summary dropped bool value: %q", joined)
	}
	if !strings.Contains(joined, `"nil": null`) {
		t.Fatalf("object summary dropped null value: %q", joined)
	}
	// Nested containers collapse to shape markers, not bare keys.
	if !strings.Contains(joined, `"nested": {"x", "y"}`) {
		t.Fatalf("nested object should collapse to key shape: %q", joined)
	}
	if !strings.Contains(joined, `"arr": [3 items]`) {
		t.Fatalf("nested array should collapse to item count: %q", joined)
	}
}

func TestSummarizeJSONObjectEntriesTruncatesKeyList(t *testing.T) {
	obj := map[string]any{}
	for i := range 20 {
		obj[fmt.Sprintf("k%02d", i)] = i
	}
	lines := summarizeJSONObjectEntries(obj)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "+12 more keys omitted") {
		t.Fatalf("expected omission tail, got %q", joined)
	}
	if strings.Contains(joined, "k19:") {
		t.Fatalf("overflow keys should be omitted: %q", joined)
	}
}

func TestSummarizeJSONArrayItemsSamplesFirstMiddleLast(t *testing.T) {
	items := make([]any, 7)
	for i := range items {
		items[i] = fmt.Sprintf("item-%d", i)
	}
	lines := summarizeJSONArrayItems(items, 3)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "[0]") || !strings.Contains(joined, "[3]") || !strings.Contains(joined, "[6]") {
		t.Fatalf("expected first/middle/last sampling, got %q", joined)
	}
	if strings.Contains(joined, "[1]") || strings.Contains(joined, "[5]") {
		t.Fatalf("unexpected interior-only indices in 3-item sample: %q", joined)
	}
}

func TestNumberedSourceStats(t *testing.T) {
	content := "1 package main\n\n3 func main() {\n5\tfmt.Println(\"x\")\n"
	meaningful, note := numberedSourceStats(content)
	if meaningful != 3 {
		t.Fatalf("meaningful lines = %d, want 3", meaningful)
	}
	if note != " range=1-5" {
		t.Fatalf("range note = %q, want range=1-5", note)
	}
	if _, note := numberedSourceStats("no numbers here\nplain text\n"); note != "" {
		t.Fatalf("range note = %q, want empty", note)
	}
	if _, note := numberedSourceStats("42 only line\n"); note != " range=42" {
		t.Fatalf("single line range note = %q, want range=42", note)
	}
}

func TestParseNumberedSourceLine(t *testing.T) {
	for _, tc := range []struct {
		line    string
		wantNum int
		wantOk  bool
	}{
		{line: "12 func main() {", wantNum: 12, wantOk: true},
		{line: "  3\t\timport \"fmt\"", wantNum: 3, wantOk: true},
		{line: "1 package main", wantNum: 1, wantOk: true},
		{line: "123", wantOk: false},    // all digits, no content
		{line: "123abc", wantOk: false}, // no separator after digits
		{line: "func main() {", wantOk: false},
		{line: "", wantOk: false},
	} {
		num, ok := parseNumberedSourceLine(tc.line)
		if ok != tc.wantOk || (ok && num != tc.wantNum) {
			t.Fatalf("parseNumberedSourceLine(%q) = (%d, %v), want num=%d ok=%v", tc.line, num, ok, tc.wantNum, tc.wantOk)
		}
	}
}
