package tui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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
		res := extractPlainByColumns(text, 0, len(stripANSI(text)))
		assert.Equal(t, "- if test.API(url) || test.Model(param) || other.config == \"value\" {", res)
	})
}

func TestExtractSelectionTextTabExpandedColumnsMatchViewportRender(t *testing.T) {
	ApplyTheme(DefaultTheme())
	v := NewViewport(120, 20)
	block := &Block{
		ID:      1,
		Type:    BlockAssistant,
		Content: "\t\tSaveArtifact\tReadArtifact\tComplete",
	}
	v.AppendBlock(block)

	// Force viewport render path to materialize and expand tabs.
	_ = v.Render("", nil, -1)
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
		if idx := strings.Index(plain, want); idx >= 0 {
			targetLine = i
			midCol := selectionPlainTextWidth(plain[:idx]) + selectionPlainTextWidth(want)/2
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
func TestExtractSelectionTextEditToolKeepsRenderedColumnsAligned(t *testing.T) {
	v := NewViewport(120, 20)
	block := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: "Edit",
		Content:  `{"path":"internal/tui/block_tool_render_write.go"}`,
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
		plain := stripANSI(line)
		idx := strings.Index(plain, want)
		if idx >= 0 {
			targetLine = i
			startCol = selectionPlainTextWidth(plain[:idx])
			endCol = startCol + selectionPlainTextWidth(want)
			break
		}
	}
	if targetLine < 0 {
		t.Fatalf("target line not found in rendered edit tool card: %q", want)
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
		t.Fatalf("ExtractSelectionText() edit tool line\n got %q\nwant %q", got, want)
	}
}
