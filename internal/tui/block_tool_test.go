package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/keakon/chord/internal/agent"
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
		ToolName:          "Read",
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
		ToolName:          "Write",
		Content:           fmt.Sprintf(`{"path":%q,"content":"hello"}`, abs),
		Collapsed:         true,
		ResultDone:        true,
		displayWorkingDir: wd,
	}
	joined := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(joined, "Write demo.txt") {
		t.Fatalf("expected Write header to show relative path; got:\n%s", joined)
	}
	if strings.Contains(joined, abs) {
		t.Fatalf("did not expect Write header to show absolute path; got:\n%s", joined)
	}
}

func TestReadHeaderKeepsAbsolutePathOutsideWorkingDir(t *testing.T) {
	wd := filepath.Join(string(os.PathSeparator), "tmp", "workspace")
	abs := filepath.Join(string(os.PathSeparator), "tmp", "other", "demo.txt")
	block := &Block{
		ID:                1,
		Type:              BlockToolCall,
		ToolName:          "Read",
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
		ToolName:      "Skill",
		Content:       `{"name":"sample-food-skill","result":"<path>` + filepath.ToSlash(filepath.Join(wd, ".agents", "skills", "sample-food-skill", "SKILL.md")) + `</path>"}`,
		ResultContent: filepath.ToSlash(filepath.Join(wd, ".agents", "skills", "sample-food-skill", "SKILL.md")),
		ResultDone:    true,
		Collapsed:     true,
	}

	joined := stripANSI(strings.Join(block.Render(140, ""), "\n"))
	if !strings.Contains(joined, "Skill sample-food-skill (from .agents)") {
		t.Fatalf("expected skill header to show name with source metadata, got:\n%s", joined)
	}
	if strings.Contains(joined, "sample-food-…") {
		t.Fatalf("skill name should not be truncated before source metadata, got:\n%s", joined)
	}
	if strings.Contains(joined, "name:") {
		t.Fatalf("collapsed skill header should not repeat a name param line, got:\n%s", joined)
	}
}

func TestCompactToolCardMarkdownHeadingKeepsCardBackgroundAcrossTrailingSpaces(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "Skill",
		Content:                `{"name":"skill-creator","result":"<path>/tmp/skills/skill-creator/SKILL.md</path>"}`,
		ResultContent:          "<skill>\n<name>skill-creator</name>\n<path>/tmp/skills/skill-creator/SKILL.md</path>\n<root>/tmp/skills/skill-creator</root>\n\n# Skill Creator\n\n- Step one\n- Step two\n</skill>",
		ResultDone:             true,
		ToolCallDetailExpanded: true,
	}

	lines := block.Render(120, "")
	if len(lines) == 0 {
		t.Fatal("expected rendered tool block")
	}

	var target string
	for _, line := range lines {
		plain := stripANSI(line)
		if strings.Contains(plain, "Skill Creator") {
			target = line
			break
		}
	}
	if target == "" {
		t.Fatalf("failed to locate markdown heading line in render output: %q", strings.Join(lines, "\n"))
	}

	buf := newScreenBuffer(ansi.StringWidth(target), 1)
	uv.NewStyledString(target).Draw(buf, buf.Bounds())
	cells := buf.Line(0)
	if len(cells) == 0 {
		t.Fatal("expected rendered cells")
	}

	cardBg := lipgloss.Color(currentTheme.ToolCallBg)
	seenHeading := false
	trailingCardBgSpaces := 0
	for _, cell := range cells {
		if cell.IsZero() || cell.Content == "" {
			continue
		}
		if cell.Content == "S" {
			seenHeading = true
			continue
		}
		if seenHeading && cell.Content == " " {
			if !colorsEqual(cell.Style.Bg, cardBg) {
				t.Fatalf("trailing space background = %v, want card bg %v", cell.Style.Bg, cardBg)
			}
			trailingCardBgSpaces++
		}
	}
	if !seenHeading {
		t.Fatal("expected to observe markdown heading cells")
	}
	if trailingCardBgSpaces == 0 {
		t.Fatal("expected to observe trailing spaces after markdown heading")
	}
}

func TestCompactToolCardMarkdownKeepsCardBackgroundWithoutPerLinePadding(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "Skill",
		Content:                `{"name":"skill-creator","result":"<path>/tmp/skills/skill-creator/SKILL.md</path>"}`,
		ResultContent:          "<skill>\n<name>skill-creator</name>\n<path>/tmp/skills/skill-creator/SKILL.md</path>\n<root>/tmp/skills/skill-creator</root>\n\n# Skill Creator\n\n- Step one\n- Step two\n</skill>",
		ResultDone:             true,
		ToolCallDetailExpanded: true,
	}

	lines := block.Render(120, "")
	if len(lines) == 0 {
		t.Fatal("expected rendered tool block")
	}

	var target string
	for _, line := range lines {
		plain := stripANSI(line)
		if strings.Contains(plain, "Skill Creator") {
			target = line
			break
		}
	}
	if target == "" {
		t.Fatalf("failed to locate markdown heading line in render output: %q", strings.Join(lines, "\n"))
	}

	buf := newScreenBuffer(ansi.StringWidth(target), 1)
	uv.NewStyledString(target).Draw(buf, buf.Bounds())
	cells := buf.Line(0)
	if len(cells) == 0 {
		t.Fatal("expected rendered cells")
	}

	cardBg := lipgloss.Color(currentTheme.ToolCallBg)
	seenHeading := false
	trailingCardBgSpaces := 0
	for _, cell := range cells {
		if cell.IsZero() || cell.Content == "" {
			continue
		}
		if cell.Content == "S" {
			seenHeading = true
		}
		if seenHeading && cell.Content == " " && colorsEqual(cell.Style.Bg, cardBg) {
			trailingCardBgSpaces++
		}
	}
	if !seenHeading {
		t.Fatal("expected to observe markdown heading cells")
	}
	if trailingCardBgSpaces == 0 {
		t.Fatal("expected to observe card-background padding after markdown heading")
	}
}

func TestOrdinaryToolResultDoesNotUseRichMarkdownFencedCode(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		Type:                   BlockToolCall,
		ToolName:               "WebFetch",
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

func TestCollapsedBashToolShowsExpandHintForHiddenOutput(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "Bash",
		Content:                `{"command":"printf 'one\ntwo\nthree\n'","description":"show lines"}`,
		ResultContent:          "one\ntwo\nthree",
		ResultDone:             true,
		Collapsed:              true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	// Short output (3 lines ≤ threshold): shown inline, no expand hint.
	if strings.Contains(joined, "[space] expand") {
		t.Fatalf("did not expect collapsed Bash with short output to show expand hint; got:\n%s", joined)
	}
	if !strings.Contains(joined, "one") || !strings.Contains(joined, "two") || !strings.Contains(joined, "three") {
		t.Fatalf("expected all output lines to be shown inline for short output; got:\n%s", joined)
	}
}

func TestCollapsedBashDoesNotDuplicateDescriptionInSummary(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "Bash",
		Content:                `{"command":"ls /tmp","description":"List temp files"}`,
		ResultContent:          "file1\nfile2\nfile3",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	// Description "List temp files" should appear in the header line, not
	// repeated in the summary line.
	headerIdx := strings.Index(joined, "List temp files")
	if headerIdx < 0 {
		t.Fatalf("expected description in header; got:\n%s", joined)
	}
	// Check it does not appear a second time after the header.
	remainder := joined[headerIdx+len("List temp files"):]
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
		ToolName:               "Bash",
		Content:                `{"command":"cat big.txt","description":"show file"}`,
		ResultContent:          strings.Join(lines, "\n"),
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "[space] expand") {
		t.Fatalf("expected collapsed Bash with long output to show expand hint; got:\n%s", joined)
	}
	if strings.Contains(joined, "line 8") {
		t.Fatalf("did not expect collapsed Bash with long output to reveal all lines; got:\n%s", joined)
	}
}

func TestToolProgressRendersConsistentlyAcrossCollapsedAndExpandedStates(t *testing.T) {
	block := &Block{
		ID:                 1,
		Type:               BlockToolCall,
		ToolName:           "Delete",
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
		ToolName:   "Bash",
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
		ToolName:               "Bash",
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

	if !strings.Contains(joined, "Bash echo first (timeout=120)") {
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
		ToolName:               "Bash",
		Content:                fmt.Sprintf(`{"command":%q,"description":%q,"timeout":120}`, cmd, "Search existing permission-related tests"),
		ResultContent:          "ok",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(joined, "Bash Search existing permission-related tests (timeout=120)") {
		t.Fatalf("expected collapsed multiline Bash header to use description; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Command:") || !strings.Contains(joined, "python3 - <<'PY'") {
		t.Fatalf("expected collapsed multiline Bash to show command preview block; got:\n%s", joined)
	}
}

func TestCollapsedBashShowsCappedForegroundTimeoutInHeader(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "Bash",
		Content:                `{"command":"sleep 1","timeout":2400}`,
		ResultContent:          "ok",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(joined, "Bash sleep 1 (timeout=2400→600)") {
		t.Fatalf("expected collapsed Bash header to show requested and effective capped foreground timeout; got:\n%s", joined)
	}
}

func TestCollapsedBashMultilineWithoutDescriptionFallsBackToCommand(t *testing.T) {
	cmd := "python3 - <<'PY'\nprint('ok')\nPY"
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "Bash",
		Content:                fmt.Sprintf(`{"command":%q,"description":"   ","timeout":120}`, cmd),
		ResultContent:          "ok",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(joined, "Bash python3 - <<'PY' (timeout=120)") {
		t.Fatalf("expected collapsed multiline Bash header to fall back to command first line; got:\n%s", joined)
	}
}

func TestExpandedBashMultilineKeepsCommandHeaderEvenWithDescription(t *testing.T) {
	cmd := "python3 - <<'PY'\nfrom pathlib import Path\nprint('ok')\nPY"
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "Bash",
		Content:                fmt.Sprintf(`{"command":%q,"description":%q,"timeout":120}`, cmd, "Search existing permission-related tests"),
		ResultContent:          "ok",
		ResultDone:             true,
		ToolCallDetailExpanded: true,
	}

	joined := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(joined, "Bash Search existing permission-related tests (timeout=120)") {
		t.Fatalf("expected expanded Bash header to use summary description; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Command:") || !strings.Contains(joined, "python3 - <<'PY'") || !strings.Contains(joined, "from pathlib import Path") || !strings.Contains(joined, "print('ok')") {
		t.Fatalf("expected expanded Bash to show full command block; got:\n%s", joined)
	}
	if !strings.Contains(joined, "description: Search existing permission-related tests") {
		t.Fatalf("expected expanded Bash detail lines to still include description metadata; got:\n%s", joined)
	}
}

func TestExpandedBashShowsIndentedContinuationLines(t *testing.T) {
	cmd := "echo first\necho second\n  pwd"
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "Bash",
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
		t.Fatalf("expected expanded Bash view to include continuation lines; got:\n%s", strings.Join(plain, "\n"))
	}
	if !foundIndentedContinuation {
		t.Fatalf("expected expanded Bash continuation line to be indented; got:\n%s", strings.Join(plain, "\n"))
	}
	if !foundIndentedNested {
		t.Fatalf("expected expanded Bash nested continuation line to preserve extra indentation; got:\n%s", strings.Join(plain, "\n"))
	}
}

func TestCollapsedBashShowsCommandPreviewAndExpandHint(t *testing.T) {
	cmd := "echo first\necho second\necho third"
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "Bash",
		Content:                fmt.Sprintf(`{"command":%q,"timeout":120}`, cmd),
		ResultContent:          "ok",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(80, ""), "\n"))
	if !strings.Contains(joined, "Command:") || !strings.Contains(joined, "echo first") || !strings.Contains(joined, "echo second") {
		t.Fatalf("expected collapsed Bash to show command preview lines; got:\n%s", joined)
	}
	// Short output (1 line) triggers inline mode: full command is shown,
	// no expand hint is needed.
	if strings.Contains(joined, "[space] expand") {
		t.Fatalf("did not expect collapsed Bash with short output to show expand hint; got:\n%s", joined)
	}
	if !strings.Contains(joined, "echo third") {
		t.Fatalf("expected collapsed Bash with short output to show full command; got:\n%s", joined)
	}
	if !strings.Contains(joined, "ok") {
		t.Fatalf("expected collapsed Bash with short output to show result inline; got:\n%s", joined)
	}
}

func TestCollapsedBashShowsSingleExpandHintWhenCommandAndOutputBothHidden(t *testing.T) {
	cmd := "echo first\necho second\necho third"
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "Bash",
		Content:                fmt.Sprintf(`{"command":%q,"timeout":120}`, cmd),
		ResultContent:          "one\ntwo\nthree",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(80, ""), "\n"))
	// Short output (3 lines ≤ threshold): everything shown inline, no expand hint.
	if strings.Contains(joined, "[space] expand") {
		t.Fatalf("did not expect collapsed Bash with short output to show expand hint; got:\n%s", joined)
	}
	if !strings.Contains(joined, "echo third") {
		t.Fatalf("expected full command to be shown for short output; got:\n%s", joined)
	}
	if !strings.Contains(joined, "one") || !strings.Contains(joined, "two") || !strings.Contains(joined, "three") {
		t.Fatalf("expected all output lines to be shown inline for short output; got:\n%s", joined)
	}
}

func TestExpandedUserLocalShellMatchesBashContinuationFormatting(t *testing.T) {
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

	if !strings.Contains(joined, "Shell echo first") {
		t.Fatalf("expected local shell header to show first command line only; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Command:") {
		t.Fatalf("expected local shell to show command block label; got:\n%s", joined)
	}
	if !strings.Contains(joined, "      echo second") {
		t.Fatalf("expected local shell continuation line to be indented; got:\n%s", joined)
	}
	if !strings.Contains(joined, "        pwd") {
		t.Fatalf("expected local shell nested continuation indentation to be preserved; got:\n%s", joined)
	}
}

func TestCollapsedCompleteShowsSummaryPreviewInsteadOfFullBody(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "Complete",
		Content:                `{"summary":"Status: success\nChanges: line one\nline two\nline three\nline four\nline five\nline six\nline seven\nline eight\nline nine\nline ten\nline eleven\nline twelve"}`,
		ResultContent:          "Status: success\nChanges: line one\nline two\nline three\nline four\nline five\nline six\nline seven\nline eight\nline nine\nline ten\nline eleven\nline twelve",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(88, ""), "\n"))
	if !strings.Contains(joined, "Status: success · Changes: line one") {
		t.Fatalf("expected collapsed Complete to show summary preview; got:\n%s", joined)
	}
	if !strings.Contains(joined, "more lines · [space] expand") {
		t.Fatalf("expected collapsed Complete to show expand hint; got:\n%s", joined)
	}
	if strings.Contains(joined, "line twelve") {
		t.Fatalf("did not expect collapsed Complete to render full body; got:\n%s", joined)
	}
}

func TestQueuedToolHeaderShowsQueuedLabelWithoutSpinner(t *testing.T) {
	block := &Block{
		ID:                 1,
		Type:               BlockToolCall,
		ToolName:           "Bash",
		Content:            `{"command":"command -v benchstat || true"}`,
		Collapsed:          true,
		ResultDone:         false,
		ToolExecutionState: agent.ToolCallExecutionStateQueued,
	}

	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if !strings.Contains(joined, "⋯ Bash command -v benchstat || true") {
		t.Fatalf("expected queued Bash header; got:\n%s", joined)
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

func TestActiveToolHeaderKeepsToolNameStableWithoutCallingText(t *testing.T) {
	block := &Block{
		ID:                 1,
		Type:               BlockToolCall,
		ToolName:           "Bash",
		Content:            `{"command":"echo hi"}`,
		Collapsed:          true,
		ResultDone:         false,
		ToolExecutionState: agent.ToolCallExecutionStateRunning,
	}

	joined := stripANSI(strings.Join(block.Render(80, "●"), "\n"))
	if strings.Contains(joined, "calling") {
		t.Fatalf("active tool header should not render calling text; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Bash echo hi") {
		t.Fatalf("expected active tool header to keep stable tool name and summary; got:\n%s", joined)
	}
}

func TestQueuedToolHeaderBadgeKeepsRightPadding(t *testing.T) {
	line := renderQueuedToolHeaderBadge("  ▸ Bash", 24)
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
		ToolName:           "Write",
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

func TestCollapsedBashErrorShowsCrossPrefixAndRedOutput(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "Bash",
		Content:                `{"command":"false"}`,
		ResultContent:          "stdout\n\nError: exit code 1",
		ResultStatus:           agent.ToolResultStatusError,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	lines := block.Render(80, "")
	joinedANSI := strings.Join(lines, "\n")
	joinedPlain := stripANSI(joinedANSI)

	if !strings.Contains(joinedPlain, "✗▸ Bash false") {
		t.Fatalf("expected collapsed Bash error prefix; got:\n%s", joinedPlain)
	}
	if !strings.Contains(joinedPlain, "stdout") {
		t.Fatalf("expected preserved stdout in collapsed Bash error; got:\n%s", joinedPlain)
	}
	if !strings.Contains(joinedANSI, "\x1b[1;38;5;196m") && !strings.Contains(joinedANSI, "\x1b[38;5;196m") {
		t.Fatalf("expected error styling ANSI sequence; got:\n%q", joinedANSI)
	}
}

func TestExpandedBashErrorKeepsToolCardBackgroundAcrossWrappedErrorBody(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "Bash",
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
		t.Fatalf("did not find wrapped bash error body in rendered lines:\n%s", strings.Join(stripANSILines(lines), "\n"))
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
		t.Fatal("expected trailing padding spaces on bash error line")
	}
}

func TestDeleteHeaderShowsRelativePathInsideWorkingDir(t *testing.T) {
	wd := filepath.Join(string(os.PathSeparator), "tmp", "workspace")
	abs := filepath.Join(wd, "internal", "tui", "obsolete.go")
	block := &Block{
		ID:                1,
		Type:              BlockToolCall,
		ToolName:          "Delete",
		Content:           fmt.Sprintf(`{"paths":[%q],"reason":"remove obsolete file"}`, abs),
		ResultContent:     "Delete completed.\n\nDeleted (1):\n- internal/tui/obsolete.go",
		ResultDone:        true,
		displayWorkingDir: wd,
	}
	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "Delete internal/tui/obsolete.go") {
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
		ToolName:               "Delete",
		Content:                `{"paths":["internal/tui/obsolete.go"],"reason":"remove obsolete file"}`,
		ResultContent:          "Delete completed.\n\nDeleted (1):\n- internal/tui/obsolete.go",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "Delete internal/tui/obsolete.go") {
		t.Fatalf("expected delete header to show file path; got:\n%s", joined)
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
			toolName:    "Delete",
			content:     `{"paths":["examples/compression-config.yaml"],"reason":"remove obsolete example"}`,
			result:      "Delete completed.\n\nDeleted (1):\n- examples/compression-config.yaml",
			wantPrefix:  "✓▾ Delete",
			wantVisible: "• examples/compression-config.yaml",
		},
		{
			name:        "grep",
			toolName:    "Grep",
			content:     `{"pattern":"TODO"}`,
			result:      strings.Join([]string{"a.go:1:TODO", "b.go:2:TODO", "c.go:3:TODO", "d.go:4:TODO", "e.go:5:TODO", "f.go:6:TODO", "g.go:7:TODO", "h.go:8:TODO", "i.go:9:TODO", "j.go:10:TODO", "k.go:11:TODO"}, "\n"),
			wantPrefix:  "✓▾ Grep",
			wantVisible: "k.go:11:TODO",
		},
		{
			name:        "glob",
			toolName:    "Glob",
			content:     `{"pattern":"**/*.go"}`,
			result:      strings.Join([]string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go", "g.go", "h.go", "i.go", "j.go", "k.go"}, "\n"),
			wantPrefix:  "✓▾ Glob",
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
			if strings.Contains(joined, "[space] expand") || strings.Contains(joined, "1 more lines") {
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
		ToolName:               "WebFetch",
		Content:                `{"url":"https://iterm2.com/documentation-images.html","timeout":40}`,
		ResultContent:          "URL: https://iterm2.com/documentation-images.html\nContent-Type: text/html",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "WebFetch https://iterm2.com/documentation-images.html (timeout=40)") {
		t.Fatalf("expected WebFetch header to include URL and timeout; got:\n%s", joined)
	}
}

func TestCollapsedTaskShowsSpawnedSummary(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "Delegate",
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
	if !strings.Contains(joined, "Delegate (reviewer)") {
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
		ToolName:               "Delegate",
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
	if !strings.Contains(joined, "1 more lines · [space] expand") {
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
	if !strings.Contains(joined, "[space] expand") {
		t.Fatalf("expected expand hint to remain after deduplication, got:\n%s", joined)
	}
}

func TestExpandedTaskShowsDescriptionAndWorkerWithTaskID(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "Delegate",
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

func TestExpandedTaskDescriptionRendersMarkdown(t *testing.T) {
	ApplyTheme(DefaultTheme())
	desc := "## Plan\n- item one\n- item two\n\n```go\nfmt.Println(\"ok\")\n```"
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "Delegate",
		Content:                `{"description":` + strconv.Quote(desc) + `,"agent_type":"reviewer"}`,
		ResultContent:          `{"status":"started","task_id":"adhoc-7","agent_id":"reviewer-2"}`,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(joined, "Description:") {
		t.Fatalf("expected expanded Delegate to show description heading; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Plan") {
		t.Fatalf("expected markdown heading text in Delegate description; got:\n%s", joined)
	}
	if !strings.Contains(joined, "• item one") {
		t.Fatalf("expected markdown bullet rendering in Delegate description; got:\n%s", joined)
	}
	if !strings.Contains(joined, "fmt.Println(\"ok\")") {
		t.Fatalf("expected fenced code content in Delegate description; got:\n%s", joined)
	}
}

func TestGrepHeaderShowsRelativeSearchPathInsideWorkingDir(t *testing.T) {
	wd := filepath.Join(string(os.PathSeparator), "tmp", "workspace")
	abs := filepath.Join(wd, "internal", "tui")
	block := &Block{
		ID:                1,
		Type:              BlockToolCall,
		ToolName:          "Grep",
		Content:           fmt.Sprintf(`{"pattern":"TODO","path":%q,"glob":"*.go"}`, abs),
		ResultDone:        true,
		displayWorkingDir: wd,
	}
	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "Grep TODO (path=internal/tui, glob=*.go)") {
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
		ToolName:          "Glob",
		Content:           fmt.Sprintf(`{"pattern":"**/*.go","path":%q}`, abs),
		ResultDone:        true,
		displayWorkingDir: wd,
	}
	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "Glob **/*.go (path=internal)") {
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
		ToolName:               "Bash",
		Content:                fmt.Sprintf(`{"command":"pwd","workdir":%q}`, workdir),
		ResultDone:             true,
		ToolCallDetailExpanded: true,
		displayWorkingDir:      wd,
	}
	joined := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "workdir: internal/tui") && !strings.Contains(joined, "Workdir: internal/tui") {
		t.Fatalf("expected bash expanded body to show relative workdir; got:\n%s", joined)
	}
	if strings.Contains(joined, workdir) {
		t.Fatalf("did not expect bash body to show absolute workdir; got:\n%s", joined)
	}
}

func TestCollapsedBashShowsResultSummary(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "Bash",
		Content:                `{"command":"go test ./internal/tui/..."}`,
		ResultContent:          "ok\nsecond line",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(90, ""), "\n"))
	if !strings.Contains(joined, "ok") {
		t.Fatalf("expected Bash collapsed summary to show success output summary; got:\n%s", joined)
	}
	if strings.Contains(joined, "Passed · 2 lines output") {
		t.Fatalf("expected Bash collapsed summary to prefer output summary over legacy line-count summary; got:\n%s", joined)
	}
}

func TestCollapsedGrepShowsMatchCountSummary(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "Grep",
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
				ToolName:               "Grep",
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
		ToolName:      "Write",
		Content:       `{"path":"foo.txt","content":""}`,
		ResultContent: "Cancelled",
		ResultStatus:  agent.ToolResultStatusCancelled,
		ResultDone:    true,
	}

	plain := stripANSI(strings.Join(block.Render(80, ""), "\n"))
	if !strings.Contains(plain, "Write foo.txt") {
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

func TestCancelledEditCallSuppressesDiffPreviewAndDuplicateCancelledText(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "Edit",
		Content:       `{"path":"foo.txt","old_string":"a","new_string":"b"}`,
		Diff:          "@@ -1,1 +1,1 @@\n-a\n+b\n",
		ResultContent: "Cancelled",
		ResultStatus:  agent.ToolResultStatusCancelled,
		ResultDone:    true,
	}

	plain := stripANSI(strings.Join(block.Render(80, ""), "\n"))
	if !strings.Contains(plain, "Edit foo.txt") {
		t.Fatalf("expected Edit header to include file path, got:\n%s", plain)
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
		ToolName:      "Read",
		Content:       `{"path":"foo.txt","limit":10}`,
		ResultContent: "Cancelled",
		ResultStatus:  agent.ToolResultStatusCancelled,
		ResultDone:    true,
	}

	plain := stripANSI(strings.Join(block.Render(80, ""), "\n"))
	if !strings.Contains(plain, "Read foo.txt") {
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
		ToolName:               "Bash",
		Content:                `{"command":"echo hi"}`,
		ResultContent:          "Cancelled",
		ResultStatus:           agent.ToolResultStatusCancelled,
		ResultDone:             true,
		ToolCallDetailExpanded: true,
	}

	plain := stripANSI(strings.Join(block.Render(80, ""), "\n"))
	if !strings.Contains(plain, "Bash") {
		t.Fatalf("expected Bash header to remain visible, got:\n%s", plain)
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
		ToolName:      "Question",
		Content:       `{"questions":[{"header":"log","question":"paste log"}]}`,
		ResultContent: "Cancelled",
		ResultStatus:  agent.ToolResultStatusCancelled,
		ResultDone:    true,
	}

	plain := stripANSI(strings.Join(block.Render(80, ""), "\n"))
	if !strings.Contains(plain, "Question") {
		t.Fatalf("expected Question header to remain visible, got:\n%s", plain)
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
		ToolName:               "Glob",
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
				ToolName:               "Glob",
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
		ToolName: "Question",
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
	emailIdx := strings.Index(emailLine, "1.")
	chatIdx := strings.Index(chatLine, "2.")
	if emailIdx < 0 || chatIdx < 0 {
		t.Fatalf("expected option numbers in both lines, got:\n%s", plain)
	}
	if got, want := ansi.StringWidth(emailLine[:emailIdx]), ansi.StringWidth(chatLine[:chatIdx]); got != want {
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
		ToolName: "Question",
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
		ToolName:      "Question",
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
		ToolName:      "Question",
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
		ToolName:      "Question",
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

func TestReadCallRendersSingleBlankLineWithoutPanic(t *testing.T) {
	block := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   "Read",
		Content:    `{"path":"internal/tui/input.go","limit":1,"offset":358}`,
		ResultDone: true,
		ResultContent: strings.Join([]string{
			"   359\t",
		}, "\n"),
	}

	lines := block.renderReadCall(80, "")
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "Read internal/tui/input.go") {
		t.Fatalf("expected Read header to remain visible, got:\n%s", plain)
	}
	if !strings.Contains(plain, "359") {
		t.Fatalf("expected blank numbered line to render safely, got:\n%s", plain)
	}
	if strings.Contains(plain, "panic") {
		t.Fatalf("unexpected panic text in rendered output: %s", plain)
	}
}

func TestReadCallStripsTrailingCarriageReturnsFromPersistedOutput(t *testing.T) {
	block := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   "Read",
		Content:    `{"path":"sample.csv","limit":20}`,
		ResultDone: true,
		ResultContent: strings.Join([]string{
			"     1\tissue,label\r",
			"     2\t\"a\",\"b\"\r",
			"(showing lines 1-2 of 10)\r",
		}, "\n"),
	}

	rows, source := parseReadDisplayLines(block.ResultContent)
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
		t.Fatalf("rendered Read card should not contain raw carriage return: %q", plain)
	}
	for _, want := range []string{"issue,label", "\"a\",\"b\"", "(showing lines 1-2 of 10)"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected rendered Read card to contain %q, got:\n%s", want, plain)
		}
	}
}

func TestCancelSubAgentCollapsedDoesNotShowRawJSON(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "Cancel",
		Collapsed:              true,
		Content:                `{"target_task_id":"adhoc-7","reason":"task superseded"}`,
		ResultContent:          `{"status":"stopped","task_id":"adhoc-7","agent_id":"reviewer-2"}`,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(90, ""), "\n"))
	if strings.Contains(joined, `"status"`) || strings.Contains(joined, `"target_task_id"`) {
		t.Fatalf("expected Cancel collapsed view to not show raw JSON; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Cancel") {
		t.Fatalf("expected Cancel header to show tool name; got:\n%s", joined)
	}
	if !strings.Contains(joined, "7") {
		t.Fatalf("expected Cancel to show readable target (7 not adhoc-7); got:\n%s", joined)
	}
	if strings.Contains(joined, "adhoc-7") {
		t.Fatalf("expected Cancel collapsed view to not expose adhoc- prefix; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Stopped") {
		t.Fatalf("expected Cancel to show stopped semantic; got:\n%s", joined)
	}
}

func TestCancelSubAgentExpandedShowsStructuredDetails(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "Cancel",
		Collapsed:              false,
		Content:                `{"target_task_id":"adhoc-7","reason":"task superseded"}`,
		ResultContent:          `{"status":"stopped","task_id":"adhoc-7","agent_id":"reviewer-2"}`,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(90, ""), "\n"))
	if strings.Contains(joined, `"status"`) || strings.Contains(joined, `"target_task_id"`) {
		t.Fatalf("expected Cancel expanded view to not show raw JSON; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Cancel") {
		t.Fatalf("expected Cancel header to show tool name; got:\n%s", joined)
	}
	if !strings.Contains(joined, "reason:") || !strings.Contains(joined, "task superseded") {
		t.Fatalf("expected Cancel expanded to show reason; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Stopped") {
		t.Fatalf("expected Cancel to show stopped semantic; got:\n%s", joined)
	}
}

func TestCancelSubAgentShowsCancelledSemantic(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "Cancel",
		Collapsed:              true,
		Content:                `{"target_task_id":"adhoc-3","reason":"user requested"}`,
		ResultContent:          `{"status":"cancelled","task_id":"adhoc-3","agent_id":"worker-1"}`,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(90, ""), "\n"))
	if !strings.Contains(joined, "Cancelled") {
		t.Fatalf("expected Cancel to show cancelled semantic; got:\n%s", joined)
	}
}

func TestNotifySubAgentCollapsedDoesNotShowRawJSON(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "Notify",
		Collapsed:              true,
		Content:                `{"target_task_id":"adhoc-5","message":"continue with option B","kind":"reply"}`,
		ResultContent:          `{"status":"delivered","task_id":"adhoc-5","agent_id":"reviewer-2"}`,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(90, ""), "\n"))
	if strings.Contains(joined, `"status"`) || strings.Contains(joined, `"target_task_id"`) {
		t.Fatalf("expected Notify collapsed view to not show raw JSON; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Notify") {
		t.Fatalf("expected Notify header to show tool name; got:\n%s", joined)
	}
	if !strings.Contains(joined, "5") {
		t.Fatalf("expected Notify to show readable target (5 not adhoc-5); got:\n%s", joined)
	}
	if strings.Contains(joined, "adhoc-5") {
		t.Fatalf("expected Notify collapsed view to not expose adhoc- prefix; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Delivered") {
		t.Fatalf("expected Notify to show delivered semantic; got:\n%s", joined)
	}
	if !strings.Contains(joined, "reply") {
		t.Fatalf("expected Notify to show kind; got:\n%s", joined)
	}
}

func TestNotifySubAgentExpandedShowsStructuredDetails(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "Notify",
		Collapsed:              false,
		Content:                `{"target_task_id":"adhoc-5","message":"continue with option B","kind":"reply"}`,
		ResultContent:          `{"status":"delivered","task_id":"adhoc-5","agent_id":"reviewer-2"}`,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := stripANSI(strings.Join(block.Render(90, ""), "\n"))
	if strings.Contains(joined, `"status"`) || strings.Contains(joined, `"target_task_id"`) {
		t.Fatalf("expected Notify expanded view to not show raw JSON; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Notify") {
		t.Fatalf("expected Notify header to show tool name; got:\n%s", joined)
	}
	if !strings.Contains(joined, "message:") || !strings.Contains(joined, "continue with option B") {
		t.Fatalf("expected Notify expanded to show message; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Delivered") {
		t.Fatalf("expected Notify to show delivered semantic; got:\n%s", joined)
	}
}

func TestNotifySubAgentShowsQueuedSemantic(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                 1,
		Type:               BlockToolCall,
		ToolName:           "Notify",
		Collapsed:          true,
		Content:            `{"target_task_id":"adhoc-slot","message":"continue","kind":"follow_up"}`,
		ToolExecutionState: agent.ToolCallExecutionStateQueued,
	}

	joined := stripANSI(strings.Join(block.Render(90, ""), "\n"))
	if !strings.Contains(joined, "Queued") {
		t.Fatalf("expected Notify queued header badge; got:\n%s", joined)
	}
}

func TestCancelToolShowsQueuedHeaderBadge(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                 1,
		Type:               BlockToolCall,
		ToolName:           "Cancel",
		Collapsed:          true,
		Content:            `{"target_task_id":"adhoc-7","reason":"stopped"}`,
		ToolExecutionState: agent.ToolCallExecutionStateQueued,
	}

	joined := stripANSI(strings.Join(block.Render(90, ""), "\n"))
	if !strings.Contains(joined, "Queued") {
		t.Fatalf("expected Cancel queued header badge; got:\n%s", joined)
	}
}

func TestQuestionToolShowsQueuedHeaderBadge(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                 1,
		Type:               BlockToolCall,
		ToolName:           "Question",
		Collapsed:          false,
		Content:            `{"questions":[{"header":"确认","question":"继续吗？","multiple":false,"options":[{"label":"继续","description":"继续执行"}]}]}`,
		ToolExecutionState: agent.ToolCallExecutionStateQueued,
	}

	joined := stripANSI(strings.Join(block.Render(90, ""), "\n"))
	if !strings.Contains(joined, "Queued") {
		t.Fatalf("expected Question queued header badge; got:\n%s", joined)
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
			name:          "Cancel",
			toolName:      "Cancel",
			content:       `{"target_task_id":"adhoc-7","reason":"stopped"}`,
			resultContent: `{"status":"stopped","task_id":"adhoc-7"}`,
		},
		{
			name:          "Notify",
			toolName:      "Notify",
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
		ToolName:   "TodoWrite",
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
	if !strings.Contains(joined, "TodoWrite") {
		t.Fatalf("missing tool header: %q", joined)
	}
}

func TestRenderTodoCall_EmptyListCollapsed(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   "TodoWrite",
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
		ToolName:   "TodoWrite",
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

func TestToolResultMarkdownTableKeepsEmailLinksInline(t *testing.T) {
	ApplyTheme(DefaultTheme())
	resetMarkdownRenderer()
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "WebFetch",
		Content:                `{"url":"https://example.com"}`,
		ResultDone:             true,
		ResultStatus:           agent.ToolResultStatusSuccess,
		ToolCallDetailExpanded: true,
		ResultContent:          "The report shows 1 expired account email entry:\n\n| Account ID | Email | Expiration Date |\n|------------|-------|-----------------|\n| a20158b8-... | gfwgfwgfwgfw@gmail.com | 2026-04-02 |\n\nThese accounts need to sign in again.",
	}

	joinedANSI := strings.Join(block.Render(100, ""), "\n")
	if strings.Contains(joinedANSI, "[1]:") {
		t.Fatalf("expected tool result markdown table links to stay inline, got footer list:\n%s", joinedANSI)
	}
	joinedPlain := stripANSI(stripOSC8ToolTest(joinedANSI))
	if !strings.Contains(joinedPlain, "gfwgfwgfwgfw@gmail.com") {
		t.Fatalf("expected tool result table to keep email inside table, got %q", joinedPlain)
	}
	if !strings.Contains(joinedPlain, "These accounts need to sign in again") {
		t.Fatalf("expected trailing paragraph after tool result table, got %q", joinedPlain)
	}
}

func TestTaskDoneSummaryRendersMarkdownWhenExpanded(t *testing.T) {
	ApplyTheme(DefaultTheme())
	doneSummary := "## Findings\n- item one\n- item two\n\n```go\nfmt.Println(\"ok\")\n```"
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "Delegate",
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
	if !strings.Contains(joined, "Findings") {
		t.Fatalf("expected markdown heading text, got:\n%s", joined)
	}
	if !strings.Contains(joined, "• item one") {
		t.Fatalf("expected markdown bullet rendering, got:\n%s", joined)
	}
	if !strings.Contains(joined, "fmt.Println(\"ok\")") {
		t.Fatalf("expected fenced code content, got:\n%s", joined)
	}
}
