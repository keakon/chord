package tui

import (
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"
)

func TestInputCursorPositionAcrossWrappedLines(t *testing.T) {
	for _, width := range []int{3, 10, 18} {
		in := NewInput()
		in.SetWidth(width)
		in.SetValue(strings.Repeat("a", in.inputContentWidth()) + "\n\n" + strings.Repeat("b", in.inputContentWidth()*3) + "\nfinal")
		in.syncHeight()
		for _, pos := range [][2]int{{0, 0}, {3, 2}, {1, 0}, {2, 1}, {0, 4}, {3, 0}, {-1, -1}, {99, 99}} {
			in.SetCursorPosition(pos[0], pos[1])
			row := min(max(pos[0], 0), 3)
			line := strings.Split(in.Value(), "\n")[row]
			col := min(max(pos[1], 0), len([]rune(line)))
			if in.Line() != row || in.Column() != col || in.textarea.HasSelection() {
				t.Fatalf("width=%d position=%v: cursor=%d:%d, want=%d:%d", width, pos, in.Line(), in.Column(), row, col)
			}
		}
	}
}

func TestInputSelectionEditAfterExactlyWrappedLine(t *testing.T) {
	in := NewInput()
	in.SetWidth(10)
	first := strings.Repeat("a", in.inputContentWidth())
	in.SetValue(first + "\nnext\nlast")
	in.SetCursorPosition(0, 0)
	start := len(first) + 1
	in.SelectRuneRange(start, start+2)
	if in.SelectionText() != "ne" {
		t.Fatalf("selection = %q", in.SelectionText())
	}
	in.ReplaceSelection("x")
	if in.Value() != first+"\nxxt\nlast" || in.Line() != 1 || in.Column() != 1 {
		t.Fatalf("edit = %q at %d:%d", in.Value(), in.Line(), in.Column())
	}
}

func TestComposerClipboardRejectsVerticalNavigation(t *testing.T) {
	for _, move := range []struct {
		name string
		code rune
		row  int
	}{{"up", tea.KeyUp, 0}, {"down", tea.KeyDown, 2}} {
		t.Run(move.name, func(t *testing.T) {
			m := NewModelWithSize(nil, 80, 24)
			m.mode = ModeInsert
			m.input.SetValue("first\nsecond\nthird")
			m.input.SetCursorPosition(1, 2)
			before := m.input.Value()
			target := m.composerClipboardTarget()
			m.handleInsertKey(tea.KeyPressMsg{Code: move.code})
			if m.input.Line() != move.row {
				t.Fatalf("cursor row = %d, want %d", m.input.Line(), move.row)
			}
			if cmd := m.handleComposerClipboardText(composerClipboardTextMsg{target: target, text: "sample"}); cmd == nil || m.input.Value() != before {
				t.Fatalf("stale clipboard changed input: %q", m.input.Value())
			}
		})
	}
}
