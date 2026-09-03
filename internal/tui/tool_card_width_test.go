package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tools"
)

// TestToolCardSurfaceSpansViewport verifies the full-width card behavior: the
// card surface (background/border) spans the whole viewport after the rail
// reservation, matching every other card kind, while only the wrapped content
// column is capped (so long lines do not stretch across ultra-wide terminals).
func TestToolCardSurfaceSpansViewport(t *testing.T) {
	ApplyTheme(DefaultTheme())
	for _, w := range []int{80, 120, 140, 200, 240, 290} {
		style := ToolBlockStyle
		wantSurface := max((w-railWidthToReserve(style))-style.GetHorizontalMargins()-style.GetHorizontalPadding()-style.GetHorizontalBorderSize(), 10)
		toolCard := newToolCardMetrics(w)
		doneCard := newDoneToolCardMetrics(w)
		wideHeaderCard := newWideHeaderToolCardMetrics(w)
		for name, c := range map[string]toolCardMetrics{
			"tool": toolCard, "done": doneCard, "wide-header": wideHeaderCard,
		} {
			if c.cardWidth != wantSurface {
				t.Errorf("%s card width = %d, want full surface %d at viewport %d", name, c.cardWidth, wantSurface, w)
			}
			if wantContent := max(min(wantSurface-4, maxProseWidth), 10); c.contentWidth != wantContent {
				t.Errorf("%s content width = %d, want capped content %d at viewport %d", name, c.contentWidth, wantContent, w)
			}
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
