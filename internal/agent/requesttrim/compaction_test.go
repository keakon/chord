package requesttrim

import (
	"reflect"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestSummarizeHeadTailLinesKeepsBoundedHeadAndTail(t *testing.T) {
	got := summarizeHeadTailLines(strings.Join([]string{
		"line 1",
		"line 2",
		"line 3",
		"line 4",
		"line 5",
	}, "\n"), 4)
	want := []string{
		"- line 1",
		"- line 2",
		"- ... (1 line omitted) ...",
		"- line 4",
		"- line 5",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("summarizeHeadTailLines() = %#v, want %#v", got, want)
	}

	got = summarizeHeadTailLines("line 1\n\nline 2", 4)
	want = []string{"- line 1", "- line 2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("summarizeHeadTailLines() without omission = %#v, want %#v", got, want)
	}
}

func TestSummarizeHeadTailLinesPreservesArtifactReference(t *testing.T) {
	// Artifact reference lines ("Full output saved to <path>") must reach the
	// model verbatim: mid-line truncation would clip the artifact path and
	// leave the model an unreadable path to read.
	ref := tools.ArtifactReferencePrefix + "/session/tool-outputs/call_" + strings.Repeat("a", 80) + ".log. " + tools.ArtifactReadGuidance
	long := strings.Repeat("x", SummaryLineSnippetChars+100)

	got := summarizeHeadTailLines(strings.Join([]string{
		ref,
		long,
		"line 3",
		"line 4",
		"line 5",
	}, "\n"), 4)
	if len(got) != 5 {
		t.Fatalf("summarizeHeadTailLines() len = %d, want 5: %#v", len(got), got)
	}
	if got[0] != "- "+ref {
		t.Fatalf("artifact reference line not preserved verbatim, got %#v", got[0])
	}
	if len(got[1]) >= len(long) {
		t.Fatalf("plain long line should still be truncated, got %d bytes", len(got[1]))
	}

	got = summarizeHeadTailLines(strings.Join([]string{
		"line 1",
		"line 2",
		"line 3",
		"line 4",
		ref,
	}, "\n"), 4)
	if got[len(got)-1] != "- "+ref {
		t.Fatalf("artifact reference in tail not preserved verbatim, got %#v", got[len(got)-1])
	}
}

func TestRequestBatchesAfterCountsParallelToolsAsOneRequest(t *testing.T) {
	messages := []message.Message{
		{Role: message.RoleAssistant, RequestBatch: 7, ToolCalls: []message.ToolCall{{ID: "a"}, {ID: "b"}}},
		{Role: message.RoleTool, ToolCallID: "a"},
		{Role: message.RoleTool, ToolCallID: "b"},
	}
	ages := RequestBatchesAfter(messages, 8)
	if ages[1] != 1 || ages[2] != 1 {
		t.Fatalf("parallel tool ages = %v, want both 1", ages)
	}
}

func TestRequestBatchesAfterIncludesFailedRequestGaps(t *testing.T) {
	messages := []message.Message{
		{Role: message.RoleAssistant, RequestBatch: 4, ToolCalls: []message.ToolCall{{ID: "a"}}},
		{Role: message.RoleTool, ToolCallID: "a"},
	}
	ages := RequestBatchesAfter(messages, 7)
	if ages[1] != 3 {
		t.Fatalf("tool age = %d, want 3 actual requests", ages[1])
	}
}

func TestReduceGoTestSuccessOutputSummaryRejectsFailedOutput(t *testing.T) {
	ctx := Context{
		Meta:    ToolCall{Args: `{"command":"go test ./..."}`},
		Content: "ok  \tgithub.com/example/project/internal/a\t(cached)\n--- FAIL: TestBroken (0.00s)\nFAIL\tgithub.com/example/project/internal/b\t0.01s\nFAIL\n",
	}
	if got, ok := reduceGoTestSuccessOutputSummary(ctx); ok {
		t.Fatalf("failed go test output must not use success summary, got %q", got)
	}
}

func TestReduceGoTestSuccessOutputSummaryRejectsErrorStatus(t *testing.T) {
	ctx := Context{
		Meta:       ToolCall{Args: `{"command":"go test ./..."}`},
		Content:    "ok  \tgithub.com/example/project/internal/a\t(cached)\n",
		ToolStatus: message.ToolStatusError,
	}
	if got, ok := reduceGoTestSuccessOutputSummary(ctx); ok {
		t.Fatalf("error-status go test output must not use success summary, got %q", got)
	}
}

func TestReduceGoTestSuccessOutputSummaryRejectsCancelledStatus(t *testing.T) {
	ctx := Context{
		Meta:       ToolCall{Args: `{"command":"go test ./..."}`},
		Content:    "ok  \tgithub.com/example/project/internal/a\t(cached)\n",
		ToolStatus: message.ToolStatusCancelled,
	}
	if got, ok := reduceGoTestSuccessOutputSummary(ctx); ok {
		t.Fatalf("cancelled go test output must not use success summary, got %q", got)
	}
}

func TestReadRetentionDoesNotProtectCancelledRead(t *testing.T) {
	ctx := Context{
		ToolName:   tools.NameRead,
		ToolStatus: message.ToolStatusCancelled,
	}
	if ctx.ReadRetentionProtects() {
		t.Fatal("cancelled read must not be protected as current file context")
	}
}

func TestDefaultContextReductionPolicyMatchesDefaultConfig(t *testing.T) {
	policy := DefaultPolicy()
	cfg := config.DefaultConfig().Context.Reduction

	if policy.ConfirmAgeTurns != cfg.ConfirmAgeTurns ||
		policy.ErrorAgeTurns != cfg.ErrorAgeTurns ||
		policy.HighRiskProtectAgeTurns != cfg.HighRiskProtectAgeTurns ||
		policy.DiffProtectAgeTurns != cfg.DiffProtectAgeTurns ||
		policy.ShellSuccessAgeTurns != cfg.ShellSuccessAgeTurns ||
		policy.ReadLikeAgeTurns != cfg.ReadLikeAgeTurns ||
		policy.StaleAgeTurns != cfg.StaleAgeTurns ||
		policy.ShellSuccessBytes != cfg.ShellSuccessBytes ||
		policy.ReadLikeOutputBytes != cfg.ReadLikeOutputBytes ||
		policy.StaleOutputBytes != cfg.StaleOutputBytes ||
		policy.WrapUpGraceRequests != cfg.WrapUpGraceRequests ||
		policy.MinToolResultsPrune != cfg.MinToolResultsPrune ||
		policy.MinIncrementalTokens != cfg.MinIncrementalTokens {
		t.Fatalf("default context reduction policy = %+v, DefaultConfig reduction = %+v", policy, cfg)
	}
}

func isShellSuccessSummaryLine(trimmed string) bool {
	return isStrongShellSuccessSummaryLine(trimmed) || isWeakShellSuccessSummaryLine(trimmed)
}

func TestShellSuccessSummaryLineMatcherAvoidsBroadSubstringMatches(t *testing.T) {
	for _, line := range []string{
		"book cache warmed",
		"passive mode enabled",
		"changedir complete",
		"successor elected",
		"unsuccessful attempt",
	} {
		if isShellSuccessSummaryLine(line) {
			t.Fatalf("%q should not be treated as a shell success summary line", line)
		}
	}
	for _, line := range []string{
		"ok github.com/example/project 1.23s",
		"PASS ./internal/foo",
		"coverage: 82.1%",
		"wrote dist/schema.json",
		"success: generated assets",
		"Successfully built abc123",
		"Successfully installed package",
	} {
		if !isShellSuccessSummaryLine(line) {
			t.Fatalf("%q should be treated as a shell success summary line", line)
		}
	}
}

func TestShellSuccessSummaryDoesNotDuplicateLongFinalSignalLine(t *testing.T) {
	longOK := "ok github.com/example/project " + strings.Repeat("x", SummaryLineSnippetChars+120)
	got := summarizeShellSuccess("prelude\n"+longOK+"\n", 4).Lines
	if len(got) != 1 {
		t.Fatalf("expected long final signal line to appear once, got %d lines: %q", len(got), got)
	}
	if !strings.Contains(got[0], "ok github.com/example/project") {
		t.Fatalf("expected summarized ok line, got %q", got)
	}
}

func TestParseDisplayedReadRangeSupportsReadResultHeader(t *testing.T) {
	content := strings.Join([]string{
		`READ_RESULT lines=41-43 total=200`,
		"func important() {",
		"\treturn true",
		"}",
	}, "\n")

	got := parseDisplayedReadRange(content)
	if !got.OK || got.Start != 41 || got.End != 43 || got.Total != 200 {
		t.Fatalf("parseDisplayedReadRange() = %+v, want 41-43/200", got)
	}
}

func TestParseDisplayedReadRangeSupportsReadResultHeaderFieldOrder(t *testing.T) {
	got := parseDisplayedReadRange("READ_RESULT total=200 truncated=aged lines=41-43\nbody")
	if !got.OK || got.Start != 41 || got.End != 43 || got.Total != 200 {
		t.Fatalf("parseDisplayedReadRange() = %+v, want 41-43/200", got)
	}
}

func TestParseDisplayedReadRangeRejectsMalformedReadResultHeader(t *testing.T) {
	for _, content := range []string{
		"READ_RESULT lines=none total=20\n",
		"READ_RESULT lines=43-41 total=200\n",
		"READ_RESULT lines=41-x total=200\n",
		"READ_RESULT lines=41-43 total=x\n",
		"READ_RESULTS lines=41-43 total=200\n",
	} {
		if got := parseDisplayedReadRange(content); got.OK {
			t.Fatalf("parseDisplayedReadRange(%q) = %+v, want invalid", content, got)
		}
	}
}

func TestParseDisplayedReadRangeReadResultAllocsGuard(t *testing.T) {
	content := "READ_RESULT lines=41-43 total=200 truncated=aged\nbody"
	allocs := testing.AllocsPerRun(100, func() {
		if got := parseDisplayedReadRange(content); !got.OK {
			t.Fatalf("parseDisplayedReadRange() = %+v, want valid", got)
		}
	})
	const maxAllocs = 0
	if allocs > maxAllocs {
		t.Fatalf("READ_RESULT range parse allocs = %.0f, want %d", allocs, maxAllocs)
	}
}

func TestParseDisplayedReadRangeSupportsLegacyFooter(t *testing.T) {
	content := strings.Join([]string{
		"source line 1",
		"source line 2",
		"(showing lines 41-42 of 200 total)",
	}, "\n")

	got := parseDisplayedReadRange(content)
	if !got.OK || got.Start != 41 || got.End != 42 || got.Total != 200 {
		t.Fatalf("parseDisplayedReadRange() = %+v, want 41-42/200", got)
	}
}

func TestParseDisplayedReadRangeSupportsLegacySingleLineFooter(t *testing.T) {
	got := parseDisplayedReadRange("source line\n(showing line 7 of 20 total)")
	if !got.OK || got.Start != 7 || got.End != 7 || got.Total != 20 {
		t.Fatalf("parseDisplayedReadRange() = %+v, want 7-7/20", got)
	}
}

func TestParseDisplayedReadRangePlainContentAllocsGuard(t *testing.T) {
	content := strings.Repeat("ordinary source line\n", 100)
	allocs := testing.AllocsPerRun(100, func() {
		if got := parseDisplayedReadRange(content); got.OK {
			t.Fatalf("parseDisplayedReadRange() = %+v, want no range", got)
		}
	})
	const maxAllocs = 1
	if allocs > maxAllocs {
		t.Fatalf("plain read range parse allocs = %.0f, want ≤%d", allocs, maxAllocs)
	}
}

func TestClassifyRequestReductionToolOutputUsesToolStatusError(t *testing.T) {
	ctx := Context{
		Content:    "patch makes no changes. No files were modified",
		ToolStatus: message.ToolStatusError,
		Age:        compactErrorAgeTurns,
		Policy:     DefaultPolicy(),
	}
	if got := Classify(ctx).Class; got != requestReductionToolError {
		t.Fatalf("class = %q, want %q", got, requestReductionToolError)
	}
}
