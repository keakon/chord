package agent

import (
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
		content.WriteString("diff --git a/f" + strconv.Itoa(i) + ".txt b/f" + strconv.Itoa(i) + ".txt\n")
		content.WriteString("index 3367afd..3e75765 100644\n")
		content.WriteString("--- a/f" + strconv.Itoa(i) + ".txt\n")
		content.WriteString("+++ b/f" + strconv.Itoa(i) + ".txt\n")
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
