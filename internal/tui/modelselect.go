package tui

import (
	"fmt"
	"image"
	"strings"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
)

type modelSwitchResultMsg struct {
	err error
}

type pendingPoolSwitchState struct {
	from string
	to   string
}

func (s pendingPoolSwitchState) display(currentPool string, busy bool) string {
	if !busy || strings.TrimSpace(s.from) == "" || strings.TrimSpace(s.to) == "" {
		return strings.TrimSpace(currentPool)
	}
	return strings.TrimSpace(s.from) + " -> " + strings.TrimSpace(s.to)
}

type modelSelectState struct {
	target        agent.ModelPoolSelectorTarget
	poolNames     []string
	poolCursor    int
	prevMode      Mode
	filter        string
	filterFocused bool
	currentPool   string

	selector overlayListSelectorState
}

func (m *Model) switchModelPoolNow(target agent.ModelPoolSelectorTarget, pool string) tea.Cmd {
	if m == nil || m.agent == nil {
		return nil
	}
	ag := m.agent
	setPending := func(currentPool string) {
		if !m.isFocusedAgentBusy() {
			m.pendingPoolSwitch = pendingPoolSwitchState{}
			return
		}
		// A pending switch means the running model has not moved yet, so its
		// origin pool is unchanged. Keep that original origin instead of the
		// freshly-selected pool; otherwise switching back to the origin would
		// render a bogus "selected -> origin" transition.
		from := currentPool
		if existing := strings.TrimSpace(m.pendingPoolSwitch.from); existing != "" {
			from = m.pendingPoolSwitch.from
		}
		if strings.TrimSpace(from) == strings.TrimSpace(pool) {
			m.pendingPoolSwitch = pendingPoolSwitchState{}
			return
		}
		m.pendingPoolSwitch = pendingPoolSwitchState{from: from, to: pool}
	}
	switch target.Kind {
	case agent.ModelPoolSelectorTargetAgentOverride:
		currentPool := ""
		if current, ok := ag.AgentOverridePoolName(target.AgentName); ok {
			currentPool = current
		} else if len(m.modelSelect.poolNames) > 0 {
			currentPool = m.modelSelect.poolNames[0]
		}
		if pool == currentPool {
			return nil
		}
		setPending(currentPool)
		return func() tea.Msg {
			return modelSwitchResultMsg{err: ag.SetAgentModelPool(target.AgentName, pool)}
		}
	default:
		currentPool := ag.MainModelPoolName()
		if pool == currentPool {
			return nil
		}
		setPending(currentPool)
		return func() tea.Msg {
			return modelSwitchResultMsg{err: ag.SetCurrentModelPool(pool)}
		}
	}
}

func (m *Model) openModelSelect() {
	m.openModelSelectFor(agent.ModelPoolSelectorTarget{Kind: agent.ModelPoolSelectorTargetCurrentView})
}

func (m *Model) openModelSelectFor(target agent.ModelPoolSelectorTarget) {
	if m.agent == nil {
		return
	}
	if target.Kind == "" {
		target.Kind = agent.ModelPoolSelectorTargetCurrentView
	}

	var (
		poolNames   []string
		currentPool string
	)
	if target.Kind == agent.ModelPoolSelectorTargetCurrentView {
		if m.focusedAgentID != "" {
			target.Kind = agent.ModelPoolSelectorTargetAgentOverride
			target.AgentName = strings.TrimSpace(m.agent.FocusedAgentName())
			if target.AgentName == "" {
				return
			}
		} else {
			target.Kind = agent.ModelPoolSelectorTargetMainRole
		}
	}
	if target.Kind == agent.ModelPoolSelectorTargetAgentOverride {
		poolNames = m.agent.PoolNames()
		if current, ok := m.agent.AgentOverridePoolName(target.AgentName); ok {
			currentPool = current
		} else if len(poolNames) > 0 {
			currentPool = poolNames[0]
		}
	} else {
		poolNames = m.agent.MainModelPoolNames()
		currentPool = m.agent.MainModelPoolName()
	}

	poolCursor := 0
	for i, name := range poolNames {
		if name == currentPool {
			poolCursor = i
			break
		}
	}

	prevMode := m.mode
	if prevMode == ModeModelSelect {
		prevMode = m.modelSelect.prevMode
	}
	m.clearActiveSearch()
	m.clearChordState()

	var list *OverlayList
	if len(poolNames) > 0 {
		list = NewOverlayList(buildModelSelectItems(poolNames, currentPool), m.modelSelectMaxVisible())
		list.SetCursor(poolCursor)
	}

	m.modelSelect = modelSelectState{
		target:      target,
		currentPool: currentPool,
		poolNames:   poolNames,
		poolCursor:  poolCursor,
		prevMode:    prevMode,
	}
	m.modelSelect.selector.list = list
	if m.mode == ModeInsert {
		m.input.Blur()
	}
	m.mode = ModeModelSelect
	m.recalcViewportSize()
}

func (m *Model) handleModelSelectKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	if m.modelSelect.filterFocused && !keyMatches(key, m.keyMap.SwitchModel) {
		return m.handleModelSelectFilterKey(msg)
	}

	if keyMatches(key, m.keyMap.SwitchModel) || key == "esc" {
		prevMode := m.modelSelect.prevMode
		cmd := m.restoreModeWithIME(prevMode)
		m.recalcViewportSize()
		if prevMode == ModeInsert {
			return tea.Batch(cmd, m.input.Focus())
		}
		return cmd
	}

	itemCount := len(m.modelSelect.poolNames)
	if m.modelSelect.selector.list != nil {
		itemCount = m.modelSelect.selector.list.Len()
	}
	switch key {
	case "/":
		m.modelSelect.filterFocused = true
		return nil
	case "j", "down":
		if itemCount > 0 {
			if m.modelSelect.selector.list != nil {
				m.modelSelect.selector.list.CursorDown()
				m.modelSelect.poolCursor = m.modelSelect.selector.list.CursorAt()
			} else if m.modelSelect.poolCursor < itemCount-1 {
				m.modelSelect.poolCursor++
			}
		}

	case "k", "up":
		if itemCount > 0 {
			if m.modelSelect.selector.list != nil {
				m.modelSelect.selector.list.CursorUp()
				m.modelSelect.poolCursor = m.modelSelect.selector.list.CursorAt()
			} else if m.modelSelect.poolCursor > 0 {
				m.modelSelect.poolCursor--
			}
		}

	case "g":
		if itemCount > 0 {
			if m.modelSelect.selector.list != nil {
				m.modelSelect.selector.list.CursorToTop()
				m.modelSelect.poolCursor = m.modelSelect.selector.list.CursorAt()
			} else {
				m.modelSelect.poolCursor = 0
			}
		}

	case "G":
		if itemCount > 0 {
			if m.modelSelect.selector.list != nil {
				m.modelSelect.selector.list.CursorToBottom()
				m.modelSelect.poolCursor = m.modelSelect.selector.list.CursorAt()
			} else {
				m.modelSelect.poolCursor = itemCount - 1
			}
		}

	case "enter":
		if m.modelSelect.selector.list != nil {
			m.modelSelect.poolCursor = m.modelSelect.selector.list.CursorAt()
		}
		return m.selectPoolAtCursor()
	}

	return nil
}

func (m *Model) selectPoolAtCursor() tea.Cmd {
	var pool string
	if m.modelSelect.selector.list != nil {
		item, ok := m.modelSelect.selector.list.SelectedItem()
		if !ok {
			return nil
		}
		pool = item.ID
	} else {
		if m.modelSelect.poolCursor < 0 || m.modelSelect.poolCursor >= len(m.modelSelect.poolNames) {
			return nil
		}
		pool = m.modelSelect.poolNames[m.modelSelect.poolCursor]
	}
	ag := m.agent
	var switchCmd tea.Cmd
	if ag != nil {
		target := m.modelSelect.target
		switchCmd = m.switchModelPoolNow(target, pool)
	}
	prevMode := m.modelSelect.prevMode
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

func (m *Model) renderModelSelectDialog() string {
	if len(m.modelSelect.poolNames) == 0 {
		dialog, _ := RenderOverlay(OverlayConfig{
			Title: modelSelectTitle(m.modelSelect.target),
			Hint:  hintLine(hint("Esc", "cancel")),

			MaxWidth: 60,
		}, DimStyle.Render("(no pools configured)"), image.Rect(0, 0, m.width, m.height))
		return dialog
	}

	currentPool := ""
	if m.agent != nil {
		if m.modelSelect.target.Kind == agent.ModelPoolSelectorTargetAgentOverride {
			if current, ok := m.agent.AgentOverridePoolName(m.modelSelect.target.AgentName); ok {
				currentPool = current
			} else if len(m.modelSelect.poolNames) > 0 {
				currentPool = m.modelSelect.poolNames[0]
			}
		} else {
			currentPool = m.agent.MainModelPoolName()
		}
	}

	m.modelSelect.currentPool = currentPool
	hints := modelSelectIdleHint()
	if m.modelSelect.filterFocused {
		hints = modelSelectFilterHint()
	}
	prefix := renderFilterLine(m.modelSelect.filter, m.modelSelect.filterFocused, "", max(dialogContentWidth(min(m.width-1, 60)), 1))
	if len(m.modelSelect.filteredItems()) == 0 {
		prefix += "\nNo matching pools"
	}
	overlayCfg := OverlayConfig{
		Title:       modelSelectTitle(m.modelSelect.target),
		Hint:        hints,
		CompactHint: hintLine(hint("Enter", "select"), hint("Esc", "close")),
		MaxWidth:    60,
	}

	if m.modelSelect.filterFocused {
		overlayCfg.CompactHint = modelSelectFilterHint()
	}
	if len(m.modelSelect.filteredItems()) == 0 {
		overlayCfg.Hint = hintLine(hint("/", "filter"), hint("Esc", "close"))
		if m.modelSelect.filterFocused {
			overlayCfg.Hint = hintLine(hint("type", "filter"), hint("Esc", "clear"))
		}
		overlayCfg.CompactHint = overlayCfg.Hint
	}

	extraKey := strings.Join(m.modelSelect.poolNames, ",") + "|" + currentPool + "|" + string(m.modelSelect.target.Kind) + "|" + strings.TrimSpace(m.modelSelect.target.AgentName) + "|" + m.modelSelect.filter + "|" + hints
	maxVisible := m.modelSelectMaxVisible()

	return m.modelSelect.selector.Render(
		m,
		overlayCfg,
		prefix,
		1,
		maxVisible,
		extraKey,
		func(list *OverlayList) {
			list.SetItems(m.modelSelect.filteredItems())
			list.SetCursor(m.modelSelect.poolCursor)
		},
		image.Rect(0, 0, m.width, m.height),
	)
}

func (m *Model) modelSelectMaxVisible() int {
	maxVisible := max(m.height/2-5, 3)
	return maxVisible
}

func buildModelSelectItems(poolNames []string, currentPool string) []OverlayListItem {
	items := make([]OverlayListItem, 0, len(poolNames))
	for _, name := range poolNames {
		items = append(items, OverlayListItem{ID: name, Label: name, Selected: name == currentPool})
	}
	return items
}

func modelSelectTitle(target agent.ModelPoolSelectorTarget) string {
	if target.Kind == agent.ModelPoolSelectorTargetAgentOverride {
		if strings.TrimSpace(target.AgentName) == "" {
			return "Agent Model Pool"
		}
		return fmt.Sprintf("%s Model Pool", target.AgentName)
	}
	return "Main Role Model Pool"
}
