package tui

import (
	"fmt"
	"image"
	"strings"

	tea "github.com/keakon/bubbletea/v2"
)

// roleSwitchResultMsg carries the outcome of a role switch chosen from the
// role selector overlay so the event loop can toast the result.
type roleSwitchResultMsg struct {
	from string
	to   string
	err  error
}

type roleSelectState struct {
	roles    []string
	cursor   int
	prevMode Mode

	selector overlayListSelectorState
}

// openRoleSelect opens the main-role selector overlay. It lists the ordered
// main-mode roles (builder first, planner second when configured, then custom
// roles alphabetically) with the current one preselected — the dialog form of
// TUI Shift+Tab, opened by /role.
func (m *Model) openRoleSelect() {
	if m.agent == nil {
		return
	}
	roles := m.agent.AvailableRoles()
	if len(roles) == 0 {
		_ = m.enqueueToast("No roles available", "warn")
		return
	}
	current := m.agent.CurrentRole()
	cursor := 0
	for i, name := range roles {
		if name == current {
			cursor = i
			break
		}
	}

	prevMode := m.mode
	if prevMode == ModeRoleSelect {
		prevMode = m.roleSelect.prevMode
	}
	m.clearActiveSearch()
	m.clearChordState()

	m.roleSelect = roleSelectState{
		roles:    append([]string(nil), roles...),
		cursor:   cursor,
		prevMode: prevMode,
	}
	m.roleSelect.selector.list = NewOverlayList(buildRoleSelectItems(roles, current), m.roleSelectMaxVisible())
	m.roleSelect.selector.list.SetCursor(cursor)
	if m.mode == ModeInsert {
		m.input.Blur()
	}
	m.mode = ModeRoleSelect
	m.recalcViewportSize()
}

func (m *Model) handleRoleSelectKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()

	if keyMatches(key, m.keyMap.SwitchRole) || key == "esc" {
		prevMode := m.roleSelect.prevMode
		cmd := m.restoreModeWithIME(prevMode)
		m.recalcViewportSize()
		if prevMode == ModeInsert {
			return tea.Batch(cmd, m.input.Focus())
		}
		return cmd
	}

	itemCount := len(m.roleSelect.roles)
	switch key {
	case "j", "down":
		if itemCount > 0 && m.roleSelect.selector.list != nil {
			m.roleSelect.selector.list.CursorDown()
			m.roleSelect.cursor = m.roleSelect.selector.list.CursorAt()
		}

	case "k", "up":
		if itemCount > 0 && m.roleSelect.selector.list != nil {
			m.roleSelect.selector.list.CursorUp()
			m.roleSelect.cursor = m.roleSelect.selector.list.CursorAt()
		}

	case "g":
		if itemCount > 0 && m.roleSelect.selector.list != nil {
			m.roleSelect.selector.list.CursorToTop()
			m.roleSelect.cursor = m.roleSelect.selector.list.CursorAt()
		}

	case "G":
		if itemCount > 0 && m.roleSelect.selector.list != nil {
			m.roleSelect.selector.list.CursorToBottom()
			m.roleSelect.cursor = m.roleSelect.selector.list.CursorAt()
		}

	case "enter":
		if m.roleSelect.selector.list != nil {
			m.roleSelect.cursor = m.roleSelect.selector.list.CursorAt()
		}
		return m.selectRoleAtCursor()
	}

	return nil
}

func (m *Model) selectRoleAtCursor() tea.Cmd {
	if len(m.roleSelect.roles) == 0 || m.roleSelect.cursor >= len(m.roleSelect.roles) {
		prevMode := m.roleSelect.prevMode
		cmd := m.restoreModeWithIME(prevMode)
		m.recalcViewportSize()
		if prevMode == ModeInsert {
			return tea.Batch(cmd, m.input.Focus())
		}
		return cmd
	}
	role := m.roleSelect.roles[m.roleSelect.cursor]
	var switchCmd tea.Cmd
	if m.agent != nil {
		switchCmd = func() tea.Msg {
			return roleSwitchResultMsg{from: m.agent.CurrentRole(), to: role, err: m.agent.SwitchRole(role)}
		}
	}
	prevMode := m.roleSelect.prevMode
	cmd := m.restoreModeWithIME(prevMode)
	m.recalcViewportSize()
	if prevMode == ModeInsert {
		if switchCmd != nil {
			return tea.Batch(cmd, m.input.Focus(), switchCmd)
		}
		return tea.Batch(cmd, m.input.Focus())
	}
	if switchCmd != nil {
		return tea.Batch(cmd, switchCmd)
	}
	return cmd
}

// handleRoleSwitchResult toasts the outcome of a role switch requested from
// the role selector. On failure the role did not change, so draw caches stay
// valid; on success the RoleChangedEvent refresh also applies.
func (m *Model) handleRoleSwitchResult(msg roleSwitchResultMsg) tea.Cmd {
	if msg.err != nil {
		return m.enqueueToast(msg.err.Error(), "error")
	}
	m.invalidateDrawCaches()
	return m.enqueueToast(fmt.Sprintf("role: %s → %s", msg.from, msg.to), "info")
}

func (m *Model) renderRoleSelectDialog() string {
	if len(m.roleSelect.roles) == 0 {
		dialog, _ := RenderOverlay(OverlayConfig{
			Title:    "Main Role",
			Hint:     "esc cancel",
			MinWidth: 40,
			MaxWidth: 60,
		}, DimStyle.Render("(no roles configured)"), 1, image.Rect(0, 0, m.width, m.height))
		return dialog
	}

	currentRole := ""
	if m.agent != nil {
		currentRole = m.agent.CurrentRole()
	}

	overlayCfg := OverlayConfig{
		Title:    "Main Role",
		Hint:     "j/k move  g/G jump  enter select  esc cancel",
		MinWidth: 30,
		MaxWidth: 60,
	}

	extraKey := strings.Join(m.roleSelect.roles, ",") + "|" + currentRole
	maxVisible := m.roleSelectMaxVisible()

	return m.roleSelect.selector.Render(
		m,
		overlayCfg,
		"",
		0,
		maxVisible,
		extraKey,
		func(list *OverlayList) {
			list.SetItems(buildRoleSelectItems(m.roleSelect.roles, currentRole))
			list.SetCursor(m.roleSelect.cursor)
		},
		image.Rect(0, 0, m.width, m.height),
	)
}

func (m *Model) roleSelectMaxVisible() int {
	maxVisible := max(m.height/2-5, 3)
	return maxVisible
}

func (m *Model) roleSelectIndexAt(x, y int) (int, bool) {
	dialog := m.renderRoleSelectDialog()
	idx, ok := m.roleSelect.selector.IndexAt(m, dialog, x, y)
	if !ok {
		return 0, false
	}
	if idx < 0 || idx >= len(m.roleSelect.roles) {
		return 0, false
	}
	return idx, true
}

func buildRoleSelectItems(roles []string, currentRole string) []OverlayListItem {
	items := make([]OverlayListItem, 0, len(roles))
	for _, name := range roles {
		items = append(items, OverlayListItem{ID: name, Label: name, Selected: name == currentRole})
	}
	return items
}
