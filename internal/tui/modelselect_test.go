package tui

import (
	"strings"
	"testing"

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
