package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
	uv "github.com/keakon/ultraviolet"
	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

var osc8RegexToolTest = regexp.MustCompile(`\x1b\]8;[^\x07\x1b]*(?:\x07|\x1b\\)`)

func stripOSC8ToolTest(s string) string {
	return osc8RegexToolTest.ReplaceAllString(s, "")
}

func containsRawCarriageReturnForTest(s string) bool {
	for _, r := range s {
		if r == '\r' {
			return true
		}
	}
	return false
}

func TestNormalizeCodeFenceLanguage(t *testing.T) {
	cases := map[string]string{
		"":          "",
		"text":      "text",
		"plaintext": "text",
		"txt":       "text",
		"js":        "javascript",
		"ts":        "typescript",
		"sh":        "bash",
		"yml":       "yaml",
	}
	for in, want := range cases {
		if got := normalizeCodeFenceLanguage(in); got != want {
			t.Fatalf("normalizeCodeFenceLanguage(%q)=%q want %q", in, got, want)
		}
	}
}

func TestToolCodeChromaStyleAdjustsCommentContrast(t *testing.T) {
	style := toolCodeChromaStyle()

	if style == nil {
		t.Fatal("tool code style should not be nil")
	}
	if got := style.Get(chroma.Comment).Colour.String(); got != darkThemeSyntaxCommentColour {
		t.Fatalf("comment colour = %q, want %q", got, darkThemeSyntaxCommentColour)
	}
	if got := style.Get(chroma.Keyword).Colour.String(); got == darkThemeSyntaxCommentColour {
		t.Fatalf("keyword colour should stay distinct from comment override; got %q", got)
	}
}

func TestNewCodeHighlighterUsesToolStyle(t *testing.T) {
	ApplyTheme(DefaultTheme())
	h := newCodeHighlighter("sample.go", "package main\n// comment\n")
	if got := h.chromaStyle.Get(chroma.CommentSingle).Colour.String(); got != darkThemeSyntaxCommentColour {
		t.Fatalf("highlighter comment colour = %q, want %q", got, darkThemeSyntaxCommentColour)
	}
}

func TestToolHeaderProgressSuffixSurvivesANSITruncateFallback(t *testing.T) {
	ApplyTheme(DefaultTheme())
	header := "🔧 \x1b[1mVeryLongToolNameWithArguments\x1b[0m /tmp/very/long/path"
	got := appendToolProgressSuffix(header, &agent.ToolProgressSnapshot{Text: "42 chars received"}, 24)
	plain := stripANSI(got)
	if !strings.Contains(plain, "42 chars received") {
		t.Fatalf("progress suffix missing after truncate fallback: %q", plain)
	}
	if runewidth.StringWidth(plain) > 24 {
		t.Fatalf("header width = %d, want <= 24: %q", runewidth.StringWidth(plain), plain)
	}
}

func TestDisplayToolPathPrefersRelativeWithinWorkingDir(t *testing.T) {
	wd := filepath.Join(string(os.PathSeparator), "tmp", "workspace")
	abs := filepath.Join(wd, "internal", "tui", "block_tool.go")
	got := displayToolPath(abs, wd)
	want := filepath.Join("internal", "tui", "block_tool.go")
	if got != want {
		t.Fatalf("displayToolPath(%q, %q) = %q, want %q", abs, wd, got, want)
	}
}

func TestDisplayToolPathKeepsAbsoluteOutsideWorkingDir(t *testing.T) {
	wd := filepath.Join(string(os.PathSeparator), "tmp", "workspace")
	abs := filepath.Join(string(os.PathSeparator), "tmp", "other", "block_tool.go")
	if got := displayToolPath(abs, wd); got != abs {
		t.Fatalf("displayToolPath(%q, %q) = %q, want original absolute path", abs, wd, got)
	}
}

func TestDisplayToolPathKeepsExistingRelativePath(t *testing.T) {
	rel := filepath.Join("internal", "tui", "block_tool.go")
	if got := displayToolPath(rel, filepath.Join(string(os.PathSeparator), "tmp", "workspace")); got != rel {
		t.Fatalf("displayToolPath(%q, wd) = %q, want unchanged relative path", rel, got)
	}
}

func TestReadHeaderShowsRelativePathInsideWorkingDir(t *testing.T) {
	wd := filepath.Join(string(os.PathSeparator), "tmp", "workspace")
	abs := filepath.Join(wd, "internal", "tui", "block_tool.go")
	block := &Block{
		ID:                1,
		Type:              BlockToolCall,
		ToolName:          "read",
		Content:           fmt.Sprintf(`{"path":%q,"limit":20,"offset":5}`, abs),
		ResultDone:        true,
		displayWorkingDir: wd,
	}
	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	want := filepath.Join("internal", "tui", "block_tool.go") + " (limit=20, offset=5)"
	if !strings.Contains(joined, want) {
		t.Fatalf("expected Read header to show relative path; got:\n%s", joined)
	}
	if strings.Contains(joined, abs) {
		t.Fatalf("did not expect Read header to show absolute path; got:\n%s", joined)
	}
}

func TestWriteHeaderShowsRelativePathInsideWorkingDir(t *testing.T) {
	wd := filepath.Join(string(os.PathSeparator), "tmp", "workspace")
	abs := filepath.Join(wd, "demo.txt")
	block := &Block{
		ID:                1,
		Type:              BlockToolCall,
		ToolName:          "write",
		Content:           fmt.Sprintf(`{"path":%q,"content":"hello"}`, abs),
		Collapsed:         true,
		ResultDone:        true,
		displayWorkingDir: wd,
	}
	joined := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(joined, "write demo.txt") {
		t.Fatalf("expected Write header to show relative path; got:\n%s", joined)
	}
	if strings.Contains(joined, abs) {
		t.Fatalf("did not expect Write header to show absolute path; got:\n%s", joined)
	}
}

func TestWriteCardMultilineResultDoesNotBypassCardWrapper(t *testing.T) {
	ApplyTheme(DefaultTheme())
	wd := filepath.Join(string(os.PathSeparator), "tmp", "workspace")
	abs := filepath.Join(wd, "demo.go")
	block := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   "write",
		Content:    fmt.Sprintf(`{"path":%q,"content":"package demo\n"}`, abs),
		Collapsed:  false,
		ResultDone: true,
		ResultContent: strings.Join([]string{
			"Successfully wrote 66 lines, 1976 bytes",
			"",
			"Diagnostics:",
			"[W] 1:1 warning",
			"... 13 diagnostics not shown due to output limits; they may still need fixing.",
		}, "\n"),
		displayWorkingDir: wd,
	}

	raw := strings.Join(block.Render(120, ""), "\n")
	plain := stripANSI(raw)
	if !strings.Contains(plain, "Successfully wrote 66 lines, 1976 bytes") {
		t.Fatalf("expected compact write summary to render before diagnostics; got:\n%s", plain)
	}
	if !strings.Contains(plain, "Diagnostics:") {
		t.Fatalf("expected diagnostics text to render; got:\n%s", plain)
	}
	if !strings.Contains(plain, "... 13 diagnostics not shown due to output limits; they may still need fixing.") {
		t.Fatalf("expected omitted diagnostics line to render; got:\n%s", plain)
	}
	if strings.Contains(plain, "Diagnostics:\n\n") {
		t.Fatalf("expected no blank line after Diagnostics header; got:\n%s", plain)
	}
	if !strings.Contains(raw, DimStyle.Render("    ... 13 diagnostics not shown due to output limits; they may still need fixing.")) {
		t.Fatalf("expected omitted diagnostics line to use dim style; got:\n%s", raw)
	}
	for i, line := range strings.Split(plain, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(line, "│") {
			t.Fatalf("line %d bypassed card wrapper (missing rail): %q\nfull:\n%s", i, trimmed, plain)
		}
	}
}

func TestEditSuccessWithLSPDiagnosticsRendersDiagnostics(t *testing.T) {
	ApplyTheme(DefaultTheme())
	for _, header := range []string{"Diagnostics:", "Diagnostics summary:"} {
		block := &Block{
			ID:         1,
			Type:       BlockToolCall,
			ToolName:   tools.NameEdit,
			Content:    `{"path":"cmd/chord/setup_templates.go","patch":"@@\n-old\n+new\n"}`,
			Collapsed:  false,
			ResultDone: true,
			ResultContent: strings.Join([]string{
				"Applied patch to cmd/chord/setup_templates.go (+12 -12)",
				"",
				header,
				"[E] 139:10 [UndeclaredName] undefined: config",
				"[E] 141:10 [UndeclaredName] undefined: config",
			}, "\n"),
		}

		plain := stripANSI(strings.Join(block.Render(120, ""), "\n"))
		if !strings.Contains(plain, "↳ Diagnostics:") {
			t.Fatalf("%s: expected edit diagnostics section to render; got:\n%s", header, plain)
		}
		if !strings.Contains(plain, "undefined: config") {
			t.Fatalf("%s: expected edit LSP error to render; got:\n%s", header, plain)
		}
		if strings.Contains(plain, "↳ Result:") {
			t.Fatalf("%s: expected edit diagnostics not to render as a generic result; got:\n%s", header, plain)
		}
		if strings.Contains(plain, "Applied patch to cmd/chord/setup_templates.go") {
			t.Fatalf("%s: expected routine edit success text to stay hidden; got:\n%s", header, plain)
		}
	}
}

func TestWriteCallRendersContentPreviewWithReadStyleExpansion(t *testing.T) {
	ApplyTheme(DefaultTheme())
	content := strings.Join([]string{
		"package main",
		"",
		"func main() {",
		`\tfmt.Println("1")`,
		`\tfmt.Println("2")`,
		`\tfmt.Println("3")`,
		`\tfmt.Println("4")`,
		`\tfmt.Println("5")`,
		`\tfmt.Println("6")`,
		`\tfmt.Println("7")`,
		`\tfmt.Println("8")`,
		"}",
	}, "\n")
	args, err := json.Marshal(map[string]string{
		"path":    "cmd/demo/main.go",
		"content": content,
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "write",
		Content:       string(args),
		Collapsed:     false,
		ResultDone:    true,
		ResultContent: "Successfully wrote 12 lines, 157 bytes",
	}

	plain := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	for _, want := range []string{"write cmd/demo/main.go", "Successfully wrote 12 lines", "1  package main", "10  \\tfmt.Println", "2 more lines", "[space] toggle expand/collapse"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected collapsed Write preview to contain %q; got:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "11  \\tfmt.Println") || strings.Contains(plain, "12  }") {
		t.Fatalf("expected collapsed Write preview to hide lines after 10; got:\n%s", plain)
	}

	block.ToggleAtWidth(120)
	if !block.ReadContentExpanded {
		t.Fatal("expected space toggle to expand Write preview")
	}
	expanded := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	for _, want := range []string{"11  \\tfmt.Println", "12  }"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("expected expanded Write preview to contain %q; got:\n%s", want, expanded)
		}
	}
	if strings.Contains(expanded, "[space] toggle expand/collapse") {
		t.Fatalf("expanded Write preview should not show expand hint; got:\n%s", expanded)
	}
}

func TestWriteCallSanitizesPreviewControlCharacters(t *testing.T) {
	ApplyTheme(DefaultTheme())
	args, err := json.Marshal(map[string]string{
		"path":    "demo.txt",
		"content": "safe\x1b[31m literal\rnext",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "write",
		Content:       string(args),
		Collapsed:     false,
		ResultDone:    true,
		ResultContent: "Successfully wrote 1 line, 22 bytes",
	}

	plain := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if strings.ContainsRune(plain, '\x1b') {
		t.Fatalf("expected rendered Write preview to not contain raw ESC: %q", plain)
	}
	if !strings.Contains(plain, `safe\x1b[31m literal`) || !strings.Contains(plain, "next") {
		t.Fatalf("expected sanitized Write preview content, got:\n%s", plain)
	}
}

func TestReadHeaderKeepsAbsolutePathOutsideWorkingDir(t *testing.T) {
	wd := filepath.Join(string(os.PathSeparator), "tmp", "workspace")
	abs := filepath.Join(string(os.PathSeparator), "tmp", "other", "demo.txt")
	block := &Block{
		ID:                1,
		Type:              BlockToolCall,
		ToolName:          "read",
		Content:           fmt.Sprintf(`{"path":%q}`, abs),
		ResultDone:        true,
		displayWorkingDir: wd,
	}
	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, abs) {
		t.Fatalf("expected Read header to keep absolute path outside working dir; got:\n%s", joined)
	}
}

func TestSkillToolDisplaySummaryUsesFullNameAndRelativeDirectoryPath(t *testing.T) {
	tmp := t.TempDir()
	wd := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(filepath.Join(wd, ".agents", "skills", "sample-food-skill"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(wd); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(prevWD) }()

	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "skill",
		Content:       `{"name":"sample-food-skill","result":"<path>` + filepath.ToSlash(filepath.Join(wd, ".agents", "skills", "sample-food-skill", "SKILL.md")) + `</path>"}`,
		ResultContent: filepath.ToSlash(filepath.Join(wd, ".agents", "skills", "sample-food-skill", "SKILL.md")),
		ResultDone:    true,
		Collapsed:     true,
	}

	joined := stripANSI(strings.Join(block.Render(140, ""), "\n"))
	if !strings.Contains(joined, "skill sample-food-skill (from .agents)") {
		t.Fatalf("expected skill header to show name with source metadata, got:\n%s", joined)
	}
	if strings.Contains(joined, "sample-food-…") {
		t.Fatalf("skill name should not be truncated before source metadata, got:\n%s", joined)
	}
	if strings.Contains(joined, "name:") {
		t.Fatalf("collapsed skill header should not repeat a name param line, got:\n%s", joined)
	}
}

func TestOrdinaryToolResultUsesPlainTextForMarkdownLookingContent(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		Type:                   BlockToolCall,
		ToolName:               "web_fetch",
		Content:                `{"url":"https://example.com"}`,
		ResultContent:          "## Ready\n\n```go\nfmt.Println(1)\n```\n\n- item one\n- item two",
		ResultDone:             true,
		ResultStatus:           agent.ToolResultStatusSuccess,
		ToolCallDetailExpanded: true,
	}

	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	for _, want := range []string{"## Ready", "```go", "fmt.Println(1)", "- item one", "- item two"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected markdown-looking tool result content to remain literal (%q), got:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "• item one") {
		t.Fatalf("ordinary tool result must not render markdown bullets, got:\n%s", joined)
	}
	if strings.Contains(joined, "GO") {
		t.Fatalf("ordinary tool result must not use assistant-style fenced code rendering, got:\n%s", joined)
	}
}

func TestExpandedSkillResultUsesPlainTextForMarkdownLookingContent(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "skill",
		Content:                `{"name":"skill-creator","result":"<path>/tmp/skills/skill-creator/SKILL.md</path>"}`,
		ResultContent:          "<skill>\n<name>skill-creator</name>\n<path>/tmp/skills/skill-creator/SKILL.md</path>\n<root>/tmp/skills/skill-creator</root>\n\n# Skill Creator\n\n- Step one\n- Step two\n</skill>",
		ResultDone:             true,
		ToolCallDetailExpanded: true,
	}

	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	for _, want := range []string{"# Skill Creator", "- Step one", "- Step two"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected expanded Skill result to keep literal markdown-looking text %q, got:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "• Step one") {
		t.Fatalf("expanded Skill result should not render markdown bullets, got:\n%s", joined)
	}
}

func TestOrdinaryToolResultDoesNotUseRichMarkdownFencedCode(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		Type:                   BlockToolCall,
		ToolName:               "web_fetch",
		Content:                `{"url":"https://example.com"}`,
		ResultContent:          "## Ready\n\n```go\nfmt.Println(1)\n```",
		ResultDone:             true,
		ResultStatus:           agent.ToolResultStatusSuccess,
		ToolCallDetailExpanded: true,
	}

	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "Ready") || !strings.Contains(joined, "fmt.Println(1)") {
		t.Fatalf("expected markdown-looking tool result content to remain visible, got:\n%s", joined)
	}
	if strings.Contains(joined, "GO") {
		t.Fatalf("ordinary tool result must not use assistant-style rich fenced code rendering, got:\n%s", joined)
	}
}

func TestCollapsedShellToolShowsExpandHintForHiddenOutput(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                `{"command":"printf 'one\ntwo\nthree\n'","description":"show lines"}`,
		ResultContent:          "one\ntwo\nthree",
		ResultDone:             true,
		Collapsed:              true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	// Short output: stdout is already shown inline, but expanded mode still adds
	// exit status + stream headers, so we should still show an expand hint.
	if !strings.Contains(joined, "[space] toggle expand/collapse") {
		t.Fatalf("expected collapsed Shell with short output to show expand hint; got:\n%s", joined)
	}
	if !strings.Contains(joined, "one") || !strings.Contains(joined, "two") || !strings.Contains(joined, "three") {
		t.Fatalf("expected all output lines to be shown inline for short output; got:\n%s", joined)
	}
}

func TestCollapsedBashDoesNotDuplicateDescriptionInSummary(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                `{"command":"ls /tmp","description":"List temp files"}`,
		ResultContent:          "file1\nfile2\nfile3",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	// Description "List temp files" should appear in the header line, not
	// repeated in the summary line.
	_, after, ok := strings.Cut(joined, "List temp files")
	if !ok {
		t.Fatalf("expected description in header; got:\n%s", joined)
	}
	// Check it does not appear a second time after the header.
	remainder := after
	if strings.Contains(remainder, "List temp files") {
		t.Fatalf("description should not be duplicated in summary; got:\n%s", joined)
	}
}

func TestCollapsedBashLongOutputStillFolds(t *testing.T) {
	// 8 lines of output exceeds bashCollapsedResultMinVisibleLines (5),
	// so collapsed mode should show a single-line summary with an expand hint.
	var lines []string
	for i := 1; i <= 8; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                `{"command":"cat big.txt","description":"show file"}`,
		ResultContent:          strings.Join(lines, "\n"),
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "[space] toggle expand/collapse") {
		t.Fatalf("expected collapsed Shell with long output to show expand hint; got:\n%s", joined)
	}
	if strings.Contains(joined, "line 8") {
		t.Fatalf("did not expect collapsed Shell with long output to reveal all lines; got:\n%s", joined)
	}
}

func TestToolProgressRendersConsistentlyAcrossCollapsedAndExpandedStates(t *testing.T) {
	block := &Block{
		ID:                 1,
		Type:               BlockToolCall,
		ToolName:           "delete",
		Content:            `{"paths":["a-very-long-file-name.txt","b-very-long-file-name.txt","c-very-long-file-name.txt"],"reason":"cleanup stale generated benchmark artifacts"}`,
		ToolExecutionState: agent.ToolCallExecutionStateRunning,
		ToolProgress: &agent.ToolProgressSnapshot{
			Label:   "paths",
			Current: 2,
			Total:   3,
		},
	}

	collapsed := stripANSI(strings.Join(block.Render(44, "●"), "\n"))
	if !strings.Contains(collapsed, "2 / 3 paths") {
		t.Fatalf("collapsed render should preserve tool progress; got:\n%s", collapsed)
	}

	block.Collapsed = false
	expanded := stripANSI(strings.Join(block.Render(44, "●"), "\n"))
	if !strings.Contains(expanded, "2 / 3 paths") {
		t.Fatalf("expanded render should preserve tool progress; got:\n%s", expanded)
	}
}

func TestToolResultWrappedContinuationKeepsCardBackgroundOnTrailingPadding(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:         1,
		Type:       BlockToolResult,
		ToolName:   "shell",
		Content:    "Error: " + strings.Repeat("x", 220),
		IsError:    true,
		Collapsed:  false,
		ResultDone: true,
	}

	lines := block.Render(88, "")
	if len(lines) == 0 {
		t.Fatal("expected rendered tool result block")
	}

	var wrapped []string
	for _, line := range lines {
		plain := stripANSI(line)
		if strings.Contains(plain, strings.Repeat("x", 20)) {
			wrapped = append(wrapped, line)
		}
	}
	if len(wrapped) < 2 {
		t.Fatalf("expected at least 2 wrapped content lines, got %d: %q", len(wrapped), strings.Join(lines, "\n"))
	}
	target := wrapped[1]

	plain := stripANSI(target)
	trimmed := strings.TrimRight(plain, " ")
	if trimmed == plain {
		t.Fatalf("wrapped continuation has no trailing spaces: %q", plain)
	}
	contentEndCol := ansi.StringWidth(trimmed) - 1
	if contentEndCol < 0 {
		t.Fatalf("invalid content end col: %d", contentEndCol)
	}

	buf := newScreenBuffer(ansi.StringWidth(target), 1)
	uv.NewStyledString(target).Draw(buf, buf.Bounds())
	cells := buf.Line(0)
	if len(cells) == 0 {
		t.Fatal("expected rendered cells")
	}

	toolBg := colorOfTheme(currentTheme.ToolCallBg)
	trailingSpaces := 0
	for i := contentEndCol + 1; i < len(cells); i++ {
		cell := cells[i]
		if cell.IsZero() || cell.Content != " " {
			continue
		}
		trailingSpaces++
		if !colorsEqual(cell.Style.Bg, toolBg) {
			t.Fatalf("trailing space at col %d background = %v, want tool card bg %v", i, cell.Style.Bg, toolBg)
		}
	}
	if trailingSpaces == 0 {
		t.Fatal("expected trailing padding spaces on wrapped continuation line")
	}
}

func TestBashHeaderUsesOnlyFirstCommandLine(t *testing.T) {
	cmd := "echo first\necho second"
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                fmt.Sprintf(`{"command":%q,"timeout":120}`, cmd),
		ResultContent:          "ok",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	lines := block.Render(80, "")
	plain := make([]string, len(lines))
	for i, line := range lines {
		plain[i] = stripANSI(line)
	}
	joined := strings.Join(plain, "\n")

	if !strings.Contains(joined, "shell echo first (timeout=120)") {
		t.Fatalf("expected header to contain only first command line with timeout; got:\n%s", joined)
	}
	if strings.Contains(joined, "echo second (timeout=120)") {
		t.Fatalf("did not expect command continuation to appear in collapsed header; got:\n%s", joined)
	}
}

func TestCollapsedBashMultilineUsesDescriptionWhenPresent(t *testing.T) {
	cmd := "python3 - <<'PY'\nfrom pathlib import Path\nprint('ok')\nPY"
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                fmt.Sprintf(`{"command":%q,"description":%q,"timeout":120}`, cmd, "Search existing permission-related tests"),
		ResultContent:          "ok",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(joined, "shell Search existing permission-related tests (timeout=120)") {
		t.Fatalf("expected collapsed multiline Shell header to use description; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Command:") || !strings.Contains(joined, "python3 - <<'PY'") {
		t.Fatalf("expected collapsed multiline Shell to show command preview block; got:\n%s", joined)
	}
}

func TestCollapsedBashShowsCappedForegroundTimeoutInHeader(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                `{"command":"sleep 1","timeout":2400}`,
		ResultContent:          "ok",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(joined, "shell sleep 1 (timeout=2400→600)") {
		t.Fatalf("expected collapsed Shell header to show requested and effective capped foreground timeout; got:\n%s", joined)
	}
}

func TestCollapsedBashMultilineWithoutDescriptionFallsBackToCommand(t *testing.T) {
	cmd := "python3 - <<'PY'\nprint('ok')\nPY"
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                fmt.Sprintf(`{"command":%q,"description":"   ","timeout":120}`, cmd),
		ResultContent:          "ok",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(joined, "shell python3 - <<'PY' (timeout=120)") {
		t.Fatalf("expected collapsed multiline Shell header to fall back to command first line; got:\n%s", joined)
	}
}

func TestExpandedBashMultilineKeepsCommandHeaderEvenWithDescription(t *testing.T) {
	cmd := "python3 - <<'PY'\nfrom pathlib import Path\nprint('ok')\nPY"
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                fmt.Sprintf(`{"command":%q,"description":%q,"timeout":120}`, cmd, "Search existing permission-related tests"),
		ResultContent:          "ok",
		ResultDone:             true,
		ToolCallDetailExpanded: true,
	}

	joined := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(joined, "shell Search existing permission-related tests (timeout=120)") {
		t.Fatalf("expected expanded Shell header to use summary description; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Command:") || !strings.Contains(joined, "python3 - <<'PY'") || !strings.Contains(joined, "from pathlib import Path") || !strings.Contains(joined, "print('ok')") {
		t.Fatalf("expected expanded Shell to show full command block; got:\n%s", joined)
	}
	if !strings.Contains(joined, "description: Search existing permission-related tests") {
		t.Fatalf("expected expanded Shell detail lines to still include description metadata; got:\n%s", joined)
	}
}

func TestExpandedBashShowsIndentedContinuationLines(t *testing.T) {
	cmd := "echo first\necho second\n  pwd"
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                fmt.Sprintf(`{"command":%q,"timeout":120}`, cmd),
		ResultContent:          "ok",
		ResultDone:             true,
		ToolCallDetailExpanded: true,
	}

	lines := block.Render(80, "")
	var plain []string
	for _, line := range lines {
		plain = append(plain, stripANSI(line))
	}

	foundContinuation := false
	foundIndentedContinuation := false
	foundIndentedNested := false
	for _, line := range plain {
		// Strip rail prefix if present
		content := line
		if strings.HasPrefix(content, "│\x1b[0m") {
			content = content[len("│\x1b[0m"):]
		} else if strings.HasPrefix(content, "│") {
			content = content[len("│"):]
		}
		if strings.Contains(content, "echo second") {
			foundContinuation = true
			if strings.HasPrefix(content, "      echo second") {
				foundIndentedContinuation = true
			}
		}
		if strings.Contains(content, "pwd") && strings.HasPrefix(content, "        pwd") {
			foundIndentedNested = true
		}
	}
	if !foundContinuation {
		t.Fatalf("expected expanded Shell view to include continuation lines; got:\n%s", strings.Join(plain, "\n"))
	}
	if !foundIndentedContinuation {
		t.Fatalf("expected expanded Shell continuation line to be indented; got:\n%s", strings.Join(plain, "\n"))
	}
	if !foundIndentedNested {
		t.Fatalf("expected expanded Shell nested continuation line to preserve extra indentation; got:\n%s", strings.Join(plain, "\n"))
	}
}

func TestCollapsedBashShowsCommandPreviewAndExpandHint(t *testing.T) {
	cmd := "echo first\necho second\necho third"
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                fmt.Sprintf(`{"command":%q,"timeout":120}`, cmd),
		ResultContent:          "ok",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(80, ""), "\n"))
	if !strings.Contains(joined, "Command:") || !strings.Contains(joined, "echo first") || !strings.Contains(joined, "echo second") {
		t.Fatalf("expected collapsed Shell to show command preview lines; got:\n%s", joined)
	}
	// Even when stdout/stderr are fully visible inline, expanded mode still adds
	// exit status + stream headers, so we should show an expand hint.
	if !strings.Contains(joined, "[space] toggle expand/collapse") {
		t.Fatalf("expected collapsed Shell with short output to show expand hint; got:\n%s", joined)
	}
	if strings.Contains(joined, "echo third") {
		t.Fatalf("did not expect collapsed Shell with short output to show hidden command lines; got:\n%s", joined)
	}
	if !strings.Contains(joined, "ok") {
		t.Fatalf("expected collapsed Shell with short output to show result inline; got:\n%s", joined)
	}
}

func TestCollapsedBashLongCommandWithNoOutputKeepsCommandPreviewCollapsed(t *testing.T) {
	cmd := "git add internal/tui/app_cached_render.go && cat <<'PATCH' | git apply --cached\n" +
		"--- a/internal/tui/session_switch_test.go\n" +
		"+++ b/internal/tui/session_switch_test.go\n" +
		"@@ -1,2 +1,8 @@\n" +
		"+func TestExample(t *testing.T) {\n" +
		"+\tt.Fatal(\"one\")\n" +
		"+\tt.Fatal(\"two\")\n" +
		"+}\n" +
		"PATCH"
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                fmt.Sprintf(`{"command":%q,"description":"暂存滚轮修复相关改动","timeout":30}`, cmd),
		ResultContent:          "(Shell completed with no output)",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "Command:") || !strings.Contains(joined, "git add internal/tui/app_cached_render.go") {
		t.Fatalf("expected collapsed Shell to show command preview; got:\n%s", joined)
	}
	if !strings.Contains(joined, "--- a/internal/tui/session_switch_test.go") {
		t.Fatalf("expected collapsed Shell to show second command preview line; got:\n%s", joined)
	}
	if strings.Contains(joined, "+++ b/internal/tui/session_switch_test.go") || strings.Contains(joined, "TestExample") {
		t.Fatalf("did not expect collapsed Shell to show long heredoc body; got:\n%s", joined)
	}
	if !strings.Contains(joined, "(Shell completed with no output)") {
		t.Fatalf("expected collapsed Shell to show no-output result inline; got:\n%s", joined)
	}
	if !strings.Contains(joined, "[space] toggle expand/collapse") {
		t.Fatalf("expected collapsed Shell to show expand hint for hidden command lines; got:\n%s", joined)
	}
}

func TestCollapsedBashShowsSingleExpandHintWhenCommandAndOutputBothHidden(t *testing.T) {
	cmd := "echo first\necho second\necho third"
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                fmt.Sprintf(`{"command":%q,"timeout":120}`, cmd),
		ResultContent:          "one\ntwo\nthree",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(80, ""), "\n"))
	// Short output: all stdout/stderr are shown inline, but expanded mode still
	// adds exit status + stream headers, so we should show an expand hint.
	if !strings.Contains(joined, "[space] toggle expand/collapse") {
		t.Fatalf("expected collapsed Shell with short output to show expand hint; got:\n%s", joined)
	}
	if strings.Contains(joined, "echo third") {
		t.Fatalf("did not expect full command to be shown for short output; got:\n%s", joined)
	}
	if !strings.Contains(joined, "one") || !strings.Contains(joined, "two") || !strings.Contains(joined, "three") {
		t.Fatalf("expected all output lines to be shown inline for short output; got:\n%s", joined)
	}
}

func TestExpandedUserTerminalMatchesBashContinuationFormatting(t *testing.T) {
	cmd := "echo first\necho second\n  pwd"
	block := &Block{
		ID:                   1,
		Type:                 BlockUser,
		UserLocalShellCmd:    cmd,
		UserLocalShellResult: "ok",
		Collapsed:            false,
	}

	lines := block.Render(80, "")
	var plain []string
	for _, line := range lines {
		plain = append(plain, stripANSI(line))
	}
	joined := strings.Join(plain, "\n")

	if !strings.Contains(joined, "shell echo first") {
		t.Fatalf("expected terminal header to show first command line only; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Command:") {
		t.Fatalf("expected terminal to show command block label; got:\n%s", joined)
	}
	if !strings.Contains(joined, "      echo second") {
		t.Fatalf("expected terminal continuation line to be indented; got:\n%s", joined)
	}
	if !strings.Contains(joined, "        pwd") {
		t.Fatalf("expected terminal nested continuation indentation to be preserved; got:\n%s", joined)
	}
}

func TestCollapsedCompleteShowsSummaryPreviewInsteadOfFullBody(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "complete",
		Content:                `{"summary":"Status: success\nChanges: line one\nline two\nline three\nline four\nline five\nline six\nline seven\nline eight\nline nine\nline ten\nline eleven\nline twelve"}`,
		ResultContent:          "Status: success\nChanges: line one\nline two\nline three\nline four\nline five\nline six\nline seven\nline eight\nline nine\nline ten\nline eleven\nline twelve",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(88, ""), "\n"))
	if !strings.Contains(joined, "Status: success · Changes: line one") {
		t.Fatalf("expected collapsed Complete to show summary preview; got:\n%s", joined)
	}
	if !strings.Contains(joined, "more lines · [space] toggle expand/collapse") {
		t.Fatalf("expected collapsed Complete to show expand hint; got:\n%s", joined)
	}
	if strings.Contains(joined, "line twelve") {
		t.Fatalf("did not expect collapsed Complete to render full body; got:\n%s", joined)
	}
}

func TestToolExpandedResultLinesHiddenCountDoesNotDoubleCountFirstHiddenLine(t *testing.T) {
	lines := make([]string, maxToolCallCompactResultLines+2)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%02d", i+1)
	}

	visible, hidden := toolExpandedResultLines(strings.Join(lines, "\n"), 100, false)
	if len(visible) != maxToolCallCompactResultLines {
		t.Fatalf("visible lines = %d, want %d", len(visible), maxToolCallCompactResultLines)
	}
	if hidden != 2 {
		t.Fatalf("hidden lines = %d, want 2", hidden)
	}
}

func TestQueuedToolHeaderShowsQueuedLabelWithoutSpinner(t *testing.T) {
	block := &Block{
		ID:                         1,
		Type:                       BlockToolCall,
		ToolName:                   "shell",
		Content:                    `{"command":"command -v benchstat || true"}`,
		Collapsed:                  true,
		ResultDone:                 false,
		ToolExecutionState:         agent.ToolCallExecutionStateQueued,
		ToolQueuedByExecutionEvent: true,
	}

	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if !strings.Contains(joined, "⏸ shell command -v benchstat || true") {
		t.Fatalf("expected queued Shell header; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Queued") {
		t.Fatalf("expected queued header badge; got:\n%s", joined)
	}
	for _, seg := range activeToolSpinnerSegments {
		if strings.Contains(joined, seg) {
			t.Fatalf("queued tool should not render active spinner segment %q; got:\n%s", seg, joined)
		}
	}
}

func TestGenericToolHeaderAndCollapsedResultEscapesANSIRichText(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "glob",
		Content:       `{"pattern":"\u001b[33m*.go\u001b[0m","path":"\u001b[35m/tmp/repo\u001b[0m"}`,
		Collapsed:     true,
		ResultContent: "\x1b[31minternal/tui/app.go\x1b[0m\ninternal/tui/block.go",
		ResultDone:    true,
	}

	joined := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if strings.ContainsRune(joined, '\x1b') {
		t.Fatalf("expected generic tool card to not contain raw ESC: %q", joined)
	}
	for _, want := range []string{`\x1b[33m*.go\x1b[0m`, `\x1b[35m/tmp/repo\x1b[0m`, "2 files"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected generic tool card to contain %q, got:\n%s", want, joined)
		}
	}
}

func TestBashCommandAndCollapsedSummaryEscapeANSIRichText(t *testing.T) {
	block := &Block{
		ID:        1,
		Type:      BlockToolCall,
		ToolName:  "shell",
		Content:   `{"command":"printf '\u001b[32mok\u001b[0m'","description":"\u001b[36mdesc\u001b[0m"}`,
		Collapsed: true,
		ResultContent: strings.Join([]string{
			"\x1b[31mline-1\x1b[0m",
			"line-2",
			"line-3",
			"line-4",
			"line-5",
			"line-6",
		}, "\n"),
		ResultDone: true,
	}

	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if strings.ContainsRune(joined, '\x1b') {
		t.Fatalf("expected shell card to not contain raw ESC: %q", joined)
	}
	for _, want := range []string{`\x1b[36mdesc\x1b[0m`, `\x1b[31mline-1\x1b[0m`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected shell card to contain %q, got:\n%s", want, joined)
		}
	}
}

func TestCollapsedLargeBashResultDoesNotRenderEntireHiddenOutput(t *testing.T) {
	lines := make([]string, 50000)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%05d", i)
	}
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "shell",
		Content:       `{"command":"cat huge.log","description":"show huge log"}`,
		Collapsed:     true,
		ResultContent: strings.Join(lines, "\n"),
		ResultDone:    true,
	}

	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "line-00000") {
		t.Fatalf("expected collapsed Shell preview to show first line, got:\n%s", joined)
	}
	if strings.Contains(joined, "line-49999") {
		t.Fatalf("collapsed Shell preview should not render the hidden tail, got:\n%s", joined)
	}
	if !strings.Contains(joined, "49999 more lines · [space] toggle expand/collapse") {
		t.Fatalf("expected cheap hidden-line hint for large output, got:\n%s", joined)
	}
}

func TestQuestionStructuredPromptAndAnswerEscapesANSIRichText(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "question",
		Content:       `{"questions":[{"header":"\u001b[31mHDR\u001b[0m","question":"Pick \u001b[32mone\u001b[0m","options":[{"label":"\u001b[34mA\u001b[0m","description":"\u001b[35mdesc\u001b[0m"}]}]}`,
		ResultContent: `[{"header":"\u001b[31mHDR\u001b[0m","selected":["\u001b[34mA\u001b[0m","\u001b[33mcustom\u001b[0m"]}]`,
		ResultDone:    true,
	}

	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if strings.ContainsRune(joined, '\x1b') {
		t.Fatalf("expected question card to not contain raw ESC: %q", joined)
	}
	for _, want := range []string{`\x1b[31mHDR\x1b[0m`, `\x1b[32mone\x1b[0m`, `\x1b[34mA\x1b[0m`, `\x1b[35mdesc\x1b[0m`, `\x1b[33mcustom\x1b[0m`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected question card to contain %q, got:\n%s", want, joined)
		}
	}
	if !strings.Contains(joined, `✓ 1. \x1b[34mA\x1b[0m`) {
		t.Fatalf("expected sanitized selected option label to stay checked, got:\n%s", joined)
	}
}

func TestDelegateAndControlCardsEscapeStructuredFields(t *testing.T) {
	cases := []struct {
		name  string
		block *Block
		want  []string
	}{
		{
			name: "delegate",
			block: &Block{
				ID:            1,
				Type:          BlockToolCall,
				ToolName:      "delegate",
				Collapsed:     false,
				Content:       `{"description":"run \u001b[32mworker\u001b[0m","agent_type":"\u001b[36mreviewer\u001b[0m"}`,
				ResultContent: `{"status":"started","task_id":"adhoc-1","agent_id":"\u001b[31mreviewer-1\u001b[0m","message":"\u001b[33mrunning\u001b[0m"}`,
				ResultDone:    true,
			},
			want: []string{`\x1b[32mworker\x1b[0m`, `\x1b[36mreviewer\x1b[0m`, `\x1b[31mreviewer-1\x1b[0m`, `\x1b[33mrunning\x1b[0m`},
		},
		{
			name: "cancel",
			block: &Block{
				ID:            2,
				Type:          BlockToolCall,
				ToolName:      "cancel",
				Collapsed:     false,
				Content:       `{"target_task_id":"adhoc-7","reason":"\u001b[35mstop now\u001b[0m"}`,
				ResultContent: `{"status":"stopped","task_id":"adhoc-7","agent_id":"\u001b[31mreviewer-2\u001b[0m","message":"\u001b[33mstopped\u001b[0m"}`,
				ResultDone:    true,
			},
			want: []string{`\x1b[35mstop now\x1b[0m`, `\x1b[31mreviewer-2\x1b[0m`, `\x1b[33mstopped\x1b[0m`},
		},
		{
			name: "notify",
			block: &Block{
				ID:            3,
				Type:          BlockToolCall,
				ToolName:      "notify",
				Collapsed:     false,
				Content:       `{"target_task_id":"adhoc-5","message":"\u001b[36mplease continue\u001b[0m","kind":"\u001b[34mreply\u001b[0m"}`,
				ResultContent: `{"status":"delivered","task_id":"adhoc-5","agent_id":"\u001b[31mreviewer-3\u001b[0m","message":"\u001b[33mdelivered\u001b[0m"}`,
				ResultDone:    true,
			},
			want: []string{`\x1b[36mplease continue\x1b[0m`, `\x1b[34mreply\x1b[0m`, `\x1b[31mreviewer-3\x1b[0m`, `\x1b[33mdelivered\x1b[0m`},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			joined := stripANSI(strings.Join(tt.block.Render(120, ""), "\n"))
			if strings.ContainsRune(joined, '\x1b') {
				t.Fatalf("expected %s card to not contain raw ESC: %q", tt.name, joined)
			}
			for _, want := range tt.want {
				if !strings.Contains(joined, want) {
					t.Fatalf("expected %s card to contain %q, got:\n%s", tt.name, want, joined)
				}
			}
		})
	}
}

func TestActiveToolHeaderKeepsToolNameStableWithoutCallingText(t *testing.T) {
	block := &Block{
		ID:                 1,
		Type:               BlockToolCall,
		ToolName:           "shell",
		Content:            `{"command":"echo hi"}`,
		Collapsed:          true,
		ResultDone:         false,
		ToolExecutionState: agent.ToolCallExecutionStateRunning,
	}

	joined := stripANSI(strings.Join(block.Render(80, "●"), "\n"))
	if strings.Contains(joined, "calling") {
		t.Fatalf("active tool header should not render calling text; got:\n%s", joined)
	}
	if !strings.Contains(joined, "shell echo hi") {
		t.Fatalf("expected active tool header to keep stable tool name and summary; got:\n%s", joined)
	}
}

func TestQueuedToolHeaderBadgeKeepsRightPadding(t *testing.T) {
	line := renderQueuedToolHeaderBadge("  ▸ Shell", 24)
	plain := stripANSI(line)
	if got := ansi.StringWidth(plain); got != 24 {
		t.Fatalf("queued header width = %d, want 24; got %q", got, plain)
	}
	if !strings.HasSuffix(plain, "Queued ") {
		t.Fatalf("expected queued badge to keep one trailing content-column space; got %q", plain)
	}
}

func TestToolBlockStyleUsesExplicitRightPadding(t *testing.T) {
	ApplyTheme(DefaultTheme())
	top, right, bottom, left := ToolBlockStyle.GetPadding()
	if top != 1 || right != 2 || bottom != 1 || left != 1 {
		t.Fatalf("tool block padding = (%d,%d,%d,%d), want (1,2,1,1)", top, right, bottom, left)
	}
}

func TestQueuedToolHeaderBadgeHidesWhenWidthIsTooNarrow(t *testing.T) {
	line := renderQueuedToolHeaderBadge("  ▸ VeryLongToolName", 20)
	plain := stripANSI(line)
	if strings.Contains(plain, "Queued") {
		t.Fatalf("expected queued badge to hide when width is too narrow; got %q", plain)
	}
	if plain != "  ▸ VeryLongToolName" {
		t.Fatalf("queued header = %q, want original trimmed header", plain)
	}
}

func TestActiveToolHeaderUsesProvidedSpinnerFrame(t *testing.T) {
	block := &Block{
		ID:                 1,
		Type:               BlockToolCall,
		ToolName:           "write",
		Content:            `{"path":"demo.txt","content":"hello"}`,
		Collapsed:          true,
		ResultDone:         false,
		ToolExecutionState: agent.ToolCallExecutionStateRunning,
	}

	joined := stripANSI(strings.Join(block.Render(80, "◳"), "\n"))
	if !strings.Contains(joined, "◳") {
		t.Fatalf("expected active tool header to use provided spinner frame; got:\n%s", joined)
	}
}

func TestActiveToolSpinnerSegmentsRotateClockwise(t *testing.T) {
	want := []string{"▖", "▘", "▝", "▗"}
	got := activeToolSpinnerSegments[:]
	if len(got) != len(want) {
		t.Fatalf("spinner segment count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("spinner segment %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestToolStatusPrefixesUseSemanticColors(t *testing.T) {
	tests := []struct {
		name   string
		block  Block
		marker string
		ansi   string
	}{
		{
			name: "success",
			block: Block{
				Type:                   BlockToolCall,
				ToolName:               "read",
				Content:                `{"path":"README.md"}`,
				ResultContent:          "ok",
				ResultStatus:           agent.ToolResultStatusSuccess,
				ResultDone:             true,
				ToolCallDetailExpanded: false,
			},
			marker: "✓",
			ansi:   "\x1b[38;5;82m✓",
		},
		{
			name: "error",
			block: Block{
				Type:                   BlockToolCall,
				ToolName:               "read",
				Content:                `{"path":"README.md"}`,
				ResultContent:          "Error: denied",
				ResultStatus:           agent.ToolResultStatusError,
				ResultDone:             true,
				ToolCallDetailExpanded: false,
			},
			marker: "✗",
			ansi:   "\x1b[38;5;196m✗",
		},
		{
			name: "cancelled",
			block: Block{
				Type:                   BlockToolCall,
				ToolName:               "read",
				Content:                `{"path":"README.md"}`,
				ResultContent:          "Cancelled",
				ResultStatus:           agent.ToolResultStatusCancelled,
				ResultDone:             true,
				ToolCallDetailExpanded: false,
			},
			marker: "◌",
			ansi:   "\x1b[38;5;250m◌",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			joinedANSI := strings.Join(tt.block.Render(80, ""), "\n")
			joinedPlain := stripANSI(joinedANSI)
			if !strings.Contains(joinedPlain, tt.marker+" "+tt.block.ToolName) {
				t.Fatalf("expected plain prefix %q; got:\n%s", tt.marker+" "+tt.block.ToolName, joinedPlain)
			}
			if !strings.Contains(joinedANSI, tt.ansi) {
				t.Fatalf("expected semantic color sequence %q; got:\n%q", tt.ansi, joinedANSI)
			}
		})
	}
}

func TestCollapsedBashErrorShowsCrossPrefixAndRedOutput(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                `{"command":"false"}`,
		ResultContent:          "stdout\n\nError: exit code 1",
		ResultStatus:           agent.ToolResultStatusError,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	lines := block.Render(80, "")
	joinedANSI := strings.Join(lines, "\n")
	joinedPlain := stripANSI(joinedANSI)

	if !strings.Contains(joinedPlain, "✗ shell false") {
		t.Fatalf("expected collapsed Shell error prefix; got:\n%s", joinedPlain)
	}
	if !strings.Contains(joinedPlain, "stdout") {
		t.Fatalf("expected preserved stdout in collapsed Shell error; got:\n%s", joinedPlain)
	}
	if !strings.Contains(joinedANSI, "\x1b[1;38;5;196m") && !strings.Contains(joinedANSI, "\x1b[38;5;196m") {
		t.Fatalf("expected error styling ANSI sequence; got:\n%q", joinedANSI)
	}
}

func TestCollapsedBashRejectedShowsExpandHintBeforeRejection(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                `{"command":"first command line\nsecond command line\nthird command line","timeout":120}`,
		ResultContent:          `tool "shell" rejected by user: sample rejection reason`,
		ResultStatus:           agent.ToolResultStatusError,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	lines := stripANSILines(block.Render(120, ""))
	hintIdx := -1
	rejectedIdx := -1
	for i, line := range lines {
		if strings.Contains(line, "more lines · [space] toggle expand/collapse") {
			hintIdx = i
		}
		if strings.Contains(line, `tool "shell" rejected by user: sample rejection reason`) {
			rejectedIdx = i
		}
	}
	if hintIdx < 0 {
		t.Fatalf("expected collapsed Shell rejection to show expand hint; got:\n%s", strings.Join(lines, "\n"))
	}
	if rejectedIdx < 0 {
		t.Fatalf("expected collapsed Shell rejection to show rejection reason; got:\n%s", strings.Join(lines, "\n"))
	}
	if hintIdx > rejectedIdx {
		t.Fatalf("expand hint should render before rejection reason; got:\n%s", strings.Join(lines, "\n"))
	}
}

func TestExpandedBashErrorKeepsToolCardBackgroundAcrossWrappedErrorBody(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                `{"command":"cd /home/user/projects/sample-repo && git ls-files -s | grep '\\u0000' || true","description":"No-op check for nulls in git index","timeout":60,"workdir":""}`,
		ResultContent:          "exit code 1 after output:\nstarting command: fork/exec /bin/bash: invalid argument",
		ResultDone:             true,
		IsError:                true,
		ToolCallDetailExpanded: true,
	}

	lines := block.Render(100, "")
	target := -1
	for i, line := range lines {
		if strings.Contains(stripANSI(line), "starting command: fork/exec /bin/bash: invalid argument") {
			target = i
			break
		}
	}
	if target < 0 {
		t.Fatalf("did not find wrapped shell error body in rendered lines:\n%s", strings.Join(stripANSILines(lines), "\n"))
	}

	line := lines[target]
	trimmed := strings.TrimRight(stripANSI(line), " ")
	contentEndCol := ansi.StringWidth(trimmed) - 1
	if contentEndCol < 0 {
		t.Fatalf("invalid content end col: %d", contentEndCol)
	}

	buf := newScreenBuffer(ansi.StringWidth(line), 1)
	uv.NewStyledString(line).Draw(buf, buf.Bounds())
	cells := buf.Line(0)
	if len(cells) == 0 {
		t.Fatal("expected rendered cells")
	}

	toolBg := colorOfTheme(currentTheme.ToolCallBg)
	checked := 0
	for i := contentEndCol + 1; i < len(cells); i++ {
		cell := cells[i]
		if cell.IsZero() || cell.Content != " " {
			continue
		}
		checked++
		if !colorsEqual(cell.Style.Bg, toolBg) {
			t.Fatalf("trailing space at col %d background = %v, want tool card bg %v", i, cell.Style.Bg, toolBg)
		}
	}
	if checked == 0 {
		t.Fatal("expected trailing padding spaces on shell error line")
	}
}

func TestDeleteHeaderShowsRelativePathInsideWorkingDir(t *testing.T) {
	wd := filepath.Join(string(os.PathSeparator), "tmp", "workspace")
	abs := filepath.Join(wd, "internal", "tui", "obsolete.go")
	block := &Block{
		ID:                1,
		Type:              BlockToolCall,
		ToolName:          "delete",
		Content:           fmt.Sprintf(`{"paths":[%q],"reason":"remove obsolete file"}`, abs),
		ResultContent:     "delete completed.\n\nDeleted (1):\n- internal/tui/obsolete.go",
		ResultDone:        true,
		displayWorkingDir: wd,
	}
	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "delete internal/tui/obsolete.go") {
		t.Fatalf("expected delete header to show relative path; got:\n%s", joined)
	}
	if strings.Contains(joined, abs) {
		t.Fatalf("did not expect delete header to show absolute path; got:\n%s", joined)
	}
}

func TestDeleteHeaderShowsFilePath(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "delete",
		Content:                `{"paths":["internal/tui/obsolete.go"],"reason":"remove obsolete file"}`,
		ResultContent:          "delete completed.\n\nDeleted (1):\n- internal/tui/obsolete.go",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "delete internal/tui/obsolete.go") {
		t.Fatalf("expected delete header to show file path; got:\n%s", joined)
	}
	if !strings.Contains(joined, "remove obsolete file") {
		t.Fatalf("expected delete header to show reason; got:\n%s", joined)
	}
}

func TestCompactToolWithOneHiddenLineForcesExpandedResult(t *testing.T) {
	tests := []struct {
		name        string
		toolName    string
		content     string
		result      string
		wantPrefix  string
		wantVisible string
	}{
		{
			name:        "delete",
			toolName:    "delete",
			content:     `{"paths":["examples/compression-config.yaml"],"reason":"remove obsolete example"}`,
			result:      "delete completed.\n\nDeleted (1):\n- examples/compression-config.yaml",
			wantPrefix:  "✓ delete",
			wantVisible: "- examples/compression-config.yaml",
		},
		{
			name:        "grep",
			toolName:    "grep",
			content:     `{"pattern":"TODO"}`,
			result:      strings.Join([]string{"a.go:1:TODO", "b.go:2:TODO", "c.go:3:TODO", "d.go:4:TODO", "e.go:5:TODO", "f.go:6:TODO", "g.go:7:TODO", "h.go:8:TODO", "i.go:9:TODO", "j.go:10:TODO", "k.go:11:TODO"}, "\n"),
			wantPrefix:  "✓ grep",
			wantVisible: "k.go:11:TODO",
		},
		{
			name:        "glob",
			toolName:    "glob",
			content:     `{"pattern":"**/*.go"}`,
			result:      strings.Join([]string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go", "g.go", "h.go", "i.go", "j.go", "k.go"}, "\n"),
			wantPrefix:  "✓ glob",
			wantVisible: "k.go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := &Block{
				ID:                     1,
				Type:                   BlockToolCall,
				ToolName:               tt.toolName,
				Content:                tt.content,
				ResultContent:          tt.result,
				ResultDone:             true,
				ToolCallDetailExpanded: false,
			}

			joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
			if strings.Contains(joined, "[space] toggle expand/collapse") || strings.Contains(joined, "1 more lines") {
				t.Fatalf("single hidden line should be shown inline without expand hint; got:\n%s", joined)
			}
			if !strings.Contains(joined, tt.wantPrefix) {
				t.Fatalf("forced-expanded compact tool should show expanded prefix %q; got:\n%s", tt.wantPrefix, joined)
			}
			if !strings.Contains(joined, tt.wantVisible) {
				t.Fatalf("single hidden line should be visible; got:\n%s", joined)
			}
		})
	}
}

func TestWebFetchHeaderShowsURLAndTimeout(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "web_fetch",
		Content:                `{"url":"https://iterm2.com/documentation-images.html","timeout":40}`,
		ResultContent:          "URL: https://iterm2.com/documentation-images.html\nContent-Type: text/html",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "web_fetch https://iterm2.com/documentation-images.html (timeout=40)") {
		t.Fatalf("expected web_fetch header to include URL and timeout; got:\n%s", joined)
	}
}

func TestCollapsedTaskShowsSpawnedSummary(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "delegate",
		Collapsed:              true,
		Content:                `{"description":"review tests","agent_type":"reviewer"}`,
		ResultContent:          `{"status":"started","task_id":"adhoc-7","agent_id":"reviewer-2","message":"running in background"}`,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(90, ""), "\n"))
	if strings.Contains(joined, "description:") {
		t.Fatalf("expected Delegate view to avoid raw description label; got:\n%s", joined)
	}
	if !strings.Contains(joined, "delegate (reviewer)") {
		t.Fatalf("expected Delegate header to show tool name + agent type only; got:\n%s", joined)
	}
	if strings.Contains(joined, "review tests") && !strings.Contains(joined, "(reviewer)") {
		t.Fatalf("expected description to appear in body, not header; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Spawned · reviewer-2") {
		t.Fatalf("expected Delegate collapsed summary to show spawned agent; got:\n%s", joined)
	}
	if strings.Contains(joined, "adhoc-7") {
		t.Fatalf("expected Delegate collapsed summary to not include task_id; got:\n%s", joined)
	}
}

func TestCollapsedTaskShowsMultilineDescription(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "delegate",
		Collapsed:              true,
		Content:                `{"description":"review tests\ncheck coverage\nupdate docs","agent_type":"reviewer"}`,
		ResultContent:          `{"status":"started","task_id":"adhoc-7","agent_id":"reviewer-2"}`,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(90, ""), "\n"))
	if !strings.Contains(joined, "review tests") {
		t.Fatalf("expected collapsed Delegate to show first description line; got:\n%s", joined)
	}
	if !strings.Contains(joined, "check coverage") {
		t.Fatalf("expected collapsed Delegate to show second description line; got:\n%s", joined)
	}
	if strings.Contains(joined, "update docs") {
		t.Fatalf("expected collapsed Delegate preview to hide later description lines; got:\n%s", joined)
	}
	if !strings.Contains(joined, "1 more lines · [space] toggle expand/collapse") {
		t.Fatalf("expected collapsed Delegate preview to show expand hint; got:\n%s", joined)
	}
	if !strings.Contains(joined, "(reviewer)") {
		t.Fatalf("expected Delegate header to show agent type; got:\n%s", joined)
	}
}

func TestCollapsedGenericToolDeduplicatesMatchingParamAndResultPreview(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "Task",
		Collapsed:              true,
		Content:                `{"command":"[Image #1]","description":"Subdivide dimension-choice failures into purer semantic subtypes"}`,
		ResultContent:          "[Image #1]\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10\nline11",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(90, ""), "\n"))
	if got := strings.Count(joined, "command: [Image #1]"); got != 1 {
		t.Fatalf("expected collapsed preview line to appear once, got %d\n%s", got, joined)
	}
	if strings.Count(joined, "[Image #1]") != 1 {
		t.Fatalf("expected duplicated result first line to be suppressed, got:\n%s", joined)
	}
	if !strings.Contains(joined, "[space] toggle expand/collapse") {
		t.Fatalf("expected expand hint to remain after deduplication, got:\n%s", joined)
	}
}

func TestExpandedTaskShowsDescriptionAndWorkerWithTaskID(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "delegate",
		Collapsed:              false,
		Content:                `{"description":"review tests","agent_type":"reviewer"}`,
		ResultContent:          `{"status":"started","task_id":"adhoc-7","agent_id":"reviewer-2"}`,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(90, ""), "\n"))
	if !strings.Contains(joined, "Description:") {
		t.Fatalf("expected expanded Delegate to show description heading; got:\n%s", joined)
	}
	if !strings.Contains(joined, "review tests") {
		t.Fatalf("expected expanded Delegate to show description content; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Worker:") {
		t.Fatalf("expected expanded Delegate to show Worker section; got:\n%s", joined)
	}
	if !strings.Contains(joined, "task_id:") || !strings.Contains(joined, "adhoc-7") {
		t.Fatalf("expected expanded Delegate worker area to include task_id; got:\n%s", joined)
	}
}

func TestExpandedTaskDescriptionUsesPlainTextForMarkdownLookingContent(t *testing.T) {
	ApplyTheme(DefaultTheme())
	desc := "## Plan\n- item one\n- item two\n\n```go\nfmt.Println(\"ok\")\n```"
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "delegate",
		Content:                `{"description":` + strconv.Quote(desc) + `,"agent_type":"reviewer"}`,
		ResultContent:          `{"status":"started","task_id":"adhoc-7","agent_id":"reviewer-2"}`,
		ResultDone:             true,
		ToolCallDetailExpanded: true,
	}

	joined := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(joined, "Description:") {
		t.Fatalf("expected expanded Delegate to show description heading; got:\n%s", joined)
	}
	for _, want := range []string{"## Plan", "- item one", "- item two", "```go", `fmt.Println("ok")`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected Delegate description to keep literal markdown-looking text %q, got:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "• item one") {
		t.Fatalf("delegate description should not render markdown bullets; got:\n%s", joined)
	}
}

func TestGrepHeaderShowsRelativeSearchPathInsideWorkingDir(t *testing.T) {
	wd := filepath.Join(string(os.PathSeparator), "tmp", "workspace")
	abs := filepath.Join(wd, "internal", "tui")
	block := &Block{
		ID:                1,
		Type:              BlockToolCall,
		ToolName:          "grep",
		Content:           fmt.Sprintf(`{"pattern":"TODO","path":%q,"glob":"*.go"}`, abs),
		ResultDone:        true,
		displayWorkingDir: wd,
	}
	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "grep TODO (path=internal/tui, glob=*.go)") {
		t.Fatalf("expected grep header to show relative search path; got:\n%s", joined)
	}
	if strings.Contains(joined, abs) {
		t.Fatalf("did not expect grep header to show absolute path; got:\n%s", joined)
	}
}

func TestGlobHeaderShowsRelativeSearchPathInsideWorkingDir(t *testing.T) {
	wd := filepath.Join(string(os.PathSeparator), "tmp", "workspace")
	abs := filepath.Join(wd, "internal")
	block := &Block{
		ID:                1,
		Type:              BlockToolCall,
		ToolName:          "glob",
		Content:           fmt.Sprintf(`{"pattern":"**/*.go","path":%q}`, abs),
		ResultDone:        true,
		displayWorkingDir: wd,
	}
	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "glob **/*.go (path=internal)") {
		t.Fatalf("expected glob header to show relative search path; got:\n%s", joined)
	}
	if strings.Contains(joined, abs) {
		t.Fatalf("did not expect glob header to show absolute path; got:\n%s", joined)
	}
}

func TestBashExpandedMetaShowsRelativeWorkdirInsideWorkingDir(t *testing.T) {
	wd := filepath.Join(string(os.PathSeparator), "tmp", "workspace")
	workdir := filepath.Join(wd, "internal", "tui")
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                fmt.Sprintf(`{"command":"pwd","workdir":%q}`, workdir),
		ResultDone:             true,
		ToolCallDetailExpanded: true,
		displayWorkingDir:      wd,
	}
	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "workdir: internal/tui") && !strings.Contains(joined, "Workdir: internal/tui") {
		t.Fatalf("expected shell expanded body to show relative workdir; got:\n%s", joined)
	}
	if strings.Contains(joined, workdir) {
		t.Fatalf("did not expect bash body to show absolute workdir; got:\n%s", joined)
	}
}

func TestCollapsedBashShowsResultSummary(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                `{"command":"go test ./internal/tui/..."}`,
		ResultContent:          "ok\nsecond line",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(90, ""), "\n"))
	if !strings.Contains(joined, "ok") {
		t.Fatalf("expected shell collapsed summary to show success output summary; got:\n%s", joined)
	}
}

func TestCollapsedGrepShowsMatchCountSummary(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "grep",
		Content:                `{"pattern":"TODO"}`,
		ResultContent:          "a.go:1:TODO\nb.go:2:TODO",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(90, ""), "\n"))
	if !strings.Contains(joined, "2 matches") {
		t.Fatalf("expected Grep collapsed summary to show match count; got:\n%s", joined)
	}
}

func TestCollapsedGrepOmitsLowCountSummary(t *testing.T) {
	tests := []struct {
		name          string
		resultContent string
		wantPresent   string
		wantAbsent    string
	}{
		{
			name:          "zero matches",
			resultContent: "No matches found.",
			wantPresent:   "No matches found.",
			wantAbsent:    "0 matches",
		},
		{
			name:          "one match",
			resultContent: "a.go:1:TODO",
			wantPresent:   "a.go:1:TODO",
			wantAbsent:    "1 matches",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := &Block{
				ID:                     1,
				Type:                   BlockToolCall,
				ToolName:               "grep",
				Content:                `{"pattern":"TODO"}`,
				ResultContent:          tt.resultContent,
				ResultDone:             true,
				ToolCallDetailExpanded: false,
			}

			joined := stripANSI(strings.Join(block.Render(90, ""), "\n"))
			if tt.wantPresent != "" && !strings.Contains(joined, tt.wantPresent) {
				t.Fatalf("expected output to contain %q; got:\n%s", tt.wantPresent, joined)
			}
			if tt.wantAbsent != "" && strings.Contains(joined, tt.wantAbsent) {
				t.Fatalf("expected output to omit %q; got:\n%s", tt.wantAbsent, joined)
			}
		})
	}
}

func TestCancelledWriteCallSuppressesEmptyFilePreviewAndDuplicateCancelledText(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "write",
		Content:       `{"path":"foo.txt","content":""}`,
		ResultContent: "Cancelled",
		ResultStatus:  agent.ToolResultStatusCancelled,
		ResultDone:    true,
	}

	plain := stripANSI(strings.Join(block.Render(80, ""), "\n"))
	if !strings.Contains(plain, "write foo.txt") {
		t.Fatalf("expected Write header to include file path, got:\n%s", plain)
	}
	if !strings.Contains(plain, "↳ Cancelled") {
		t.Fatalf("expected cancelled summary line, got:\n%s", plain)
	}
	if strings.Contains(plain, "(empty file)") {
		t.Fatalf("expected cancelled Write card to suppress empty file preview, got:\n%s", plain)
	}
	if strings.Contains(plain, "↳ Cancelled:") {
		t.Fatalf("expected cancelled Write card to omit duplicate cancelled detail header, got:\n%s", plain)
	}
	if strings.Count(plain, "Cancelled") != 1 {
		t.Fatalf("expected exactly one visible Cancelled marker, got:\n%s", plain)
	}
}

func TestEditErrorPreservesExampleBlockIndentation(t *testing.T) {
	block := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: tools.NameEdit,
		Content:  `{"patch":"*** Begin Patch\n*** Update File: internal/tui/sidebar_render.go\n@@\n-for _, fe := range files {\n-baseName := filepath.Base(fe.Path)\n-var parts string\n*** End Patch\n"}`,
		ResultContent: strings.Join([]string{
			"hunk not found. Indentation mismatch? A unique match exists if leading whitespace is ignored. Example block:",
			"\t\t\tfor _, fe := range files {",
			"\t\t\t\tbaseName := filepath.Base(fe.Path)",
			"\t\t\t\tvar parts string",
		}, "\n"),
		ResultStatus: agent.ToolResultStatusError,
		ResultDone:   true,
	}

	plain := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(plain, "            for _, fe := range files {") {
		t.Fatalf("expected first example line indentation to be preserved; got:\n%s", plain)
	}
	if !strings.Contains(plain, "                baseName := filepath.Base(fe.Path)") {
		t.Fatalf("expected nested example line indentation to be preserved; got:\n%s", plain)
	}
	if strings.Contains(plain, "    for _, fe := range files {") && !strings.Contains(plain, "            for _, fe := range files {") {
		t.Fatalf("example block indentation appears trimmed; got:\n%s", plain)
	}
}

func TestCancelledEditCallSuppressesDiffPreviewAndDuplicateCancelledText(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameEdit,
		Content:       `{"path":"foo.txt","patch":"@@\n-a\n+b\n"}`,
		Diff:          "@@ -1,1 +1,1 @@\n-a\n+b\n",
		ResultContent: "Cancelled",
		ResultStatus:  agent.ToolResultStatusCancelled,
		ResultDone:    true,
	}

	plain := stripANSI(strings.Join(block.Render(80, ""), "\n"))
	if !strings.Contains(plain, "edit") || !strings.Contains(plain, "foo.txt") {
		t.Fatalf("expected edit header to include file path, got:\n%s", plain)
	}
	if !strings.Contains(plain, "↳ Cancelled") {
		t.Fatalf("expected cancelled summary line, got:\n%s", plain)
	}
	if strings.Contains(plain, "-a") || strings.Contains(plain, "+b") {
		t.Fatalf("expected cancelled Edit card to suppress diff preview, got:\n%s", plain)
	}
	if strings.Contains(plain, "↳ Cancelled:") {
		t.Fatalf("expected cancelled Edit card to omit duplicate cancelled detail header, got:\n%s", plain)
	}
	if strings.Count(plain, "Cancelled") != 1 {
		t.Fatalf("expected exactly one visible Cancelled marker, got:\n%s", plain)
	}
}

func TestCancelledReadCallOmitsDuplicateCancelledText(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "read",
		Content:       `{"path":"foo.txt","limit":10}`,
		ResultContent: "Cancelled",
		ResultStatus:  agent.ToolResultStatusCancelled,
		ResultDone:    true,
	}

	plain := stripANSI(strings.Join(block.Render(80, ""), "\n"))
	if !strings.Contains(plain, "read foo.txt") {
		t.Fatalf("expected Read header to include file path, got:\n%s", plain)
	}
	if !strings.Contains(plain, "↳ Cancelled") {
		t.Fatalf("expected cancelled summary line, got:\n%s", plain)
	}
	if strings.Contains(plain, "↳ Cancelled:") {
		t.Fatalf("expected Read cancelled card to omit duplicate cancelled detail header, got:\n%s", plain)
	}
	if strings.Count(plain, "Cancelled") != 1 {
		t.Fatalf("expected exactly one visible Cancelled marker, got:\n%s", plain)
	}
}

func TestCancelledGenericToolOmitsDuplicateCancelledText(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                `{"command":"echo hi"}`,
		ResultContent:          "Cancelled",
		ResultStatus:           agent.ToolResultStatusCancelled,
		ResultDone:             true,
		ToolCallDetailExpanded: true,
	}

	plain := stripANSI(strings.Join(block.Render(80, ""), "\n"))
	if !strings.Contains(plain, "shell echo hi") {
		t.Fatalf("expected shell header to remain visible, got:\n%s", plain)
	}
	if !strings.Contains(plain, "↳ Cancelled") {
		t.Fatalf("expected cancelled summary line, got:\n%s", plain)
	}
	if strings.Contains(plain, "↳ Cancelled:") {
		t.Fatalf("expected generic cancelled card to omit duplicate cancelled detail header, got:\n%s", plain)
	}
	if strings.Count(plain, "Cancelled") != 1 {
		t.Fatalf("expected exactly one visible Cancelled marker, got:\n%s", plain)
	}
}

func TestCancelledSpecialToolOmitsDuplicateCancelledText(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "question",
		Content:       `{"questions":[{"header":"log","question":"paste log"}]}`,
		ResultContent: "Cancelled",
		ResultStatus:  agent.ToolResultStatusCancelled,
		ResultDone:    true,
	}

	plain := stripANSI(strings.Join(block.Render(80, ""), "\n"))
	if !strings.Contains(plain, "question") {
		t.Fatalf("expected question header to remain visible, got:\n%s", plain)
	}
	if !strings.Contains(plain, "↳ Cancelled") {
		t.Fatalf("expected cancelled summary line, got:\n%s", plain)
	}
	if strings.Contains(plain, "↳ Cancelled:") {
		t.Fatalf("expected special cancelled card to omit duplicate cancelled detail header, got:\n%s", plain)
	}
	if strings.Count(plain, "Cancelled") != 1 {
		t.Fatalf("expected exactly one visible Cancelled marker, got:\n%s", plain)
	}
}

func TestCollapsedGlobShowsFileCountSummary(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "glob",
		Content:                `{"pattern":"**/*.go"}`,
		ResultContent:          "a.go\nb.go\nc.go",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(90, ""), "\n"))
	if !strings.Contains(joined, "3 files") {
		t.Fatalf("expected Glob collapsed summary to show file count; got:\n%s", joined)
	}
}

func TestCollapsedGlobOmitsLowCountSummary(t *testing.T) {
	tests := []struct {
		name          string
		resultContent string
		wantPresent   string
		wantAbsent    string
	}{
		{
			name:          "zero files",
			resultContent: "No files matched the pattern.",
			wantPresent:   "No files matched the pattern.",
			wantAbsent:    "0 files",
		},
		{
			name:          "one file",
			resultContent: "a.go",
			wantPresent:   "a.go",
			wantAbsent:    "1 files",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := &Block{
				ID:                     1,
				Type:                   BlockToolCall,
				ToolName:               "glob",
				Content:                `{"pattern":"**/*.go"}`,
				ResultContent:          tt.resultContent,
				ResultDone:             true,
				ToolCallDetailExpanded: false,
			}

			joined := stripANSI(strings.Join(block.Render(90, ""), "\n"))
			if tt.wantPresent != "" && !strings.Contains(joined, tt.wantPresent) {
				t.Fatalf("expected output to contain %q; got:\n%s", tt.wantPresent, joined)
			}
			if tt.wantAbsent != "" && strings.Contains(joined, tt.wantAbsent) {
				t.Fatalf("expected output to omit %q; got:\n%s", tt.wantAbsent, joined)
			}
		})
	}
}

func TestQuestionCallMarksSelectedOptionInline(t *testing.T) {
	ApplyTheme(DefaultTheme())

	answers := `[{"header":"Messaging platform","selected":["Chat App"]}]`
	block := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: "question",
		Content: `{"questions":[{"header":"Messaging platform","question":"Which one should we use first?","options":[` +
			`{"label":"Email","description":"One inbox per workspace"},` +
			`{"label":"Chat App","description":"Single workspace with direct messages"}` +
			`],"custom":true}]}`,
		ResultContent: answers,
		ResultDone:    true,
	}

	plain := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(plain, "✓ 2. Chat App") {
		t.Fatalf("expected selected option to be marked inline, got:\n%s", plain)
	}
	lines := strings.Split(plain, "\n")
	var emailLine, chatLine string
	for _, line := range lines {
		switch {
		case strings.Contains(line, "Email"):
			emailLine = line
		case strings.Contains(line, "Chat App"):
			chatLine = line
		}
	}
	if emailLine == "" || chatLine == "" {
		t.Fatalf("expected both option lines, got:\n%s", plain)
	}
	emailBefore, _, emailOK := strings.Cut(emailLine, "1.")
	chatBefore, _, chatOK := strings.Cut(chatLine, "2.")
	if !emailOK || !chatOK {
		t.Fatalf("expected option numbers in both lines, got:\n%s", plain)
	}
	if got, want := ansi.StringWidth(emailBefore), ansi.StringWidth(chatBefore); got != want {
		t.Fatalf("expected option numbers to align, got email=%d chat=%d\n%s", got, want, plain)
	}
	if strings.Contains(plain, "↳ Answer:") || strings.Contains(plain, "↳ Answers:") {
		t.Fatalf("expected inline answer rendering without separate answer section, got:\n%s", plain)
	}
	if strings.Contains(plain, "Custom: Enabled") {
		t.Fatalf("expected custom capability line to be replaced/omitted after inline result, got:\n%s", plain)
	}
}

func TestQuestionCallRendersCustomAnswerInline(t *testing.T) {
	ApplyTheme(DefaultTheme())

	answers := `[{"header":"Project location","selected":["/tmp/sample-project\n/tmp/sample-cache"]}]`
	block := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: "question",
		Content: `{"questions":[{"header":"Project location","question":"Where should it go?","options":[` +
			`{"label":"Sibling directory","description":"For example /home/user/projects/sample-gateway"},` +
			`{"label":"Project subdirectory","description":"For example /home/user/projects/sample-app/gateway"}` +
			`],"custom":true}]}`,
		ResultContent: answers,
		ResultDone:    true,
	}

	plain := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(plain, "Custom: /tmp/sample-project") {
		t.Fatalf("expected inline custom answer, got:\n%s", plain)
	}
	if !strings.Contains(plain, "            /tmp/sample-cache") {
		t.Fatalf("expected multiline custom continuation indent, got:\n%s", plain)
	}
	if strings.Contains(plain, "↳ Answer:") || strings.Contains(plain, "↳ Answers:") {
		t.Fatalf("expected inline custom rendering without separate answer section, got:\n%s", plain)
	}
}

func TestQuestionCallRendersMultilineAnswerWithContinuationIndent(t *testing.T) {
	ApplyTheme(DefaultTheme())

	answers := `[{"header":"top output","selected":["line 1\nline 2\nline 3"]}]`
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "question",
		Content:       `{"questions":[{"header":"top output","question":"Paste a few lines from top","custom":true}]}`,
		ResultContent: answers,
		ResultDone:    true,
	}

	plain := stripANSI(strings.Join(block.Render(70, ""), "\n"))
	if !strings.Contains(plain, "Answer: line 1") {
		t.Fatalf("expected inline Question answer, got:\n%s", plain)
	}
	if !strings.Contains(plain, "            line 2") {
		t.Fatalf("expected continuation indent for second line, got:\n%s", plain)
	}
	if !strings.Contains(plain, "            line 3") {
		t.Fatalf("expected continuation indent for third line, got:\n%s", plain)
	}
}

func TestQuestionCallPreservesWhitespaceInMultilineAnswer(t *testing.T) {
	ApplyTheme(DefaultTheme())

	answers := `[{"header":"top output","selected":["PID  CPU   COMMAND\n1    10%   sample-app"]}]`
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "question",
		Content:       `{"questions":[{"header":"top output","question":"Paste a few lines from top","custom":true}]}`,
		ResultContent: answers,
		ResultDone:    true,
	}

	plain := stripANSI(strings.Join(block.Render(90, ""), "\n"))
	if !strings.Contains(plain, "Answer: PID  CPU   COMMAND") {
		t.Fatalf("expected aligned spaces in first line, got:\n%s", plain)
	}
	if !strings.Contains(plain, "            1    10%   sample-app") {
		t.Fatalf("expected aligned spaces in continuation line, got:\n%s", plain)
	}
}

func TestQuestionCallFallbackResultPreservesMultilineText(t *testing.T) {
	ApplyTheme(DefaultTheme())

	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "question",
		Content:       `{"questions":[{"header":"log","question":"paste log"}]}`,
		ResultContent: "first line\nsecond line\nthird line",
		ResultDone:    true,
	}

	plain := stripANSI(strings.Join(block.Render(70, ""), "\n"))
	if !strings.Contains(plain, "↳ ✓") {
		t.Fatalf("expected fallback success header, got:\n%s", plain)
	}
	for _, want := range []string{"first line", "second line", "third line"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected fallback multiline result to contain %q, got:\n%s", want, plain)
		}
	}
}

func TestDoneCallRejectedUsesCrossAndSimplifiedReason(t *testing.T) {
	ApplyTheme(DefaultTheme())

	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "done",
		ResultDone:    true,
		ResultStatus:  agent.ToolResultStatusCancelled,
		ResultContent: "Done rejected: require all checks to pass before exit and include verification results",
	}

	plain := stripANSI(strings.Join(block.Render(90, ""), "\n"))
	if !strings.Contains(plain, "✗ done") {
		t.Fatalf("expected rejected Done to use failure icon, got:\n%s", plain)
	}
	if !strings.Contains(plain, "rejected reason: require all checks to pass before exit and include") ||
		!strings.Contains(plain, "verification results") {
		t.Fatalf("expected simplified rejected reason text, got:\n%s", plain)
	}
	for _, unwanted := range []string{"Status:", "Done rejected:"} {
		if strings.Contains(plain, unwanted) {
			t.Fatalf("expected rejected Done to omit %q, got:\n%s", unwanted, plain)
		}
	}
}

func TestDoneCallAutoRejectedUsesCrossAndSimplifiedReason(t *testing.T) {
	ApplyTheme(DefaultTheme())

	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "done",
		ResultDone:    true,
		ResultStatus:  agent.ToolResultStatusSuccess,
		ResultContent: "Done rejected automatically: loop exit conditions are not satisfied yet: open TODO items remain. Finish the remaining work before calling Done again.",
	}

	plain := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(plain, "✗ done") {
		t.Fatalf("expected auto-rejected Done to use failure icon, got:\n%s", plain)
	}
	for _, want := range []string{"rejected reason: loop exit conditions are not", "satisfied yet: open TODO items remain.", "Finish the remaining work before calling", "Done again."} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected auto-rejected Done render to contain %q, got:\n%s", want, plain)
		}
	}
	for _, unwanted := range []string{"Status:", "Done rejected automatically:"} {
		if strings.Contains(plain, unwanted) {
			t.Fatalf("expected auto-rejected Done to omit %q, got:\n%s", unwanted, plain)
		}
	}
}

func TestDoneCallRejectedRendersReportAndSingleReason(t *testing.T) {
	ApplyTheme(DefaultTheme())

	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "done",
		ResultDone:    true,
		ResultStatus:  agent.ToolResultStatusSuccess,
		ResultContent: "Done rejected: coverage must be >= 70% before exiting",
		DoneReport:    "## Completion status\nReport body stays visible",
	}

	plain := stripANSI(strings.Join(block.Render(72, ""), "\n"))
	for _, want := range []string{"Completion status", "Report body stays visible", "rejected reason: coverage must be >= 70% before", "exiting"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected rejected Done render to contain %q, got:\n%s", want, plain)
		}
	}
	if strings.Count(plain, "rejected reason:") != 1 {
		t.Fatalf("expected exactly one rejected reason line, got:\n%s", plain)
	}
	if strings.Contains(plain, "Done rejected:") || strings.Contains(plain, "Status:") {
		t.Fatalf("expected rejected Done render to omit raw rejection/status, got:\n%s", plain)
	}
}

func TestDoneCallAcceptedOmitsStatusAndRejectedReason(t *testing.T) {
	ApplyTheme(DefaultTheme())

	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "done",
		ResultDone:    true,
		ResultStatus:  agent.ToolResultStatusSuccess,
		ResultContent: "done",
		DoneReport:    "## Summary\n- shipped\n- verified",
	}

	plain := stripANSI(strings.Join(block.Render(90, ""), "\n"))
	for _, want := range []string{"✓ done", "Summary", "• shipped", "• verified"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected accepted Done render to contain %q, got:\n%s", want, plain)
		}
	}
	for _, unwanted := range []string{"Status:", "rejected reason:", "Done rejected:"} {
		if strings.Contains(plain, unwanted) {
			t.Fatalf("expected accepted Done render to omit %q, got:\n%s", unwanted, plain)
		}
	}
}

func TestDoneCallRendersDoneReportMarkdown(t *testing.T) {
	ApplyTheme(DefaultTheme())

	block := &Block{
		ID:           1,
		Type:         BlockToolCall,
		ToolName:     "done",
		ResultDone:   true,
		ResultStatus: agent.ToolResultStatusSuccess,
		DoneReport:   "## Summary\n- shipped\n- verified",
	}

	plain := stripANSI(strings.Join(block.Render(90, ""), "\n"))
	for _, want := range []string{"Summary", "• shipped", "• verified"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected rendered Done report to contain %q, got:\n%s", want, plain)
		}
	}
}

func TestReadCallRendersSingleBlankLineWithoutPanic(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "read",
		Content:       `{"path":"internal/tui/input.go","limit":1,"offset":358}`,
		ResultDone:    true,
		ResultContent: "\n",
	}

	lines := block.renderReadCall(80, "")
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "read internal/tui/input.go") {
		t.Fatalf("expected Read header to remain visible, got:\n%s", plain)
	}
	if !strings.Contains(plain, "359") {
		t.Fatalf("expected blank numbered line to render safely, got:\n%s", plain)
	}
	if strings.Contains(plain, "panic") {
		t.Fatalf("unexpected panic text in rendered output: %s", plain)
	}
}

func TestReadCallEscapesANSIRichDumpContent(t *testing.T) {
	block := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   "read",
		Content:    `{"path":"/tmp/chord-diag/tui-dump.txt","limit":120,"offset":1535}`,
		ResultDone: true,
		ResultContent: strings.Join([]string{
			"\x1b[38;5;61m│\x1b[m\x1b[48;5;235m dump line\x1b[m",
			"[screen_buffer]",
		}, "\n"),
	}

	rows, source := parseReadDisplayLines(block.ResultContent, 1536)
	if len(rows) != 2 {
		t.Fatalf("rows len = %d, want 2", len(rows))
	}
	if strings.ContainsRune(rows[0].Content, '\x1b') {
		t.Fatalf("sanitized Read row should not contain raw ESC: %q", rows[0].Content)
	}
	if strings.ContainsRune(source, '\x1b') {
		t.Fatalf("sanitized Read source sample should not contain raw ESC: %q", source)
	}
	if !strings.Contains(rows[0].Content, `\x1b[38;5;61m│\x1b[m\x1b[48;5;235m dump line\x1b[m`) {
		t.Fatalf("sanitized Read row should expose ANSI literally, got %q", rows[0].Content)
	}

	plain := stripANSI(strings.Join(block.renderReadCall(140, ""), "\n"))
	for _, want := range []string{
		`read /tmp/chord-diag/tui-dump.txt`,
		`\x1b[38;5;61m│\x1b[m\x1b[48;5;235m dump line\x1b[m`,
		`[screen_buffer]`,
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected rendered read card to contain %q, got:\n%s", want, plain)
		}
	}
}

func TestReadCallStripsTrailingCarriageReturnsFromPersistedOutput(t *testing.T) {
	block := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   "read",
		Content:    `{"path":"sample.csv","limit":20}`,
		ResultDone: true,
		ResultContent: strings.Join([]string{
			"issue,label\r",
			"\"a\",\"b\"\r",
			"(showing lines 1-2 of 10)\r",
		}, "\n"),
	}

	rows, source := parseReadDisplayLines(block.ResultContent, 1)
	if len(rows) != 3 {
		t.Fatalf("rows len = %d, want 3", len(rows))
	}
	for i, row := range rows {
		if containsRawCarriageReturnForTest(row.Content) {
			t.Fatalf("row %d content should not contain raw carriage return: %q", i, row.Content)
		}
	}
	if containsRawCarriageReturnForTest(source) {
		t.Fatalf("source sample should not contain raw carriage return: %q", source)
	}

	plain := stripANSI(strings.Join(block.renderReadCall(80, ""), "\n"))
	if containsRawCarriageReturnForTest(plain) {
		t.Fatalf("rendered read card should not contain raw carriage return: %q", plain)
	}
	for _, want := range []string{"issue,label", "\"a\",\"b\"", "(showing lines 1-2 of 10)"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected rendered read card to contain %q, got:\n%s", want, plain)
		}
	}
}

func TestReadCallTreatsReadResultHeaderAsMetadata(t *testing.T) {
	result := strings.Join([]string{
		`READ_RESULT path="sample.csv" lines=41-42/200 content_lines=2 truncated=true encoding="utf-8"`,
		"issue,label",
		`"a","b"`,
	}, "\n")

	rows, source := parseReadDisplayLines(result, 1)
	if len(rows) != 3 {
		t.Fatalf("rows len = %d, want 3", len(rows))
	}
	if rows[0].IsCode {
		t.Fatalf("READ_RESULT header should be metadata, got code row %#v", rows[0])
	}
	if !rows[1].IsCode || rows[1].LineNo != "41" || rows[1].Content != "issue,label" {
		t.Fatalf("first source row = %#v, want line 41 issue,label", rows[1])
	}
	if !rows[2].IsCode || rows[2].LineNo != "42" || rows[2].Content != `"a","b"` {
		t.Fatalf("second source row = %#v, want line 42 quoted row", rows[2])
	}
	if source != "issue,label\n\"a\",\"b\"" {
		t.Fatalf("source = %q, want raw content without header", source)
	}
}

func TestReadCallAlignsLineNumberGutterAcrossDigitWidths(t *testing.T) {
	block := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   "read",
		Content:    `{"path":"sample.go","limit":3}`,
		ResultDone: true,
		ResultContent: strings.Join([]string{
			"package main",
			"import \"fmt\"",
			"func main() {}",
		}, "\n"),
	}

	plainLines := strings.Split(stripANSI(strings.Join(block.renderReadCall(100, ""), "\n")), "\n")
	var cols []int
	for _, line := range plainLines {
		for _, marker := range []string{"package main", "import \"fmt\"", "func main() {}"} {
			if idx := strings.Index(line, marker); idx >= 0 {
				cols = append(cols, idx)
			}
		}
	}
	if len(cols) != 3 {
		t.Fatalf("expected three rendered source rows, got columns %v in:\n%s", cols, strings.Join(plainLines, "\n"))
	}
	if cols[0] != cols[1] || cols[1] != cols[2] {
		t.Fatalf("source columns should align across line-number digit widths, got %v in:\n%s", cols, strings.Join(plainLines, "\n"))
	}
}

func TestReadCallUsesCompactLineNumberGutterForSameDigitWidth(t *testing.T) {
	twoDigitBlock := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   "read",
		Content:    `{"path":"sample.go","limit":2,"offset":9}`,
		ResultDone: true,
		ResultContent: strings.Join([]string{
			"alpha",
			"beta",
		}, "\n"),
	}
	threeDigitBlock := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   "read",
		Content:    `{"path":"sample.go","limit":2,"offset":98}`,
		ResultDone: true,
		ResultContent: strings.Join([]string{
			"alpha",
			"beta",
		}, "\n"),
	}

	twoDigitLines := strings.Split(stripANSI(strings.Join(twoDigitBlock.renderReadCall(100, ""), "\n")), "\n")
	threeDigitLines := strings.Split(stripANSI(strings.Join(threeDigitBlock.renderReadCall(100, ""), "\n")), "\n")
	twoDigitCol := renderedMarkerColumn(twoDigitLines, "alpha")
	threeDigitCol := renderedMarkerColumn(threeDigitLines, "alpha")
	if twoDigitCol < 0 || threeDigitCol < 0 {
		t.Fatalf("expected rendered source rows, got two-digit=%d three-digit=%d\ntwo-digit:\n%s\nthree-digit:\n%s", twoDigitCol, threeDigitCol, strings.Join(twoDigitLines, "\n"), strings.Join(threeDigitLines, "\n"))
	}
	if got, want := threeDigitCol-twoDigitCol, 1; got != want {
		t.Fatalf("two-digit gutter should not reserve a third digit, column delta=%d want %d\ntwo-digit:\n%s\nthree-digit:\n%s", got, want, strings.Join(twoDigitLines, "\n"), strings.Join(threeDigitLines, "\n"))
	}
}

func TestReadCallLineNumberGutterIgnoresRowsBeyondRenderLimit(t *testing.T) {
	lines := make([]string, 1000)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i+1)
	}
	block := &Block{
		ID:                  1,
		Type:                BlockToolCall,
		ToolName:            "read",
		Content:             `{"path":"sample.go"}`,
		ResultDone:          true,
		ReadContentExpanded: true,
		ResultContent:       strings.Join(lines, "\n"),
	}

	plainLines := strings.Split(stripANSI(strings.Join(block.renderReadCall(100, ""), "\n")), "\n")
	line1Col := renderedMarkerColumn(plainLines, "line 1")
	line200Col := renderedMarkerColumn(plainLines, "line 200")
	if line1Col < 0 || line200Col < 0 {
		t.Fatalf("expected visible rows 1 and 200, got columns %d/%d in:\n%s", line1Col, line200Col, strings.Join(plainLines, "\n"))
	}
	if line1Col != line200Col {
		t.Fatalf("visible rows should align, got line 1 column %d and line 200 column %d in:\n%s", line1Col, line200Col, strings.Join(plainLines, "\n"))
	}
	if line1000Col := renderedMarkerColumn(plainLines, "line 1000"); line1000Col >= 0 {
		t.Fatalf("line 1000 should be beyond the render limit, got column %d in:\n%s", line1000Col, strings.Join(plainLines, "\n"))
	}
	if got, want := line1Col-renderedMarkerColumn(plainLines, "1  line 1"), len("1  "); got != want {
		t.Fatalf("gutter should be based on visible max line 200, not hidden line 1000; got separator offset %d want %d in:\n%s", got, want, strings.Join(plainLines, "\n"))
	}
}

func renderedMarkerColumn(lines []string, marker string) int {
	for _, line := range lines {
		if idx := strings.Index(line, marker); idx >= 0 {
			return idx
		}
	}
	return -1
}

func TestBashResultEscapesANSIRichOutput(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                `{"command":"printf test"}`,
		ResultContent:          "\x1b[31mred\x1b[0m\nplain",
		ResultDone:             true,
		ToolCallDetailExpanded: true,
	}

	joined := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if strings.ContainsRune(joined, '\x1b') {
		t.Fatalf("expected rendered Shell card to not contain raw ESC: %q", joined)
	}
	if !strings.Contains(joined, `\x1b[31mred\x1b[0m`) {
		t.Fatalf("expected shell output to show ANSI literally, got:\n%s", joined)
	}
	if !strings.Contains(joined, "plain") {
		t.Fatalf("expected shell output to keep plain text, got:\n%s", joined)
	}
}

func TestQuestionFallbackResultEscapesANSIRichOutput(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "question",
		Content:       `{"questions":[{"header":"mode","question":"Pick one"}]}`,
		ResultDone:    true,
		ResultContent: "\x1b[35muser typed answer\x1b[0m",
	}

	joined := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if strings.ContainsRune(joined, '\x1b') {
		t.Fatalf("expected question card to not contain raw ESC: %q", joined)
	}
	if !strings.Contains(joined, `\x1b[35muser typed answer\x1b[0m`) {
		t.Fatalf("expected question fallback result to show ANSI literally, got:\n%s", joined)
	}
}

func TestNotifyExpandedEscapesANSIRichMessageAndResult(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "notify",
		Collapsed:              false,
		Content:                `{"target_task_id":"adhoc-5","message":"\u001b[36mreply now\u001b[0m","kind":"reply"}`,
		ResultContent:          "\x1b[33mdelivered\x1b[0m",
		ResultDone:             true,
		ToolCallDetailExpanded: true,
	}

	joined := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if strings.ContainsRune(joined, '\x1b') {
		t.Fatalf("expected notify card to not contain raw ESC: %q", joined)
	}
	for _, want := range []string{`\x1b[36mreply now\x1b[0m`, `\x1b[33mdelivered\x1b[0m`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected notify card to contain %q, got:\n%s", want, joined)
		}
	}
}

func TestRenderQuestionDialogEscapesANSIRichPromptAndDescriptions(t *testing.T) {
	m := NewModel(nil)
	m.width = 100
	m.theme = DefaultTheme()
	m.question.request = &QuestionRequest{Questions: []tools.QuestionItem{{
		Header:   "\u001b[31munsafe\u001b[0m",
		Question: "line1\n\u001b[32mline2\u001b[0m",
		Options:  []tools.QuestionOption{{Label: "one", Description: "\u001b[34mdesc\u001b[0m"}},
	}}}
	m.question.input = newQuestionTextarea(m.width)

	view := stripANSI(m.renderQuestionDialog())
	if strings.ContainsRune(view, '\x1b') {
		t.Fatalf("expected question dialog to not contain raw ESC: %q", view)
	}
	for _, want := range []string{`\x1b[31munsafe\x1b[0m`, `\x1b[32mline2\x1b[0m`, `\x1b[34mdesc\x1b[0m`} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected question dialog to contain %q, got:\n%s", want, view)
		}
	}
}

func TestRenderConfirmFieldEscapesANSIRichLiteralValue(t *testing.T) {
	field := newConfirmLiteralField("Command", "\x1b[31mrm -rf /tmp/demo\x1b[0m", true)
	lines := renderConfirmField(field, 60, true)
	joined := stripANSI(strings.Join(lines, "\n"))
	if strings.ContainsRune(joined, '\x1b') {
		t.Fatalf("expected confirm field to not contain raw ESC: %q", joined)
	}
	if !strings.Contains(joined, `\x1b[31mrm -rf /tmp/demo\x1b[0m`) {
		t.Fatalf("expected confirm field to show ANSI literally, got:\n%s", joined)
	}
}

func TestTerminalResultEscapesANSIRichOutput(t *testing.T) {
	block := &Block{
		ID:                   1,
		Type:                 BlockUser,
		UserLocalShellCmd:    "printf demo",
		UserLocalShellResult: "\x1b[32mok\x1b[0m",
		Collapsed:            false,
	}
	joined := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if strings.ContainsRune(joined, '\x1b') {
		t.Fatalf("expected terminal card to not contain raw ESC: %q", joined)
	}
	if !strings.Contains(joined, `\x1b[32mok\x1b[0m`) {
		t.Fatalf("expected terminal result to show ANSI literally, got:\n%s", joined)
	}
}

func TestSanitizeToolDisplayTextEscapesBareCarriageReturnLiterally(t *testing.T) {
	got := sanitizeToolDisplayText("progress 10%\rprogress 90%")
	if containsRawCarriageReturnForTest(got) {
		t.Fatalf("expected sanitized text to not contain raw carriage return: %q", got)
	}
	if !strings.Contains(got, `progress 10%\rprogress 90%`) {
		t.Fatalf("expected bare carriage return to display literally, got %q", got)
	}
}

func TestSanitizeToolDisplayTextPreservesCRLFAsLogicalNewline(t *testing.T) {
	got := sanitizeToolDisplayText("line1\r\nline2")
	if containsRawCarriageReturnForTest(got) {
		t.Fatalf("expected CRLF sanitization to not contain raw carriage return: %q", got)
	}
	if got != "line1\nline2" {
		t.Fatalf("sanitizeToolDisplayText(CRLF) = %q, want %q", got, "line1\nline2")
	}
}

func TestCollapsedSummaryEscapesBareCarriageReturnLiterally(t *testing.T) {
	var lines []string
	appendCollapsedSummaryLines(&lines, "phase 1\rphase 2", 80, ToolResultStyle)
	joined := stripANSI(strings.Join(lines, "\n"))
	if containsRawCarriageReturnForTest(joined) {
		t.Fatalf("expected collapsed summary to not contain raw carriage return: %q", joined)
	}
	if !strings.Contains(joined, `phase 1\rphase 2`) {
		t.Fatalf("expected collapsed summary to show bare carriage return literally, got:\n%s", joined)
	}
}

func TestCancelSubAgentCollapsedDoesNotShowRawJSON(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "cancel",
		Collapsed:              true,
		Content:                `{"target_task_id":"adhoc-7","reason":"task superseded"}`,
		ResultContent:          `{"status":"stopped","task_id":"adhoc-7","agent_id":"reviewer-2"}`,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(90, ""), "\n"))
	if strings.Contains(joined, `"status"`) || strings.Contains(joined, `"target_task_id"`) {
		t.Fatalf("expected cancel collapsed view to not show raw JSON; got:\n%s", joined)
	}
	if !strings.Contains(joined, "cancel") {
		t.Fatalf("expected cancel header to show tool name; got:\n%s", joined)
	}
	if !strings.Contains(joined, "7") {
		t.Fatalf("expected cancel to show readable target (7 not adhoc-7); got:\n%s", joined)
	}
	if strings.Contains(joined, "adhoc-7") {
		t.Fatalf("expected cancel collapsed view to not expose adhoc- prefix; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Stopped") {
		t.Fatalf("expected cancel to show stopped semantic; got:\n%s", joined)
	}
}

func TestCancelSubAgentExpandedShowsStructuredDetails(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "cancel",
		Collapsed:              false,
		Content:                `{"target_task_id":"adhoc-7","reason":"task superseded"}`,
		ResultContent:          `{"status":"stopped","task_id":"adhoc-7","agent_id":"reviewer-2"}`,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(90, ""), "\n"))
	if strings.Contains(joined, `"status"`) || strings.Contains(joined, `"target_task_id"`) {
		t.Fatalf("expected cancel expanded view to not show raw JSON; got:\n%s", joined)
	}
	if !strings.Contains(joined, "cancel") {
		t.Fatalf("expected cancel header to show tool name; got:\n%s", joined)
	}
	if !strings.Contains(joined, "reason:") || !strings.Contains(joined, "task superseded") {
		t.Fatalf("expected cancel expanded to show reason; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Stopped") {
		t.Fatalf("expected cancel to show stopped semantic; got:\n%s", joined)
	}
}

func TestCancelSubAgentShowsCancelledSemantic(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "cancel",
		Collapsed:              true,
		Content:                `{"target_task_id":"adhoc-3","reason":"user requested"}`,
		ResultContent:          `{"status":"cancelled","task_id":"adhoc-3","agent_id":"worker-1"}`,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(90, ""), "\n"))
	if !strings.Contains(joined, "Cancelled") {
		t.Fatalf("expected cancel to show cancelled semantic; got:\n%s", joined)
	}
}

func TestNotifySubAgentCollapsedDoesNotShowRawJSON(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "notify",
		Collapsed:              true,
		Content:                `{"target_task_id":"adhoc-5","message":"continue with option B","kind":"reply"}`,
		ResultContent:          `{"status":"delivered","task_id":"adhoc-5","agent_id":"reviewer-2"}`,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(90, ""), "\n"))
	if strings.Contains(joined, `"status"`) || strings.Contains(joined, `"target_task_id"`) {
		t.Fatalf("expected notify collapsed view to not show raw JSON; got:\n%s", joined)
	}
	if !strings.Contains(joined, "notify") {
		t.Fatalf("expected notify header to show tool name; got:\n%s", joined)
	}
	if !strings.Contains(joined, "5") {
		t.Fatalf("expected notify to show readable target (5 not adhoc-5); got:\n%s", joined)
	}
	if strings.Contains(joined, "adhoc-5") {
		t.Fatalf("expected notify collapsed view to not expose adhoc- prefix; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Delivered") {
		t.Fatalf("expected notify to show delivered semantic; got:\n%s", joined)
	}
	if !strings.Contains(joined, "reply") {
		t.Fatalf("expected notify to show kind; got:\n%s", joined)
	}
}

func TestNotifySubAgentExpandedShowsStructuredDetails(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "notify",
		Collapsed:              false,
		Content:                `{"target_task_id":"adhoc-5","message":"continue with option B","kind":"reply"}`,
		ResultContent:          `{"status":"delivered","task_id":"adhoc-5","agent_id":"reviewer-2"}`,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(90, ""), "\n"))
	if strings.Contains(joined, `"status"`) || strings.Contains(joined, `"target_task_id"`) {
		t.Fatalf("expected notify expanded view to not show raw JSON; got:\n%s", joined)
	}
	if !strings.Contains(joined, "notify") {
		t.Fatalf("expected notify header to show tool name; got:\n%s", joined)
	}
	if !strings.Contains(joined, "message:") || !strings.Contains(joined, "continue with option B") {
		t.Fatalf("expected notify expanded to show message; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Delivered") {
		t.Fatalf("expected notify to show delivered semantic; got:\n%s", joined)
	}
}

func TestNotifySubAgentShowsQueuedSemantic(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                         1,
		Type:                       BlockToolCall,
		ToolName:                   "notify",
		Collapsed:                  true,
		Content:                    `{"target_task_id":"adhoc-slot","message":"continue","kind":"follow_up"}`,
		ToolExecutionState:         agent.ToolCallExecutionStateQueued,
		ToolQueuedByExecutionEvent: true,
	}

	joined := stripANSI(strings.Join(block.Render(90, ""), "\n"))
	if !strings.Contains(joined, "Queued") {
		t.Fatalf("expected notify queued header badge; got:\n%s", joined)
	}
}

func TestCancelToolShowsQueuedHeaderBadge(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                         1,
		Type:                       BlockToolCall,
		ToolName:                   "cancel",
		Collapsed:                  true,
		Content:                    `{"target_task_id":"adhoc-7","reason":"stopped"}`,
		ToolExecutionState:         agent.ToolCallExecutionStateQueued,
		ToolQueuedByExecutionEvent: true,
	}

	joined := stripANSI(strings.Join(block.Render(90, ""), "\n"))
	if !strings.Contains(joined, "Queued") {
		t.Fatalf("expected cancel queued header badge; got:\n%s", joined)
	}
}

func TestQuestionToolShowsQueuedHeaderBadge(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                         1,
		Type:                       BlockToolCall,
		ToolName:                   "question",
		Collapsed:                  false,
		Content:                    `{"questions":[{"header":"Confirm","question":"Continue?","multiple":false,"options":[{"label":"Continue","description":"Continue execution"}]}]}`,
		ToolExecutionState:         agent.ToolCallExecutionStateQueued,
		ToolQueuedByExecutionEvent: true,
	}

	joined := stripANSI(strings.Join(block.Render(90, ""), "\n"))
	if !strings.Contains(joined, "Queued") {
		t.Fatalf("expected question queued header badge; got:\n%s", joined)
	}
}

func TestControlToolsCollapsedDoNotExposeAdhocTaskID(t *testing.T) {
	ApplyTheme(DefaultTheme())
	tests := []struct {
		name          string
		toolName      string
		content       string
		resultContent string
	}{
		{
			name:          "cancel",
			toolName:      "cancel",
			content:       `{"target_task_id":"adhoc-7","reason":"stopped"}`,
			resultContent: `{"status":"stopped","task_id":"adhoc-7"}`,
		},
		{
			name:          "notify",
			toolName:      "notify",
			content:       `{"target_task_id":"adhoc-5","message":"continue","kind":"reply"}`,
			resultContent: `{"status":"delivered","task_id":"adhoc-5"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := &Block{
				ID:                     1,
				Type:                   BlockToolCall,
				ToolName:               tt.toolName,
				Collapsed:              true,
				Content:                tt.content,
				ResultContent:          tt.resultContent,
				ResultDone:             true,
				ToolCallDetailExpanded: false,
			}

			joined := stripANSI(strings.Join(block.Render(90, ""), "\n"))
			if strings.Contains(joined, "adhoc-") {
				t.Fatalf("expected %s collapsed view to not expose adhoc- prefix; got:\n%s", tt.toolName, joined)
			}
		})
	}
}

func TestRenderTodoCall_EmptyList(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   "todo_write",
		Content:    `{"todos":[]}`,
		ResultDone: true,
		Collapsed:  false,
		Streaming:  false,
	}
	lines := block.Render(80, "")
	plain := make([]string, len(lines))
	for i, line := range lines {
		plain[i] = stripANSI(line)
	}
	joined := strings.Join(plain, "\n")
	// Empty list should not prominently show "(no items)"
	if strings.Contains(joined, "(no items)") {
		t.Fatalf("empty TodoWrite should not show '(no items)': %q", joined)
	}
	// Should still show the tool header
	if !strings.Contains(joined, "todo_write") {
		t.Fatalf("missing tool header: %q", joined)
	}
}

func TestRenderTodoCall_EmptyListCollapsed(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   "todo_write",
		Content:    `{"todos":[]}`,
		ResultDone: true,
		Collapsed:  true,
		Streaming:  false,
	}
	lines := block.Render(80, "")
	plain := make([]string, len(lines))
	for i, line := range lines {
		plain[i] = stripANSI(line)
	}
	joined := strings.Join(plain, "\n")
	if strings.Contains(joined, "(no items)") {
		t.Fatalf("collapsed empty TodoWrite should not show '(no items)': %q", joined)
	}
}

func TestRenderTodoCall_WithItems(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   "todo_write",
		Content:    `{"todos":[{"content":"Task 1","status":"completed"},{"content":"Task 2","status":"pending"}]}`,
		ResultDone: true,
		Collapsed:  false,
		Streaming:  false,
	}
	lines := block.Render(80, "")
	plain := make([]string, len(lines))
	for i, line := range lines {
		plain[i] = stripANSI(line)
	}
	joined := strings.Join(plain, "\n")
	if !strings.Contains(joined, "Task 1") {
		t.Fatalf("missing Task 1: %q", joined)
	}
	if !strings.Contains(joined, "Task 2") {
		t.Fatalf("missing Task 2: %q", joined)
	}
}

func TestToolResultTableLikeContentUsesPlainText(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "web_fetch",
		Content:                `{"url":"https://example.com"}`,
		ResultDone:             true,
		ResultStatus:           agent.ToolResultStatusSuccess,
		ToolCallDetailExpanded: true,
		ResultContent:          "The report shows 1 expired account email entry:\n\n| Account ID | Email | Expiration Date |\n|------------|-------|-----------------|\n| a20158b8-... | gfwgfwgfwgfw@gmail.com | 2026-04-02 |\n\nThese accounts need to sign in again.",
	}

	joinedPlain := stripANSI(stripOSC8ToolTest(strings.Join(block.Render(100, ""), "\n")))
	for _, want := range []string{"| Account ID | Email | Expiration Date |", "gfwgfwgfwgfw@gmail.com", "These accounts need to sign in again."} {
		if !strings.Contains(joinedPlain, want) {
			t.Fatalf("expected tool result to keep table-like content literal (%q), got %q", want, joinedPlain)
		}
	}
}

func TestTaskDoneSummaryUsesPlainTextWhenExpanded(t *testing.T) {
	ApplyTheme(DefaultTheme())
	doneSummary := "## Findings\n- item one\n- item two\n\n```go\nfmt.Println(\"ok\")\n```"
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "delegate",
		Content:                `{"description":"Investigate task-complete rendering","agent_type":"reviewer"}`,
		ResultContent:          `{"status":"started","task_id":"adhoc-7","agent_id":"reviewer-2","message":"running in background"}`,
		ResultDone:             true,
		ToolCallDetailExpanded: true,
		DoneSummary:            doneSummary,
	}

	joined := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(joined, "Completed:") {
		t.Fatalf("expected Completed section, got:\n%s", joined)
	}
	for _, want := range []string{"## Findings", "- item one", "- item two", "```go", `fmt.Println("ok")`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected done summary to keep literal markdown-looking text %q, got:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "• item one") {
		t.Fatalf("done summary should not render markdown bullets, got:\n%s", joined)
	}
}
