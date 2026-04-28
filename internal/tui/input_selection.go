package tui

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
	"github.com/rivo/uniseg"
)

const inputPromptWidth = 2

type inputSelection struct {
	active              bool
	startLine, startCol int
	endLine, endCol     int
}

func (s inputSelection) empty() bool {
	return !s.active || (s.startLine == s.endLine && s.startCol == s.endCol)
}

func (s inputSelection) normalized() inputSelection {
	if !s.active {
		return s
	}
	if s.endLine < s.startLine || (s.endLine == s.startLine && s.endCol < s.startCol) {
		s.startLine, s.endLine = s.endLine, s.startLine
		s.startCol, s.endCol = s.endCol, s.startCol
	}
	return s
}

func (i *Input) ClearSelection() {
	i.selection = inputSelection{}
}

func (i *Input) HasSelection() bool {
	return !i.selection.empty()
}

func (i *Input) SelectionState() inputSelection {
	return i.selection
}

func (i *Input) StartSelection(line, col int) {
	i.selection = inputSelection{
		active:    true,
		startLine: line,
		startCol:  col,
		endLine:   line,
		endCol:    col,
	}
}

func (i *Input) UpdateSelection(line, col int) {
	if !i.selection.active {
		i.StartSelection(line, col)
		return
	}
	i.selection.endLine = line
	i.selection.endCol = col
}

// SelectionPointAt maps a visible input row and content column to a clamped
// selection point. The returned column is relative to the content area (prompt excluded).
func (i *Input) SelectionPointAt(displayLine, contentCol int) (line, col int, ok bool) {
	content, ok := i.visibleContentLine(displayLine)
	if !ok {
		return 0, 0, false
	}
	if contentCol < 0 {
		contentCol = 0
	}
	width := ansi.StringWidth(content)
	if contentCol > width {
		contentCol = width
	}
	return displayLine, contentCol, true
}

// ViewWithSelection renders the textarea and overlays a reverse-video highlight
// for the current visible selection, if any.
func (i *Input) ViewWithSelection() string {
	view := i.textarea.View()
	if !i.HasSelection() {
		return view
	}
	lines := splitRenderedLines(view)
	visibleCount := i.visibleContentLineCount()
	sel := i.selection.normalized()
	for idx := 0; idx < len(lines) && idx < visibleCount; idx++ {
		colStart, colEnd, inRange := inputSelectionColRange(idx, sel)
		if !inRange {
			continue
		}
		lines[idx] = applyHighlightToLine(lines[idx], inputPromptWidth+colStart, inputPromptWidth+colEnd)
	}
	out := strings.Join(lines, "\n")
	if strings.HasSuffix(view, "\n") {
		out += "\n"
	}
	return out
}

// SelectionText returns the selected text from the currently visible input content.
// The minimal input-selection feature is intentionally view-based: wrapped lines are
// copied as visible lines, rather than reconstructing the underlying unwrapped buffer.
func (i *Input) SelectionText() string {
	if !i.HasSelection() {
		return ""
	}
	sel := i.selection.normalized()
	var parts []string
	for line := sel.startLine; line <= sel.endLine; line++ {
		content, ok := i.visibleContentLine(line)
		if !ok {
			continue
		}
		colStart, colEnd, inRange := inputSelectionColRange(line, sel)
		if !inRange {
			continue
		}
		width := ansi.StringWidth(content)
		if colStart > width {
			colStart = width
		}
		if colEnd > width {
			colEnd = width
		}
		segment := extractPlainByColumns(content, colStart, colEnd)
		segment = strings.TrimRight(segment, " ")
		if line != sel.startLine || line != sel.endLine || segment != "" {
			parts = append(parts, segment)
		}
	}
	return strings.TrimRight(strings.Join(parts, "\n"), "\n")
}

func (i *Input) visibleContentLine(displayLine int) (string, bool) {
	lines := i.visibleWrappedContentLines()
	if displayLine < 0 || displayLine >= len(lines) {
		return "", false
	}
	return strings.TrimRight(lines[displayLine], " "), true
}

func (i *Input) visibleContentLineCount() int {
	total := i.totalDisplayLineCount()
	offset := i.textarea.ScrollYOffset()
	if total <= offset {
		return 0
	}
	visible := total - offset
	if height := i.textarea.Height(); visible > height {
		visible = height
	}
	return visible
}

func (i *Input) totalDisplayLineCount() int {
	lines := i.wrappedContentLines()
	if len(lines) == 0 {
		return 1
	}
	return len(lines)
}

func (i *Input) visibleWrappedContentLines() []string {
	all := i.wrappedContentLines()
	if len(all) == 0 {
		return []string{""}
	}
	offset := i.textarea.ScrollYOffset()
	if offset < 0 {
		offset = 0
	}
	if offset >= len(all) {
		return []string{""}
	}
	visible := len(all) - offset
	if height := i.textarea.Height(); visible > height {
		visible = height
	}
	return all[offset : offset+visible]
}

func (i *Input) wrappedContentLines() []string {
	width := i.inputContentWidth()
	rawLines := strings.Split(i.textarea.Value(), "\n")
	if len(rawLines) == 0 {
		return []string{""}
	}
	out := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		wrapped := inputWrap([]rune(line), width)
		if len(wrapped) == 0 {
			out = append(out, "")
			continue
		}
		for _, row := range wrapped {
			out = append(out, string(row))
		}
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

func (i *Input) inputContentWidth() int {
	width := i.textarea.Width()
	if width < 1 {
		return 1
	}
	return width
}

func splitRenderedLines(view string) []string {
	view = strings.TrimSuffix(view, "\n")
	if view == "" {
		return []string{""}
	}
	return strings.Split(view, "\n")
}

func inputSelectionColRange(line int, sel inputSelection) (colStart, colEnd int, inRange bool) {
	if !sel.active {
		return 0, 0, false
	}
	if line < sel.startLine || line > sel.endLine {
		return 0, 0, false
	}
	inRange = true
	if line == sel.startLine {
		colStart = sel.startCol
	}
	if line == sel.endLine {
		colEnd = sel.endCol
	} else {
		colEnd = 1 << 30
	}
	return colStart, colEnd, inRange
}

func inputWrap(runes []rune, width int) [][]rune {
	var (
		lines  = [][]rune{{}}
		word   []rune
		row    int
		spaces int
	)

	for _, r := range runes {
		if unicode.IsSpace(r) {
			spaces++
		} else {
			word = append(word, r)
		}

		if spaces > 0 {
			if uniseg.StringWidth(string(lines[row]))+uniseg.StringWidth(string(word))+spaces > width {
				row++
				lines = append(lines, []rune{})
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], inputRepeatSpaces(spaces)...)
				spaces = 0
				word = nil
			} else {
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], inputRepeatSpaces(spaces)...)
				spaces = 0
				word = nil
			}
		} else if len(word) > 0 {
			lastCharLen := runewidth.RuneWidth(word[len(word)-1])
			if uniseg.StringWidth(string(word))+lastCharLen > width {
				if len(lines[row]) > 0 {
					row++
					lines = append(lines, []rune{})
				}
				lines[row] = append(lines[row], word...)
				word = nil
			}
		}
	}

	if uniseg.StringWidth(string(lines[row]))+uniseg.StringWidth(string(word))+spaces >= width {
		lines = append(lines, []rune{})
		lines[row+1] = append(lines[row+1], word...)
		spaces++
		lines[row+1] = append(lines[row+1], inputRepeatSpaces(spaces)...)
	} else {
		lines[row] = append(lines[row], word...)
		spaces++
		lines[row] = append(lines[row], inputRepeatSpaces(spaces)...)
	}

	return lines
}

func inputRepeatSpaces(n int) []rune {
	if n <= 0 {
		return nil
	}
	return []rune(strings.Repeat(" ", n))
}
