package tui

import tea "github.com/keakon/bubbletea/v2"

// Transfer admission blocks new work, not answers or an explicit user stop.
// Keep these routes separate from composer and configuration shortcuts.
func (m *Model) handleTakeoverKey(msg tea.KeyMsg) tea.Cmd {
	if isCtrlC(msg) {
		return m.handleCtrlC()
	}
	if isSuperC(msg) {
		return m.handleSuperCopy()
	}
	key := msg.String()
	switch m.mode {
	case ModeQuestion:
		// Withdrawal adds a new user requirement; answer/decline only settle
		// the already running interaction.
		if key != "ctrl+w" {
			return m.handleQuestionKey(msg)
		}
	case ModeConfirm, ModeStopJobConfirm, ModeJobsOverlay, ModeStatus,
		ModeContentViewer, ModeImageViewer, ModeUsageStats, ModeErrorPanel, ModeHelp:
		return m.handleModeKey(msg)
	case ModeInsert, ModeNormal:
		switch {
		case key == "esc", keyMatches(key, m.keyMap.Stop):
			return m.stopCurrentOperation()
		case keyMatches(key, m.keyMap.BackgroundJobs):
			return m.openJobsOverlay()
		case keyMatches(key, m.keyMap.InsertPageUp), keyMatches(key, m.keyMap.FullPageUp):
			return m.pageTranscript(-1)
		case keyMatches(key, m.keyMap.InsertPageDown), keyMatches(key, m.keyMap.FullPageDown):
			return m.pageTranscript(1)
		}
		if m.mode == ModeNormal {
			switch {
			case keyMatches(key, m.keyMap.ScrollUp):
				return m.repeatNormalVertical(-1, 1)
			case keyMatches(key, m.keyMap.ScrollDown):
				return m.repeatNormalVertical(1, 1)
			case keyMatches(key, m.keyMap.ScrollToBottom):
				return m.jumpToLastVisibleBlock()
			}
		}
	default:
		if key == "esc" {
			return m.handleModeKey(msg)
		}
	}
	return m.enqueueToast("Session transfer is in progress; input remains paused", "info")
}

func (m *Model) handleTakeoverMouse(msg tea.MouseMsg) tea.Cmd {
	switch m.mode {
	case ModeQuestion, ModeConfirm, ModeStopJobConfirm, ModeJobsOverlay, ModeStatus,
		ModeContentViewer, ModeImageViewer, ModeUsageStats, ModeErrorPanel, ModeHelp:
		if cmd, handled := m.handleModalMouseMsg(msg); handled {
			return cmd
		}
	}
	mouse := msg.Mouse()
	hits := m.mouseHitZones(mouse)
	switch msg.(type) {
	case tea.MouseWheelMsg:
		return m.handleMouseWheel(mouse)
	case tea.MouseClickMsg:
		if mouse.Button != tea.MouseLeft {
			return nil
		}
		if hits.inInfoPanel {
			return m.handleInfoPanelMouseClick(mouse)
		}
		if cmd, handled := m.handleStatusCopyClick(mouse.X, mouse.Y); handled {
			return cmd
		}
		if hits.inViewport {
			return m.handleViewportMouseClick(mouse, hits)
		}
	case tea.MouseMotionMsg:
		return m.handleMouseMotion(mouse, hits)
	case tea.MouseReleaseMsg:
		return m.handleMouseRelease(mouse)
	}
	return nil
}
