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

func TestEditDiffHeaderRecoversPathFromInvalidArgs(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameEdit,
		Content:       `{"path":"src/app.go","old_string":123,"new_string":"b"}`,
		ResultDone:    true,
		ResultStatus:  agent.ToolResultStatusError,
		ResultContent: "arguments do not match edit schema: args.old_string must be a string, got number 123",
	}
	if got := block.diffToolFilePath(); got != "src/app.go" {
		t.Fatalf("diffToolFilePath with invalid sibling arg = %q, want %q", got, "src/app.go")
	}
}

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

func TestApplyPatchToolCardRendersNamePathAndHighlightedPreview(t *testing.T) {
	ApplyTheme(DefaultTheme())
	args, _ := json.Marshal(map[string]string{"patch": strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: src/demo.go",
		"@@",
		"-func old() {}",
		"+func main() {}",
		"*** End Patch",
	}, "\n")})
	block := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: tools.NameApplyPatch,
		Content:  applyPatchToolDisplayArgs(string(args)),
		RawArgs:  string(args),
	}

	lines := block.Render(100, "●")
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "apply_patch src/demo.go") {
		t.Fatalf("expected apply_patch header with path, got:\n%s", plain)
	}
	if strings.Contains(plain, "↳ Targets:") || strings.Contains(plain, "M src/demo.go") {
		t.Fatalf("expected single-file target list to be omitted as duplicate, got:\n%s", plain)
	}
	if !strings.Contains(plain, "+func main() {}") {
		t.Fatalf("expected patch preview, got:\n%s", plain)
	}
	added := renderedLineContaining(t, lines, "func main")
	assertRenderedTextForeground(t, added, "func", colorOfTheme(toolCodeChromaStyle().Get(chroma.Keyword).Colour.String()))
	assertRenderedTextBackground(t, added, "func", colorOfTheme(currentTheme.DiffAddLineBg))
}

func TestApplyPatchToolCardSummarizesMultiplePaths(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"patch": strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: src/old.go",
		"*** Move to: src/new.go",
		"@@",
		"-package old",
		"+package renamed",
		"*** Add File: docs/new.md",
		"+# New",
		"*** Delete File: tmp/old.txt",
		"*** End Patch",
	}, "\n")})
	block := &Block{ID: 1, Type: BlockToolCall, ToolName: tools.NameApplyPatch, Content: applyPatchToolDisplayArgs(string(args)), RawArgs: string(args)}
	plain := stripANSI(strings.Join(block.Render(120, "●"), "\n"))
	for _, want := range []string{
		"apply_patch src/old.go → src/new.go +2 files",
		"↳ Targets:",
		"R src/old.go → src/new.go",
		"A docs/new.md",
		"D tmp/old.txt",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected multi-file apply_patch display to contain %q, got:\n%s", want, plain)
		}
	}
}

func TestApplyPatchToolCardGroupsMultiFileDiffByFile(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"patch": strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: src/first.go",
		"@@",
		"-const first = 1",
		"+const first = 2",
		"*** Update File: src/second.go",
		"@@",
		"-const second = 1",
		"+const second = 2",
		"*** End Patch",
	}, "\n")})
	block := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: tools.NameApplyPatch,
		Content:  applyPatchToolDisplayArgs(string(args)),
		RawArgs:  string(args),
		Diff: strings.Join([]string{
			"--- src/first.go",
			"+++ src/first.go",
			"@@ -1,1 +1,1 @@",
			"-const first = 1",
			"+const first = 2",
			"--- src/second.go",
			"+++ src/second.go",
			"@@ -1,1 +1,1 @@",
			"-const second = 1",
			"+const second = 2",
		}, "\n"),
	}

	plain := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if strings.Contains(plain, "↳ Targets:") {
		t.Fatalf("expected per-file diff sections instead of a detached file list, got:\n%s", plain)
	}
	firstHeader := strings.Index(plain, "↳ M src/first.go")
	firstChange := strings.Index(plain, "const first = 2")
	secondHeader := strings.Index(plain, "↳ M src/second.go")
	secondChange := strings.Index(plain, "const second = 2")
	if firstHeader < 0 || firstChange < 0 || secondHeader < 0 || secondChange < 0 ||
		!(firstHeader < firstChange && firstChange < secondHeader && secondHeader < secondChange) {
		t.Fatalf("expected each file heading immediately before its own diff section, got:\n%s", plain)
	}
}

func TestApplyPatchToolCardSummarizesDeleteAndKeepsMoveUpdateDiff(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"patch": strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: src/old.go",
		"*** Move to: src/new.go",
		"@@",
		"-const moved = 1",
		"+const moved = 2",
		"*** Add File: docs/new.md",
		"+# New",
		"*** Delete File: tmp/old.txt",
		"*** End Patch",
	}, "\n")})
	block := &Block{
		ID:           1,
		Type:         BlockToolCall,
		ToolName:     tools.NameApplyPatch,
		Content:      applyPatchToolDisplayArgs(string(args)),
		RawArgs:      string(args),
		ResultDone:   true,
		ResultStatus: agent.ToolResultStatusSuccess,
		Diff: strings.Join([]string{
			"--- src/old.go",
			"+++ src/new.go",
			"@@ -1,1 +1,1 @@",
			"-const moved = 1",
			"+const moved = 2",
			"--- /dev/null",
			"+++ docs/new.md",
			"@@ -0,0 +1,1 @@",
			"+# New",
			"--- tmp/old.txt",
			"+++ /dev/null",
			"@@ -1,1 +0,0 @@",
			"-obsolete",
		}, "\n"),
	}

	plain := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	for _, want := range []string{
		"↳ R src/old.go → src/new.go",
		"↳ A docs/new.md",
		"↳ D tmp/old.txt",
		"const moved = 2",
		"# New",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected multi-file diff section %q, got:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "obsolete") {
		t.Fatalf("expected deleted file contents to be hidden, got:\n%s", plain)
	}
}

func TestApplyPatchToolCardHidesSingleDeleteDiff(t *testing.T) {
	args := `{"patch":"*** Begin Patch\n*** Delete File: tmp/old.txt\n*** End Patch"}`
	block := &Block{
		ID: 1, Type: BlockToolCall, ToolName: tools.NameApplyPatch,
		Content: applyPatchToolDisplayArgs(args), RawArgs: args,
		ResultDone: true, ResultStatus: agent.ToolResultStatusSuccess,
		ResultContent: "Applied patch:\nD tmp/old.txt",
		Diff:          "--- tmp/old.txt\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-obsolete\n-secret\n",
	}

	plain := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(plain, "apply_patch D tmp/old.txt") {
		t.Fatalf("expected delete summary in header, got:\n%s", plain)
	}
	for _, hidden := range []string{"obsolete", "secret", "↳ Requested patch:"} {
		if strings.Contains(plain, hidden) {
			t.Fatalf("expected %q to be hidden for file deletion, got:\n%s", hidden, plain)
		}
	}
}

func TestApplyPatchToolCardShowsNoChangesWithoutPatchPreview(t *testing.T) {
	args := `{"patch":"*** Begin Patch\n*** Update File: docs/guide.md\n@@\n section\n*** End Patch"}`
	block := &Block{
		ID: 1, Type: BlockToolCall, ToolName: tools.NameApplyPatch,
		Content: applyPatchToolDisplayArgs(args), RawArgs: args,
		ResultDone: true, ResultStatus: agent.ToolResultStatusSuccess,
		ResultContent: "Applied patch:\nNo net file changes",
	}

	plain := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(plain, "No changes") {
		t.Fatalf("expected no-changes indicator, got:\n%s", plain)
	}
	if strings.Contains(plain, "*** Begin Patch") {
		t.Fatalf("expected successful no-change card to hide patch preview, got:\n%s", plain)
	}
}

func TestApplyPatchToolCardHidesPureMoveDiff(t *testing.T) {
	args := `{"patch":"*** Begin Patch\n*** Update File: src/old.go\n*** Move to: src/new.go\n*** End Patch"}`
	block := &Block{
		ID: 1, Type: BlockToolCall, ToolName: tools.NameApplyPatch,
		Content: applyPatchToolDisplayArgs(args), RawArgs: args,
		ResultDone: true, ResultStatus: agent.ToolResultStatusSuccess,
		ResultContent: "Applied patch:\nR src/old.go -> src/new.go",
		Diff:          "--- src/old.go\n+++ src/new.go\n",
	}

	plain := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(plain, "apply_patch src/old.go → src/new.go") {
		t.Fatalf("expected move path summary in header, got:\n%s", plain)
	}
	if strings.Contains(plain, "↳ Requested patch:") || strings.Contains(plain, "Applied patch:") {
		t.Fatalf("expected pure move to omit patch and generic result bodies, got:\n%s", plain)
	}
}

func TestApplyPatchToolCardKeepsMoveWithUpdateDiff(t *testing.T) {
	args := `{"patch":"*** Begin Patch\n*** Update File: src/old.go\n*** Move to: src/new.go\n@@\n-old\n+new\n*** End Patch"}`
	block := &Block{
		ID: 1, Type: BlockToolCall, ToolName: tools.NameApplyPatch,
		Content: applyPatchToolDisplayArgs(args), RawArgs: args,
		ResultDone: true, ResultStatus: agent.ToolResultStatusSuccess,
		Diff: "--- src/old.go\n+++ src/new.go\n@@ -1,1 +1,1 @@\n-old\n+new\n",
	}

	plain := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(plain, "apply_patch src/old.go → src/new.go") ||
		!strings.Contains(plain, "-old") || !strings.Contains(plain, "+new") {
		t.Fatalf("expected move path and content update diff, got:\n%s", plain)
	}
}

func TestLegacyPatchToolCardUsesApplyPatchDisplay(t *testing.T) {
	args := `{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old\n+new\n*** End Patch"}`
	block := &Block{ID: 1, Type: BlockToolCall, ToolName: "patch", Content: args, RawArgs: args}
	plain := stripANSI(strings.Join(block.Render(100, "●"), "\n"))
	if !strings.Contains(plain, "apply_patch src/demo.go") || strings.Contains(plain, " patch ") {
		t.Fatalf("expected legacy patch card to use apply_patch display, got:\n%s", plain)
	}
}

func TestApplyPatchErrorCardKeepsHighlightedPatchPreview(t *testing.T) {
	ApplyTheme(DefaultTheme())
	args := `{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n@@\n-func old() {}\n+func main() {}\n*** End Patch"}`
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameApplyPatch,
		Content:       applyPatchToolDisplayArgs(args),
		RawArgs:       args,
		ResultContent: "hunk not found",
		ResultDone:    true,
		ResultStatus:  agent.ToolResultStatusError,
	}
	lines := block.Render(100, "")
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "apply_patch src/demo.go") || !strings.Contains(plain, "↳ Requested patch:") || !strings.Contains(plain, "+func main() {}") || !strings.Contains(plain, "hunk not found") {
		t.Fatalf("expected error card to preserve path, patch, and error, got:\n%s", plain)
	}
	added := renderedLineContaining(t, lines, "func main")
	assertRenderedTextForeground(t, added, "func", colorOfTheme(toolCodeChromaStyle().Get(chroma.Keyword).Colour.String()))
	assertRenderedTextBackground(t, added, "func", colorOfTheme(currentTheme.DiffAddLineBg))
}

func TestPartiallyAppliedPatchShowsOnlyAppliedDiff(t *testing.T) {
	args := `{"patch":"*** Begin Patch\n*** Update File: committed.go\n@@\n-old\n+new\n*** Update File: failed.go\n@@\n-missing\n+replacement\n*** End Patch"}`
	longReasonTail := "failure-reason-tail-must-not-wrap"
	block := &Block{
		ID: 1, Type: BlockToolCall, ToolName: tools.NameApplyPatch,
		Content: applyPatchToolDisplayArgs(args), RawArgs: args,
		ResultDone: true, ResultStatus: agent.ToolResultStatusError,
		ResultContent: strings.Join([]string{
			"apply_patch partially applied: 1 change committed; 1 file group not applied.",
			"Applied patch:",
			"M committed.go",
			"",
			"Diagnostics:",
			"committed.go (1 new, 0 resolved):",
			"[E] 1:1 [MissingFieldOrMethod] missing field",
			"",
			"LSP diagnostics in other files:",
			"other.go:",
			"[I] 2:1 [default] informational diagnostic",
			"",
			"Not applied:",
			"- failed.go: hunk not found; " + strings.Repeat("long explanation ", 8) + longReasonTail,
			"Changes under \"Applied patch\" are already on disk; resolve each cause above and resubmit only the failed file groups rebuilt from current file contents.",
		}, "\n"),
		Diff: "--- committed.go\n+++ committed.go\n@@ -1,1 +1,1 @@\n-old\n+new\n",
	}

	plain := stripANSI(strings.Join(block.Render(80, ""), "\n"))
	for _, want := range []string{"↳ Targets:", "↳ Applied changes:", "+new", "↳ Error:", "Not applied:", "↳ Diagnostics:", "missing field", "informational diagnostic"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected partially applied patch to contain %q, got:\n%s", want, plain)
		}
	}
	for _, duplicate := range []string{"↳ Requested patch:", "Applied patch:", longReasonTail, "*** Begin Patch"} {
		if strings.Contains(plain, duplicate) {
			t.Fatalf("expected partially applied patch to omit requested patch content %q, got:\n%s", duplicate, plain)
		}
	}
	if strings.Count(plain, "M committed.go") != 1 {
		t.Fatalf("expected committed target to appear once, got:\n%s", plain)
	}
	errorAt := strings.Index(plain, "↳ Error:")
	notAppliedAt := strings.Index(plain, "Not applied:")
	diagnosticsAt := strings.Index(plain, "↳ Diagnostics:")
	diagnosticAt := strings.Index(plain, "missing field")
	if errorAt < 0 || notAppliedAt < errorAt || diagnosticsAt < notAppliedAt || diagnosticAt < diagnosticsAt {
		t.Fatalf("expected failure and diagnostics to render as separate ordered sections, got:\n%s", plain)
	}
	failureLine := renderedLineContaining(t, block.Render(80, ""), "- failed.go: hunk not found")
	if !strings.Contains(stripANSI(failureLine), "…") {
		t.Fatalf("expected long failure line to be truncated, got %q", stripANSI(failureLine))
	}
	copyContent := toolCallMarkdownContent(block)
	for _, preserved := range []string{longReasonTail} {
		if !strings.Contains(copyContent, preserved) {
			t.Fatalf("expected copied tool content to preserve %q, got:\n%s", preserved, copyContent)
		}
	}
}

func TestApplyPatchPreviewTruncatesLongLinesWithoutWrapping(t *testing.T) {
	ApplyTheme(DefaultTheme())
	longLine := `const message = "` + strings.Repeat("x", 100) + `tail"`
	args, _ := json.Marshal(map[string]string{"patch": strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: src/demo.go",
		"@@",
		"+" + longLine,
		"*** End Patch",
	}, "\n")})
	block := &Block{
		ID: 1, Type: BlockToolCall, ToolName: tools.NameApplyPatch,
		Content: applyPatchToolDisplayArgs(string(args)), RawArgs: string(args),
	}

	lines := block.Render(60, "")
	plain := stripANSI(strings.Join(lines, "\n"))
	if strings.Count(plain, "const message") != 1 || strings.Contains(plain, "tail") {
		t.Fatalf("expected long patch line to be truncated once without a continuation, got:\n%s", plain)
	}
	line := renderedLineContaining(t, lines, "const message")
	if !strings.Contains(stripANSI(line), "…") {
		t.Fatalf("expected truncated patch line to show an ellipsis, got %q", stripANSI(line))
	}
	assertRenderedTextBackground(t, line, "…", colorOfTheme(currentTheme.DiffAddLineBg))
}

func TestEditAndApplyPatchToolCardsKeepAddedAndDeletedLineBackgrounds(t *testing.T) {
	ApplyTheme(DefaultTheme())
	args, _ := json.Marshal(map[string]string{"path": "src/demo.go"})
	diff := "@@ -1,1 +1,1 @@\n-package old\n+package main\n"
	for _, toolName := range []string{tools.NameEdit, tools.NameApplyPatch} {
		t.Run(toolName, func(t *testing.T) {
			block := &Block{ID: 1, Type: BlockToolCall, ToolName: toolName, Content: string(args), Diff: diff, ResultDone: true}
			lines := block.Render(80, "")
			deleted := renderedLineContaining(t, lines, "package old")
			added := renderedLineContaining(t, lines, "package main")
			assertRenderedTextBackground(t, deleted, "package", colorOfTheme(currentTheme.DiffDelLineBg))
			assertRenderedTextBackground(t, added, "package", colorOfTheme(currentTheme.DiffAddLineBg))
		})
	}
}

func TestEditAndApplyPatchToolCardsHighlightContextLines(t *testing.T) {
	ApplyTheme(DefaultTheme())
	args, _ := json.Marshal(map[string]string{"path": "src/demo.go"})
	diff := "@@ -1,5 +1,6 @@\n func demo() error {\n+\tvalue := 1\n if value != 0 {\n\treturn fmt.Errorf(\"bad: %d\", value)\n }\n return nil\n"
	wantKeyword := colorOfTheme(toolCodeChromaStyle().Get(chroma.Keyword).Colour.String())
	for _, toolName := range []string{tools.NameEdit, tools.NameApplyPatch} {
		t.Run(toolName, func(t *testing.T) {
			block := &Block{ID: 1, Type: BlockToolCall, ToolName: toolName, Content: string(args), Diff: diff, ResultDone: true}
			contextLine := renderedLineContaining(t, block.Render(100, ""), "if value")
			assertRenderedTextForeground(t, contextLine, "if", wantKeyword)
			assertRenderedTextBackground(t, contextLine, "if", colorOfTheme(currentTheme.ToolCallBg))
		})
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
	ApplyTheme(DefaultTheme())
	backgrounds := []struct {
		name  string
		value string
	}{
		{name: "tool", value: currentTheme.ToolCallBg},
		{name: "diff add", value: currentTheme.DiffAddLineBg},
		{name: "diff delete", value: currentTheme.DiffDelLineBg},
	}
	for _, background := range backgrounds {
		t.Run(background.name, func(t *testing.T) {
			hl := newCodeHighlighter("notes.unknownext", "package tools\n")
			rendered := hl.highlightLine("package tools", background.value)
			if plain := stripANSI(rendered); plain != "package tools" {
				t.Fatalf("expected unknown extension background fallback to preserve text, got %q", plain)
			}
			wantBg := ansiSeqForColor(lipgloss.Color(background.value), false)
			if wantBg == "" {
				t.Fatal("expected background color to produce an ANSI sequence")
			}
			if !strings.Contains(rendered, wantBg) {
				t.Fatalf("expected unknown extension fallback to keep background %q, got %q", wantBg, rendered)
			}
			spaceRendered := hl.highlightLine("    ", background.value)
			if plain := stripANSI(spaceRendered); plain != "    " {
				t.Fatalf("expected whitespace-only fallback to preserve spaces, got %q", plain)
			}
			if !strings.Contains(spaceRendered, wantBg) {
				t.Fatalf("expected whitespace-only fallback to keep background %q, got %q", wantBg, spaceRendered)
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
	if !strings.Contains(stripANSI(lines[0]), "+return 10") {
		t.Fatalf("expected changed line to stay visible, got %q", stripANSI(lines[0]))
	}
	keywordColour := colorOfTheme(toolCodeChromaStyle().Get(chroma.Keyword).Colour.String())
	assertRenderedTextForeground(t, lines[0], "return", keywordColour)
	assertRenderedTextBackground(t, lines[0], "0", colorOfTheme(currentTheme.DiffAddInlineBg))
}

func TestEditToolCardKeepsFinalInlineDiffBackgrounds(t *testing.T) {
	ApplyTheme(DefaultTheme())
	longPrefix := strings.Repeat("prefix", 12)
	longSuffix := strings.Repeat("suffix", 12)
	tests := []struct {
		name              string
		oldLine           string
		newLine           string
		lineText          string
		unchangedText     string
		changedText       string
		changedBackground string
		wantEllipsis      bool
	}{
		{
			name:              "short insertion",
			oldLine:           "return 1",
			newLine:           "return 10",
			lineText:          "+return 10",
			unchangedText:     "return",
			changedText:       "0",
			changedBackground: currentTheme.DiffAddInlineBg,
		},
		{
			name:              "short deletion",
			oldLine:           "return 10",
			newLine:           "return 1",
			lineText:          "-return 10",
			unchangedText:     "return",
			changedText:       "0",
			changedBackground: currentTheme.DiffDelInlineBg,
		},
		{
			name:              "long insertion window",
			oldLine:           longPrefix + " value " + longSuffix,
			newLine:           longPrefix + " valueHTTP " + longSuffix,
			lineText:          "valueHTTP",
			unchangedText:     "suffix",
			changedText:       "HTTP",
			changedBackground: currentTheme.DiffAddInlineBg,
			wantEllipsis:      true,
		},
	}

	args, _ := json.Marshal(map[string]string{"path": "example.go"})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := &Block{
				ID:         1,
				Type:       BlockToolCall,
				ToolName:   tools.NameEdit,
				Content:    string(args),
				Diff:       "@@ -1,1 +1,1 @@\n-" + tt.oldLine + "\n+" + tt.newLine + "\n",
				ResultDone: true,
			}
			line := renderedLineContaining(t, block.Render(60, ""), tt.lineText)
			assertRenderedTextBackground(t, line, tt.unchangedText, colorOfTheme(currentTheme.ToolCallBg))
			assertRenderedTextBackground(t, line, tt.changedText, colorOfTheme(tt.changedBackground))
			if tt.wantEllipsis && !strings.Contains(stripANSI(line), "…") {
				t.Fatalf("expected long inline diff to use a snippet window, got %q", stripANSI(line))
			}
		})
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

func TestRenderFileDiffCallUnequalMinusPlusBlocksUseWholeLineBackground(t *testing.T) {
	tests := []struct {
		name            string
		oldLines        []string
		newLines        []string
		wantLineNumbers bool
	}{
		{
			name:     "two deletions one addition",
			oldLines: []string{"func demo() {", "    value := 1"},
			newLines: []string{"func demo() {    value := 1"},
		},
		{
			name:            "one deletion two additions",
			oldLines:        []string{"old line"},
			newLines:        []string{"new line one", "new line two"},
			wantLineNumbers: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ApplyTheme(DefaultTheme())
			old := strings.Join(tt.oldLines, "\n") + "\n"
			new := strings.Join(tt.newLines, "\n") + "\n"
			diff := tools.GenerateUnifiedDiff(old, new, "example.go")
			if diff == "" {
				t.Fatal("expected non-empty unified diff")
			}
			block := &Block{
				ID:         1,
				Type:       BlockToolCall,
				ToolName:   tools.NameEdit,
				Content:    `{"path":"example.go","patch":"@@\n-old\n+new\n"}`,
				Diff:       diff,
				ResultDone: true,
			}

			lines := block.Render(120, "")
			plain := stripANSI(strings.Join(lines, "\n"))
			for _, oldLine := range tt.oldLines {
				deleted := renderedLineContaining(t, lines, oldLine)
				assertRenderedTextBackground(t, deleted, oldLine, colorOfTheme(currentTheme.DiffDelLineBg))
			}
			for _, newLine := range tt.newLines {
				added := renderedLineContaining(t, lines, newLine)
				assertRenderedTextBackground(t, added, newLine, colorOfTheme(currentTheme.DiffAddLineBg))
			}
			if tt.wantLineNumbers {
				for _, want := range []string{"  1 -old line", "  1 +new line one", "  2 +new line two"} {
					if !strings.Contains(plain, want) {
						t.Fatalf("expected line-numbered uneven diff to contain %q, got:\n%s", want, plain)
					}
				}
			}
		})
	}
}

func TestRenderFileDiffCallDoesNotExceedLineLimitForTwoLinePair(t *testing.T) {
	ApplyTheme(DefaultTheme())
	diffLines := []string{
		"--- example.go",
		"+++ example.go",
		fmt.Sprintf("@@ -1,%d +1,%d @@", maxTUIDiffLines, maxTUIDiffLines),
	}
	for i := 1; i < maxTUIDiffLines; i++ {
		diffLines = append(diffLines, fmt.Sprintf(" context line %d", i))
	}
	diffLines = append(diffLines, "-old value", "+new value")
	block := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   tools.NameEdit,
		Content:    `{"path":"example.go","patch":"@@\n-old\n+new\n"}`,
		Diff:       strings.Join(diffLines, "\n") + "\n",
		ResultDone: true,
	}

	plain := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(plain, "... (diff truncated)") {
		t.Fatalf("expected diff truncation marker, got:\n%s", plain)
	}
	for _, hidden := range []string{"old value", "new value"} {
		if strings.Contains(plain, hidden) {
			t.Fatalf("expected two-line pair %q to be omitted at the line limit, got:\n%s", hidden, plain)
		}
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
	rendered := renderHighlightedSnippetLine(line, spans, 26, hl, currentTheme.ToolCallBg)
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
	rendered := renderHighlightedSnippetLine(line, spans, 12, hl, currentTheme.ToolCallBg)
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
