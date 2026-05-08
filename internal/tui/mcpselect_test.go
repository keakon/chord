package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
)

func TestHandleMCPSelectKeyToggleKeepsPanelOpen(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "alpha", Disabled: true, Manual: true}}
	m := NewModelWithSize(backend, 100, 30)
	m.mode = ModeNormal
	m.openMCPSelect()

	if m.mode != ModeMCPSelect {
		t.Fatalf("mode after open = %v, want ModeMCPSelect", m.mode)
	}

	cmd := m.handleMCPSelectKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if cmd != nil {
		t.Fatal("toggle should not close overlay or emit follow-up command")
	}
	if got := len(backend.sentMessages); got != 1 {
		t.Fatalf("SendUserMessage() calls = %d, want 1", got)
	}
	if got := backend.sentMessages[0]; got != "/mcp toggle alpha" {
		t.Fatalf("sent message = %q, want %q", got, "/mcp toggle alpha")
	}
	if m.mode != ModeMCPSelect {
		t.Fatalf("mode after toggle = %v, want ModeMCPSelect", m.mode)
	}
	if m.mcpSelect.list == nil {
		t.Fatal("MCP overlay list should remain present after toggle")
	}
}

func TestHandleAgentEventEnvStatusUpdateRefreshesMCPSelectItems(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.mcpRows = []agent.MCPServerDisplay{
		{Name: "alpha", Disabled: true, Manual: true},
		{Name: "beta", OK: true, Manual: true},
	}
	m := NewModelWithSize(backend, 100, 30)
	m.openMCPSelect()
	if m.mcpSelect.list == nil {
		t.Fatal("expected MCP overlay list")
	}
	m.mcpSelect.list.SetCursor(1)

	backend.mcpRows = []agent.MCPServerDisplay{
		{Name: "alpha", Pending: true, Manual: true},
		{Name: "beta", OK: true, Manual: true},
	}
	cmd := m.handleAgentEvent(agentEventMsg{event: agent.EnvStatusUpdateEvent{}})
	applyTestCmd(t, &m, cmd)

	selected, ok := m.mcpSelect.list.SelectedItem()
	if !ok {
		t.Fatal("expected selected MCP item after refresh")
	}
	if selected.ID != "beta" {
		t.Fatalf("selected item after refresh = %q, want beta", selected.ID)
	}
	plain := stripANSI(m.renderMCPSelectDialog())
	if !strings.Contains(plain, "alpha — connecting") {
		t.Fatalf("rendered MCP dialog = %q, want updated connecting state", plain)
	}
	if !strings.Contains(plain, "esc close") {
		t.Fatalf("rendered MCP dialog = %q, want close hint", plain)
	}
}
