package tui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

var osc8Regex = regexp.MustCompile(`\x1b\]8;[^\x07\x1b]*(?:\x07|\x1b\\)`)

func stripOSC8(s string) string {
	return osc8Regex.ReplaceAllString(s, "")
}

func TestPreprocessThinkingMarkdown_GluedBoldSection(t *testing.T) {
	in := "That works properly too.**Planning image rendering**"
	got := preprocessThinkingMarkdown(in)
	if !strings.Contains(got, "too.\n\n**Planning") {
		t.Fatalf("expected paragraph break before glued ** header, got %q", got)
	}
	if strings.Contains(got, "too.**Planning") {
		t.Fatalf("glue should be split, got %q", got)
	}
}

func TestPreprocessThinkingMarkdown_PunctuationBeforeBold(t *testing.T) {
	in := "which is great!**Considering placements**"
	got := preprocessThinkingMarkdown(in)
	if strings.Contains(got, "great!**Considering") {
		t.Fatalf("expected break after !, got %q", got)
	}
	if !strings.Contains(got, "great!\n\n**Considering") {
		t.Fatalf("want newline before **, got %q", got)
	}
}

func TestPreprocessThinkingMarkdown_NoSplitInlineLowercaseBold(t *testing.T) {
	in := "use the **bold** emphasis here"
	got := preprocessThinkingMarkdown(in)
	if got != in {
		t.Fatalf("inline lowercase bold should stay intact, got %q", got)
	}
}

func TestPreprocessThinkingMarkdown_CJKHeaderStart(t *testing.T) {
	in := "Done.**Implementation notes**"
	got := preprocessThinkingMarkdown(in)
	if !strings.Contains(got, "\n\n**Implementation") {
		t.Fatalf("expected break before glued bold heading, got %q", got)
	}
}

func TestPreprocessThinkingMarkdown_AlreadySeparated(t *testing.T) {
	in := "**Planning**\n\nBody text."
	got := preprocessThinkingMarkdown(in)
	if got != in {
		t.Fatalf("well-formed markdown should be unchanged, got %q", got)
	}
}

func TestStyleRenderedThinkingLines_ParagraphTitles(t *testing.T) {
	mdLines := []string{"line-a", "", "line-b"}
	out := styleRenderedThinkingLines(mdLines)
	if len(out) != 3 {
		t.Fatalf("len=%d want 3", len(out))
	}
	if !strings.Contains(out[0], "line-a") {
		t.Fatalf("first line missing: %q", out[0])
	}
	if out[1] != "" {
		t.Fatalf("blank line preserved, got %q", out[1])
	}
	if !strings.Contains(out[2], "line-b") {
		t.Fatalf("second para first line: %q", out[2])
	}
}

func TestStyleRenderedThinkingLinesItalicPreservesCJKWidth(t *testing.T) {
	ApplyTheme(DefaultTheme())
	mdLines := []string{"Thinking...", "Validate the cache next"}
	out := styleRenderedThinkingLines(mdLines)
	if len(out) != len(mdLines) {
		t.Fatalf("len=%d want %d", len(out), len(mdLines))
	}
	for i, line := range out {
		plain := stripANSI(line)
		if !strings.Contains(plain, mdLines[i]) {
			t.Fatalf("plain line %d = %q, want to contain %q", i, plain, mdLines[i])
		}
		if got, want := ansi.StringWidth(plain), ansi.StringWidth("  "+mdLines[i]); got != want {
			t.Fatalf("line %d width = %d, want %d; plain=%q", i, got, want, plain)
		}
	}
}

func TestStyleRenderedThinkingLinesPreservesBodyItalicAcrossInlineMarkdownResets(t *testing.T) {
	ApplyTheme(DefaultTheme())
	bodyLines := renderMarkdownContent("Body with **bold** and `code` tail", 80)
	if len(bodyLines) != 1 {
		t.Fatalf("markdown line count=%d want 1", len(bodyLines))
	}
	styled := styleRenderedThinkingLines([]string{"Heading", bodyLines[0]})
	if len(styled) != 2 {
		t.Fatalf("len=%d want 2", len(styled))
	}

	buf := newScreenBuffer(ansi.StringWidth(stripANSI(styled[1])), 1)
	uv.NewStyledString(styled[1]).Draw(buf, buf.Bounds())
	line := buf.Line(0)

	var checkedLetters int
	var sawBold bool
	for _, cell := range line {
		if cell.IsZero() || cell.Content == "" || cell.Content == " " {
			continue
		}
		if strings.ContainsRune("Bodywithandtailcode", []rune(cell.Content)[0]) {
			checkedLetters++
			if cell.Style.Attrs&uv.AttrItalic == 0 {
				t.Fatalf("expected italic thinking body cell %q to retain italic attrs=%08b", cell.Content, cell.Style.Attrs)
			}
		}
		if strings.ContainsRune("bold", []rune(cell.Content)[0]) && cell.Style.Attrs&uv.AttrBold != 0 {
			sawBold = true
		}
	}
	if checkedLetters == 0 {
		t.Fatal("expected to inspect body letters")
	}
	if !sawBold {
		t.Fatal("expected inline markdown strong span to remain bold")
	}
}

func TestStyleRenderedThinkingLinesPreservesTitleBoldAcrossInlineMarkdownResets(t *testing.T) {
	ApplyTheme(DefaultTheme())
	titleLines := renderMarkdownContent("**Planning** `cache`", 80)
	if len(titleLines) != 1 {
		t.Fatalf("markdown line count=%d want 1", len(titleLines))
	}
	styled := styleRenderedThinkingLines(titleLines)
	if len(styled) != 1 {
		t.Fatalf("len=%d want 1", len(styled))
	}

	buf := newScreenBuffer(ansi.StringWidth(stripANSI(styled[0])), 1)
	uv.NewStyledString(styled[0]).Draw(buf, buf.Bounds())
	line := buf.Line(0)

	var sawTail bool
	for _, cell := range line {
		if cell.IsZero() || cell.Content == "" || cell.Content == " " {
			continue
		}
		if strings.ContainsRune("Planningcache", []rune(cell.Content)[0]) {
			sawTail = true
			if cell.Style.Attrs&uv.AttrBold == 0 {
				t.Fatalf("expected title cell %q to retain bold attrs=%08b", cell.Content, cell.Style.Attrs)
			}
		}
	}
	if !sawTail {
		t.Fatal("expected to inspect title cells")
	}
}

func TestRenderThinkingHidesElapsedWhileStreaming(t *testing.T) {
	ApplyTheme(DefaultTheme())
	b := &Block{
		Type:             BlockThinking,
		Content:          "Analyzing the problem carefully.",
		Streaming:        true,
		ThinkingDuration: 9 * time.Second,
	}
	joined := stripANSI(strings.Join(b.renderThinking(80), "\n"))
	if strings.Contains(joined, "⏱") {
		t.Fatalf("streaming thinking should not show elapsed footer; got:\n%s", joined)
	}
}

func TestRenderThinkingShowsFinalElapsedAfterStreamingEnds(t *testing.T) {
	ApplyTheme(DefaultTheme())
	b := &Block{
		Type:             BlockThinking,
		Content:          "Analyzing the problem carefully.",
		Streaming:        false,
		ThinkingDuration: 9 * time.Second,
	}
	joined := stripANSI(strings.Join(b.renderThinking(80), "\n"))
	if !strings.Contains(joined, "⏱ 9s") {
		t.Fatalf("settled thinking should show final elapsed footer; got:\n%s", joined)
	}
}

func TestSplitAssistantMarkdownSegmentsCapturesFenceLanguage(t *testing.T) {
	segments := splitAssistantMarkdownSegments("before\n```yaml\nkey: value\n```\nafter\n")
	if len(segments) != 3 {
		t.Fatalf("len=%d want 3", len(segments))
	}
	if segments[1].fenceLang != "yaml" {
		t.Fatalf("fenceLang=%q want yaml", segments[1].fenceLang)
	}
	if !segments[1].code {
		t.Fatal("middle segment should be code")
	}
}

func TestRenderAssistantCodeFenceShowsTextBadgeWithoutHighlight(t *testing.T) {
	ApplyTheme(DefaultTheme())
	seg := assistantMarkdownSegment{raw: "```text\nAGENTS.md\n```", code: true, fenceLang: "text"}
	var hl *codeHighlighter
	lines, _, _ := renderAssistantCodeFence(seg, "AGENTS.md", 24, 2, &hl)
	joinedANSI := strings.Join(lines, "\n")
	joinedPlain := stripANSI(joinedANSI)
	if !strings.Contains(joinedPlain, "TEXT") {
		t.Fatalf("expected TEXT badge, got %q", joinedPlain)
	}
	if strings.Contains(joinedANSI, "38;2;249;38;2") {
		t.Fatalf("text fence should not include token-level syntax highlight artifacts: %q", joinedANSI)
	}
}

func TestRenderAssistantCodeFenceUsesExplicitLanguageLexer(t *testing.T) {
	ApplyTheme(DefaultTheme())
	seg := assistantMarkdownSegment{raw: "```go\nfunc main() {}\n```", code: true, fenceLang: "go"}
	var hl *codeHighlighter
	lines, _, _ := renderAssistantCodeFence(seg, "func main() {}", 32, 2, &hl)
	if hl == nil {
		t.Fatal("expected code highlighter")
	}
	if got := hl.language; got != "go" {
		t.Fatalf("language=%q want go", got)
	}
	joinedPlain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(joinedPlain, "GO") {
		t.Fatalf("expected GO badge, got %q", joinedPlain)
	}
}

func TestRenderAssistantCodeFenceAddsHorizontalPadding(t *testing.T) {
	ApplyTheme(DefaultTheme())
	seg := assistantMarkdownSegment{raw: "```text\nhello\n```", code: true, fenceLang: "text"}
	var hl *codeHighlighter
	lines, synthetic, _ := renderAssistantCodeFence(seg, "hello", 16, 2, &hl)
	if len(lines) < 5 {
		t.Fatalf("len=%d want >=5", len(lines))
	}
	if strings.TrimSpace(stripANSI(lines[0])) != "" || strings.TrimSpace(stripANSI(lines[len(lines)-1])) != "" {
		t.Fatalf("top/bottom padding lines should be blank: first=%q last=%q", stripANSI(lines[0]), stripANSI(lines[len(lines)-1]))
	}
	plainLabel := stripANSI(lines[1])
	if !strings.HasPrefix(plainLabel, " ") || !strings.HasSuffix(plainLabel, " ") {
		t.Fatalf("label should have horizontal padding, got %q", plainLabel)
	}
	plainBody := stripANSI(lines[3])
	if !strings.HasPrefix(plainBody, " hello") {
		t.Fatalf("body should have left padding, got %q", plainBody)
	}
	if got := ansi.StringWidth(lines[3]); got != 16 {
		t.Fatalf("body width=%d want 16", got)
	}
	if synthetic[0] != 1 || synthetic[1] != 1 || synthetic[2] != 1 || synthetic[3] < 1 {
		t.Fatalf("synthetic prefix widths=%v, want leading padding tracked", synthetic)
	}
}

func TestParseAssistantSummaryRequiresSummaryBlockAtStart(t *testing.T) {
	content := "First body paragraph.\n\nSummary: This is a body heading, not an execution summary.\n\nThird body paragraph."
	summary := parseAssistantSummary(content)
	if summary.HasMeta {
		t.Fatalf("expected body paragraph not to be parsed as assistant meta: %+v", summary)
	}
	if got := stripAssistantSummary(content, summary); got != content {
		t.Fatalf("stripAssistantSummary should keep body unchanged, got %q", got)
	}
}

func TestParseAssistantSummaryRequiresLeadingConsecutiveFields(t *testing.T) {
	content := "Tasks completed: fixed body splitting\nBody starts here, not a summary block continuation.\nSummary: This is also body text."
	summary := parseAssistantSummary(content)
	if summary.HasMeta {
		t.Fatalf("expected isolated leading field line not to trigger summary mode: %+v", summary)
	}
}

func TestParseAssistantSummaryRecognizesLeadingSummaryBlock(t *testing.T) {
	content := "Tasks completed: fixed body splitting\nSummary: narrowed assistant summary detection\n\nThis is the actual body."
	summary := parseAssistantSummary(content)
	if !summary.HasMeta {
		t.Fatal("expected leading summary block to be recognized")
	}
	if summary.TasksCompleted != "fixed body splitting" || summary.Summary != "narrowed assistant summary detection" {
		t.Fatalf("unexpected summary parse result: %+v", summary)
	}
	if got := stripAssistantSummary(content, summary); got != "This is the actual body." {
		t.Fatalf("unexpected stripped body: %q", got)
	}
}

func TestRenderAssistantCodeBlockUsesFullWidthCodeSurface(t *testing.T) {
	ApplyTheme(DefaultTheme())
	var hl *codeHighlighter
	lines, _, _ := renderAssistantMarkdownContent("```go\nfmt.Println(1)\n```", "fmt.Println(1)", 24, 2, &hl)
	if len(lines) != 5 {
		t.Fatalf("len=%d want 5, lines=%#v", len(lines), lines)
	}
	if got := ansi.StringWidth(lines[3]); got != 24 {
		t.Fatalf("code line width = %d, want 24; line=%q", got, lines[3])
	}
	joinedPlain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(joinedPlain, "GO") || !strings.Contains(joinedPlain, "fmt.Println(1)") {
		t.Fatalf("plain block = %q, want GO badge and code content", joinedPlain)
	}
}

func TestRenderCompactionSummaryUsesMarkdownPreviewAndBlankLine(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		Type:                   BlockCompactionSummary,
		Collapsed:              true,
		CompactionPreviewLines: 10,
		Content:                "## Goal\n- keep markdown\n- show preview\n\n## Progress\n- render markdown",
	}
	lines := block.Render(60, "")
	plain := make([]string, len(lines))
	for i, line := range lines {
		cleaned := strings.TrimLeft(stripANSI(line), " ")
		cleaned = strings.TrimPrefix(cleaned, "│")
		plain[i] = cleaned
	}
	idx := -1
	for i, line := range plain {
		if strings.Contains(line, "CONTEXT SUMMARY") {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatalf("missing summary label in %q", strings.Join(plain, "\n"))
	}
	if idx+1 >= len(plain) || strings.TrimSpace(plain[idx+1]) != "" {
		end := idx + 3
		if end > len(plain) {
			end = len(plain)
		}
		t.Fatalf("expected blank line after label, got %q", strings.Join(plain[idx:end], " | "))
	}
	joined := strings.Join(plain, "\n")
	if !strings.Contains(joined, "Goal") {
		t.Fatalf("expected markdown heading text, got %q", joined)
	}
	if !strings.Contains(joined, "• keep markdown") {
		t.Fatalf("expected markdown bullet rendering, got %q", joined)
	}
	if !strings.Contains(joined, "[space] expand full preserved context") {
		t.Fatalf("expected collapsed hint, got %q", joined)
	}
}

func TestRenderCompactionSummaryHighlightsFencedCode(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		Type:                   BlockCompactionSummary,
		Collapsed:              false,
		CompactionPreviewLines: 10,
		Content:                "## Files\n```go\nfmt.Println(1)\n```",
	}
	lines := block.Render(60, "")
	joinedPlain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(joinedPlain, "GO") {
		t.Fatalf("expected fenced code language label, got %q", joinedPlain)
	}
	if !strings.Contains(joinedPlain, "fmt.Println(1)") {
		t.Fatalf("expected fenced code body, got %q", joinedPlain)
	}
	joinedANSI := strings.Join(lines, "\n")
	if !strings.Contains(joinedANSI, "\x1b[") {
		t.Fatalf("expected ANSI styling for highlighted code fence, got %q", joinedANSI)
	}
}

func TestRenderAssistantCodeBlockWrappedContinuationIndented(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{Type: BlockAssistant, Content: "```go\nfunc TestCancelCurrentTurnRoutesToFocusedSubAgentAndPersistsCancelledToolResult(t *testing.T) {}\n```"}
	lines := block.Render(60, "")
	var codeLines []string
	for _, line := range lines {
		plain := stripANSI(line)
		trimmed := strings.TrimSpace(plain)
		if strings.Contains(trimmed, "CancelCurrentTurnRoutesToFocusedSubAgent") || strings.Contains(trimmed, "sistsCancelledToolResult") || strings.Contains(trimmed, "ersistsCancelledToolResult") {
			codeLines = append(codeLines, plain)
		}
	}
	if len(codeLines) != 2 {
		t.Fatalf("expected 2 wrapped code lines, got %d: %#v", len(codeLines), codeLines)
	}
	firstIndent := countLeadingWhitespace(codeLines[0])
	secondIndent := countLeadingWhitespace(codeLines[1])
	if secondIndent <= firstIndent {
		t.Fatalf("wrapped continuation should be more indented: first=%d second=%d lines=%q", firstIndent, secondIndent, codeLines)
	}
	got := strings.TrimSpace(codeLines[1])
	got = strings.TrimLeft(got, "│ ")
	if !strings.HasPrefix(got, "sistsCancelledToolResult") && !strings.HasPrefix(got, "ersistsCancelledToolResult") {
		t.Fatalf("continuation content got %q", got)
	}
}

func TestRenderAssistantCodeBlockLongLineDoesNotLeakBackgroundIntoCardPadding(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{Type: BlockAssistant, Content: "```text\n" + strings.Repeat("x", 120) + "\n```"}
	lines := block.Render(140, "")
	if len(lines) == 0 {
		t.Fatal("expected rendered assistant block")
	}

	var target string
	for _, line := range lines {
		plain := stripANSI(line)
		if strings.Contains(plain, strings.Repeat("x", 40)) {
			target = line
			break
		}
	}
	if target == "" {
		t.Fatalf("failed to locate long code line in render output: %q", strings.Join(lines, "\n"))
	}

	buf := newScreenBuffer(ansi.StringWidth(target), 1)
	uv.NewStyledString(target).Draw(buf, buf.Bounds())
	cells := buf.Line(0)
	if len(cells) == 0 {
		t.Fatal("expected rendered cells")
	}

	cardBg := colorOfTheme(currentTheme.AssistantCardBg)
	codeBg := colorOfTheme(currentTheme.CodeBlockBg)
	if colorsEqual(cardBg, codeBg) {
		t.Fatal("test requires assistant and code block backgrounds to differ")
	}

	seenCode := false
	codeBgSpaces := 0
	cardBgSpaces := 0
	for _, cell := range cells {
		if cell.IsZero() || cell.Content == "" {
			continue
		}
		if cell.Content == "x" {
			if !colorsEqual(cell.Style.Bg, codeBg) {
				t.Fatalf("code cell background = %v, want code bg %v", cell.Style.Bg, codeBg)
			}
			seenCode = true
			continue
		}
		if seenCode && cell.Content == " " {
			if colorsEqual(cell.Style.Bg, codeBg) {
				codeBgSpaces++
				continue
			}
			cardBgSpaces++
		}
	}
	if !seenCode {
		t.Fatal("expected to observe code cells")
	}
	if codeBgSpaces > 1 {
		t.Fatalf("padding after long code line kept code-block background for %d spaces, want at most 1 inner pad cell", codeBgSpaces)
	}
	if cardBgSpaces == 0 {
		t.Fatal("expected to observe card-background padding after long code line")
	}
}

func TestRenderAssistantInlineMarkdownKeepsCardBackgroundOnTrailingPadding(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{Type: BlockAssistant, Content: "prefix **bold**"}
	lines := block.Render(120, "")
	if len(lines) == 0 {
		t.Fatal("expected rendered assistant block")
	}

	var target string
	for _, line := range lines {
		plain := stripANSI(line)
		if strings.Contains(plain, "prefix") && strings.Contains(plain, "bold") {
			target = line
			break
		}
	}
	if target == "" {
		t.Fatalf("failed to locate inline markdown line in render output: %q", strings.Join(lines, "\n"))
	}

	plain := stripANSI(target)
	trimmed := strings.TrimRight(plain, " ")
	if trimmed == plain {
		t.Fatalf("target line has no trailing spaces: %q", plain)
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

	assistantBg := colorOfTheme(currentTheme.AssistantCardBg)
	trailingSpaces := 0
	for i := contentEndCol + 1; i < len(cells); i++ {
		cell := cells[i]
		if cell.IsZero() || cell.Content != " " {
			continue
		}
		trailingSpaces++
		if !colorsEqual(cell.Style.Bg, assistantBg) {
			t.Fatalf("trailing space at col %d background = %v, want assistant bg %v", i, cell.Style.Bg, assistantBg)
		}
	}
	if trailingSpaces == 0 {
		t.Fatal("expected trailing padding spaces after inline markdown content")
	}
}

func TestRenderAssistantTableKeepsEmailLinksInline(t *testing.T) {
	ApplyTheme(DefaultTheme())
	resetMarkdownRenderer()
	block := &Block{Type: BlockAssistant, Content: `The report shows 5 expired account email entries:

| Account ID | Email | Expiration Date |
|------------|-------|-----------------|
| a20158b8-... | gfwgfwgfwgfw@gmail.com | 2026-04-02 |

These accounts need to sign in again before new tokens can be issued.`}
	lines := block.Render(100, "")
	joinedANSI := strings.Join(lines, "\n")
	if strings.Contains(joinedANSI, "[1]:") {
		t.Fatalf("expected assistant table links to stay inline, got footer list:\n%s", joinedANSI)
	}
	joinedPlain := stripANSI(stripOSC8(joinedANSI))
	if !strings.Contains(joinedPlain, "gfwgfwgfwgfw@gmail.com") {
		t.Fatalf("expected assistant card to keep email inside table, got %q", joinedPlain)
	}
	if !strings.Contains(joinedPlain, "These accounts need to sign in again") {
		t.Fatalf("expected trailing paragraph after table, got %q", joinedPlain)
	}
}

func colorOfTheme(term string) interface{ RGBA() (r, g, b, a uint32) } {
	return lipgloss.Color(term)
}

func colorsEqual(a, b interface{ RGBA() (r, g, b, a uint32) }) bool {
	if a == nil || b == nil {
		return a == b
	}
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}

func TestParseAssistantSummary_FourFields(t *testing.T) {
	content := `Tasks completed: Fixed bug in parser
Files modified: internal/parser.go
Summary: Updated error handling
Issues: None

This is the body content.`

	summary := parseAssistantSummary(content)
	if !summary.HasMeta {
		t.Fatal("expected HasMeta=true")
	}
	if summary.TasksCompleted != "Fixed bug in parser" {
		t.Fatalf("TasksCompleted=%q", summary.TasksCompleted)
	}
	if summary.FilesModified != "internal/parser.go" {
		t.Fatalf("FilesModified=%q", summary.FilesModified)
	}
	if summary.Summary != "Updated error handling" {
		t.Fatalf("Summary=%q", summary.Summary)
	}
	if summary.Issues != "None" {
		t.Fatalf("Issues=%q", summary.Issues)
	}
}

func TestParseAssistantSummary_PartialFields(t *testing.T) {
	content := `Tasks completed: Item 1, Item 2
Summary: Brief overview

Body here.`

	summary := parseAssistantSummary(content)
	if !summary.HasMeta {
		t.Fatal("expected HasMeta=true")
	}
	if summary.TasksCompleted != "Item 1, Item 2" {
		t.Fatalf("TasksCompleted=%q", summary.TasksCompleted)
	}
	if summary.FilesModified != "" {
		t.Fatalf("FilesModified should be empty, got %q", summary.FilesModified)
	}
	if summary.Summary != "Brief overview" {
		t.Fatalf("Summary=%q", summary.Summary)
	}
}

func TestParseAssistantSummary_NoMeta(t *testing.T) {
	content := `Just a regular message without summary fields.

Nothing special here.`

	summary := parseAssistantSummary(content)
	if summary.HasMeta {
		t.Fatal("expected HasMeta=false")
	}
}

func TestStripAssistantSummary_RemovesHeader(t *testing.T) {
	content := `Tasks completed: X
Files modified: Y
Summary: Z
Issues: W

Actual body content here.`

	summary := parseAssistantSummary(content)
	body := stripAssistantSummary(content, summary)
	if strings.Contains(body, "Tasks completed:") {
		t.Fatalf("body should not contain summary header: %q", body)
	}
	if !strings.Contains(body, "Actual body content here") {
		t.Fatalf("body should contain content: %q", body)
	}
}

func TestStripAssistantSummary_NoMetaReturnsOriginal(t *testing.T) {
	content := "Regular message without meta"
	summary := parseAssistantSummary(content)
	body := stripAssistantSummary(content, summary)
	if body != content {
		t.Fatalf("expected original content, got %q", body)
	}
}

func TestRenderAssistantSummaryLines(t *testing.T) {
	ApplyTheme(DefaultTheme())
	summary := assistantSummary{
		TasksCompleted: "Fixed parser",
		FilesModified:  "parser.go",
		Summary:        "Bug fixes",
		Issues:         "None",
		HasMeta:        true,
	}
	lines := renderAssistantSummaryLines(summary, 60)
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines, got %d: %#v", len(lines), lines)
	}
	plain := stripANSI(strings.Join(lines, ""))
	if !strings.Contains(plain, "Tasks completed: Fixed parser") {
		t.Fatalf("missing tasks line: %q", plain)
	}
	if !strings.Contains(plain, "Files modified: parser.go") {
		t.Fatalf("missing files line: %q", plain)
	}
}

func TestRenderAssistantSummaryLines_EmptyFields(t *testing.T) {
	summary := assistantSummary{
		TasksCompleted: "",
		FilesModified:  "",
		Summary:        "",
		Issues:         "",
		HasMeta:        false,
	}
	lines := renderAssistantSummaryLines(summary, 60)
	if lines != nil {
		t.Fatalf("expected nil for empty summary, got %#v", lines)
	}
}

func TestRenderAssistant_WithSummaryMeta(t *testing.T) {
	ApplyTheme(DefaultTheme())
	content := `Tasks completed: Fixed bug
Files modified: fix.go
Summary: One-line fix
Issues: None

Detailed explanation of the fix.`

	block := &Block{
		Type:    BlockAssistant,
		Content: content,
	}
	lines := block.Render(80, "")
	plain := make([]string, len(lines))
	for i, line := range lines {
		plain[i] = stripANSI(line)
	}
	joined := strings.Join(plain, "\n")
	if !strings.Contains(joined, "ASSISTANT") {
		t.Fatalf("missing ASSISTANT label: %q", joined)
	}
	if !strings.Contains(joined, "Tasks completed: Fixed bug") {
		t.Fatalf("missing summary meta: %q", joined)
	}
	if !strings.Contains(joined, "Detailed explanation of the fix") {
		t.Fatalf("missing body: %q", joined)
	}
}

func TestRenderAssistant_SummaryMetaStreaming(t *testing.T) {
	ApplyTheme(DefaultTheme())
	content := `Tasks completed: X
Summary: Y

Body`

	block := &Block{
		Type:      BlockAssistant,
		Content:   content,
		Streaming: true,
	}
	lines := block.Render(80, "")
	plain := make([]string, len(lines))
	for i, line := range lines {
		plain[i] = stripANSI(line)
	}
	joined := strings.Join(plain, "\n")
	// During streaming, the summary meta rendering is skipped, but the raw content
	// (including the summary field text) is still shown as part of the body
	if !strings.Contains(joined, "Body") {
		t.Fatalf("missing body: %q", joined)
	}
	// The raw text of summary fields may appear in the content during streaming
	// (this is expected - the special weakened rendering is just skipped)
}

func TestIsASCIILettersOnly(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"Okay", true},
		{"Sure", true},
		{"Let", true},
		{"Ok!", false},
		{"OK", true},
		{"123", false},
		{"", false},
		{"Hi there", false},
	}
	for _, tt := range tests {
		got := isASCIILettersOnly(tt.input)
		if got != tt.want {
			t.Errorf("isASCIILettersOnly(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
