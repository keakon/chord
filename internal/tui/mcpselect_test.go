package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
)

func markMCPModelBusy(m *Model) {
	m.activities["main"] = agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityStreaming}
}

func TestMCPShortcutOpensPanelDirectlyWhileBusy(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "alpha", OK: true, Manual: true}}
	m := NewModelWithSize(backend, 100, 30)
	m.mode = ModeNormal
	markMCPModelBusy(&m)

	cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: 'o', Mod: tea.ModCtrl}))
	if cmd != nil {
		t.Fatalf("ctrl+o should open MCP panel without command, got cmd %T", cmd)
	}
	if m.mode != ModeMCPSelect {
		t.Fatalf("mode after ctrl+o while busy = %v, want ModeMCPSelect", m.mode)
	}
	if got := len(backend.sentMessages); got != 0 {
		t.Fatalf("SendUserMessage() calls = %d, want 0", got)
	}
}

func TestMCPShortcutOpensPanelDirectlyFromInsertWhileBusy(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "alpha", OK: true, Manual: true}}
	m := NewModelWithSize(backend, 100, 30)
	m.mode = ModeInsert
	markMCPModelBusy(&m)

	cmd := m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'o', Mod: tea.ModCtrl}))
	if cmd != nil {
		t.Fatalf("ctrl+o should open MCP panel without command, got cmd %T", cmd)
	}
	if m.mode != ModeMCPSelect {
		t.Fatalf("mode after insert ctrl+o while busy = %v, want ModeMCPSelect", m.mode)
	}
	if got := len(backend.sentMessages); got != 0 {
		t.Fatalf("SendUserMessage() calls = %d, want 0", got)
	}
}

func TestRenderMCPSelectDialogShowsReadOnlyWhenBusy(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "alpha", OK: true, Manual: true}}
	m := NewModelWithSize(backend, 100, 30)
	markMCPModelBusy(&m)
	m.openMCPSelect()

	plain := stripANSI(m.renderMCPSelectDialog())
	if !strings.Contains(plain, "Read-only while agent is running") {
		t.Fatalf("rendered MCP dialog = %q, want read-only running hint", plain)
	}
	if !strings.Contains(plain, "enter/e/d disabled while running") {
		t.Fatalf("rendered MCP dialog = %q, want disabled button hint", plain)
	}
	if strings.Contains(plain, "enter toggle") {
		t.Fatalf("rendered MCP dialog = %q, should not show active toggle hint while busy", plain)
	}
}

func TestMCPSelectBusyRejectsToggleWithoutSendingCommand(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "alpha", OK: true, Manual: true}}
	m := NewModelWithSize(backend, 100, 30)
	markMCPModelBusy(&m)
	m.openMCPSelect()

	cmd := m.handleMCPSelectKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if cmd == nil {
		t.Fatal("expected busy MCP toggle to return toast command")
	}
	if got := len(backend.sentMessages); got != 0 {
		t.Fatalf("SendUserMessage() calls = %d, want 0", got)
	}
	if m.activeToast == nil {
		t.Fatal("expected busy MCP toggle to show toast")
	}
	if got := m.activeToast.Message; got != mcpSelectBusyMessage {
		t.Fatalf("toast message = %q, want %q", got, mcpSelectBusyMessage)
	}
}

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
		t.Fatal("enter should not close overlay or emit follow-up command")
	}
	if got := len(backend.sentMessages); got != 1 {
		t.Fatalf("SendUserMessage() calls = %d, want 1", got)
	}
	if got := backend.sentMessages[0]; got != "/mcp enable alpha" {
		t.Fatalf("sent message = %q, want %q", got, "/mcp enable alpha")
	}
	if m.mode != ModeMCPSelect {
		t.Fatalf("mode after enter = %v, want ModeMCPSelect", m.mode)
	}
	if m.mcpSelect.selector.list == nil {
		t.Fatal("MCP overlay list should remain present after enter")
	}
}

func TestMCPSelectDispatchRejectsAutoServerWithoutSendingCommand(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.mcpRows = []agent.MCPServerDisplay{
		{Name: "alpha", OK: true, Manual: false},
		{Name: "beta", Disabled: true, Manual: true},
	}
	m := NewModelWithSize(backend, 100, 30)
	m.mode = ModeNormal
	m.openMCPSelect()
	if m.mode != ModeMCPSelect {
		t.Fatalf("mode after open = %v, want ModeMCPSelect", m.mode)
	}
	if m.mcpSelect.selector.list == nil {
		t.Fatal("expected MCP overlay list")
	}

	// OverlayList normally keeps the cursor on selectable items; force the
	// cursor onto an auto/read-only item to verify dispatch itself rejects it.
	m.mcpSelect.selector.list.cursor = 0
	cmd := m.mcpSelectDispatch(agent.MCPControlEnable)
	if cmd == nil {
		t.Fatal("expected read-only MCP selection to return toast command")
	}

	if got := len(backend.sentMessages); got != 0 {
		t.Fatalf("SendUserMessage() calls = %d, want 0", got)
	}
	if m.activeToast == nil {
		t.Fatal("expected read-only MCP selection to show toast")
	}
	if got := m.activeToast.Message; got != "Auto-start MCP servers are read-only" {
		t.Fatalf("toast message = %q, want %q", got, "Auto-start MCP servers are read-only")
	}
	if m.mode != ModeMCPSelect {
		t.Fatalf("mode after read-only dispatch = %v, want ModeMCPSelect", m.mode)
	}
}

func TestMouseClickMCPSelectReadOnlyItemDoesNotToggleNeighbor(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.mcpRows = []agent.MCPServerDisplay{
		{Name: "alpha", OK: true, Manual: false},
		{Name: "beta", Disabled: true, Manual: true},
	}
	m := NewModelWithSize(backend, 100, 30)
	m.openMCPSelect()
	if m.mode != ModeMCPSelect {
		t.Fatalf("mode after open = %v, want ModeMCPSelect", m.mode)
	}
	if m.mcpSelect.selector.list == nil {
		t.Fatal("expected MCP overlay list")
	}
	_ = m.renderMCPSelectDialog()

	dialogRect := m.overlayRect(m.renderMCPSelectDialog())
	clickX := dialogRect.Min.X + 2
	clickY := dialogRect.Min.Y + 1 + 3
	updated, cmd := m.Update(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	m = *model
	if cmd == nil {
		t.Fatal("expected read-only MCP click to return toast command")
	}

	if got := len(backend.sentMessages); got != 0 {
		t.Fatalf("SendUserMessage() calls = %d, want 0", got)
	}
	if m.activeToast == nil {
		t.Fatal("expected read-only MCP click to show toast")
	}
	if got := m.activeToast.Message; got != "Auto-start MCP servers are read-only" {
		t.Fatalf("toast message = %q, want %q", got, "Auto-start MCP servers are read-only")
	}
	selected, ok := m.mcpSelect.selector.list.SelectedItem()
	if !ok {
		t.Fatal("expected MCP selection to remain available")
	}
	if selected.ID != "beta" {
		t.Fatalf("selected MCP item after read-only click = %q, want %q", selected.ID, "beta")
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
	if m.mcpSelect.selector.list == nil {
		t.Fatal("expected MCP overlay list")
	}
	m.mcpSelect.selector.list.SetCursor(1)

	backend.mcpRows = []agent.MCPServerDisplay{
		{Name: "alpha", Pending: true, Manual: true},
		{Name: "beta", OK: true, Manual: true},
	}
	cmd := m.handleAgentEvent(agentEventMsg{event: agent.EnvStatusUpdateEvent{}})
	applyTestCmd(t, &m, cmd)

	selected, ok := m.mcpSelect.selector.list.SelectedItem()
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

func TestMCPSelectOptionIndexAtUsesListBaseRow(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.mcpRows = []agent.MCPServerDisplay{
		{Name: "alpha", Disabled: true, Manual: true},
		{Name: "beta", OK: true, Manual: true},
		{Name: "gamma", OK: true, Manual: true},
	}
	m := NewModelWithSize(backend, 100, 30)
	m.openMCPSelect()
	if m.mode != ModeMCPSelect {
		t.Fatalf("mode after open = %v, want ModeMCPSelect", m.mode)
	}
	if m.mcpSelect.selector.list == nil {
		t.Fatal("expected MCP overlay list")
	}
	_ = m.renderMCPSelectDialog()

	dialogRect := m.overlayRect(m.renderMCPSelectDialog())
	x := dialogRect.Min.X + 2
	y := dialogRect.Min.Y + 1 + 3 // title + blank + prefix line
	idx, ok := m.mcpSelectOptionIndexAt(x, y)
	if !ok {
		t.Fatal("expected hit test to resolve first list row")
	}
	if idx != 0 {
		t.Fatalf("hit-test index = %d, want 0", idx)
	}
}

func TestMCPSelectOptionIndexAtAccountsForScrollWindowStart(t *testing.T) {
	rows := make([]agent.MCPServerDisplay, 0, 12)
	for i := 0; i < 12; i++ {
		rows = append(rows, agent.MCPServerDisplay{
			Name:   fmt.Sprintf("srv-%02d", i),
			Manual: true,
			OK:     true,
		})
	}
	backend := newInfoPanelAgent()
	backend.mcpRows = rows

	// Height chosen so mcpSelectMaxVisible() clamps to 3.
	m := NewModelWithSize(backend, 100, 16)
	m.openMCPSelect()
	if m.mcpSelect.selector.list == nil {
		t.Fatal("expected MCP overlay list")
	}
	m.mcpSelect.selector.list.SetCursor(11)
	_ = m.renderMCPSelectDialog()

	start, end := m.mcpSelect.selector.list.WindowRange()
	if end-start != 3 {
		t.Fatalf("visible window = %d, want 3", end-start)
	}
	if start == 0 {
		t.Fatal("expected list to be scrolled")
	}

	dialogRect := m.overlayRect(m.renderMCPSelectDialog())
	x := dialogRect.Min.X + 2
	y := dialogRect.Min.Y + 1 + 3 // first visible row
	idx, ok := m.mcpSelectOptionIndexAt(x, y)
	if !ok {
		t.Fatal("expected hit test to resolve first visible list row")
	}
	if idx != start {
		t.Fatalf("hit-test index = %d, want %d (window start)", idx, start)
	}
}
