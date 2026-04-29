package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

func TestLexerForFilePathPrefersWhitelistedExtensionOverConflictingBasename(t *testing.T) {
	hl := newCodeHighlighter("bash_jobs.go", "package tools\n")
	lexer := hl.getLexer("package tools\n")
	if lexer == nil {
		t.Fatal("expected lexer for whitelisted .go extension")
	}
	if got := lexer.Config().Name; got != "Go" {
		t.Fatalf("expected Go lexer for bash_jobs.go, got %q", got)
	}
}

func TestLexerForFilePathUsesSpecialFilenameWithSuffix(t *testing.T) {
	hl := newCodeHighlighter("Dockerfile.prod", "FROM alpine:3.20\nRUN echo hi\n")
	lexer := hl.getLexer("FROM alpine:3.20\nRUN echo hi\n")
	if lexer == nil {
		t.Fatal("expected lexer for Dockerfile.prod")
	}
	if got := lexer.Config().Name; got != "Docker" {
		t.Fatalf("expected Docker lexer for Dockerfile.prod, got %q", got)
	}
}

func TestLexerForFilePathDisablesHighlightForUnknownExtension(t *testing.T) {
	hl := newCodeHighlighter("notes.unknownext", "package tools\n")
	rendered := hl.highlightLine("package tools", "")
	if rendered != "package tools" {
		t.Fatalf("expected unknown extension to remain plain text, got %q", rendered)
	}
}

func TestLexerForFilePathDisablesHighlightForUnknownBasenameWithoutExtension(t *testing.T) {
	hl := newCodeHighlighter("Jenkinsfile", "pipeline { agent any }\n")
	rendered := hl.highlightLine("pipeline { agent any }", "")
	if rendered != "pipeline { agent any }" {
		t.Fatalf("expected unsupported basename to remain plain text, got %q", rendered)
	}
}

func TestLexerForContentOnlyStillUsesAnalysis(t *testing.T) {
	hl := newCodeHighlighter("", "package tools\n")
	lexer := hl.getLexer("package tools\n")
	if lexer == nil {
		t.Fatal("expected lexer when no file path is available")
	}
	if got := lexer.Config().Name; got == "fallback" {
		t.Fatalf("expected analysis-based lexer for content-only highlighting, got %q", got)
	}
}

func TestRenderInlineDiffLineKeepsSingleTokenInsertionSingleLine(t *testing.T) {
	lines := renderInlineDiffLine("myVariable", "myHTTPVariable", 40)
	if len(lines) != 1 {
		t.Fatalf("expected single-line inline diff, got %d lines: %#v", len(lines), lines)
	}
	plain := stripANSI(lines[0])
	if !strings.HasPrefix(plain, "+") {
		t.Fatalf("expected insertion line, got %q", plain)
	}
	if !strings.Contains(plain, "myHTTPVariable") {
		t.Fatalf("expected inserted token to remain visible, got %q", plain)
	}
}

func TestRenderInlineDiffLineFallsBackForMixedTokenRewrite(t *testing.T) {
	lines := renderInlineDiffLine("prefixSuffix", "preFIXmidSUFsuffix", 80)
	if lines != nil {
		t.Fatalf("expected mixed token rewrite to fall back to two-line diff, got %#v", lines)
	}
}

func TestRenderInlineDiffLineFallsBackForPureInsertionWithMultipleRunsInOneToken(t *testing.T) {
	lines := renderInlineDiffLine("myVariable", "myHVariableTTPX", 80)
	if lines != nil {
		t.Fatalf("expected fragmented same-token insertion to fall back to two-line diff, got %#v", lines)
	}
}

func TestRenderInlineDiffLineKeepsSingleTokenDeletionSingleLine(t *testing.T) {
	lines := renderInlineDiffLine("github.com/org/service/internal/api", "github.com/org/service/api", 60)
	if len(lines) != 1 {
		t.Fatalf("expected single-line deletion diff, got %#v", lines)
	}
	plain := stripANSI(lines[0])
	if !strings.HasPrefix(plain, "-") {
		t.Fatalf("expected deletion line, got %q", plain)
	}
	if !strings.Contains(plain, "internal/") {
		t.Fatalf("expected deleted path segment to remain visible, got %q", plain)
	}
}

func TestRenderInlineDiffLineFunctionArgumentExpansionFallsBackToTwoLineDiff(t *testing.T) {
	oldLine := strings.Repeat("prefix", 6) + " foo(bar, baz) " + strings.Repeat("suffix", 6)
	newLine := strings.Repeat("prefix", 6) + " foo(longBar, baz) " + strings.Repeat("suffix", 6)
	lines := renderInlineDiffLine(oldLine, newLine, 28)
	if lines != nil {
		t.Fatalf("expected argument expansion with token rewrite to fall back to two-line diff, got %#v", lines)
	}
}

func TestRenderInlineDiffLineFallsBackForMultiTokenMixedRewrite(t *testing.T) {
	lines := renderInlineDiffLine("foo(bar, baz)", "foo(longBar, qux)", 80)
	if lines != nil {
		t.Fatalf("expected multi-token mixed rewrite to use two-line diff, got %#v", lines)
	}
}

func TestRenderInlineDiffLineFallsBackBeyondHardColumnLimit(t *testing.T) {
	oldLine := strings.Repeat("a", 201)
	newLine := oldLine + "HTTP"
	lines := renderInlineDiffLine(oldLine, newLine, 120)
	if lines != nil {
		t.Fatalf("expected >200-column line to force two-line diff, got %#v", lines)
	}
}

func TestRenderInlineDiffLineFallsBackBeyondConfiguredColumnLimit(t *testing.T) {
	oldLimit := singleLineDiffColumnsLimit
	SetSingleLineDiffColumnsLimit(20)
	defer SetSingleLineDiffColumnsLimit(oldLimit)

	oldLine := "012345678901234567890"
	newLine := oldLine + "HTTP"
	lines := renderInlineDiffLine(oldLine, newLine, 80)
	if lines != nil {
		t.Fatalf("expected configured width limit to force two-line diff, got %#v", lines)
	}
}

func TestSetSingleLineDiffColumnsLimitResetsOnInvalidValue(t *testing.T) {
	oldLimit := singleLineDiffColumnsLimit
	defer SetSingleLineDiffColumnsLimit(oldLimit)

	SetSingleLineDiffColumnsLimit(123)
	if singleLineDiffColumnsLimit != 123 {
		t.Fatalf("singleLineDiffColumnsLimit = %d, want 123", singleLineDiffColumnsLimit)
	}
	SetSingleLineDiffColumnsLimit(0)
	if singleLineDiffColumnsLimit != defaultSingleLineDiffColumns {
		t.Fatalf("singleLineDiffColumnsLimit = %d, want default %d", singleLineDiffColumnsLimit, defaultSingleLineDiffColumns)
	}
}

func TestRenderInlineDiffLineLongLineUsesChangeSnippet(t *testing.T) {
	oldLine := strings.Repeat("prefix", 8) + " myVariable " + strings.Repeat("suffix", 8)
	newLine := strings.Repeat("prefix", 8) + " myHTTPVariable " + strings.Repeat("suffix", 8)
	lines := renderInlineDiffLine(oldLine, newLine, 24)
	if len(lines) != 1 {
		t.Fatalf("expected single-line snippet diff, got %#v", lines)
	}
	plain := stripANSI(lines[0])
	if !strings.Contains(plain, "myHTTPVariable") {
		t.Fatalf("expected snippet to keep changed region visible, got %q", plain)
	}
	if !strings.Contains(plain, "…") {
		t.Fatalf("expected snippet ellipsis for long line, got %q", plain)
	}
}

func TestRenderFileDiffCallHeaderShowsRelativePathInsideWorkingDir(t *testing.T) {
	wd := filepath.Join(string(os.PathSeparator), "tmp", "workspace")
	abs := filepath.Join(wd, "internal", "tui", "example.go")
	block := &Block{
		ID:                1,
		Type:              BlockToolCall,
		ToolName:          "Edit",
		Content:           fmt.Sprintf(`{"path":%q}`, abs),
		Diff:              "--- example.go\n+++ example.go\n@@ -1,1 +1,1 @@\n-old\n+new\n",
		ResultDone:        true,
		ResultStatus:      agent.ToolResultStatusSuccess,
		displayWorkingDir: wd,
	}
	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	want := filepath.Join("internal", "tui", "example.go")
	if !strings.Contains(joined, "Edit "+want) {
		t.Fatalf("expected Edit header to show relative path; got:\n%s", joined)
	}
	if strings.Contains(joined, abs) {
		t.Fatalf("did not expect Edit header to show absolute path; got:\n%s", joined)
	}
}

func TestRenderFileDiffCallGroupedMinusPlusBlockUsesInlineOneSidedPairs(t *testing.T) {
	old := "\t\t// separator(1) + content(lines) + bottom margin(1) + extra bars\n\t\treturn lines + 2 + extraBars\n"
	new := "\t\t// separator(1) + content(lines) + bottom margin(1)\n\t\treturn lines + 2\n"
	diff := tools.GenerateUnifiedDiff(old, new, "example.go")
	if diff == "" {
		t.Fatal("expected non-empty unified diff")
	}
	block := &Block{
		ID:           1,
		Type:         BlockToolCall,
		ToolName:     "Edit",
		Content:      `{"path":"example.go"}`,
		Diff:         diff,
		ResultDone:   true,
		ResultStatus: agent.ToolResultStatusSuccess,
	}
	lines := block.Render(120, "")
	plain := stripANSI(strings.Join(lines, "\n"))
	if strings.Contains(plain, "   1 +") || strings.Contains(plain, "   2 +") {
		t.Fatalf("expected grouped pure deletions to render as inline '-' lines only, got:\n%s", plain)
	}
	if !strings.Contains(plain, "extra bars") || !strings.Contains(plain, "extraBars") {
		t.Fatalf("expected deleted fragments to remain visible in inline diff, got:\n%s", plain)
	}
}

func TestRenderFileDiffCallPureDeletionLongLineUsesSnippets(t *testing.T) {
	oldLine := strings.Repeat("prefix", 7) + " github.com/org/service/internal/api " + strings.Repeat("suffix", 7)
	newLine := strings.Repeat("prefix", 7) + " github.com/org/service/api " + strings.Repeat("suffix", 7)
	block := &Block{
		ID:           1,
		Type:         BlockToolCall,
		ToolName:     "Edit",
		Content:      `{"path":"example.go"}`,
		Diff:         fmt.Sprintf("--- example.go\n+++ example.go\n@@ -1,1 +1,1 @@\n-%s\n+%s\n", oldLine, newLine),
		ResultDone:   true,
		ResultStatus: agent.ToolResultStatusSuccess,
	}
	lines := block.Render(46, "")
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "service/internal/api") {
		t.Fatalf("expected snippet to keep deleted path region visible, got:\n%s", plain)
	}
	if !strings.Contains(plain, "…") {
		t.Fatalf("expected snippet to use ellipsis, got:\n%s", plain)
	}
}

func TestRenderFileDiffCallOverHardColumnLimitUsesTwoLines(t *testing.T) {
	oldLine := strings.Repeat("a", 201)
	newLine := oldLine + "HTTP"
	block := &Block{
		ID:           1,
		Type:         BlockToolCall,
		ToolName:     "Edit",
		Content:      `{"path":"example.go"}`,
		Diff:         fmt.Sprintf("--- example.go\n+++ example.go\n@@ -1,1 +1,1 @@\n-%s\n+%s\n", oldLine, newLine),
		ResultDone:   true,
		ResultStatus: agent.ToolResultStatusSuccess,
	}
	lines := block.Render(120, "")
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "   1 -") {
		t.Fatalf("expected old line in two-line diff, got:\n%s", plain)
	}
	if !strings.Contains(plain, "   1 +") {
		t.Fatalf("expected new line in two-line diff, got:\n%s", plain)
	}
}

func TestRenderFileDiffCallMixedLongLinesUseTwoLineSnippets(t *testing.T) {
	oldLine := strings.Repeat("prefix", 8) + " myVariable " + strings.Repeat("suffix", 8)
	newLine := strings.Repeat("prefix", 8) + " otherHTTPValue " + strings.Repeat("suffix", 8)
	block := &Block{
		ID:           1,
		Type:         BlockToolCall,
		ToolName:     "Edit",
		Content:      `{"path":"example.go"}`,
		Diff:         fmt.Sprintf("--- example.go\n+++ example.go\n@@ -1,1 +1,1 @@\n-%s\n+%s\n", oldLine, newLine),
		ResultDone:   true,
		ResultStatus: agent.ToolResultStatusSuccess,
	}
	lines := block.Render(38, "")
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "   1 -") || !strings.Contains(plain, "   1 +") {
		t.Fatalf("expected mixed rewrite to render as two lines, got:\n%s", plain)
	}
	if !strings.Contains(plain, "myVariable") {
		t.Fatalf("expected old snippet to preserve changed token, got:\n%s", plain)
	}
	if !strings.Contains(plain, "otherHTTPValue") {
		t.Fatalf("expected new snippet to preserve changed token, got:\n%s", plain)
	}
	if !strings.Contains(plain, "…") {
		t.Fatalf("expected long two-line diff to use ellipsis snippets, got:\n%s", plain)
	}
}

func TestRenderHighlightedSnippetLineShowsHiddenClusterSummary(t *testing.T) {
	hl := newCodeHighlighter("example.go", "")
	line := "alpha ONE beta TWO gamma THREE delta"
	spans := []diffSegmentSpan{
		{StartCol: strings.Index(line, "ONE"), EndCol: strings.Index(line, "ONE") + len("ONE")},
		{StartCol: strings.Index(line, "TWO"), EndCol: strings.Index(line, "TWO") + len("TWO")},
		{StartCol: strings.Index(line, "THREE"), EndCol: strings.Index(line, "THREE") + len("THREE")},
	}
	rendered := renderHighlightedSnippetLine(line, spans, 26, hl, diffAddBg)
	plain := stripANSI(rendered)
	if !strings.Contains(plain, "(+1)") {
		t.Fatalf("expected hidden cluster summary, got %q", plain)
	}
	if !strings.Contains(plain, "ONE") || !strings.Contains(plain, "TWO") {
		t.Fatalf("expected visible clusters to remain in snippet, got %q", plain)
	}
}

func TestRenderHighlightedSnippetLineOmitsSummaryWhenTooNarrow(t *testing.T) {
	hl := newCodeHighlighter("example.go", "")
	line := "alpha ONE beta TWO gamma THREE delta"
	spans := []diffSegmentSpan{
		{StartCol: strings.Index(line, "ONE"), EndCol: strings.Index(line, "ONE") + len("ONE")},
		{StartCol: strings.Index(line, "TWO"), EndCol: strings.Index(line, "TWO") + len("TWO")},
		{StartCol: strings.Index(line, "THREE"), EndCol: strings.Index(line, "THREE") + len("THREE")},
	}
	rendered := renderHighlightedSnippetLine(line, spans, 12, hl, diffAddBg)
	plain := stripANSI(rendered)
	if strings.Contains(plain, "(+1)") {
		t.Fatalf("expected hidden cluster summary to be omitted when width is too narrow, got %q", plain)
	}
}
