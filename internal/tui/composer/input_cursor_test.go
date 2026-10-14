package composer

import (
	"strings"
	"testing"

	"github.com/keakon/bubbles/v2/textarea"
)

func TestInputCursorPositionAcrossWrappedLines(t *testing.T) {
	for _, width := range []int{3, 10, 18} {
		in := New(textarea.DefaultDarkStyles(), nil)
		in.SetWidth(width)
		in.SetValue(strings.Repeat("a", in.inputContentWidth()) + "\n\n" + strings.Repeat("b", in.inputContentWidth()*3) + "\nfinal")
		in.SyncHeight()
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
	in := New(textarea.DefaultDarkStyles(), nil)
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
