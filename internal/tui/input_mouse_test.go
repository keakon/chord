package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestInputDoubleClickSelectsWord(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	m.mode = ModeInsert
	m.input.SetValue("alpha beta gamma")
	m.recalcViewportSize()
	m.layout = m.generateLayout(m.width, m.height)

	click := tea.MouseClickMsg{
		X:      m.layout.input.Min.X + inputPromptWidth + len("alpha ") + 1,
		Y:      m.layout.input.Min.Y + 1,
		Button: tea.MouseLeft,
	}
	_ = m.handleMouseMsg(click)
	_ = m.handleMouseMsg(click)

	if got := m.input.SelectionText(); got != "beta" {
		t.Fatalf("selected text = %q, want beta", got)
	}
	if m.inputMouseDown {
		t.Fatal("double-click selection should not remain in dragging state")
	}
}

func TestInputTripleClickSelectsLine(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	m.mode = ModeInsert
	m.input.SetValue("alpha beta gamma")
	m.recalcViewportSize()
	m.layout = m.generateLayout(m.width, m.height)

	click := tea.MouseClickMsg{
		X:      m.layout.input.Min.X + inputPromptWidth + len("alpha ") + 1,
		Y:      m.layout.input.Min.Y + 1,
		Button: tea.MouseLeft,
	}
	for i := 0; i < 3; i++ {
		_ = m.handleMouseMsg(click)
	}

	if got := m.input.SelectionText(); got != "alpha beta gamma" {
		t.Fatalf("selected text = %q, want full line", got)
	}
	if m.inputMouseDown {
		t.Fatal("triple-click selection should not remain in dragging state")
	}
}
