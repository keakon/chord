package tui

import (
	"strings"

	"github.com/keakon/x/ansi"
)

const inputPromptWidth = 2

// Endpoints index runes in the display buffer, independent of wrapping and scroll.
type inputSelection struct {
	active       bool
	anchor, head int
}

func (s inputSelection) empty() bool { return !s.active || s.anchor == s.head }

func (i *Input) ClearSelection() {
	if i.selection.active {
		i.interactionVersion++
	}
	i.selection = inputSelection{}
}

func (i *Input) HasSelection() bool             { return !i.selection.empty() }
func (i *Input) SelectionState() inputSelection { return i.selection }

func (i *Input) StartSelection(offset int) {
	offset = i.graphemeBoundary(offset, false)
	i.selection = inputSelection{active: true, anchor: offset, head: offset}
	i.interactionVersion++
	i.setCursorRuneOffset(offset)
	i.ensureCursorOutsideInlinePastes()
}

func (i *Input) UpdateSelection(offset int) {
	if !i.selection.active {
		i.StartSelection(offset)
		return
	}
	offset = i.graphemeBoundary(offset, offset > i.selection.anchor)
	if i.selection.head != offset {
		i.interactionVersion++
	}
	i.selection.head = offset
	start, end, selected := i.SelectionRange()
	cursor := offset
	if selected {
		cursor = end
		if offset < i.selection.anchor {
			cursor = start
		}
	}
	i.setCursorRuneOffset(cursor)
}

// SelectionRange expands non-empty intersections to complete graphemes and objects.
func (i *Input) SelectionRange() (start, end int, ok bool) {
	if !i.HasSelection() {
		return 0, 0, false
	}
	start, end = min(i.selection.anchor, i.selection.head), max(i.selection.anchor, i.selection.head)
	start = i.graphemeBoundary(start, false)
	end = i.graphemeBoundary(end, true)
	for _, paste := range i.inlinePastes {
		if start < paste.End && end > paste.Start {
			start, end = min(start, paste.Start), max(end, paste.End)
		}
	}
	return start, end, start < end
}

func (i *Input) SelectRuneRange(start, end int) {
	i.StartSelection(start)
	i.UpdateSelection(end)
}

func (i *Input) SelectionText() string {
	start, end, ok := i.SelectionRange()
	if !ok {
		return ""
	}
	return string([]rune(i.DisplayValue())[start:end])
}

func (i *Input) ViewWithSelection() string {
	view := i.textarea.View()
	start, end, ok := i.SelectionRange()
	if !ok {
		return view
	}
	lines := splitRenderedLines(view)
	rows := i.selectionDisplayRows()
	offset := i.ScrollYOffset()
	for y := 0; y < len(lines) && y < i.Height() && y+offset < len(rows); y++ {
		row := rows[y+offset]
		from, to := max(start, row.start), min(end, row.end)
		if from >= to {
			continue
		}
		content := []rune(row.text)
		x1 := ansi.StringWidth(string(content[:from-row.start]))
		x2 := ansi.StringWidth(string(content[:to-row.start]))
		lines[y] = highlightInputColumns(lines[y], inputPromptWidth+x1, inputPromptWidth+x2)
	}
	out := strings.Join(lines, "\n")
	if strings.HasSuffix(view, "\n") {
		out += "\n"
	}
	return out
}

// DecodeSequence advances by whole graphemes while keeping the original ANSI
// bytes and surface styles. Rune-width slicing would split combining emoji.
func highlightInputColumns(line string, from, to int) string {
	start, end, column, offset := -1, -1, 0, 0
	var state byte
	for offset < len(line) {
		_, width, n, next := ansi.DecodeSequence(line[offset:], state, nil)
		if n == 0 {
			break
		}
		state = next
		if width > 0 {
			if column >= to {
				break
			}
			if column >= from {
				if start < 0 {
					start = offset
				}
				end = offset + n
			}
			column += width
		} else if start >= 0 {
			end = offset + n
		}
		offset += n
	}
	if start < 0 || end <= start {
		return line
	}
	const on, off = "\x1b[7m", "\x1b[27m"
	selected := ansiSGRRegex.ReplaceAllString(line[start:end], "$0"+on)
	return line[:start] + on + selected + off + line[end:]
}

func (i *Input) totalDisplayLineCount() int {
	return len(i.selectionDisplayRows())
}

func (i *Input) inputContentWidth() int { return max(i.textarea.Width(), 1) }

func splitRenderedLines(view string) []string {
	return strings.Split(strings.TrimSuffix(view, "\n"), "\n")
}
