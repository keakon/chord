package tui

import (
	"fmt"
	"image"

	tea "charm.land/bubbletea/v2"
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
	planPath string
	prevMode Mode

	selector overlayListSelectorState
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
		planPath: planPath,
		prevMode: m.mode,
	}
	m.handoffSelect.selector.list = NewOverlayList(handoffItems(options), m.handoffSelectMaxVisible())
	if m.handoffSelect.selector.list != nil {
		m.handoffSelect.selector.list.SetCursor(cursorIdx)
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
		if m.handoffSelect.selector.list != nil {
			m.handoffSelect.selector.list.CursorDown()
		}

	case "k", "up":
		if m.handoffSelect.selector.list != nil {
			m.handoffSelect.selector.list.CursorUp()
		}

	case "g":
		if m.handoffSelect.selector.list != nil {
			m.handoffSelect.selector.list.CursorToTop()
		}

	case "G":
		if m.handoffSelect.selector.list != nil {
			m.handoffSelect.selector.list.CursorToBottom()
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
	if m.handoffSelect.selector.list != nil {
		cursor = m.handoffSelect.selector.list.CursorAt()
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
	if m.handoffSelect.selector.list == nil {
		return ""
	}

	overlayCfg := OverlayConfig{
		Title:    "Handoff To Agent",
		Hint:     "j/k move  g/G jump  enter confirm  esc cancel",
		MinWidth: 30,
		MaxWidth: 70,
	}
	area := image.Rect(0, 0, m.width, m.height)
	overlayCfg = normalizeOverlayConfig(overlayCfg, area)
	contentWidth := overlayCfg.MaxWidth - 4
	planLine := DimStyle.Render(ansi.Truncate(fmt.Sprintf("Plan: %s", m.handoffSelect.planPath), contentWidth, "…"))

	extraKey := "plan=" + m.handoffSelect.planPath
	maxVisible := m.handoffSelectMaxVisible()
	return m.handoffSelect.selector.Render(
		m,
		overlayCfg,
		planLine,
		0,
		maxVisible,
		extraKey,
		nil,
		area,
	)
}
