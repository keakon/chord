package tui

import (
	"fmt"
	"image"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/keakon/chord/internal/agent"
)

// ---------------------------------------------------------------------------
// MCP picker state (/mcp)
// ---------------------------------------------------------------------------

type mcpSelectState struct {
	selector overlayListSelectorState
	prevMode Mode
}

func buildMCPSelectItems(rows []agent.MCPServerDisplay) []OverlayListItem {
	rowsCopy := append([]agent.MCPServerDisplay(nil), rows...)
	sort.Slice(rowsCopy, func(i, j int) bool { return rowsCopy[i].Name < rowsCopy[j].Name })

	items := make([]OverlayListItem, 0, len(rowsCopy))
	for _, r := range rowsCopy {
		state := "error"
		switch {
		case r.OK:
			state = "enabled"
		case r.Disabled:
			state = "disabled"
		case r.Pending && r.Retrying:
			state = "retrying"
		case r.Pending:
			state = "connecting"
		}
		item := OverlayListItem{ID: r.Name, Label: fmt.Sprintf("%s — %s", r.Name, state)}
		if !r.Manual {
			item.Disabled = true
			item.Label = fmt.Sprintf("%s — auto", r.Name)
		}
		items = append(items, item)
	}
	return items
}

func (m *Model) refreshMCPSelectItems() {
	if m.mcpSelect.selector.list == nil || m.agent == nil {
		return
	}
	mp, ok := m.agent.(agent.MCPStateProvider)
	if !ok {
		return
	}
	rows := mp.MCPServerList()
	items := buildMCPSelectItems(rows)
	if len(items) == 0 {
		m.mcpSelect.selector.list.SetItems(nil)
		return
	}
	selectedID, hasSelection := m.mcpSelectCurrent()
	m.mcpSelect.selector.list.SetItems(items)
	if !hasSelection {
		return
	}
	for i, item := range items {
		if item.ID == selectedID {
			m.mcpSelect.selector.list.SetCursor(i)
			return
		}
	}
}

func (m *Model) openMCPSelect() {
	if m.agent == nil {
		return
	}
	mp, ok := m.agent.(agent.MCPStateProvider)
	if !ok {
		_ = m.enqueueToast("MCP status is not available", "warn")
		return
	}
	rows := mp.MCPServerList()
	if len(rows) == 0 {
		_ = m.enqueueToast("No MCP servers configured", "info")
		return
	}

	m.clearChordState()
	m.mcpSelect = mcpSelectState{prevMode: m.mode}
	m.mcpSelect.selector.list = NewOverlayList(buildMCPSelectItems(rows), m.mcpSelectMaxVisible())
	m.mode = ModeMCPSelect
	m.recalcViewportSize()
}

func (m *Model) mcpSelectMaxVisible() int {
	maxVisible := m.height/2 - 5
	if maxVisible < 3 {
		maxVisible = 3
	}
	return maxVisible
}

func (m *Model) renderMCPSelectDialog() string {
	if m.mcpSelect.selector.list == nil {
		return ""
	}

	overlayCfg := OverlayConfig{
		Title:    "MCP Servers",
		Hint:     "j/k move  g/G jump  enter toggle  e enable  d disable  esc close  (auto servers are read-only)",
		MinWidth: 30,
		MaxWidth: 70,
	}
	area := image.Rect(0, 0, m.width, m.height)
	overlayCfg = normalizeOverlayConfig(overlayCfg, area)
	contentWidth := overlayCfg.MaxWidth - 4
	prefix := DimStyle.Render(ansi.Truncate("Changes apply immediately while idle", contentWidth, "…"))

	maxVisible := m.mcpSelectMaxVisible()
	return m.mcpSelect.selector.Render(
		m,
		overlayCfg,
		prefix,
		0,
		maxVisible,
		prefix,
		nil,
		area,
	)
}

func (m *Model) closeMCPSelect() tea.Cmd {
	prevMode := m.mcpSelect.prevMode
	m.mcpSelect = mcpSelectState{}
	cmd := m.restoreModeWithIME(prevMode)
	m.recalcViewportSize()
	if prevMode == ModeInsert {
		return tea.Batch(cmd, m.input.Focus())
	}
	return cmd
}

func (m *Model) mcpSelectCurrent() (string, bool) {
	if m.mcpSelect.selector.list == nil {
		return "", false
	}
	item, ok := m.mcpSelect.selector.list.SelectedItem()
	if !ok {
		return "", false
	}
	name := strings.TrimSpace(item.ID)
	if name == "" {
		return "", false
	}
	return name, true
}

func (m *Model) handleMCPSelectKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	if keyMatches(key, m.keyMap.InsertEscape) || key == "esc" {
		return m.closeMCPSelect()
	}

	switch key {
	case "j", "down":
		if m.mcpSelect.selector.list != nil {
			m.mcpSelect.selector.list.CursorDown()
		}
		return nil
	case "k", "up":
		if m.mcpSelect.selector.list != nil {
			m.mcpSelect.selector.list.CursorUp()
		}
		return nil
	case "g":
		if m.mcpSelect.selector.list != nil {
			m.mcpSelect.selector.list.CursorToTop()
		}
		return nil
	case "G":
		if m.mcpSelect.selector.list != nil {
			m.mcpSelect.selector.list.CursorToBottom()
		}
		return nil
	case "enter", "t":
		return m.mcpSelectDispatch(agent.MCPControlToggle)
	case "e":
		return m.mcpSelectDispatch(agent.MCPControlEnable)
	case "d":
		return m.mcpSelectDispatch(agent.MCPControlDisable)
	}
	return nil
}

func (m *Model) mcpSelectDispatch(action agent.MCPControlAction) tea.Cmd {
	if m.agent == nil {
		return nil
	}
	if m.isAgentBusy() {
		return m.enqueueToast("Wait until the agent is idle before changing MCP", "warn")
	}
	if m.mcpSelect.selector.list == nil {
		return nil
	}
	item, ok := m.mcpSelect.selector.list.SelectedItem()
	if !ok {
		return nil
	}
	if item.Disabled {
		return m.enqueueToast("Auto-start MCP servers are read-only", "info")
	}
	name := strings.TrimSpace(item.ID)
	if name == "" {
		return nil
	}
	m.agent.SendUserMessage(fmt.Sprintf("/mcp %s %s", string(action), name))
	return nil
}
