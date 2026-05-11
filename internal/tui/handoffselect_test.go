package tui

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestHandoffSelectOptionIndexAtUsesListBaseRow(t *testing.T) {
	backend := &sessionControlAgent{
		availableAgents: []string{"builder", "reviewer", "qa"},
	}
	m := NewModelWithSize(backend, 120, 24)
	m.openHandoffSelect("docs/plans/example.md")
	if m.mode != ModeHandoffSelect {
		t.Fatalf("mode after open = %v, want ModeHandoffSelect", m.mode)
	}
	if m.handoffSelect.selector.list == nil {
		t.Fatal("expected handoff overlay list")
	}
	_ = m.renderHandoffSelectDialog()

	dialogRect := m.overlayRect(m.renderHandoffSelectDialog())
	x := dialogRect.Min.X + 2
	y := dialogRect.Min.Y + 1 + 3 // title + blank + prefix line
	idx, ok := m.handoffSelectOptionIndexAt(x, y)
	if !ok {
		t.Fatal("expected hit test to resolve first list row")
	}
	if idx != 0 {
		t.Fatalf("hit-test index = %d, want 0", idx)
	}
}

func TestHandoffSelectOptionIndexAtAccountsForScrollWindowStart(t *testing.T) {
	agents := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		agents = append(agents, fmt.Sprintf("agent-%02d", i))
	}
	backend := &sessionControlAgent{availableAgents: agents}

	// Height chosen so handoffSelectMaxVisible() clamps to 3.
	m := NewModelWithSize(backend, 120, 16)
	m.openHandoffSelect("docs/plans/example.md")
	if m.handoffSelect.selector.list == nil {
		t.Fatal("expected handoff overlay list")
	}
	m.handoffSelect.selector.list.SetCursor(11)
	_ = m.renderHandoffSelectDialog()

	start, end := m.handoffSelect.selector.list.WindowRange()
	if end-start != 3 {
		t.Fatalf("visible window = %d, want 3", end-start)
	}
	if start == 0 {
		t.Fatal("expected list to be scrolled")
	}

	dialogRect := m.overlayRect(m.renderHandoffSelectDialog())
	x := dialogRect.Min.X + 2
	y := dialogRect.Min.Y + 1 + 3 // first visible row
	idx, ok := m.handoffSelectOptionIndexAt(x, y)
	if !ok {
		t.Fatal("expected hit test to resolve first visible list row")
	}
	if idx != start {
		t.Fatalf("hit-test index = %d, want %d (window start)", idx, start)
	}
}

func TestHandoffSelectModalMouseWheelMovesCursor(t *testing.T) {
	backend := &sessionControlAgent{availableAgents: []string{"builder", "reviewer", "qa"}}
	m := NewModelWithSize(backend, 120, 24)
	m.openHandoffSelect("docs/plans/example.md")
	m.layout = m.generateLayout(m.width, m.height)

	cmd, handled := m.handleModalMouseMsg(tea.MouseWheelMsg{X: 1, Y: 1, Button: tea.MouseWheelDown})
	if !handled {
		t.Fatal("handoff select wheel was not handled")
	}
	if cmd != nil {
		t.Fatalf("wheel returned cmd %#v, want nil", cmd)
	}
	if got := m.handoffSelect.selector.list.CursorAt(); got != 2 {
		t.Fatalf("cursor after wheel down = %d, want 2", got)
	}
}

func TestHandoffSelectModalMouseClickUpdatesCursorAndReturnsCommand(t *testing.T) {
	backend := &sessionControlAgent{availableAgents: []string{"builder", "reviewer", "qa"}}
	m := NewModelWithSize(backend, 120, 24)
	m.openHandoffSelect("docs/plans/example.md")
	m.layout = m.generateLayout(m.width, m.height)
	_ = m.renderHandoffSelectDialog()
	dialogRect := m.overlayRect(m.renderHandoffSelectDialog())
	clickX := dialogRect.Min.X + 2
	clickY := dialogRect.Min.Y + 1 + m.handoffSelect.selector.listBaseRow + 1

	cmd, handled := m.handleModalMouseMsg(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	if !handled {
		t.Fatal("handoff select click was not handled")
	}
	_ = cmd
	if got := m.handoffSelect.selector.list.CursorAt(); got != 1 {
		t.Fatalf("cursor after click = %d, want 1", got)
	}
}
