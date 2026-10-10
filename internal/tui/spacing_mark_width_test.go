package tui

import (
	"fmt"
	"strings"
	"testing"
	"unicode"

	"github.com/keakon/x/ansi"
)

func TestSpacingMarkWidthContract(t *testing.T) {
	for _, text := range []string{"\u092a\u0949", "a\u0a3e", "a\u093e", "a\u093e\u093e", "\u0e19\u0e49\u0e33", "\u0e99\u0eb3"} {
		t.Run(fmt.Sprintf("%U", []rune(text)), func(t *testing.T) {
			if got := tuiStringWidth(text); got != 2 {
				t.Fatalf("width of %q = %d, want 2", text, got)
			}
			if got := tuiCut(text+"x", 0, 1); got != "" {
				t.Fatalf("one-column cut included a two-column cluster: %q", got)
			}
			if got := tuiCut(text+"x", 0, 2); got != text {
				t.Fatalf("two-column cut = %q, want intact %q", got, text)
			}
			for _, line := range tuiHardwrap(strings.Repeat(text, 4), 5) {
				if got := spacingFixtureTerminalWidth(line); got > 5 {
					t.Fatalf("terminal width of wrapped line %q = %d", line, got)
				}
			}
		})
	}
}

// The fixture contains ASCII and spacing marks, without emoji. Its terminal
// advance is measured independently from GraphemeWidth: multiple spacing marks
// share a two-column cell rather than adding a column each.
func spacingFixtureTerminalWidth(text string) int {
	var width int
	for len(text) > 0 {
		cluster, _ := ansi.FirstGraphemeCluster(text, ansi.GraphemeWidth)
		if cluster == "" {
			break
		}
		spacing := false
		for i, r := range cluster {
			if i > 0 && (unicode.Is(unicode.Mc, r) || r == '\u0e33' || r == '\u0eb3') {
				spacing = true
			}
		}
		if spacing {
			width += 2
		} else {
			width += ansi.WcWidth.StringWidth(cluster)
		}
		text = text[len(cluster):]
	}
	return width
}

func TestSpacingMarksKeepCodeAndSidebarAligned(t *testing.T) {
	ApplyTheme(DefaultTheme())
	for _, width := range []int{100, 160, 215, 240} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("width_%d/streaming_%t", width, streaming), func(t *testing.T) {
				m := NewModelWithSize(newInfoPanelAgent(), width, 61)
				m.rightPanelVisible = true
				m.mode = ModeInsert
				m.recalcViewportSize()
				block := &Block{ID: 1, Type: BlockAssistant, Streaming: streaming}
				m.viewport.AppendBlock(block)
				var checked int
				baseline := cloneScreenLines(nil, nil)
				for _, text := range []string{"ab", "\u092a\u0949", "a\u0a3e", "a\u093e\u093e", "\u0e19\u0e49\u0e33", "\u0e99\u0eb3", "ab"} {
					block.Content = strings.Repeat("sample "+text+" ", 15) + "\n\n```text\n" + strings.Repeat("entry "+text+" ", 15) + "\n```"
					// Replacing content must invalidate settled streaming prefixes too.
					block.InvalidateStreamingSettledCache()
					block.InvalidateCache()
					m.viewport.UpdateBlock(block.ID)
					m.viewport.offset = 0
					first := m.View().Content
					if second := m.View().Content; second != first {
						t.Fatal("cached frame differs from initial frame")
					}
					if baseline == nil {
						baseline = cloneScreenLines(nil, m.screenBuf.Lines)
					}
					for y, line := range m.screenBuf.Lines {
						terminalX := 0
						for x, cell := range line {
							// Wide-cell tails inherit the lead cell's background in
							// the terminal. Compare effective surfaces with the
							// equal-width ASCII frame, including code and panel edges.
							bg := cell.Style.Bg
							if cell.Content == "" && x > 0 && line[x-1].Width == 2 {
								bg = line[x-1].Style.Bg
							}
							if want := baseline[y][x].Style.Bg; !bg.Equal(want) {
								t.Fatalf("row %d, column %d: spacing text moved the background edge", y, x)
							}
							if x == m.layout.infoPanel.Min.X && terminalX != x {
								t.Fatalf("row %d: sidebar starts at terminal column %d, want %d", y, terminalX, x)
							}
							if cell.Content == "" {
								continue
							}
							advance := spacingFixtureTerminalWidth(cell.Content)
							if strings.Contains(cell.Content, text) && text != "ab" {
								checked++
								if cell.Width != 2 || advance != 2 {
									t.Fatalf("row %d, column %d: %q occupies %d model/%d terminal columns", y, x, cell.Content, cell.Width, advance)
								}
							}
							terminalX += advance
						}
						if terminalX > width {
							t.Fatalf("row %d exceeds terminal width: %d > %d", y, terminalX, width)
						}
					}
				}
				if checked == 0 {
					t.Fatal("no spacing-mark cells were rendered")
				}
			})
		}
	}
}

func TestSpacingMarksKeepInputCursorAndRowsAligned(t *testing.T) {
	const glyph = "a\u093e\u093e"
	in := NewInput()
	in.SetWidth(6) // Two prompt columns and four content columns.
	in.SetValue(glyph + glyph)
	in.syncHeight()
	rows := in.selectionDisplayRows()
	if len(rows) != 2 || rows[0].text != glyph+glyph || rows[1].text != "" {
		t.Fatalf("wrapped rows = %#v", rows)
	}
	in.SetCursorPosition(0, 3)
	in.Focus()
	cursor := in.Cursor()
	if cursor == nil || cursor.Position.X != inputPromptWidth+2 || cursor.Position.Y != 0 {
		t.Fatalf("cursor = %#v, want (%d, 0)", cursor, inputPromptWidth+2)
	}
	if offset, ok := in.SelectionPositionAt(inputPromptWidth+3, 0); !ok || offset != 3 {
		t.Fatalf("second cluster hit = %d, %v", offset, ok)
	}
}

func TestSpacingMarksKeepTablePaddingAligned(t *testing.T) {
	const glyph = "a\u0cf3"
	if got := formatTableCell(glyph, 4, 0); got != glyph+"  " {
		t.Fatalf("padded cell = %q", got)
	}
}
