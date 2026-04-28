package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSyncHeightGrowsForSoftWrappedSingleLine(t *testing.T) {
	in := NewInput()
	in.SetWidth(20)
	in.SetValue(strings.Repeat("x", 80))
	in.syncHeight()
	if got := in.textarea.Height(); got < 2 {
		t.Fatalf("textarea.Height() = %d, want >= 2 after soft wrap", got)
	}
}

func TestClampedDisplayLineCountMatchesWrapRows(t *testing.T) {
	in := NewInput()
	in.SetWidth(10)
	in.SetValue("abcdefghijklmnop")
	// 16 latin letters at width 10 → 2 visual rows minimum
	if got := in.ClampedDisplayLineCount(); got < 2 {
		t.Fatalf("ClampedDisplayLineCount() = %d, want >= 2", got)
	}
}

func TestViewPreservesSoftWrappedInputHeight(t *testing.T) {
	m := NewModelWithSize(nil, 24, 12)
	m.input.SetValue(strings.Repeat("x", 57))
	m.input.syncHeight()

	wantHeight := m.input.ClampedDisplayLineCount()
	if wantHeight < 2 {
		t.Fatalf("precondition failed: ClampedDisplayLineCount() = %d, want >= 2", wantHeight)
	}
	wantCursorRow := m.input.visualCursorRow()

	view := m.View()
	if got := m.input.textarea.Height(); got != wantHeight {
		t.Fatalf("textarea.Height() after View = %d, want %d", got, wantHeight)
	}
	if got := m.input.textarea.ScrollYOffset(); got != 0 {
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
	m.input.syncHeight()
	_ = m.View() // exercise the render-path safety net before inserting a newline

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift}))

	if got := m.input.Line(); got != 1 {
		t.Fatalf("input.Line() after shift+enter = %d, want 1", got)
	}
	if got := m.input.Column(); got != 0 {
		t.Fatalf("input.Column() after shift+enter = %d, want 0", got)
	}

	wantHeight := m.input.ClampedDisplayLineCount()
	wantCursorRow := m.input.visualCursorRow()
	view := m.View()

	if got := m.input.textarea.Height(); got != wantHeight {
		t.Fatalf("textarea.Height() after newline View = %d, want %d", got, wantHeight)
	}
	if got := m.input.textarea.ScrollYOffset(); got != 0 {
		t.Fatalf("ScrollYOffset() after newline View = %d, want 0 when wrapped text + new line fit", got)
	}
	if view.Cursor == nil {
		t.Fatal("View().Cursor = nil after newline, want visible input cursor")
	}
	if got := view.Cursor.Y - (m.layout.input.Min.Y + 1); got != wantCursorRow {
		t.Fatalf("cursor visual row after newline View = %d, want %d", got, wantCursorRow)
	}
}
