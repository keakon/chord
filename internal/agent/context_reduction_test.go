package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
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
		name        string
		ctx         requestReductionContext
		reducibleAt int
	}{
		{name: "shell success", ctx: requestReductionContext{ToolName: tools.NameShell, Content: bigShellSuccess, Policy: policy}, reducibleAt: policy.ShellSuccessAgeTurns},
		{name: "search result", ctx: requestReductionContext{ToolName: tools.NameGrep, Content: bigSearch, Policy: policy}, reducibleAt: policy.ReadLikeAgeTurns},
		{name: "edit diagnostics", ctx: requestReductionContext{ToolName: tools.NameEdit, Content: diagnostics, Policy: policy}, reducibleAt: policy.ErrorAgeTurns},
	} {
		tc.ctx.Age = 1
		if got := classifyRequestReductionToolOutput(tc.ctx); got != requestReductionNone {
			t.Fatalf("%s at first sight (age 1) classified %q, want none", tc.name, got)
		}
		tc.ctx.Age = tc.reducibleAt
		if got := classifyRequestReductionToolOutput(tc.ctx); got == requestReductionNone {
			t.Fatalf("%s at age %d should be reducible, got none", tc.name, tc.reducibleAt)
		}
	}
	diagCtx := requestReductionContext{ToolName: tools.NameEdit, Content: diagnostics, Age: policy.ErrorAgeTurns, Policy: policy}
	if got := classifyRequestReductionToolOutput(diagCtx); got != requestReductionDiagnostics {
		t.Fatalf("edit diagnostics at ErrorAgeTurns classified %q, want diagnostics", got)
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

// A failing tool result is the strongest signal in its payload: while it is
// younger than the error age it must stay complete (the model is about to act
// on it), and shape-based rules (search, build-log, JSON) must never miscast a
// failure's path:line:col lines as a search hit. Once aged, the error summary
// still keeps the failing paths/lines and the tail.
func TestFailedToolOutputStaysCompleteUntilErrorAge(t *testing.T) {
	policy := defaultContextReductionPolicy()
	content := "go build ./...\n./internal/agent/main.go:23:5: undefined: foo\n./internal/agent/main.go:24:9: undefined: bar\n" +
		strings.Repeat("build log filler line\n", 200)
	ctx := requestReductionContext{
		ToolName:   tools.NameShell,
		Content:    content,
		ToolStatus: string(ToolResultStatusError),
		Age:        policy.ErrorAgeTurns - 1,
		Policy:     policy,
	}
	if got := classifyRequestReductionToolOutput(ctx); got != requestReductionNone {
		t.Fatalf("young failed output classified %q, want none (keep complete)", got)
	}
	ctx.Age = policy.ErrorAgeTurns
	if got := classifyRequestReductionToolOutput(ctx); got != requestReductionToolError {
		t.Fatalf("aged failed output classified %q, want tool_error", got)
	}
	reduced, rule, ok := reduceRequestToolOutput(requestReductionToolError, ctx)
	if !ok || rule != "error" {
		t.Fatalf("reduction = (%q, %q, %v), want error summary", reduced, rule, ok)
	}
	for _, want := range []string{"undefined", "main.go:23:5", "main.go:24:9"} {
		if !strings.Contains(reduced, want) {
			t.Fatalf("error summary lost %q: %q", want, reduced)
		}
	}
}

// A cancelled result is not an error: it must not adopt the tool-error
// semantics, even past the error age, when its payload carries no error.
func TestCancelledToolOutputIsNotTreatedAsFailed(t *testing.T) {
	policy := defaultContextReductionPolicy()
	ctx := requestReductionContext{
		ToolName:   tools.NameShell,
		Content:    strings.Repeat("plain log line\n", 60),
		ToolStatus: string(ToolResultStatusCancelled),
		Age:        policy.ErrorAgeTurns + 2,
		Policy:     policy,
	}
	if got := classifyRequestReductionToolOutput(ctx); got == requestReductionToolError {
		t.Fatalf("cancelled output classified as tool_error, want a non-error class")
	}
}

// The diagnostics summary threshold comes from the configured policy
// (ErrorAgeTurns), not a hardcoded constant: raising it keeps edit-diagnostics
// output complete for longer.
func TestDiagnosticsReductionAgeComesFromPolicy(t *testing.T) {
	policy := defaultContextReductionPolicy()
	policy.ErrorAgeTurns = 5
	ctx := requestReductionContext{
		ToolName: tools.NameApplyPatch,
		Content:  "Replaced 1 occurrence\n\nDiagnostics:\n[E] 10:1 [F821] Undefined name `x`",
		Policy:   policy,
		Age:      3,
	}
	if got := classifyRequestReductionToolOutput(ctx); got != requestReductionNone {
		t.Fatalf("edit diagnostics below the configured age classified %q, want none", got)
	}
	ctx.Age = policy.ErrorAgeTurns
	if got := classifyRequestReductionToolOutput(ctx); got != requestReductionDiagnostics {
		t.Fatalf("edit diagnostics at the configured age classified %q, want diagnostics", got)
	}
}

// A large edit-diagnostics block must not be miscast as a build log by the
// read-like gate while it is younger than the diagnostics summary age; it
// stays complete until the configured ErrorAgeTurns.
func TestEditDiagnosticsLargeOutputStaysCompleteBeforeSummaryAge(t *testing.T) {
	policy := defaultContextReductionPolicy()
	content := "Replaced 1 occurrence\n\nDiagnostics:\n" + strings.Repeat("[E] 3:4 [E1] undefined name `x`\n", 200)
	ctx := requestReductionContext{
		ToolName: tools.NameApplyPatch,
		Content:  content,
		Age:      policy.ErrorAgeTurns - 1,
		Policy:   policy,
	}
	if len(ctx.Content) <= policy.ReadLikeOutputBytes {
		t.Fatalf("test fixture must exceed the read-like size gate (%d bytes)", policy.ReadLikeOutputBytes)
	}
	if got := classifyRequestReductionToolOutput(ctx); got != requestReductionNone {
		t.Fatalf("young large diagnostics classified %q, want none (keep complete)", got)
	}
	ctx.Age = policy.ErrorAgeTurns
	if got := classifyRequestReductionToolOutput(ctx); got != requestReductionDiagnostics {
		t.Fatalf("large diagnostics at the summary age classified %q, want diagnostics", got)
	}
	reduced, rule, ok := reduceRequestToolOutput(requestReductionDiagnostics, ctx)
	if !ok || rule != "diagnostics" {
		t.Fatalf("reduction = (%q, %q, %v), want diagnostics summary", reduced, rule, ok)
	}
	if !strings.Contains(reduced, "undefined name") {
		t.Fatalf("diagnostics summary lost the location lines: %q", reduced)
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

// An invalidated or superseded read renders its validity marker regardless of
// size. The frozen incremental path force-refreshes such reads unconditionally,
// so gating the full scan on ReadLikeOutputBytes made the two paths disagree on
// small reads and rewrite the cached prefix on alternating requests.
func TestInvalidatedReadRendersValidityMarkerBelowSizeGate(t *testing.T) {
	base := requestReductionContext{
		ToolName: tools.NameRead,
		Content:  "READ_RESULT lines=1-2 total=2\npackage agent\n",
		Age:      0,
		Policy:   defaultContextReductionPolicy(),
	}
	if len(base.Content) > base.Policy.ReadLikeOutputBytes {
		t.Fatalf("fixture must sit below the size gate: %d bytes", len(base.Content))
	}
	for _, tc := range []struct {
		name        string
		invalidated bool
		superseded  bool
		want        requestReductionClass
	}{
		{name: "invalidated", invalidated: true, want: requestReductionReadLike},
		{name: "superseded", superseded: true, want: requestReductionReadLike},
		{name: "valid", want: requestReductionNone},
	} {
		ctx := base
		ctx.ReadInvalidated = tc.invalidated
		ctx.ReadSuperseded = tc.superseded
		if got := classifyRequestReductionToolOutput(ctx); got != tc.want {
			t.Fatalf("%s: class = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A time-varying command (git status, go test, ls) reissued with identical
// arguments produces a different output, so the earlier result is the "before"
// state rather than a duplicate. It must reach the shape-based summarizers
// instead of collapsing to a marker asserting an identical later call.
func TestTimeVaryingShellRerunIsSummarizedNotCollapsedAsRepeated(t *testing.T) {
	a := &MainAgent{}
	args := json.RawMessage(`{"command":"go test ./..."}`)
	before := "FAIL\tgithub.com/keakon/chord/internal/agent\n" + strings.Repeat("--- FAIL: TestOld (0.01s)\n", 200)
	after := "ok\tgithub.com/keakon/chord/internal/agent\t1.2s\n"
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameShell, Args: args}}},
		{Role: message.RoleTool, ToolCallID: "tc1", ToolStatus: "success", Content: before},
		{Role: message.RoleAssistant, RequestBatch: 2, ToolCalls: []message.ToolCall{{ID: "tc2", Name: tools.NameShell, Args: args}}},
		{Role: message.RoleTool, ToolCallID: "tc2", ToolStatus: "success", Content: after},
	}
	setTestRequestBatch(a, msgs, 6)
	prepared := a.prepareMessagesForLLM(msgs)
	if strings.Contains(prepared[2].Content, "Repeated ") {
		t.Fatalf("differing earlier output must not collapse to the repeated marker: %q", prepared[2].Content)
	}
	if len(prepared[2].Content) >= len(before) {
		t.Fatalf("the earlier output should still be summarized once past its age gates: %q", prepared[2].Content)
	}
	if !strings.Contains(prepared[2].Content, "FAIL") {
		t.Fatalf("the before-state summary must keep its failing evidence: %q", prepared[2].Content)
	}
	if prepared[4].Content != after {
		t.Fatalf("the newest output must stay verbatim: %q", prepared[4].Content)
	}

	// A byte-identical rerun still collapses: nothing is lost because the
	// later copy carries the same content.
	msgs[4].Content = before
	prepared = a.prepareMessagesForLLM(msgs)
	if !strings.Contains(prepared[2].Content, "Repeated ") {
		t.Fatalf("identical rerun must still collapse: %q", prepared[2].Content)
	}
}

// Go and Rust test runners report failures as "--- FAIL: Name" and a bare
// "FAIL\tpkg" line, which match none of the longer "failed"/"failure" markers.
// Such output must stay protected while recent and keep its failing lines once
// summarized, instead of collapsing to "no preserved log lines".
func TestTestRunnerFailureLinesAreRecognizedAsFailureEvidence(t *testing.T) {
	content := "--- FAIL: TestSomething (0.01s)\n    x_test.go:12: mismatch\nFAIL\tgithub.com/keakon/chord/internal/agent\t1.2s\n" +
		strings.Repeat("ok      github.com/keakon/chord/internal/quiet\t0.1s\n", 200)
	ctx := requestReductionContext{
		ToolName:   tools.NameShell,
		Content:    content,
		ToolStatus: "success",
		Age:        1,
		Policy:     defaultContextReductionPolicy(),
	}
	if !isHighRiskToolOutput(ctx) {
		t.Fatal("a failing test run must count as recent high-risk output")
	}
	if got := classifyRequestReductionToolOutput(ctx); got != requestReductionNone {
		t.Fatalf("class = %q, want the recent high-risk protection", got)
	}
	ctx.Age = ctx.Policy.HighRiskProtectAgeTurns
	if got := classifyRequestReductionToolOutput(ctx); got != requestReductionLongLog {
		t.Fatalf("aged class = %q, want long_log", got)
	}
	summary := reduceLongLogOutputSummary(ctx)
	if !strings.Contains(summary, "--- FAIL: TestSomething") {
		t.Fatalf("summary dropped the failing case: %q", summary)
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

// A commit listing is not a build log. `git log --oneline` prints one commit
// per line, and a subject such as "fix(tools): improve match-failure recovery"
// contains "fail"; classifying the listing as a build log routes it through
// the signal-based log summary, which keeps only the marker line and drops
// every other commit. That misread a 30-commit range as a single commit and
// held the wrong conclusion for dozens of requests.
func TestLooksLikeBuildLikeLogIgnoresIncidentalMarkerInListing(t *testing.T) {
	commitLog := strings.Join([]string{
		"cde48810 feat(agent): add the context reduction frontier",
		"58ba8e1d fix(tools): improve edit/apply_patch match-failure recovery",
		"a625b9b8 docs: describe the retained recent messages budget",
		"7f10c2d4 refactor(llm): fold the retry helpers together",
	}, "\n")
	ctx := requestReductionContext{ToolName: tools.NameShell, Content: commitLog}
	if looksLikeBuildLikeLog(ctx) {
		t.Fatal("a commit listing with one incidental 'failure' subject must not be a build log")
	}
	// A real runner still classifies: it leads a line with the verdict.
	ctx.Content = "ok  \tgithub.com/x/y\t0.2s\n--- FAIL: TestThing (0.00s)\n"
	if !looksLikeBuildLikeLog(ctx) {
		t.Fatal("a failing test run must still be treated as a build log")
	}
	// So does output whose markers span more than one line.
	ctx.Content = "warning: unused import\nerror: undefined name\n"
	if !looksLikeBuildLikeLog(ctx) {
		t.Fatal("multi-marker output must still be treated as a build log")
	}
}

// A timestamp is not a path:line:text hit. Parsing `12:34:56 INFO ...` as line
// 34 of a file named "12" made plain logs look like search results, and the
// search summary then asserts a match count and a preserved query for output
// that never was a search.
func TestSearchResultParsingRejectsTimestampedLogLines(t *testing.T) {
	for _, line := range []string{
		"12:34:56 INFO starting the worker pool",
		"2026-09-06 12:34:56 WARN retrying the upstream call",
		"10:00:03 request completed in 12ms",
	} {
		if _, _, _, ok := parseSearchResultLine(line); ok {
			t.Fatalf("timestamped log line parsed as a search hit: %q", line)
		}
	}
	logOutput := "12:34:56 INFO starting\n12:34:57 INFO listening on :8080\n12:34:58 INFO ready\n"
	if looksLikeSearchResultContent(logOutput) {
		t.Fatal("a timestamped log must not be classified as search output")
	}
	// Real hits still parse.
	path, lineNo, snippet, ok := parseSearchResultLine("internal/agent/main.go:120: func run() error {")
	if !ok || path != "internal/agent/main.go" || lineNo != "120" || snippet == "" {
		t.Fatalf("real search hit failed to parse: path=%q line=%q snippet=%q ok=%v", path, lineNo, snippet, ok)
	}
}

// The long-log summary keeps only marker lines, so it must say how many lines
// it dropped. Without a count the model cannot tell "one matching line" from
// "the output only had one line" and may read the summary as the whole result.
func TestLongLogSummaryReportsOmittedLineCount(t *testing.T) {
	var b strings.Builder
	b.WriteString("error: first problem\n")
	for i := range 40 {
		fmt.Fprintf(&b, "step %d completed\n", i)
	}
	ctx := requestReductionContext{ToolName: tools.NameShell, Content: b.String()}
	summary := reduceLongLogOutputSummary(ctx)
	if !strings.Contains(summary, "lines=41") {
		t.Fatalf("summary should report the total meaningful line count, got %q", summary)
	}
	if !strings.Contains(summary, "40 more lines omitted") {
		t.Fatalf("summary should report how many lines it dropped, got %q", summary)
	}
}

// The restorable invariant: a lossy summary must leave an address the model
// can read back. The tool layer archives anything over its inline budget and
// compaction exports history before rewriting it; request-level reduction
// works in the band between the byte gates and that budget, so without this it
// is the one layer that can destroy a payload outright.
func TestLossySummaryLeavesRecoveryAddress(t *testing.T) {
	dir := t.TempDir()
	content := strings.Repeat("build step ok line\n", 400)
	ctx := requestReductionContext{
		ToolName:   tools.NameShell,
		Meta:       toolCallMeta{Name: tools.NameShell},
		Content:    content,
		Age:        3,
		Policy:     defaultContextReductionPolicy(),
		ArchiveDir: dir,
	}
	reduced, _, ok := reduceRequestToolOutput(requestReductionShellOK, ctx)
	if !ok {
		t.Fatal("shell success output should reduce")
	}
	refs := tools.ExtractArtifactReferences(reduced)
	if len(refs) == 0 {
		t.Fatalf("lossy summary must carry a recovery address, got %q", reduced)
	}
	path := strings.TrimSuffix(strings.TrimPrefix(refs[0], tools.ArtifactReferencePrefix), ".")
	saved, err := os.ReadFile(strings.TrimSpace(path))
	if err != nil {
		t.Fatalf("archived payload unreadable: %v", err)
	}
	if string(saved) != content {
		t.Fatalf("archived payload differs from the original (%d vs %d bytes)", len(saved), len(content))
	}

	// Identical payloads are content-addressed, so repeated copies converge on
	// one file instead of writing one archive each.
	repeated, _, ok := reduceRequestToolOutput(requestReductionRepeated, ctx)
	if !ok {
		t.Fatal("repeated output should reduce")
	}
	repeatedRefs := tools.ExtractArtifactReferences(repeated)
	if len(repeatedRefs) == 0 || repeatedRefs[0] != refs[0] {
		t.Fatalf("identical payloads should share one archive, got %v want %v", repeatedRefs, refs)
	}

	// A stale read already carries its own recovery route (re-read the file),
	// so it must not spend a disk write on an archive.
	readCtx := requestReductionContext{
		ToolName:        tools.NameRead,
		Meta:            toolCallMeta{Name: tools.NameRead},
		Content:         "READ_RESULT lines=1-400 total=400\n" + strings.Repeat("stale line\n", 300),
		Age:             3,
		Policy:          defaultContextReductionPolicy(),
		ReadInvalidated: true,
		ArchiveDir:      dir,
	}
	readReduced, _, ok := reduceRequestToolOutput(requestReductionReadLike, readCtx)
	if !ok {
		t.Fatal("stale read should reduce")
	}
	if len(tools.ExtractArtifactReferences(readReduced)) > 0 {
		t.Fatalf("a stale read guides a re-read and must not be archived, got %q", readReduced)
	}

	// Small payloads are not worth an archive.
	smallCtx := ctx
	smallCtx.Content = "ok\n"
	small, _, ok := reduceRequestToolOutput(requestReductionShellOK, smallCtx)
	if ok && len(tools.ExtractArtifactReferences(small)) > 0 {
		t.Fatalf("small payload should not be archived, got %q", small)
	}
}
