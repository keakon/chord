package tui

import tea "charm.land/bubbletea/v2"

type helpState struct {
	scrollOffset int
	prevMode     Mode
	cachedLines  []string // cached output of helpLines; invalidated on width change
	cachedWidth  int      // width used to produce cachedLines

	renderCacheWidth  int
	renderCacheHeight int
	renderCacheOffset int
	renderCacheTheme  string
	renderCacheText   string
}

func (m *Model) openHelp() tea.Cmd {
	if m.mode == ModeHelp {
		return nil
	}
	prevMode := m.mode
	m.clearActiveSearch()
	if prevMode == ModeInsert {
		m.input.Blur()
	}
	m.clearChordState()
	m.help = helpState{prevMode: prevMode}
	m.mode = ModeHelp
	m.recalcViewportSize()
	return nil
}

func (m *Model) closeHelp() tea.Cmd {
	if m.mode != ModeHelp {
		return nil
	}
	prevMode := m.help.prevMode
	m.help = helpState{}
	cmd := m.restoreModeWithIME(prevMode)
	m.recalcViewportSize()
	if prevMode == ModeInsert {
		return tea.Batch(cmd, m.input.Focus())
	}
	return cmd
}

// cachedHelpLines returns the help content lines, reusing a cached copy when
// the width has not changed. This avoids regenerating the full help text on
// every key press and render.
func (m *Model) cachedHelpLines(width int) []string {
	if m.help.cachedWidth == width && m.help.cachedLines != nil {
		return m.help.cachedLines
	}
	lines := m.helpLines(width)
	m.help.cachedLines = lines
	m.help.cachedWidth = width
	return lines
}

func (m *Model) helpMaxScroll(width int) int {
	lines := m.cachedHelpLines(width)
	if len(lines) <= m.viewport.height {
		return 0
	}
	return len(lines) - m.viewport.height
}

func (m *Model) clampHelpScroll(width int) {
	maxScroll := m.helpMaxScroll(width)
	if m.help.scrollOffset < 0 {
		m.help.scrollOffset = 0
	}
	if m.help.scrollOffset > maxScroll {
		m.help.scrollOffset = maxScroll
	}
}

func (m *Model) handleHelpKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "q", "?", "shift+/":
		return m.closeHelp()
	case "j", "down":
		m.help.scrollOffset++
	case "k", "up":
		m.help.scrollOffset--
	case "ctrl+f":
		m.help.scrollOffset += max(1, m.viewport.height-3)
	case "ctrl+b":
		m.help.scrollOffset -= max(1, m.viewport.height-3)
	case "g":
		m.help.scrollOffset = 0
	case "G":
		m.help.scrollOffset = m.helpMaxScroll(m.viewport.width)
	}
	m.clampHelpScroll(m.viewport.width)
	return nil
}
