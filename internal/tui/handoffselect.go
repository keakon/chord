package tui

import (
	"fmt"
	"image"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// ---------------------------------------------------------------------------
// Handoff agent selector state
// ---------------------------------------------------------------------------

// handoffOption describes a single agent entry in the Handoff selector.
type handoffOption struct {
	Name        string // agent name (e.g. "builder")
	Description string // human-readable description
	IsDefault   bool   // true if this is the default target (builder)
}

// handoffSelectState holds the transient state for the Handoff agent selector.
type handoffSelectState struct {
	options  []handoffOption
	list     *OverlayList
	planPath string
	prevMode Mode

	renderCacheWidth      int
	renderCacheHeight     int
	renderCacheMaxVisible int
	renderCacheTheme      string
	renderCacheListVer    uint64
	renderCachePlanPath   string
	renderCacheText       string
}

// ---------------------------------------------------------------------------
// Opening the selector
// ---------------------------------------------------------------------------

// openHandoffSelect opens the Handoff agent selection dialog.
func (m *Model) openHandoffSelect(planPath string) {
	if m.agent == nil {
		return
	}

	// Build options from available agent configs.
	// Always put builder first as the default.
	agentNames := m.agent.AvailableAgents()
	options := make([]handoffOption, 0, len(agentNames)+1)
	cursorIdx := 0

	for i, name := range agentNames {
		isDefault := name == "builder"
		options = append(options, handoffOption{
			Name:      name,
			IsDefault: isDefault,
		})
		if isDefault {
			cursorIdx = i
		}
	}

	// Fallback: if no agents available, add builder as sole option.
	if len(options) == 0 {
		options = append(options, handoffOption{Name: "builder", IsDefault: true})
	}

	m.clearChordState()
	m.clearActiveSearch()
	m.handoffSelect = handoffSelectState{
		options:  options,
		list:     NewOverlayList(handoffItems(options), m.handoffSelectMaxVisible()),
		planPath: planPath,
		prevMode: m.mode,
	}
	if m.handoffSelect.list != nil {
		m.handoffSelect.list.SetCursor(cursorIdx)
	}
	if m.mode == ModeInsert {
		m.input.Blur()
	}
	m.mode = ModeHandoffSelect
	m.recalcViewportSize()
}

// ---------------------------------------------------------------------------
// Key handling
// ---------------------------------------------------------------------------

// handleHandoffSelectKey processes keyboard input for the Handoff agent selector.
func (m *Model) handleHandoffSelectKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()

	switch key {
	case "esc":
		// Cancel — resume normal session.
		prevMode := m.handoffSelect.prevMode
		cmd := m.restoreModeWithIME(prevMode)
		m.recalcViewportSize()
		if prevMode == ModeInsert {
			return tea.Batch(cmd, m.input.Focus())
		}
		return cmd

	case "j", "down":
		if m.handoffSelect.list != nil {
			m.handoffSelect.list.CursorDown()
		}

	case "k", "up":
		if m.handoffSelect.list != nil {
			m.handoffSelect.list.CursorUp()
		}

	case "g":
		if m.handoffSelect.list != nil {
			m.handoffSelect.list.CursorToTop()
		}

	case "G":
		if m.handoffSelect.list != nil {
			m.handoffSelect.list.CursorToBottom()
		}

	case "enter":
		return m.confirmHandoff()
	}

	return nil
}

func (m *Model) confirmHandoff() tea.Cmd {
	if m.agent == nil || len(m.handoffSelect.options) == 0 {
		return nil
	}
	cursor := 0
	if m.handoffSelect.list != nil {
		cursor = m.handoffSelect.list.CursorAt()
	}
	if cursor < 0 || cursor >= len(m.handoffSelect.options) {
		return nil
	}
	selected := m.handoffSelect.options[cursor]
	planPath := m.handoffSelect.planPath

	// Restore mode before triggering execution.
	prevMode := m.handoffSelect.prevMode
	cmd := m.restoreModeWithIME(prevMode)
	m.recalcViewportSize()

	m.agent.ExecutePlan(planPath, selected.Name)
	if prevMode == ModeInsert {
		return tea.Batch(cmd, m.input.Focus())
	}
	return cmd
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

func (m *Model) handoffSelectMaxVisible() int {
	maxVisible := m.height/2 - 5
	if maxVisible < 3 {
		maxVisible = 3
	}
	return maxVisible
}

func handoffItems(options []handoffOption) []OverlayListItem {
	items := make([]OverlayListItem, 0, len(options))
	for _, opt := range options {
		label := opt.Name
		if opt.IsDefault {
			label += " (default)"
		}
		items = append(items, OverlayListItem{ID: opt.Name, Label: label})
	}
	return items
}

func (m *Model) renderHandoffSelectDialog() string {
	if m.handoffSelect.list == nil {
		return ""
	}
	maxVisible := m.handoffSelectMaxVisible()
	m.handoffSelect.list.SetMaxVisible(maxVisible)
	listVersion := m.handoffSelect.list.RenderVersion()
	if m.handoffSelect.renderCacheText != "" &&
		m.handoffSelect.renderCacheWidth == m.width &&
		m.handoffSelect.renderCacheHeight == m.height &&
		m.handoffSelect.renderCacheMaxVisible == maxVisible &&
		m.handoffSelect.renderCacheTheme == m.theme.Name &&
		m.handoffSelect.renderCacheListVer == listVersion &&
		m.handoffSelect.renderCachePlanPath == m.handoffSelect.planPath {
		return m.handoffSelect.renderCacheText
	}
	overlayCfg := OverlayConfig{
		Title:    "Handoff To Agent",
		Hint:     "j/k move  g/G jump  enter confirm  esc cancel",
		MinWidth: 30,
		MaxWidth: 70,
	}
	contentWidth := overlayCfg.MaxWidth - 4
	content := lipgloss.JoinVertical(lipgloss.Left,
		DimStyle.Render(ansi.Truncate(fmt.Sprintf("Plan: %s", m.handoffSelect.planPath), contentWidth, "…")),
		m.handoffSelect.list.Render(contentWidth),
	)
	dialog, _ := RenderOverlay(overlayCfg, content, lipgloss.Height(content), image.Rect(0, 0, m.width, m.height))
	m.handoffSelect.renderCacheWidth = m.width
	m.handoffSelect.renderCacheHeight = m.height
	m.handoffSelect.renderCacheMaxVisible = maxVisible
	m.handoffSelect.renderCacheTheme = m.theme.Name
	m.handoffSelect.renderCacheListVer = listVersion
	m.handoffSelect.renderCachePlanPath = m.handoffSelect.planPath
	m.handoffSelect.renderCacheText = dialog
	return dialog
}
