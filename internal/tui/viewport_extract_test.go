package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"
	"github.com/stretchr/testify/assert"

	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/terminaltext"
	uitheme "github.com/keakon/chord/internal/tui/theme"
)

func TestExtractPlainByColumns(t *testing.T) {
	t.Run("plain text without ansi", func(t *testing.T) {
		text := "test.Model(param) || other.config == \"value\" {"
		// Select test.Model(param) || segment (length 21)
		res := extractPlainByColumns(text, 0, 21)
		assert.Equal(t, "test.Model(param) || ", res)
	})

	t.Run("text with ansi highlight codes", func(t *testing.T) {
		// Simulate ANSI text with deletion highlight around the selected segment.
		text := "if test.API(url) || \x1b[41mtest.Model(param) || \x1b[0mother.config == \"value\" {"
		// Plain text is "if test.API(url) || test.Model(param) || other.config == \"value\" {"
		// The highlighted segment begins after the leading "if test.API(url) || " prefix.
		res := extractPlainByColumns(text, 20, 41)
		assert.Equal(t, "test.Model(param) || ", res)
		// Verify no offset truncation issue occurs.
		assert.NotEqual(t, "Model(param) || othe", res)
	})

	t.Run("empty selection", func(t *testing.T) {
		text := "test line with \x1b[32mcolor\x1b[0m"
		res := extractPlainByColumns(text, 5, 5)
		assert.Equal(t, "", res)
	})

	t.Run("select entire line with ansi", func(t *testing.T) {
		text := "\x1b[31m-\x1b[0m if test.API(url) || test.Model(param) || other.config == \"value\" {"
		res := extractPlainByColumns(text, 0, len(terminaltext.StripANSI(text)))
		assert.Equal(t, "- if test.API(url) || test.Model(param) || other.config == \"value\" {", res)
	})

	// 而[0,2) 且[2,4) 是[4,6) 单[6,8) 点[8,10) 开[10,12) 关[12,14) 。[14,16)
	t.Run("start col inside a wide rune matches highlight rounding", func(t *testing.T) {
		text := "而且是单点开关。"
		// Drag started on the right half of 是 (col 5): the highlight begins at
		// 单, so the copy must not include 是.
		res := extractPlainByColumns(text, 5, 14)
		assert.Equal(t, "单点开关", res)
		startByte, endByte := findColumnByteOffsets(text, 5, 14)
		assert.Equal(t, "单点开关", text[startByte:endByte])
	})

	t.Run("end col inside a wide rune keeps the straddling rune", func(t *testing.T) {
		text := "而且是单点开关。"
		// End on the right half of 开 (col 11): highlight and copy both keep it.
		res := extractPlainByColumns(text, 6, 11)
		assert.Equal(t, "单点开", res)
		startByte, endByte := findColumnByteOffsets(text, 6, 11)
		assert.Equal(t, "单点开", text[startByte:endByte])
	})

	t.Run("start col at a wide rune boundary keeps the rune", func(t *testing.T) {
		text := "而且是单点开关。"
		res := extractPlainByColumns(text, 4, 14)
		assert.Equal(t, "是单点开关", res)
	})
}

func TestExtractSelectionTextWideRuneStartMatchesHighlight(t *testing.T) {
	ApplyTheme(uitheme.Default())
	v := NewViewport(120, 40)
	block := &Block{ID: 1, Type: BlockAssistant, Content: "而且是单点开关。"}
	v.AppendBlock(block)
	_ = v.Render("", nil, -1, -1, "")

	lines := block.Render(v.width, "")
	targetLine := -1
	var line string
	for i := range lines {
		if strings.Contains(terminaltext.StripANSI(lines[i]), "单点开关") {
			targetLine, line = i, lines[i]
			break
		}
	}
	if targetLine < 0 {
		t.Fatal("content line not rendered")
	}
	plain := terminaltext.StripANSI(line)
	base := 0
	if idx := strings.Index(plain, "而"); idx > 0 {
		base = selectionPlainTextWidth(plain[:idx])
	}

	for _, tc := range []struct {
		name             string
		startCol, endCol int
	}{
		{"start inside a wide rune, ending before the tail", base + 5, base + 14},
		{"start inside a wide rune, ending at the line width", base + 5, selectionPlainTextWidth(plain)},
		{"start on a wide rune boundary", base + 4, base + 14},
		{"end inside a wide rune", base + 6, base + 11},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := v.ExtractSelectionText(SelectionRange{
				StartBlockID: block.ID, StartLine: targetLine, StartCol: tc.startCol,
				EndBlockID: block.ID, EndLine: targetLine, EndCol: tc.endCol,
			})
			startByte, endByte := findColumnByteOffsets(line, tc.startCol, tc.endCol)
			if startByte < 0 || endByte <= startByte {
				t.Fatalf("findColumnByteOffsets(%d,%d) selected nothing", tc.startCol, tc.endCol)
			}
			want := strings.TrimSpace(terminaltext.StripANSI(line[startByte:endByte]))
			if got != want {
				t.Fatalf("copy = %q, want the highlighted %q", got, want)
			}
		})
	}
}

func TestExtractSelectionTextTabExpandedColumnsMatchViewportRender(t *testing.T) {
	ApplyTheme(uitheme.Default())
	v := NewViewport(120, 20)
	block := &Block{
		ID:      1,
		Type:    BlockAssistant,
		Content: "\t\tSaveArtifact\tReadArtifact\tComplete",
	}
	v.AppendBlock(block)

	// Force viewport render path to materialize and expand tabs.
	_ = v.Render("", nil, -1, -1, "")
	cached := block.GetViewportCache(v.width, "")
	if cached == nil {
		t.Fatal("expected viewport cache after render")
	}

	// Find the visible row containing SaveArtifact and compute the selection columns
	// from the *rendered* (tab-expanded) text.
	targetLine := -1
	startCol := -1
	endCol := -1
	want := "SaveArtifact"
	for i := range cached {
		plain, _ := v.GetLinePlain(block.ID, i)
		if before, _, ok := strings.Cut(plain, want); ok {
			targetLine = i
			midCol := selectionPlainTextWidth(before) + selectionPlainTextWidth(want)/2
			startCol, endCol = WordBoundsAtCol(plain, midCol)
			break
		}
	}
	if targetLine < 0 {
		t.Fatalf("failed to locate %q in viewport cached lines: %#v", want, stripANSILines(cached))
	}

	got := v.ExtractSelectionText(SelectionRange{
		StartBlockID: block.ID,
		StartLine:    targetLine,
		StartCol:     startCol,
		EndBlockID:   block.ID,
		EndLine:      targetLine,
		EndCol:       endCol,
	})
	if got != want {
		t.Fatalf("ExtractSelectionText() tabbed line\n got %q\nwant %q", got, want)
	}
}
func TestExtractSelectionTextAssistantCodeFenceLineAfterSoftWrappedLine(t *testing.T) {
	ApplyTheme(uitheme.Default())
	v := NewViewport(100, 40)
	longLine := "first_key: " + strings.Repeat("payload_segment_", 12)
	block := &Block{
		ID:      1,
		Type:    BlockAssistant,
		Content: "intro paragraph\n\n```json\n" + longLine + "\nsecond_key: short\n```\n",
	}
	v.AppendBlock(block)

	lines := block.Render(v.width, "")
	targetLine := -1
	startCol := -1
	endCol := -1
	want := "second_key"
	for i, line := range lines {
		plain := terminaltext.StripANSI(line)
		before, _, ok := strings.Cut(plain, want)
		if !ok {
			continue
		}
		targetLine = i
		startCol = selectionPlainTextWidth(before)
		endCol = startCol + selectionPlainTextWidth(want)
		break
	}
	if targetLine < 0 {
		t.Fatalf("target line not found in rendered assistant code fence: %q", want)
	}

	got := v.ExtractSelectionText(SelectionRange{
		StartBlockID: block.ID,
		StartLine:    targetLine,
		StartCol:     startCol,
		EndBlockID:   block.ID,
		EndLine:      targetLine,
		EndCol:       endCol,
	})
	if got != want {
		t.Fatalf("ExtractSelectionText() code fence line after soft wrap\n got %q\nwant %q", got, want)
	}
}

func TestAssistantRenderSyntheticPrefixWidthsAlignWithRenderedLines(t *testing.T) {
	ApplyTheme(uitheme.Default())
	v := NewViewport(100, 40)
	block := &Block{
		ID:      1,
		Type:    BlockAssistant,
		Content: "intro paragraph\n\n```json\n" + "first_key: " + strings.Repeat("payload_segment_", 12) + "\nsecond_key: short\n```\n",
	}
	v.AppendBlock(block)

	lines := block.Render(v.width, "")
	if len(block.renderSyntheticPrefixWidths) != len(lines) {
		t.Fatalf("synthetic prefix widths length = %d, rendered lines = %d",
			len(block.renderSyntheticPrefixWidths), len(lines))
	}
	for i, line := range lines {
		plain := terminaltext.StripANSI(line)
		if strings.TrimSpace(plain) == "" {
			continue
		}
		adjust := block.renderSyntheticPrefixWidths[i]
		if adjust == 0 {
			continue
		}
		indent := selectionPlainTextWidth(plain) - selectionPlainTextWidth(strings.TrimLeft(plain, " │"))
		if adjust > indent {
			t.Fatalf("line %d synthetic prefix %d exceeds rendered indent %d: %q", i, adjust, indent, plain)
		}
	}
}

func TestAssistantSyntheticPrefixWidthsStayAlignedWhenCachedLineEmbedsNewline(t *testing.T) {
	ApplyTheme(uitheme.Default())
	v := NewViewport(100, 40)
	longLine := "first_key: " + strings.Repeat("payload_segment_", 12)
	block := &Block{
		ID:      1,
		Type:    BlockAssistant,
		Content: "intro paragraph\n\n```json\n" + longLine + "\nsecond_key: short\n```\n",
	}
	v.AppendBlock(block)

	_ = block.Render(v.width, "")
	injected := false
	for i, line := range block.mdCache {
		if strings.Contains(terminaltext.StripANSI(line), "intro paragraph") {
			block.mdCache[i] = line + "\nsplit-fallback"
			injected = true
			break
		}
	}
	if !injected {
		t.Fatal("intro line not found in markdown cache")
	}

	lines := block.Render(v.width, "")
	if len(block.renderSyntheticPrefixWidths) != len(lines) {
		t.Fatalf("synthetic prefix widths length = %d, rendered lines = %d",
			len(block.renderSyntheticPrefixWidths), len(lines))
	}

	targetLine := -1
	startCol := -1
	endCol := -1
	want := "first_key"
	for i, line := range lines {
		plain := terminaltext.StripANSI(line)
		before, _, ok := strings.Cut(plain, want)
		if !ok {
			continue
		}
		targetLine = i
		startCol = selectionPlainTextWidth(before)
		endCol = startCol + selectionPlainTextWidth(want)
		break
	}
	if targetLine < 0 {
		t.Fatalf("target line not found after embedded-newline injection: %q", want)
	}

	got := v.ExtractSelectionText(SelectionRange{
		StartBlockID: block.ID,
		StartLine:    targetLine,
		StartCol:     startCol,
		EndBlockID:   block.ID,
		EndLine:      targetLine,
		EndCol:       endCol,
	})
	if got != want {
		t.Fatalf("ExtractSelectionText() after embedded-newline split\n got %q\nwant %q", got, want)
	}
}

func TestExtractSelectionTextEditToolKeepsRenderedColumnsAligned(t *testing.T) {
	v := NewViewport(120, 20)
	block := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: tools.NameEdit,
		Content:  `{"path":"internal/tui/block_tool_render_write.go","patch":"@@\n-old\n+new\n"}`,
		Diff: strings.Join([]string{
			"@@ -1,4 +1,4 @@",
			"-\t\tblockStyle2 := ToolBlockStyle",
			"-\t\tcardBgStyle2 := lipgloss.NewStyle().Background(blockStyle2.GetBackground())",
			"+\t\tcardBgStyle := lipgloss.NewStyle().Background(blockStyle.GetBackground())",
			"+\t\tblockStyle2 := ToolBlockStyle",
		}, "\n"),
	}
	v.AppendBlock(block)

	lines := block.Render(v.width, "")
	targetLine := -1
	startCol := -1
	endCol := -1
	want := "cardBgStyle2 := lipgloss.NewStyle().Background(blockStyle2.GetBackground())"
	for i, line := range lines {
		plain := terminaltext.StripANSI(line)
		before, _, ok := strings.Cut(plain, want)
		if ok {
			targetLine = i
			startCol = selectionPlainTextWidth(before)
			endCol = startCol + selectionPlainTextWidth(want)
			break
		}
	}
	if targetLine < 0 {
		t.Fatalf("target line not found in rendered Edit tool card: %q", want)
	}

	got := v.ExtractSelectionText(SelectionRange{
		StartBlockID: block.ID,
		StartLine:    targetLine,
		StartCol:     startCol,
		EndBlockID:   block.ID,
		EndLine:      targetLine,
		EndCol:       endCol,
	})
	if got != want {
		t.Fatalf("ExtractSelectionText() Edit tool line\n got %q\nwant %q", got, want)
	}
}

// copyWordAtColumn runs the real double-click path on a rendered line at the
// display column of want, then returns the extracted selection text.
func copyWordAtColumn(t *testing.T, m *Model, block *Block, line int, want string) string {
	t.Helper()
	plain, _ := m.viewport.GetLinePlain(block.ID, line)
	before, _, ok := strings.Cut(plain, want)
	if !ok {
		t.Fatalf("line %d does not contain %q: %q", line, want, plain)
	}
	// The word's own first cell, in the same zero-based column space the click
	// handler and ExtractSelectionText use.
	col := selectionPlainTextWidth(before)
	m.clickCount = 1
	m.lastClickTime = time.Now()
	m.lastClickX, m.lastClickY = col, 0
	m.handleViewportSelectionClick(tea.Mouse{X: col, Y: 0, Button: tea.MouseLeft}, block, line, col)
	if !m.hasMouseSelection() {
		t.Fatalf("double click on %q at line %d line selected nothing", want, line)
	}
	return m.viewport.ExtractSelectionText(m.mouseSelectionRange())
}

// The assistant role badge and the summary meta lines carry no body content
// prefix, so word-copying them must not drop their leading characters: the
// selection clamp is driven by per-line synthetic prefix widths, and treating
// the body prefix as indent cut the badge's first letter (ASSISTANT ->
// SSISTANT) and the meta rows' first two (Tasks -> sks).
func TestExtractSelectionTextKeepsAssistantBadgeAndMetaPrefix(t *testing.T) {
	ApplyTheme(uitheme.Default())
	m := NewModelWithSize(nil, 100, 40)
	m.mode = ModeNormal
	block := &Block{
		ID:      1,
		Type:    BlockAssistant,
		Content: "Tasks completed: alpha\nFiles modified: beta\nbody line",
	}
	m.viewport.AppendBlock(block)
	_ = block.Render(m.viewport.width, "")
	_ = m.viewport.Render("", nil, -1, -1, "")

	for _, tc := range []struct {
		want string
	}{
		{want: "ASSISTANT"},
		{want: "Tasks"},
		{want: "Files"},
		{want: "alpha"},
		{want: "body"},
	} {
		found := false
		for line := range len(block.renderSyntheticPrefixWidths) {
			plain, _ := m.viewport.GetLinePlain(block.ID, line)
			if !strings.Contains(plain, tc.want) {
				continue
			}
			found = true
			if got := copyWordAtColumn(t, &m, block, line, tc.want); got != tc.want {
				t.Fatalf("word copy of %q on line %d = %q, want %q", tc.want, line, got, tc.want)
			}
			break
		}
		if !found {
			t.Fatalf("word %q not found on any rendered line", tc.want)
		}
	}
}

// A soft-wrapped continuation line inside an assistant code fence carries a
// continuation indent. GetLinePlain must return the same display columns the
// click handler sees, or word selection reads a neighbour word; selecting the
// whole line must not truncate its tail either.
func TestExtractSelectionTextSoftWrapContinuationColumnsAlign(t *testing.T) {
	ApplyTheme(uitheme.Default())
	tokens := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet", "kilo", "lima", "mike"}
	block := &Block{ID: 1, Type: BlockAssistant, Content: "```text\n" + strings.Join(tokens, " ") + "\n```\n"}
	m := NewModelWithSize(nil, 50, 40)
	m.mode = ModeNormal
	m.viewport.AppendBlock(block)
	_ = block.Render(m.viewport.width, "")
	_ = m.viewport.Render("", nil, -1, -1, "")

	continuation := -1
	for i := range block.renderSoftWrapContinuations {
		if block.renderSoftWrapContinuations[i] {
			continuation = i
			break
		}
	}
	if continuation < 0 {
		t.Fatal("setup produced no soft-wrap continuation line")
	}

	plain, _ := m.viewport.GetLinePlain(block.ID, continuation)
	checked := 0
	for _, tok := range tokens {
		if !strings.Contains(plain, tok) {
			continue
		}
		checked++
		if got := copyWordAtColumn(t, &m, block, continuation, tok); got != tok {
			t.Fatalf("word copy of %q on continuation line %d = %q, want %q", tok, continuation, got, tok)
		}
	}
	if checked == 0 {
		t.Fatalf("continuation line %d contains none of the sample tokens: %q", continuation, plain)
	}

	// Triple-click selects the whole visible line; on a continuation line the
	// copied text must equal the rendered (stripped) line, not a prefix of it.
	m.clickCount = 3
	m.lastClickTime = time.Now()
	m.lastClickX, m.lastClickY = 1, 0
	m.handleViewportSelectionClick(tea.Mouse{X: 1, Y: 0, Button: tea.MouseLeft}, block, continuation, 1)
	if !m.hasMouseSelection() {
		t.Fatal("triple click selected nothing on the continuation line")
	}
	got := m.viewport.ExtractSelectionText(m.mouseSelectionRange())
	want := strings.TrimRight(extractPlainByColumns(plain, firstContentColumn(plain), selectionPlainTextWidth(plain)), " ")
	if got != want {
		t.Fatalf("triple-click on continuation line %d = %q, want %q", continuation, got, want)
	}
}

func firstContentColumn(plain string) int {
	col := 0
	for _, r := range plain {
		if r != ' ' && r != '│' && r != '┃' {
			return col
		}
		col += selectionRuneWidthAtCol(r, col)
	}
	return 0
}
