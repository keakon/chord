package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
)

func TestPoolSelectCursorNavigation(t *testing.T) {
	t.Parallel()
	m := benchmarkModelForView()
	m.mode = ModeModelSelect
	m.modelSelect = modelSelectState{
		poolNames:  []string{"alpha", "beta", "gamma"},
		poolCursor: 0,
		prevMode:   ModeNormal,
	}

	if m.modelSelect.poolCursor != 0 {
		t.Fatalf("initial cursor = %d, want 0", m.modelSelect.poolCursor)
	}

	m.modelSelect.poolCursor = 1
	if m.modelSelect.poolCursor != 1 {
		t.Fatalf("after j: cursor = %d, want 1", m.modelSelect.poolCursor)
	}

	m.modelSelect.poolCursor = 2
	if m.modelSelect.poolCursor != 2 {
		t.Fatalf("after second j: cursor = %d, want 2", m.modelSelect.poolCursor)
	}

	if len(m.modelSelect.poolNames) > 0 && m.modelSelect.poolCursor >= len(m.modelSelect.poolNames) {
		t.Fatalf("cursor should not exceed last pool index")
	}

	m.modelSelect.poolCursor = 1
	if m.modelSelect.poolCursor != 1 {
		t.Fatalf("after k: cursor = %d, want 1", m.modelSelect.poolCursor)
	}

	m.modelSelect.poolCursor = 0
	if m.modelSelect.poolCursor < 0 {
		t.Fatalf("cursor should not go below 0")
	}
}

func TestOpenModelSelectForAgentShowsTargetTitle(t *testing.T) {
	backend := &sessionControlAgent{
		focused: "reviewer",
		poolNamesByFocus: map[string][]string{
			"reviewer": {"thinking", "fast"},
		},
		currentPoolByFocus: map[string]string{
			"reviewer": "thinking",
		},
	}
	m := NewModelWithSize(backend, 120, 24)

	m.openModelSelectFor(agent.ModelPoolSelectorTarget{Kind: agent.ModelPoolSelectorTargetAgentOverride, AgentName: "reviewer"})

	if got := m.modelSelect.poolCursor; got != 0 {
		t.Fatalf("poolCursor = %d, want 0 for current pool", got)
	}
	plain := stripANSI(m.renderModelSelectDialog())
	if !strings.Contains(plain, "reviewer Model Pool") {
		t.Fatalf("dialog title missing agent target:\n%s", plain)
	}
}

func TestOpenModelSelectForAgentUsesFirstPoolWhenUnset(t *testing.T) {
	backend := &sessionControlAgent{
		focused: "reviewer",
		poolNamesByFocus: map[string][]string{
			"reviewer": {"thinking", "fast"},
		},
	}
	m := NewModelWithSize(backend, 120, 24)

	m.openModelSelectFor(agent.ModelPoolSelectorTarget{Kind: agent.ModelPoolSelectorTargetAgentOverride, AgentName: "reviewer"})

	if got := m.modelSelect.poolCursor; got != 0 {
		t.Fatalf("poolCursor = %d, want 0 for first/default pool", got)
	}
	plain := stripANSI(m.renderModelSelectDialog())
	if !strings.Contains(plain, "reviewer Model Pool") {
		t.Fatalf("dialog title missing agent target:\n%s", plain)
	}
	if strings.Contains(plain, "Restore default") {
		t.Fatalf("dialog should not include restore default:\n%s", plain)
	}
}

func TestPoolSelectIndexAtUsesListBaseRow(t *testing.T) {
	backend := &sessionControlAgent{mainModelPoolNames: []string{"alpha", "beta", "gamma"}, mainModelPool: "alpha"}
	m := NewModelWithSize(backend, 120, 24)
	m.openModelSelectFor(agent.ModelPoolSelectorTarget{Kind: agent.ModelPoolSelectorTargetMainRole})
	_ = m.renderModelSelectDialog()

	dialogRect := m.overlayRect(m.renderModelSelectDialog())
	x := dialogRect.Min.X + 2
	y := dialogRect.Min.Y + 1 + 2 // title + blank
	idx, ok := m.poolSelectIndexAt(x, y)
	if !ok {
		t.Fatal("expected hit test to resolve first list row")
	}
	if idx != 0 {
		t.Fatalf("hit-test index = %d, want 0", idx)
	}
}

func TestPoolSelectIndexAtAccountsForScrollWindowStart(t *testing.T) {
	pools := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		pools = append(pools, fmt.Sprintf("pool-%02d", i))
	}
	backend := &sessionControlAgent{mainModelPoolNames: pools, mainModelPool: pools[0]}

	// Height chosen so modelSelectMaxVisible() clamps to 3.
	m := NewModelWithSize(backend, 120, 16)
	m.openModelSelectFor(agent.ModelPoolSelectorTarget{Kind: agent.ModelPoolSelectorTargetMainRole})
	if m.modelSelect.selector.list == nil {
		t.Fatal("expected modelSelect.list to be initialized")
	}
	m.modelSelect.selector.list.SetCursor(11)
	m.modelSelect.poolCursor = 11
	_ = m.renderModelSelectDialog()

	start, end := m.modelSelect.selector.list.WindowRange()
	if end-start != 3 {
		t.Fatalf("visible window = %d, want 3", end-start)
	}
	if start == 0 {
		t.Fatal("expected list to be scrolled")
	}

	dialogRect := m.overlayRect(m.renderModelSelectDialog())
	x := dialogRect.Min.X + 2
	y := dialogRect.Min.Y + 1 + 2 // first visible row
	idx, ok := m.poolSelectIndexAt(x, y)
	if !ok {
		t.Fatal("expected hit test to resolve first visible list row")
	}
	if idx != start {
		t.Fatalf("hit-test index = %d, want %d (window start)", idx, start)
	}
}

func TestModelSelectModalMouseWheelMovesCursor(t *testing.T) {
	backend := &sessionControlAgent{mainModelPoolNames: []string{"alpha", "beta", "gamma"}, mainModelPool: "alpha"}
	m := NewModelWithSize(backend, 120, 24)
	m.openModelSelectFor(agent.ModelPoolSelectorTarget{Kind: agent.ModelPoolSelectorTargetMainRole})
	m.layout = m.generateLayout(m.width, m.height)

	cmd, handled := m.handleModalMouseMsg(tea.MouseWheelMsg{X: 1, Y: 1, Button: tea.MouseWheelDown})
	if !handled {
		t.Fatal("model select wheel was not handled")
	}
	if cmd != nil {
		t.Fatalf("wheel returned cmd %#v, want nil", cmd)
	}
	if got := m.modelSelect.poolCursor; got != 2 {
		t.Fatalf("poolCursor after wheel down = %d, want 2", got)
	}
}

func TestModelSelectModalMouseClickSelectsPool(t *testing.T) {
	backend := &sessionControlAgent{mainModelPoolNames: []string{"alpha", "beta", "gamma"}, mainModelPool: "alpha"}
	m := NewModelWithSize(backend, 120, 24)
	m.openModelSelectFor(agent.ModelPoolSelectorTarget{Kind: agent.ModelPoolSelectorTargetMainRole})
	m.layout = m.generateLayout(m.width, m.height)
	_ = m.renderModelSelectDialog()
	dialogRect := m.overlayRect(m.renderModelSelectDialog())
	clickX := dialogRect.Min.X + 2
	clickY := dialogRect.Min.Y + 1 + m.modelSelect.selector.listBaseRow + 1

	cmd, handled := m.handleModalMouseMsg(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	if !handled {
		t.Fatal("model select click was not handled")
	}
	if cmd == nil {
		t.Fatal("model select click should return selection command")
	}
	if got := m.modelSelect.poolCursor; got != 1 {
		t.Fatalf("poolCursor after click = %d, want 1", got)
	}
}
