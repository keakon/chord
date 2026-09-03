package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tools"
)

// TestToolCardWidthUnifiedWithProse verifies the background-width fix: a plain
// tool-call card now caps at the same inner width as a prose/done card, so tool
// cards no longer stop ~40 columns short of thinking/assistant cards on wide
// terminals (the original "background looks wrong / truncated" report).
func TestToolCardWidthUnifiedWithProse(t *testing.T) {
	ApplyTheme(DefaultTheme())
	for _, w := range []int{140, 200, 240, 290} {
		toolCard := newToolCardMetrics(w)
		doneCard := newDoneToolCardMetrics(w)
		if toolCard.cardWidth != doneCard.cardWidth {
			t.Errorf("tool card width = %d, done card width = %d at viewport %d; tool cards must share the prose cap", toolCard.cardWidth, doneCard.cardWidth, w)
		}
	}
}

// TestFileToolsRenderWithoutOverflow verifies apply_patch/edit/write cards never
// emit a line wider than the card surface (no horizontal overflow) across a range
// of viewport widths.
func TestFileToolsRenderWithoutOverflow(t *testing.T) {
	ApplyTheme(DefaultTheme())
	content := "package main"
	writeArgs, _ := json.Marshal(map[string]string{"path": "main.go", "content": content})
	diffArgs, _ := json.Marshal(map[string]string{"path": "main.go"})
	diff := "@@ -1,2 +1,2 @@\n package context\n-package old\n+package main\n"

	blocks := []*Block{
		{ID: 2, Type: BlockToolCall, ToolName: tools.NameWrite, Content: string(writeArgs), ResultDone: true},
		{ID: 3, Type: BlockToolCall, ToolName: tools.NameEdit, Content: string(diffArgs), Diff: diff, ResultDone: true},
		{ID: 4, Type: BlockToolCall, ToolName: tools.NameApplyPatch, Content: string(diffArgs), Diff: diff, ResultDone: true},
	}
	for _, b := range blocks {
		for _, w := range []int{80, 120, 160, 240, 290} {
			metrics := newToolCardMetrics(w)
			style := metrics.blockStyle
			maxLineWidth := style.GetMarginLeft() + style.GetPaddingLeft() + metrics.cardWidth + style.GetPaddingRight() + style.GetMarginRight()
			if railANSISeq("tool", false) != "" {
				maxLineWidth++
			}
			lines := b.Render(w, "")
			for i, line := range lines {
				if dw := tuiStringWidth(line); dw > maxLineWidth {
					t.Errorf("%s @viewport %d: line %d width %d exceeds card width %d: %q", b.ToolName, w, i, dw, maxLineWidth, line)
				}
			}
		}
	}
}

// TestEditPatchPreviewTruncatesNotWraps verifies the edit "Requested patch"
// preview clips long lines (with an ellipsis) instead of wrapping them, matching
// the apply_patch preview and the diff body. Diff / file content is
// column-aligned, so wrapping would break the +/- gutter alignment.
func TestEditPatchPreviewTruncatesNotWraps(t *testing.T) {
	ApplyTheme(DefaultTheme())
	long := strings.Repeat("y", 300)
	args, _ := json.Marshal(map[string]string{
		"path":  "main.go",
		"patch": "@@ -1 +1 @@\n-" + long + "\n+" + long,
	})
	width := 80
	metrics := newToolCardMetrics(width)
	previewWidth := metrics.cardWidth - 4

	var result []string
	result = appendEditPatchPreview(result, string(args), previewWidth)
	if len(result) == 0 {
		t.Fatal("expected edit patch preview lines")
	}
	for _, line := range result {
		if dw := tuiStringWidth(line); dw > metrics.cardWidth {
			t.Errorf("edit patch preview line width %d exceeds card width %d: %q", dw, metrics.cardWidth, line)
		}
	}
	joined := strings.Join(result, "\n")
	if !strings.Contains(joined, "…") {
		t.Errorf("expected clipped edit patch preview with ellipsis; got:\n%s", joined)
	}
}
