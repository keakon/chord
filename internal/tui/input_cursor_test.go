package tui

import (
	"testing"

	tea "github.com/keakon/bubbletea/v2"
)

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
