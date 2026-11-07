package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	uv "github.com/keakon/ultraviolet"

	"github.com/keakon/chord/internal/tui/terminaltext"
	uitheme "github.com/keakon/chord/internal/tui/theme"
)

func TestStreamingCJKStrongSettledPrefixMatchesFullRender(t *testing.T) {
	const paragraph = "前文**说明：**后文"
	const final = paragraph + "\n\n下一段内容。\n\n尾段"
	for _, width := range []int{32, 80} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			ApplyTheme(uitheme.Default())
			streaming := &Block{Type: BlockAssistant, Streaming: true}
			previousFrontier := 0
			advances := 0
			for _, chunk := range []string{
				"前文*", "前文**说明", "前文**说明：", "前文**说明：*",
				"前文**说明：**", "前文**说明：**后", paragraph,
				paragraph + "\n", paragraph + "\n\n下一段内容。", final,
			} {
				streaming.Content = chunk
				streaming.InvalidateCache()
				got := streaming.Render(width, "")
				fresh := &Block{Type: BlockAssistant, Content: chunk, Streaming: true}
				if want := fresh.Render(width, ""); !slices.Equal(got, want) {
					t.Fatalf("cached stream differs at %q:\n got=%q\nwant=%q", chunk, got, want)
				}
				if streaming.streamSettledFrontier <= previousFrontier {
					continue
				}
				advances++
				previousFrontier = streaming.streamSettledFrontier
				prefix := strings.Join(streaming.streamSettledLines, "\n")
				plain := terminaltext.StripANSI(prefix)
				if strings.Contains(plain, "*") || !strings.Contains(plain, "前文说明：后文") {
					t.Fatalf("stable prefix was not rendered: %q", plain)
				}
				for _, tc := range []struct {
					text string
					bold bool
				}{{"说", true}, {"明", true}, {"：", true}, {"前", false}, {"后", false}} {
					cell, ok := findRenderedCell(findRenderedLineContaining(prefix, tc.text), tc.text)
					if !ok || (cell.Style.Attrs&uv.AttrBold != 0) != tc.bold {
						t.Fatalf("incorrect bold state for %q: cell=%+v found=%t", tc.text, cell, ok)
					}
				}
			}
			if advances < 2 {
				t.Fatalf("expected repeated frontier advances, got %d", advances)
			}
			streaming.Streaming = false
			streaming.InvalidateCache()
			got := streaming.Render(width, "")
			settled := &Block{Type: BlockAssistant, Content: final}
			if want := settled.Render(width, ""); !slices.Equal(got, want) {
				t.Fatalf("stream end differs from full render:\n got=%q\nwant=%q", got, want)
			}
		})
	}
}

// Pin the architectural boundary: an unfinished CJK strong span in the
// unsettled tail is not parsed, so its asterisks are still visible. This is
// expected; requiring instant bold there is a separate streaming-render change.
func TestStreamingCJKStrongUnsettledTailKeepsLiteralDelimiters(t *testing.T) {
	ApplyTheme(uitheme.Default())
	content := "第一段已经完成。\n\n**说明：**这里是正文"
	block := &Block{Type: BlockAssistant, Content: content, Streaming: true}
	joined := terminaltext.StripANSI(strings.Join(block.Render(80, ""), "\n"))
	if !strings.Contains(joined, "第一段已经完成") {
		t.Fatalf("stable prefix text missing: %q", joined)
	}
	if !strings.Contains(joined, "**") {
		t.Fatalf("unsettled tail should still use the cheap wrap path with literal asterisks: %q", joined)
	}
}

// Visible selection and search read rendered lines, not the source Content.
// After the asterisks disappear, both must work against the new visible text.
func TestCJKStrongSelectionAndSearchUseRenderedResult(t *testing.T) {
	content := strings.Repeat("这是较长的介绍文字。", 8) + "\n\n前文**说明：**后文"
	const query = "明：后"
	for _, width := range []int{32, 80} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			ApplyTheme(uitheme.Default())
			view := NewViewport(width, 40)
			block := &Block{ID: 1, Type: BlockAssistant, Content: content}
			view.AppendBlock(block)
			view.Render("", nil, -1, -1, "")
			targetLine := -1
			introLines := 0
			for line := 0; line < block.LineCount(width); line++ {
				plain, _ := view.GetLinePlain(block.ID, line)
				if strings.ContainsAny(plain, "介绍文字") {
					introLines++
				}
				if strings.Contains(plain, "*") {
					t.Fatalf("visible text still contains delimiters: %q", plain)
				}
				before, _, found := strings.Cut(plain, query)
				if !found {
					continue
				}
				targetLine = line
				col := selectionPlainTextWidth(before)
				got := view.ExtractSelectionText(SelectionRange{
					StartBlockID: block.ID, EndBlockID: block.ID,
					StartLine: line, EndLine: line,
					StartCol: col, EndCol: col + selectionPlainTextWidth(query),
				})
				if got != query {
					t.Fatalf("selected text = %q, want %q", got, query)
				}
			}
			if targetLine < 0 || introLines < 2 {
				t.Fatalf("sample must include wrapping and the selected text: target=%d intro=%d", targetLine, introLines)
			}
			matches := FindMatchesAtWidth([]*Block{block}, query, width)
			if len(matches) != 1 || matches[0].InnerOffset != targetLine || matches[0].BlockID != block.ID {
				t.Fatalf("search = %+v, want block %d line %d", matches, block.ID, targetLine)
			}
			if got := blockCopyBody(block); got != content || block.Content != content {
				t.Fatalf("source or card copy changed: %q", got)
			}
		})
	}
}
