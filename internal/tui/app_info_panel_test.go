package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

func TestClickInfoPanelSectionHeaderTogglesCollapse(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.todos = []tools.TodoItem{{ID: "1", Content: "Investigate spacing", Status: "in_progress"}}
	m := NewModelWithSize(backend, 140, 24)
	m.layout = m.generateLayout(m.width, m.height)
	_ = m.renderInfoPanel(m.layout.infoPanel.Dx(), m.viewport.height)

	clickX := m.layout.infoPanel.Min.X + 1
	clickY := m.layout.infoPanel.Min.Y
	for _, hit := range m.infoPanelHitBoxes {
		if hit.section == infoPanelSectionTodos {
			clickY = m.layout.infoPanel.Min.Y + hit.startY
			break
		}
	}

	updated, cmd := m.Update(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd != nil {
		t.Fatalf("info panel click should not schedule command, got %#v", cmd)
	}
	if !model.infoPanelCollapsedSections[infoPanelSectionTodos] {
		t.Fatal("clicking TODOS header should collapse section")
	}

	plain := stripANSI(model.renderInfoPanel(model.layout.infoPanel.Dx(), model.viewport.height))
	if !strings.Contains(plain, "▶ TODOS") || !strings.Contains(plain, "0/1") {
		t.Fatalf("collapsed info panel should show collapsed TODOS header with progress, got %q", plain)
	}
}

func TestClickInfoPanelLSPHeaderTogglesCollapse(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.lspRows = []agent.LSPServerDisplay{{Name: "gopls", OK: true}, {Name: "pyright", Pending: true}}
	m := NewModelWithSize(backend, 140, 24)
	m.layout = m.generateLayout(m.width, m.height)
	_ = m.renderInfoPanel(m.layout.infoPanel.Dx(), m.viewport.height)

	clickX := m.layout.infoPanel.Min.X + 1
	clickY := m.layout.infoPanel.Min.Y
	for _, hit := range m.infoPanelHitBoxes {
		if hit.section == infoPanelSectionLSP {
			clickY = m.layout.infoPanel.Min.Y + hit.startY
			break
		}
	}

	updated, cmd := m.Update(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd != nil {
		t.Fatalf("info panel click should not schedule command, got %#v", cmd)
	}
	if !model.infoPanelCollapsedSections[infoPanelSectionLSP] {
		t.Fatal("clicking LSP header should collapse section")
	}

	plain := stripANSI(model.renderInfoPanel(model.layout.infoPanel.Dx(), model.viewport.height))
	if !strings.Contains(plain, "▶ LSP") || !strings.Contains(plain, "2") {
		t.Fatalf("collapsed info panel should show collapsed LSP header with count, got %q", plain)
	}
}

func TestClickInfoPanelMCPHeaderTogglesCollapse(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "exa", OK: true}, {Name: "browser", Pending: true}}
	m := NewModelWithSize(backend, 140, 24)
	m.layout = m.generateLayout(m.width, m.height)
	_ = m.renderInfoPanel(m.layout.infoPanel.Dx(), m.viewport.height)

	clickX := m.layout.infoPanel.Min.X + 1
	clickY := m.layout.infoPanel.Min.Y
	for _, hit := range m.infoPanelHitBoxes {
		if hit.section == infoPanelSectionMCP {
			clickY = m.layout.infoPanel.Min.Y + hit.startY
			break
		}
	}

	updated, cmd := m.Update(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd != nil {
		t.Fatalf("info panel click should not schedule command, got %#v", cmd)
	}
	if !model.infoPanelCollapsedSections[infoPanelSectionMCP] {
		t.Fatal("clicking MCP header should collapse section")
	}

	plain := stripANSI(model.renderInfoPanel(model.layout.infoPanel.Dx(), model.viewport.height))
	if !strings.Contains(plain, "▶ MCP") || !strings.Contains(plain, "2") {
		t.Fatalf("collapsed info panel should show collapsed MCP header with count, got %q", plain)
	}
}

func TestClickInfoPanelAgentRowSwitchesFocus(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.subAgents = []agent.SubAgentInfo{{InstanceID: "agent-1", TaskDesc: "ship tests"}}
	m := NewModelWithSize(backend, 140, 24)
	m.refreshSidebar()
	m.layout = m.generateLayout(m.width, m.height)
	_ = m.renderInfoPanel(m.layout.infoPanel.Dx(), m.viewport.height)

	clickX := m.layout.infoPanel.Min.X + 1
	clickY := -1
	for _, hit := range m.infoPanelHitBoxes {
		if hit.agentID == "agent-1" {
			clickY = m.layout.infoPanel.Min.Y + hit.startY
			break
		}
	}
	if clickY < 0 {
		t.Fatal("expected AGENTS row hitbox for agent-1")
	}

	updated, _ := m.Update(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if model.focusedAgentID != "agent-1" {
		t.Fatalf("focusedAgentID after AGENTS click = %q, want agent-1", model.focusedAgentID)
	}
	if backend.focused != "agent-1" {
		t.Fatalf("backend focused agent after AGENTS click = %q, want agent-1", backend.focused)
	}
}

func TestInfoPanelWheelScrollsPanelWhenOverflowing(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModelWithSize(backend, 140, 24)
	m.refreshSidebar()
	m.layout = m.generateLayout(m.width, m.height)
	for i := range 30 {
		m.sidebar.AddFileEdit("main", fmt.Sprintf("/tmp/file-%02d.go", i), i+1, 0)
	}
	_ = m.renderInfoPanel(m.layout.infoPanel.Dx(), m.viewport.height)
	if m.infoPanelContentHeight <= m.infoPanelViewportHeight {
		t.Fatalf("test setup did not overflow info panel: content=%d viewport=%d", m.infoPanelContentHeight, m.infoPanelViewportHeight)
	}

	updated, cmd := m.Update(tea.MouseWheelMsg{X: m.layout.infoPanel.Min.X + 1, Y: m.layout.infoPanel.Min.Y + 1, Button: tea.MouseWheelDown})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd != nil {
		t.Fatalf("info panel wheel should not schedule viewport scroll, got %#v", cmd)
	}
	if model.infoPanelScrollOffset != mouseWheelScrollStep {
		t.Fatalf("infoPanelScrollOffset = %d, want %d", model.infoPanelScrollOffset, mouseWheelScrollStep)
	}
	if model.pendingScrollDelta != 0 {
		t.Fatalf("pendingScrollDelta = %d, want 0", model.pendingScrollDelta)
	}
}

func TestInfoPanelWheelDoesNotScrollViewportWhenPanelDoesNotOverflow(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModelWithSize(backend, 140, 24)
	m.layout = m.generateLayout(m.width, m.height)
	_ = m.renderInfoPanel(m.layout.infoPanel.Dx(), m.viewport.height)
	if m.infoPanelContentHeight > m.infoPanelViewportHeight {
		t.Fatalf("test setup unexpectedly overflowed info panel: content=%d viewport=%d", m.infoPanelContentHeight, m.infoPanelViewportHeight)
	}

	updated, cmd := m.Update(tea.MouseWheelMsg{X: m.layout.infoPanel.Min.X + 1, Y: m.layout.infoPanel.Min.Y + 1, Button: tea.MouseWheelDown})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd != nil {
		t.Fatalf("non-overflowing info panel wheel should not schedule viewport scroll, got %#v", cmd)
	}
	if model.infoPanelScrollOffset != 0 {
		t.Fatalf("infoPanelScrollOffset = %d, want 0", model.infoPanelScrollOffset)
	}
	if model.pendingScrollDelta != 0 {
		t.Fatalf("pendingScrollDelta = %d, want 0", model.pendingScrollDelta)
	}
}

func TestInfoPanelClickHitBoxesAccountForScrollOffset(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModelWithSize(backend, 140, 24)
	m.refreshSidebar()
	m.layout = m.generateLayout(m.width, m.height)
	for i := range 30 {
		m.sidebar.AddFileEdit("main", fmt.Sprintf("/tmp/file-%02d.go", i), i+1, 0)
	}
	_ = m.renderInfoPanel(m.layout.infoPanel.Dx(), m.viewport.height)

	var filesHit infoPanelSectionHitBox
	found := false
	for _, hit := range m.infoPanelHitBoxes {
		if hit.section == infoPanelSectionFiles {
			filesHit = hit
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected CHANGED FILES hitbox")
	}
	m.infoPanelScrollOffset = filesHit.startY
	m.clearInfoPanelRenderCache()
	_ = m.renderInfoPanel(m.layout.infoPanel.Dx(), m.viewport.height)

	updated, cmd := m.Update(tea.MouseClickMsg{X: m.layout.infoPanel.Min.X + 1, Y: m.layout.infoPanel.Min.Y, Button: tea.MouseLeft})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd != nil {
		t.Fatalf("info panel click should not schedule command, got %#v", cmd)
	}
	if !model.infoPanelCollapsedSections[infoPanelSectionFiles] {
		t.Fatal("clicking scrolled CHANGED FILES header should collapse section")
	}
}

func TestClickOutsideViewportClearsFocusedBlock(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "hello"})

	updated, cmd := m.Update(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd != nil {
		t.Fatalf("viewport click should not schedule command, got %#v", cmd)
	}
	if model.focusedBlockID != 1 {
		t.Fatalf("focusedBlockID after viewport click = %d, want 1", model.focusedBlockID)
	}
	block := model.viewport.GetFocusedBlock(1)
	if block == nil || !block.Focused {
		t.Fatal("expected block to be focused after viewport click")
	}

	rightPanelX := model.viewport.width
	updated, cmd = model.Update(tea.MouseClickMsg{X: rightPanelX, Y: 0, Button: tea.MouseLeft})
	model, ok = updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd != nil {
		t.Fatalf("outside click should not schedule command, got %#v", cmd)
	}
	if model.focusedBlockID != -1 {
		t.Fatalf("focusedBlockID after outside click = %d, want -1", model.focusedBlockID)
	}
	block = model.viewport.GetFocusedBlock(1)
	if block == nil {
		t.Fatal("expected block to remain in viewport")
	}
	if block.Focused {
		t.Fatal("expected outside click to clear block focus")
	}
}

func TestClickBlockErrorFocusesForCopy(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockError, Content: "boom"})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: "ok"})

	updated, cmd := m.Update(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd != nil {
		t.Fatalf("viewport click should not schedule command, got %#v", cmd)
	}
	if model.focusedBlockID != 1 {
		t.Fatalf("focusedBlockID after clicking BlockError = %d, want 1", model.focusedBlockID)
	}
	if errBlock := model.viewport.GetFocusedBlock(1); errBlock == nil {
		t.Fatal("expected error block to remain in viewport")
	} else if !errBlock.Focused {
		t.Fatal("BlockError should become focused for copy")
	}
}
