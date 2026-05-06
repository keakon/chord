package tui

import (
	"fmt"
	"image"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/keakon/chord/internal/agent"
)

type pendingModelSwitchState struct {
	target agent.ModelPoolSelectorTarget
	pool   string
}

type modelSwitchResultMsg struct {
	err error
}

type modelSelectState struct {
	target     agent.ModelPoolSelectorTarget
	poolNames  []string
	poolCursor int
	prevMode   Mode

	renderCacheWidth      int
	renderCacheHeight     int
	renderCacheText       string
	renderCachePoolCursor int
	renderCachePoolNames  string
}

func (m *Model) switchModelPoolNow(target agent.ModelPoolSelectorTarget, pool string) tea.Cmd {
	if m == nil || m.agent == nil {
		return nil
	}
	ag := m.agent
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
		return func() tea.Msg {
			return modelSwitchResultMsg{err: ag.SetAgentModelPool(target.AgentName, pool)}
		}
	default:
		if pool == ag.MainRoleCurrentPoolName() {
			return nil
		}
		return func() tea.Msg {
			return modelSwitchResultMsg{err: ag.SetCurrentRolePool(pool)}
		}
	}
}

// applyPendingPoolSwitch synchronously applies a deferred pool switch and
// clears the pending state. It is called before sending the next user draft
// so the upcoming turn runs under the requested pool. Errors are returned as
// modelSwitchResultMsg cmd so state updates stay on the Bubble Tea Update path.
func (m *Model) applyPendingPoolSwitch() tea.Cmd {
	if m.pendingModelSwitch == nil {
		return nil
	}
	ps := m.pendingModelSwitch
	m.pendingModelSwitch = nil
	if m.agent == nil {
		return nil
	}
	return func() tea.Msg {
		var err error
		switch ps.target.Kind {
		case agent.ModelPoolSelectorTargetAgentOverride:
			err = m.agent.SetAgentModelPool(ps.target.AgentName, ps.pool)
		default:
			err = m.agent.SetCurrentRolePool(ps.pool)
		}
		return modelSwitchResultMsg{err: err}
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
		poolNames = m.agent.MainRolePoolNames()
		currentPool = m.agent.MainRoleCurrentPoolName()
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
	m.modelSelect = modelSelectState{
		target:     target,
		poolNames:  poolNames,
		poolCursor: poolCursor,
		prevMode:   prevMode,
	}
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
	if key == "ctrl+d" {
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
		if itemCount > 0 && m.modelSelect.poolCursor < itemCount-1 {
			m.modelSelect.poolCursor++
		}

	case "k", "up":
		if m.modelSelect.poolCursor > 0 {
			m.modelSelect.poolCursor--
		}

	case "g":
		m.modelSelect.poolCursor = 0

	case "G":
		if itemCount > 0 {
			m.modelSelect.poolCursor = itemCount - 1
		}

	case "enter":
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
		if m.isAgentBusy() {
			// Defer switching pools until the current turn finishes.
			m.pendingModelSwitch = &pendingModelSwitchState{target: target, pool: pool}
			switchCmd = m.enqueueToast(fmt.Sprintf("Model pool switch to %q queued", pool), "info")
		} else {
			switchCmd = m.switchModelPoolNow(target, pool)
		}
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

	if m.modelSelect.renderCacheText != "" &&
		m.modelSelect.renderCacheWidth == m.width &&
		m.modelSelect.renderCacheHeight == m.height &&
		m.modelSelect.renderCachePoolCursor == m.modelSelect.poolCursor &&
		m.modelSelect.renderCachePoolNames == strings.Join(m.modelSelect.poolNames, ",") {
		return m.modelSelect.renderCacheText
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
			currentPool = m.agent.MainRoleCurrentPoolName()
		}
	}

	activeStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ffffff"))
	cursorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#ffff00"))
	dimStyle := DimStyle

	var lines []string
	for i, name := range m.modelSelect.poolNames {
		if name == currentPool {
			label := "✓ " + name
			if i == m.modelSelect.poolCursor {
				lines = append(lines, cursorStyle.Render("▸ ")+activeStyle.Render(label))
			} else {
				lines = append(lines, "  "+activeStyle.Render(label))
			}
		} else {
			label := "  " + name
			if i == m.modelSelect.poolCursor {
				lines = append(lines, cursorStyle.Render("▸ ")+dimStyle.Render(label))
			} else {
				lines = append(lines, "  "+dimStyle.Render(label))
			}
		}
	}
	content := strings.Join(lines, "\n")

	overlayCfg := OverlayConfig{
		Title:    modelSelectTitle(m.modelSelect.target),
		Hint:     modelSelectHint(m.modelSelect.target),
		MinWidth: 30,
		MaxWidth: 60,
	}
	dialog, _ := RenderOverlay(overlayCfg, content, len(lines), image.Rect(0, 0, m.width, m.height))

	m.modelSelect.renderCacheWidth = m.width
	m.modelSelect.renderCacheHeight = m.height
	m.modelSelect.renderCachePoolCursor = m.modelSelect.poolCursor
	m.modelSelect.renderCachePoolNames = strings.Join(m.modelSelect.poolNames, ",")
	m.modelSelect.renderCacheText = dialog
	return dialog
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
	return "j/k move  enter select  esc cancel"
}
