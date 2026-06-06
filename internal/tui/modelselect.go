package tui

import (
	"fmt"
	"image"
	"strings"

	tea "charm.land/bubbletea/v2"

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
	target     agent.ModelPoolSelectorTarget
	poolNames  []string
	poolCursor int
	prevMode   Mode

	selector overlayListSelectorState
}

func (m *Model) switchModelPoolNow(target agent.ModelPoolSelectorTarget, pool string) tea.Cmd {
	if m == nil || m.agent == nil {
		return nil
	}
	ag := m.agent
	setPending := func(currentPool string) {
		if m.isFocusedAgentBusy() {
			m.pendingPoolSwitch = pendingPoolSwitchState{from: currentPool, to: pool}
		} else {
			m.pendingPoolSwitch = pendingPoolSwitchState{}
		}
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
		target:     target,
		poolNames:  poolNames,
		poolCursor: poolCursor,
		prevMode:   prevMode,
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
	switch key {
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
	if len(m.modelSelect.poolNames) == 0 || m.modelSelect.poolCursor >= len(m.modelSelect.poolNames) {
		prevMode := m.modelSelect.prevMode
		cmd := m.restoreModeWithIME(prevMode)
		m.recalcViewportSize()
		if prevMode == ModeInsert {
			return tea.Batch(cmd, m.input.Focus())
		}
		return cmd
	}
	pool := m.modelSelect.poolNames[m.modelSelect.poolCursor]
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
			Title:    modelSelectTitle(m.modelSelect.target),
			Hint:     "esc cancel",
			MinWidth: 40,
			MaxWidth: 60,
		}, DimStyle.Render("(no pools configured)"), 1, image.Rect(0, 0, m.width, m.height))
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

	overlayCfg := OverlayConfig{
		Title:    modelSelectTitle(m.modelSelect.target),
		Hint:     modelSelectHint(m.modelSelect.target),
		MinWidth: 30,
		MaxWidth: 60,
	}

	extraKey := strings.Join(m.modelSelect.poolNames, ",") + "|" + currentPool + "|" + string(m.modelSelect.target.Kind) + "|" + strings.TrimSpace(m.modelSelect.target.AgentName)
	maxVisible := m.modelSelectMaxVisible()

	return m.modelSelect.selector.Render(
		m,
		overlayCfg,
		"",
		0,
		maxVisible,
		extraKey,
		func(list *OverlayList) {
			list.SetItems(buildModelSelectItems(m.modelSelect.poolNames, currentPool))
			list.SetCursor(m.modelSelect.poolCursor)
		},
		image.Rect(0, 0, m.width, m.height),
	)
}

func (m *Model) modelSelectMaxVisible() int {
	maxVisible := m.height/2 - 5
	if maxVisible < 3 {
		maxVisible = 3
	}
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

func modelSelectHint(target agent.ModelPoolSelectorTarget) string {
	_ = target
	return "j/k move  g/G jump  enter select  esc cancel"
}
