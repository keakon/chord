package tui

import (
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"
)

func TestViewPreservesSoftWrappedInputHeight(t *testing.T) {
	m := NewModelWithSize(nil, 24, 12)
	m.input.SetValue(strings.Repeat("x", 57))
	m.input.SyncHeight()

	wantHeight := m.input.ClampedDisplayLineCount()
	if wantHeight < 2 {
		t.Fatalf("precondition failed: ClampedDisplayLineCount() = %d, want >= 2", wantHeight)
	}
	wantCursorRow := m.input.Cursor().Y + m.input.ScrollYOffset()

	view := m.View()
	if got := m.input.Height(); got != wantHeight {
		t.Fatalf("textarea.Height() after View = %d, want %d", got, wantHeight)
	}
	if got := m.input.ScrollYOffset(); got != 0 {
		t.Fatalf("ScrollYOffset() after View = %d, want 0 when all wrapped rows fit", got)
	}
	if view.Cursor == nil {
		t.Fatal("View().Cursor = nil, want visible input cursor")
	}
	if got := view.Cursor.Y - (m.layout.input.Min.Y + 1); got != wantCursorRow {
		t.Fatalf("cursor visual row after View = %d, want %d", got, wantCursorRow)
	}
}

func TestManualNewlineAfterSoftWrapRemainsVisibleAfterView(t *testing.T) {
	m := NewModelWithSize(nil, 24, 12)
	m.input.SetValue(strings.Repeat("x", 41))
	m.input.SyncHeight()
	_ = m.View() // exercise the render-path safety net before inserting a newline

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift}))

	if got := m.input.Line(); got != 1 {
		t.Fatalf("input.Line() after shift+enter = %d, want 1", got)
	}
	if got := m.input.Column(); got != 0 {
		t.Fatalf("input.Column() after shift+enter = %d, want 0", got)
	}

	wantHeight := m.input.ClampedDisplayLineCount()
	wantCursorRow := m.input.Cursor().Y + m.input.ScrollYOffset()
	view := m.View()

	if got := m.input.Height(); got != wantHeight {
		t.Fatalf("textarea.Height() after newline View = %d, want %d", got, wantHeight)
	}
	if got := m.input.ScrollYOffset(); got != 0 {
		t.Fatalf("ScrollYOffset() after newline View = %d, want 0 when wrapped text + new line fit", got)
	}
	if view.Cursor == nil {
		t.Fatal("View().Cursor = nil after newline, want visible input cursor")
	}
	if got := view.Cursor.Y - (m.layout.input.Min.Y + 1); got != wantCursorRow {
		t.Fatalf("cursor visual row after newline View = %d, want %d", got, wantCursorRow)
	}
}
