package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

type inputDisplayRow struct {
	start, end int
	text       string
}

// Word bounds use source offsets, so a ZWJ emoji before the clicked word cannot
// shift the hit through accumulated per-rune display widths.
func (i *Input) selectionWordRange(offset int, row inputDisplayRow) (int, int) {
	runes := []rune(i.DisplayValue())[row.start:row.end]
	if len(runes) == 0 {
		return offset, offset
	}
	index := min(max(offset-row.start, 0), len(runes)-1)
	if unicode.IsSpace(runes[index]) {
		return offset, offset
	}
	start, end := index, index+1
	for start > 0 && !unicode.IsSpace(runes[start-1]) {
		start--
	}
	for end < len(runes) && !unicode.IsSpace(runes[end]) {
		end++
	}
	return row.start + start, row.start + end
}

// Use the same wrapping as height measurement and textarea. Only the final
// cursor space is synthetic; source spaces remain part of the selectable row.
func (i *Input) selectionDisplayRows() []inputDisplayRow {
	value, width := i.DisplayValue(), i.inputContentWidth()
	if i.selectionRows != nil && i.selectionRowsValue == value && i.selectionRowsWidth == width {
		return i.selectionRows
	}
	var rows []inputDisplayRow
	base := 0
	for line := range strings.SplitSeq(value, "\n") {
		length := utf8.RuneCountInString(line)
		consumed := 0
		for _, wrapped := range inputWrap([]rune(line), width) {
			n := min(len(wrapped), length-consumed)
			rows = append(rows, inputDisplayRow{start: base + consumed, end: base + consumed + n, text: string(wrapped[:n])})
			consumed += n
		}
		base += length + 1
	}
	i.selectionRows, i.selectionRowsValue, i.selectionRowsWidth = rows, value, width
	return rows
}

// SelectionPositionAt accepts textarea-relative coordinates including its prompt.
// It rejects rows outside the rendered content instead of hitting nearby objects.
func (i *Input) SelectionPositionAt(x, y int) (int, bool) {
	rows := i.selectionDisplayRows()
	rowIndex := y + i.ScrollYOffset()
	if y < 0 || y >= i.Height() || rowIndex >= len(rows) {
		return 0, false
	}
	pos := i.textarea.PositionAt(inputPromptWidth, y)
	offset := runeOffsetFromRowCol(i.DisplayValue(), pos.Row, pos.Col)
	row := rows[rowIndex]
	// PositionAt counts widths per rune. Correct it using complete graphemes,
	// including cumulative widths after modifiers and ZWJ sequences.
	x = max(x-inputPromptWidth, 0)
	column, consumed := 0, 0
	g := uniseg.NewGraphemes(row.text)
	for g.Next() {
		width := ansi.StringWidth(g.Str())
		if column+width > x {
			return i.graphemeBoundary(offset+consumed, false), true
		}
		column += width
		consumed += utf8.RuneCountInString(g.Str())
	}
	return i.graphemeBoundary(row.end, false), true
}

// RunePositionAt returns a clamped insertion point and whether a source character
// was hit. Unlike selection mapping, padding must not resolve to an object.
func (i *Input) RunePositionAt(x, y int) (int, bool) {
	offset, ok := i.SelectionPositionAt(x, y)
	if !ok || x < inputPromptWidth {
		return offset, false
	}
	row := i.selectionDisplayRows()[y+i.ScrollYOffset()]
	return offset, x-inputPromptWidth < ansi.StringWidth(row.text)
}

func (i *Input) graphemeBoundary(offset int, forward bool) int {
	text := i.DisplayValue()
	offset = min(max(offset, 0), utf8.RuneCountInString(text))
	position := 0
	g := uniseg.NewGraphemes(text)
	for g.Next() {
		next := position + utf8.RuneCountInString(g.Str())
		if offset == position || offset == next {
			return offset
		}
		if offset < next {
			if forward {
				return next
			}
			return position
		}
		position = next
	}
	return offset
}

func (i *Input) setCursorRuneOffset(offset int) {
	row, col := rowColFromRuneOffset(i.DisplayValue(), offset)
	i.SetCursorPosition(row, col)
}
