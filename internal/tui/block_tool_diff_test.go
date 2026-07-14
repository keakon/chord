package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

func TestEditToolCardRendersHighlightedDiffWithPath(t *testing.T) {
	patch := "@@\n-old\n+new\n"
	args, _ := json.Marshal(map[string]string{"path": "src/demo.go", "patch": patch})
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameEdit,
		Content:       string(args),
		ResultDone:    true,
		ResultStatus:  agent.ToolResultStatusSuccess,
		ResultContent: "Applied patch to src/demo.go (+1 -1)",
		Diff:          "--- src/demo.go\n+++ src/demo.go\n@@ -1 +1 @@\n-old\n+new\n",
	}

	rendered := strings.Join(block.Render(100, ""), "\n")
	plain := stripANSI(rendered)
	if !strings.Contains(plain, "edit") || !strings.Contains(plain, "src/demo.go") {
		t.Fatalf("expected edit header to show path, got:\n%s", plain)
	}
	if !strings.Contains(plain, "-old") || !strings.Contains(plain, "+new") {
		t.Fatalf("expected diff lines to render, got:\n%s", plain)
	}
	if rendered == plain {
		t.Fatal("expected diff render to include ANSI highlighting")
	}
}

func TestEditToolCardRendersDiagnosticsSummaryWithDiff(t *testing.T) {
	block := &Block{
		ID:           1,
		Type:         BlockToolCall,
		ToolName:     tools.NameEdit,
		Content:      `{"path":"internal/config/config_project_test.go","patch":"@@\n-old\n+new\n"}`,
		ResultDone:   true,
		ResultStatus: agent.ToolResultStatusSuccess,
		ResultContent: strings.Join([]string{
			"Applied patch to internal/config/config_project_test.go (+3 -2)",
			"",
			"Diagnostics summary:",
			"[E] 34:36 [UndeclaredName] undefined: DefaultContextReductionConfig",
		}, "\n"),
		Diff: "--- internal/config/config_project_test.go\n+++ internal/config/config_project_test.go\n@@ -1 +1 @@\n-old\n+new\n",
	}

	plain := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(plain, "↳ Diagnostics:") {
		t.Fatalf("expected diagnostics section to render with edit diff, got:\n%s", plain)
	}
	if !strings.Contains(plain, "undefined: DefaultContextReductionConfig") {
		t.Fatalf("expected diagnostic detail to render with edit diff, got:\n%s", plain)
	}
	if strings.Contains(plain, "Diagnostics summary:") || strings.Contains(plain, "Applied patch to internal/config/config_project_test.go") {
		t.Fatalf("expected only diagnostics detail in edit success diagnostics section, got:\n%s", plain)
	}
}

func TestEditLiveArgsWithoutCompletePathDoNotRenderDot(t *testing.T) {
	displayArgs := streamingToolDisplayArgs(tools.NameEdit, `{"patch":"*** Begin Patch\n*** Update File:`, "")
	if displayArgs != "" {
		t.Fatalf("display args = %q, want empty until path is parsed", displayArgs)
	}
	block := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: tools.NameEdit,
		Content:  displayArgs,
	}
	plain := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if strings.Contains(plain, "edit .") {
		t.Fatalf("expected incomplete Edit args not to render dot path, got:\n%s", plain)
	}
}

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

func TestLexerForFilePathUsesMDXAsMarkdown(t *testing.T) {
	hl := newCodeHighlighter("website/src/content/docs/index.mdx", "# Hello\n\n<Component />\n")
	lexer := hl.getLexer("# Hello\n")
	if lexer == nil {
		t.Fatal("expected lexer for .mdx extension")
	}
	if got := lexer.Config().Name; got != "markdown" {
		t.Fatalf("expected Markdown lexer for .mdx, got %q", got)
	}
}

func TestLexerForFilePathUsesMarkdownExtensionAsMarkdown(t *testing.T) {
	hl := newCodeHighlighter("README.markdown", "# Hello\n\nContent\n")
	lexer := hl.getLexer("# Hello\n")
	if lexer == nil {
		t.Fatal("expected lexer for .markdown extension")
	}
	if got := lexer.Config().Name; got != "markdown" {
		t.Fatalf("expected Markdown lexer for .markdown, got %q", got)
	}
}

func TestLexerForExplicitMDXLanguageUsesMarkdown(t *testing.T) {
	hl := newCodeHighlighterWithLanguage("", "# Hello\n\n<Component />\n", "mdx")
	lexer := hl.getLexer("# Hello\n")
	if lexer == nil {
		t.Fatal("expected lexer for mdx language hint")
	}
	if got := lexer.Config().Name; got != "markdown" {
		t.Fatalf("expected Markdown lexer for mdx language hint, got %q", got)
	}
}

func TestLexerForFilePathDisablesHighlightForUnknownExtension(t *testing.T) {
	hl := newCodeHighlighter("notes.unknownext", "package tools\n")
	rendered := hl.highlightLine("package tools", "")
	if rendered != "package tools" {
		t.Fatalf("expected unknown extension to remain plain text, got %q", rendered)
	}
}

func TestLexerForFilePathKeepsBackgroundForUnknownExtension(t *testing.T) {
	for _, tt := range []struct {
		name   string
		bgTerm string
	}{
		{name: "add", bgTerm: diffAddBg},
		{name: "delete", bgTerm: diffDelBg},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hl := newCodeHighlighter("notes.unknownext", "package tools\n")
			rendered := hl.highlightLine("package tools", tt.bgTerm)
			if plain := stripANSI(rendered); plain != "package tools" {
				t.Fatalf("expected unknown extension background fallback to preserve text, got %q", plain)
			}
			wantBg := ansiSeqForColor(lipgloss.Color(tt.bgTerm), false)
			if wantBg == "" {
				t.Fatal("expected diff background color to produce ANSI background sequence")
			}
			if !strings.Contains(rendered, wantBg) {
				t.Fatalf("expected unknown extension fallback to keep diff background %q, got %q", wantBg, rendered)
			}
			spaceRendered := hl.highlightLine("    ", tt.bgTerm)
			if plain := stripANSI(spaceRendered); plain != "    " {
				t.Fatalf("expected whitespace-only fallback to preserve spaces, got %q", plain)
			}
			if !strings.Contains(spaceRendered, wantBg) {
				t.Fatalf("expected whitespace-only fallback to keep diff background %q, got %q", wantBg, spaceRendered)
			}
		})
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
	lines := renderInlineDiffLine("myVariable", "myHTTPVariable", 40, nil)
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

func TestRenderInlineDiffLineKeepsSyntaxHighlighting(t *testing.T) {
	hl := newCodeHighlighter("example.go", "func demo() {\n\treturn 1\n}\n")
	lines := renderInlineDiffLine("return 1", "return 10", 80, hl)
	if len(lines) != 1 {
		t.Fatalf("expected single-line inline diff, got %#v", lines)
	}
	keywordSeq := ansiSeqForColor(lipgloss.Color(toolCodeChromaStyle().Get(chroma.Keyword).Colour.String()), true)
	if keywordSeq == "" {
		t.Fatal("expected Go keyword colour to produce an ANSI sequence")
	}
	if !strings.Contains(lines[0], keywordSeq+"return") {
		t.Fatalf("expected inline diff to keep Go keyword highlighting, got %q", lines[0])
	}
	if !strings.Contains(stripANSI(lines[0]), "+return 10") {
		t.Fatalf("expected changed line to stay visible, got %q", stripANSI(lines[0]))
	}
}

func TestRenderInlineDiffLineKeepsTabIndentedDeletionAligned(t *testing.T) {
	oldLine := "\tcase tools.NameGrep, tools.NameGlob, tools.NameShell, tools.NameSpawn, tools.NameLsp:"
	newLine := "\tcase tools.NameGrep, tools.NameGlob, tools.NameShell, tools.NameSpawn:"
	hl := newCodeHighlighter("example.go", "package tui\n\nfunc example() {\n"+oldLine+"\n}\n")

	lines := renderInlineDiffLine(oldLine, newLine, 120, hl)
	if len(lines) != 1 {
		t.Fatalf("expected single-line inline diff, got %#v", lines)
	}
	wantLine := "-" + expandTabsForDisplay(oldLine, preformattedTabWidth)
	if got := stripANSI(lines[0]); got != wantLine {
		t.Fatalf("rendered line = %q, want %q", got, wantLine)
	}
	want := DiffDelInlineStyle.Render(", tools.NameLsp")
	if !strings.Contains(lines[0], want) {
		t.Fatalf("expected deletion style to cover only %q, got %q", ", tools.NameLsp", lines[0])
	}
}

func TestDiffTextWidthMatchesGraphemeRendererWithTabs(t *testing.T) {
	for _, text := range []string{
		"界",
		"👨‍👩‍👧‍👦",
		"👍🏽",
		"e\u0301",
		"\t👨‍👩‍👧‍👦",
		"a\t👍🏽",
	} {
		expanded := expandTabsForDisplay(text, preformattedTabWidth)
		if got, want := diffTextWidth(text), tuiStringWidth(expanded); got != want {
			t.Fatalf("diffTextWidth(%q) = %d, want rendered width %d", text, got, want)
		}
	}
}

func TestRenderInlineDiffLineUsesGraphemeWidthLimit(t *testing.T) {
	prefix := strings.Repeat("👨‍👩‍👧‍👦", 40)
	oldLine := prefix
	newLine := prefix + "x"
	if got := tuiStringWidth(oldLine); got > singleLineDiffColumnsLimit {
		t.Fatalf("fixture rendered width = %d, want <= %d", got, singleLineDiffColumnsLimit)
	}

	lines := renderInlineDiffLine(oldLine, newLine, 120, nil)
	if len(lines) != 1 {
		t.Fatalf("grapheme-width eligible inline diff = %#v, want one line", lines)
	}
	if got := stripANSI(lines[0]); got != "+"+newLine {
		t.Fatalf("rendered line = %q, want %q", got, "+"+newLine)
	}
}

func TestHighlightCodeLinesKeepsMarkdownEOFBlockMarkersStyled(t *testing.T) {
	hl := newCodeHighlighter("plan.md", "")
	lines := []string{
		"1. first item",
		"2. second item",
	}

	rendered := highlightCodeLines(hl, lines, "")
	if len(rendered) != len(lines) {
		t.Fatalf("expected %d highlighted lines, got %d: %#v", len(lines), len(rendered), rendered)
	}
	keywordSeq := ansiSeqForColor(lipgloss.Color(toolCodeChromaStyle().Get(chroma.Keyword).Colour.String()), true)
	if keywordSeq == "" {
		t.Fatal("expected markdown keyword colour to produce an ANSI sequence")
	}
	for i, line := range rendered {
		marker := fmt.Sprintf("%d.", i+1)
		if !strings.Contains(line, keywordSeq+marker) {
			t.Fatalf("expected marker %q to be highlighted with keyword style; got %q", marker, line)
		}
	}
}

func TestHighlightCodeLinesKeepsMarkdownEOFHeadingStyled(t *testing.T) {
	hl := newCodeHighlighter("notes.md", "")
	lines := []string{
		"# first heading",
		"## second heading",
	}

	rendered := highlightCodeLines(hl, lines, "")
	if len(rendered) != len(lines) {
		t.Fatalf("expected %d highlighted lines, got %d: %#v", len(lines), len(rendered), rendered)
	}
	subheadingSeq := ansiSeqForColor(lipgloss.Color(toolCodeChromaStyle().Get(chroma.GenericSubheading).Colour.String()), true)
	if subheadingSeq == "" {
		t.Fatal("expected markdown subheading colour to produce an ANSI sequence")
	}
	if !strings.Contains(rendered[1], subheadingSeq+"## second heading") {
		t.Fatalf("expected EOF subheading to be highlighted with subheading style; got %q", rendered[1])
	}
}

func TestRenderInlineDiffLineFallsBackForMixedTokenRewrite(t *testing.T) {
	lines := renderInlineDiffLine("prefixSuffix", "preFIXmidSUFsuffix", 80, nil)
	if lines != nil {
		t.Fatalf("expected mixed token rewrite to fall back to two-line diff, got %#v", lines)
	}
}

func TestRenderInlineDiffLineFallsBackForPureInsertionWithMultipleRunsInOneToken(t *testing.T) {
	lines := renderInlineDiffLine("myVariable", "myHVariableTTPX", 80, nil)
	if lines != nil {
		t.Fatalf("expected fragmented same-token insertion to fall back to two-line diff, got %#v", lines)
	}
}

func TestRenderInlineDiffLineKeepsSingleTokenDeletionSingleLine(t *testing.T) {
	lines := renderInlineDiffLine("github.com/org/service/internal/api", "github.com/org/service/api", 60, nil)
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
	lines := renderInlineDiffLine(oldLine, newLine, 28, nil)
	if lines != nil {
		t.Fatalf("expected argument expansion with token rewrite to fall back to two-line diff, got %#v", lines)
	}
}

func TestRenderInlineDiffLineFallsBackForMultiTokenMixedRewrite(t *testing.T) {
	lines := renderInlineDiffLine("foo(bar, baz)", "foo(longBar, qux)", 80, nil)
	if lines != nil {
		t.Fatalf("expected multi-token mixed rewrite to use two-line diff, got %#v", lines)
	}
}

func TestRenderInlineDiffLineFallsBackBeyondHardColumnLimit(t *testing.T) {
	oldLine := strings.Repeat("a", 201)
	newLine := oldLine + "HTTP"
	lines := renderInlineDiffLine(oldLine, newLine, 120, nil)
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
	lines := renderInlineDiffLine(oldLine, newLine, 80, nil)
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
	lines := renderInlineDiffLine(oldLine, newLine, 24, nil)
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
		ToolName:          tools.NameEdit,
		Content:           fmt.Sprintf(`{"path":"%s","patch":"@@\n-old\n+new\n"}`, filepath.Join("internal", "tui", "example.go")),
		Diff:              "--- example.go\n+++ example.go\n@@ -1,1 +1,1 @@\n-old\n+new\n",
		ResultDone:        true,
		ResultStatus:      agent.ToolResultStatusSuccess,
		displayWorkingDir: wd,
	}
	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	want := filepath.Join("internal", "tui", "example.go")
	if !strings.Contains(joined, "edit") || !strings.Contains(joined, want) {
		t.Fatalf("expected edit header to show relative path; got:\n%s", joined)
	}
	_ = abs
}

func TestRenderFileDiffCallInsertionContextUsesNewLineNumbers(t *testing.T) {
	block := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: tools.NameEdit,
		Content:  `{"path":"example.py","patch":"@@\n-old\n+new\n"}`,
		Diff: "--- a/example.py\n+++ b/example.py\n@@ -8,4 +8,5 @@\n" +
			" def build_items():\n" +
			"     items = [\n" +
			"+        \"added\",\n" +
			"         \"existing\",\n",
		ResultDone:   true,
		ResultStatus: agent.ToolResultStatusSuccess,
	}
	plain := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(plain, "  10 +        \"added\",") {
		t.Fatalf("expected inserted line to use new line number 10, got:\n%s", plain)
	}
	if !strings.Contains(plain, "  11          \"existing\",") {
		t.Fatalf("expected following context line to use new line number 11, got:\n%s", plain)
	}
}

func TestRenderFileDiffCallDeletionContextDoesNotDecreaseLineNumbers(t *testing.T) {
	block := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: tools.NameEdit,
		Content:  `{"path":"example.py","patch":"@@\n-old\n+new\n"}`,
		Diff: "--- a/example.py\n+++ b/example.py\n@@ -8,5 +8,4 @@\n" +
			" def build_items():\n" +
			"     items = [\n" +
			"-        \"removed\",\n" +
			"         \"existing\",\n",
		ResultDone:   true,
		ResultStatus: agent.ToolResultStatusSuccess,
	}
	plain := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(plain, "  10 -        \"removed\",") {
		t.Fatalf("expected deleted line to use old line number 10, got:\n%s", plain)
	}
	if !strings.Contains(plain, "  11          \"existing\",") {
		t.Fatalf("expected following context line to avoid decreasing from deleted line number 10, got:\n%s", plain)
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
		ToolName:     tools.NameEdit,
		Content:      `{"path":"example.go","patch":"@@\n-old\n+new\n"}`,
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
		ToolName:     tools.NameEdit,
		Content:      `{"path":"example.go","patch":"@@\n-old\n+new\n"}`,
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
		ToolName:     tools.NameEdit,
		Content:      `{"path":"example.go","patch":"@@\n-old\n+new\n"}`,
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
		ToolName:     tools.NameEdit,
		Content:      `{"path":"example.go","patch":"@@\n-old\n+new\n"}`,
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

// TestEditDiffHeaderPathIsCwdRelative guards against regressing to an absolute
// edit path in the diff header. ExtractEditPathFromArgs resolves to an absolute
// path; diffToolFilePath must shorten it to a cwd-relative form so a long
// absolute prefix (deep tree, long $HOME, git worktree) cannot push the file
// name out of the width-clipped header.
func TestEditDiffHeaderPathIsCwdRelative(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"path": "src/demo.go", "patch": "@@\n-old\n+new\n"})
	block := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: tools.NameEdit,
		Content:  string(args),
	}

	got := block.diffToolFilePath()
	if filepath.IsAbs(got) {
		t.Fatalf("diffToolFilePath returned absolute path %q, want cwd-relative", got)
	}
	if got != "src/demo.go" {
		t.Fatalf("diffToolFilePath = %q, want %q", got, "src/demo.go")
	}

	// The rendered header keeps the file name even at a width far smaller than
	// the absolute path length.
	block.ResultDone = true
	block.ResultStatus = agent.ToolResultStatusSuccess
	block.Diff = "--- src/demo.go\n+++ src/demo.go\n@@ -1 +1 @@\n-old\n+new\n"
	plain := stripANSI(strings.Join(block.Render(60, ""), "\n"))
	if !strings.Contains(plain, "demo.go") {
		t.Fatalf("expected file name to survive header truncation, got:\n%s", plain)
	}
}

// TestRelToProcessWorkingDir covers the helper directly: absolute paths under
// cwd become relative; paths outside it or already relative are left for the
// caller to handle.
func TestRelToProcessWorkingDir(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if got := relToProcessWorkingDir(filepath.Join(wd, "a", "b.go")); got != filepath.Join("a", "b.go") {
		t.Fatalf("under-cwd abs path = %q, want %q", got, filepath.Join("a", "b.go"))
	}
	if got := relToProcessWorkingDir("a/b.go"); got != "a/b.go" {
		t.Fatalf("relative path = %q, want unchanged", got)
	}
	if got := relToProcessWorkingDir(filepath.Dir(wd)); got != "" {
		t.Fatalf("parent of cwd = %q, want \"\" (escapes upward)", got)
	}
}
