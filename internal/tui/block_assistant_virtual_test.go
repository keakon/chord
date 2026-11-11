package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tui/terminaltext"
	uitheme "github.com/keakon/chord/internal/tui/theme"
)

func virtualAssistantTestContent(paragraphs int) string {
	var b strings.Builder
	for i := range paragraphs {
		if i%17 == 0 {
			fmt.Fprintf(&b, "## Section %d with several ordinary words that wrap at narrow widths\n\n", i)
			continue
		}
		fmt.Fprintf(&b, "Paragraph %d keeps ordinary prose without markdown markup so its height follows from wrapping the visible text.\n\n", i)
	}
	return b.String()
}

func TestAssistantVirtualLayoutMatchesFullRender(t *testing.T) {
	ApplyTheme(uitheme.Default())
	content := virtualAssistantTestContent(220)
	for _, width := range []int{20, 40, 100} {
		virtual := &Block{Type: BlockAssistant, Content: content}
		fullBlock := &Block{Type: BlockAssistant, Content: content}
		full := fullBlock.renderAssistant(width)

		count := virtual.LineCount(width)
		if count != len(full) {
			t.Fatalf("width %d: virtual line count = %d, full = %d", width, count, len(full))
		}
		layout := virtual.virtualAssistant
		if layout == nil {
			t.Fatalf("width %d: expected a virtual layout", width)
		}
		_ = virtual.RenderRange(width, "", 0, min(40, count))
		for _, chunk := range layout.chunks[1:] {
			if chunk.rendered {
				t.Fatalf("width %d: first-window render materialized a later chunk", width)
			}
		}
		if got := virtual.RenderRange(width, "", 0, count); !slices.Equal(got, full) {
			t.Fatalf("width %d: full virtual range differs from full render", width)
		}
		for _, window := range [][2]int{
			{0, min(40, count)},
			{1, min(17, count)},
			{max(count/2-13, 0), min(count/2+27, count)},
			{max(count-31, 0), count},
		} {
			if window[0] >= window[1] {
				continue
			}
			got := virtual.RenderRange(width, "", window[0], window[1])
			if !slices.Equal(got, full[window[0]:window[1]]) {
				t.Fatalf("width %d: warm range %v differs from full render", width, window)
			}

			cold := &Block{Type: BlockAssistant, Content: content}
			if got := cold.LineCount(width); got != len(full) {
				t.Fatalf("width %d: cold line count = %d, full = %d", width, got, len(full))
			}
			got = cold.RenderRange(width, "", window[0], window[1])
			if !slices.Equal(got, full[window[0]:window[1]]) {
				t.Fatalf("width %d: cold range %v differs from full render", width, window)
			}
		}
	}
}

func TestAssistantVirtualHeadingBoundariesMatchFullRender(t *testing.T) {
	ApplyTheme(uitheme.Default())
	content := strings.Repeat("## A section heading with enough ordinary words to exercise wrapping at narrow widths\n\n", 120)
	content += "## Final section heading with enough ordinary words to exercise wrapping at narrow widths\n"
	for _, width := range []int{20, 40, 100} {
		virtual := &Block{Type: BlockAssistant, Content: content}
		full := (&Block{Type: BlockAssistant, Content: content}).renderAssistant(width)
		count := virtual.LineCount(width)
		if count != len(full) {
			t.Fatalf("width %d: virtual line count = %d, full = %d", width, count, len(full))
		}
		if got := virtual.RenderRange(width, "", 0, count); !slices.Equal(got, full) {
			t.Fatalf("width %d: heading-only virtual range differs from full render", width)
		}
	}
}

func TestViewportVirtualAssistantScrollMatchesFullLines(t *testing.T) {
	ApplyTheme(uitheme.Default())
	content := virtualAssistantTestContent(220)
	width, height := 40, 18
	full := (&Block{Type: BlockAssistant, Content: content}).renderAssistant(width)

	v := NewViewport(width, height)
	v.SetSticky(false)
	block := &Block{Type: BlockAssistant, Content: content}
	v.AppendBlock(block)
	if got := v.TotalLines(); got != len(full) {
		t.Fatalf("viewport total = %d, full = %d", got, len(full))
	}

	empty := strings.Repeat(" ", width)
	for _, start := range []int{0, 3, len(full) / 3, max(len(full)-height-2, 0)} {
		v.offset = start
		got := strings.Split(v.Render("", nil, -1, 0, ""), "\n")
		want := make([]string, height)
		for i := range want {
			line := empty
			if start+i < len(full) {
				line = terminaltext.ExpandTabsANSI(full[start+i], terminaltext.TabWidth)
				line = terminaltext.Truncate(line, width)
				line = terminaltext.PadOutsideStyle(line, width)
			}
			want[i] = line
		}
		if !slices.Equal(got, want) {
			t.Fatalf("offset %d: viewport virtual lines differ from full render", start)
		}
	}
}

func TestAssistantVirtualLayoutFallsBackForComplexMarkdown(t *testing.T) {
	ApplyTheme(uitheme.Default())
	for name, content := range map[string]string{
		"inline markup": strings.Repeat("A **bold** paragraph that keeps ordinary markdown markup and enough text to cross the virtual minimum size.\n\n", 90),
		"table":         strings.Repeat("| key | value |\n|---|---|\n| a | b |\n\n", 90),
		"quote":         strings.Repeat("> a quoted paragraph that is long enough to cross the virtual minimum size when repeated.\n\n", 90),
	} {
		t.Run(name, func(t *testing.T) {
			block := &Block{Type: BlockAssistant, Content: content}
			if _, ok := block.assistantVirtualLayout(100); ok {
				t.Fatal("complex Markdown unexpectedly enabled virtual rendering")
			}
			_ = block.LineCount(100)
			if block.virtualAssistant != nil {
				t.Fatal("complex Markdown unexpectedly retained a virtual layout")
			}
		})
	}
}

func TestAssistantVirtualSelectionPrefixMetadataIsRangeRelative(t *testing.T) {
	ApplyTheme(uitheme.Default())
	content := virtualAssistantTestContent(220)
	width := 100
	block := &Block{Type: BlockAssistant, Content: content}
	layout, ok := block.assistantVirtualLayout(width)
	if !ok {
		t.Fatal("expected virtual layout")
	}
	start := layout.bodyOffset + 11
	end := start + 4
	_ = block.RenderRange(width, "", start, end)
	if block.renderSyntheticPrefixWidthsStart != start || len(block.renderSyntheticPrefixWidths) != end-start {
		t.Fatalf("synthetic prefix metadata covers [%d,%d), want [%d,%d)",
			block.renderSyntheticPrefixWidthsStart, block.renderSyntheticPrefixWidthsStart+len(block.renderSyntheticPrefixWidths), start, end)
	}
	v := &Viewport{width: width}
	want := block.renderSyntheticPrefixWidths[0]
	if got := v.selectionLinePrefixWidth(block, start, ""); got != want {
		t.Fatalf("selection prefix width = %d, want %d", got, want)
	}
}
