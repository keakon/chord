package tui

import (
	"fmt"
	"image"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"
	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
)

// ---------------------------------------------------------------------------
// MCP picker state (/mcp)
// ---------------------------------------------------------------------------

type mcpSelectState struct {
	selector overlayListSelectorState
	prevMode Mode
}

const (
	mcpSelectIdleHint = "j/k move  g/G jump  enter toggle  e enable  d disable  esc close  (auto servers are read-only)"
	mcpSelectBusyHint = "j/k move  g/G jump  enter toggle next request  e enable  d disable  esc close"
)

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

	readOnly := m.mcpSelectReadOnly()
	hint := mcpSelectIdleHint
	if readOnly {
		hint = mcpSelectBusyHint
	}
	overlayCfg := OverlayConfig{
		Title:    "MCP Servers",
		Hint:     hint,
		MinWidth: 30,
		MaxWidth: 70,
	}
	area := image.Rect(0, 0, m.width, m.height)
	overlayCfg = normalizeOverlayConfig(overlayCfg, area)
	contentWidth := overlayCfg.MaxWidth - 4
	prefixText := "Changes apply on the next request"
	if readOnly {
		prefixText = "Changes are allowed while running and apply on the next request"
	}
	prefix := DimStyle.Render(ansi.Truncate(prefixText, contentWidth, "…"))

	maxVisible := m.mcpSelectMaxVisible()
	return m.mcpSelect.selector.Render(
		m,
		overlayCfg,
		prefix,
		0,
		maxVisible,
		prefix+"\x00"+hint,
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
	case "enter":
		return m.mcpSelectToggleFocused()
	case "e":
		return m.mcpSelectDispatch(agent.MCPControlEnable)
	case "d":
		return m.mcpSelectDispatch(agent.MCPControlDisable)
	}
	return nil
}

func (m *Model) mcpSelectReadOnly() bool {
	return m.isAgentBusy()
}

func (m *Model) mcpSelectDispatch(action agent.MCPControlAction) tea.Cmd {
	if m.agent == nil {
		return nil
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

// mcpSelectToggleFocused determines the current state of the focused MCP server
// and dispatches the appropriate enable/disable action.
func (m *Model) mcpSelectToggleFocused() tea.Cmd {
	if m.mcpSelect.selector.list == nil {
		return nil
	}
	return m.mcpSelectToggleAtIndex(m.mcpSelect.selector.list.CursorAt())
}

// mcpSelectToggleAtIndex determines the current state of the MCP server at the
// given list index and dispatches the appropriate enable/disable action.
func (m *Model) mcpSelectToggleAtIndex(idx int) tea.Cmd {
	mp, ok := m.agent.(agent.MCPStateProvider)
	if !ok || m.mcpSelect.selector.list == nil {
		return nil
	}
	if idx < 0 || idx >= m.mcpSelect.selector.list.Len() {
		return nil
	}
	item := m.mcpSelect.selector.list.items[idx]
	if item.Disabled {
		return m.enqueueToast("Auto-start MCP servers are read-only", "info")
	}
	name := strings.TrimSpace(item.ID)
	if name == "" {
		return nil
	}
	m.mcpSelect.selector.list.SetCursor(idx)
	// Look up current state: enable if not connected/pending, disable otherwise.
	action := agent.MCPControlEnable
	for _, r := range mp.MCPServerList() {
		if r.Name == name {
			if r.OK || r.Pending {
				action = agent.MCPControlDisable
			}
			break
		}
	}
	return m.mcpSelectDispatch(action)
}
