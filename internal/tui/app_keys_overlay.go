package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

const pendingQuitWindow = 2 * time.Second

func isCtrlC(msg tea.KeyMsg) bool {
	if msg.String() == "ctrl+c" {
		return true
	}
	key := msg.Key()
	if key.Code == 3 {
		return true
	}
	return key.Code == 'c' && key.Mod&tea.ModCtrl != 0
}

func isSuperC(msg tea.KeyMsg) bool {
	key := msg.Key()
	return key.Code == 'c' && key.Mod&tea.ModSuper != 0
}

func isSuperV(msg tea.KeyMsg) bool {
	key := msg.Key()
	return key.Code == 'v' && key.Mod&tea.ModSuper != 0
}

func (m *Model) cancelBusyAgent() tea.Cmd {
	if m.agent == nil || !m.isAgentBusy() {
		return nil
	}
	if cancelled := m.agent.CancelCurrentTurn(); cancelled {
		m.pauseQueuedDraftDrainOnce = true
		delete(m.turnBusyStartedAt, turnBusyKey(m.focusedAgentID))
		return m.enqueueToast("Cancelled current operation", "info")
	}
	return nil
}

// clearPendingQuitTick schedules a 2s timer that auto-clears the pending quit
// hint. The generation parameter prevents stale timers from clearing newer state.
func clearPendingQuitTick(gen uint64) tea.Cmd {
	return tea.Tick(pendingQuitWindow, func(time.Time) tea.Msg {
		return clearPendingQuitMsg{generation: gen}
	})
}

// clearPendingQuit resets the exit-confirmation state. pendingQuitGen is
// intentionally left unchanged so each new clearPendingQuitTick uses a
// strictly newer generation and stale timers can never match again.
func (m *Model) clearPendingQuit() {
	m.pendingQuitAt = time.Time{}
	m.pendingQuitBy = ""
}

func (m *Model) clearPendingQuitForKey(msg tea.KeyMsg) {
	if m.pendingQuitBy == "" || m.pendingQuitAt.IsZero() {
		return
	}
	m.clearPendingQuit()
}

func (m *Model) handleCtrlC() tea.Cmd {
	now := time.Now()
	if m.mode == ModeConfirm && m.confirm.request != nil {
		return m.resolveConfirm(ConfirmResult{Action: ConfirmDeny})
	}
	if m.mode == ModeQuestion && m.question.request != nil {
		return m.cancelQuestion()
	}
	if m.mode == ModeSearch {
		ClearSearch(&m.search.State)
		m.search.Input.Blur()
		cmd := m.restoreModeWithIME(m.search.PrevMode)
		m.recalcViewportSize()
		return cmd
	}
	if m.mode == ModeModelSelect {
		prevMode := m.modelSelect.prevMode
		cmd := m.restoreModeWithIME(prevMode)
		m.recalcViewportSize()
		if prevMode == ModeInsert {
			return tea.Batch(cmd, m.input.Focus())
		}
		return cmd
	}
	if m.mode == ModeSessionSelect {
		prevMode := m.sessionSelect.prevMode
		cmd := m.restoreModeWithIME(prevMode)
		m.recalcViewportSize()
		if prevMode == ModeInsert {
			return tea.Batch(cmd, m.input.Focus())
		}
		return cmd
	}
	if m.mode == ModeUsageStats {
		return m.closeUsageStats()
	}
	if m.pendingQuitBy == "ctrl+c" && !m.pendingQuitAt.IsZero() && now.Sub(m.pendingQuitAt) < pendingQuitWindow {
		m.clearPendingQuit()
		m.quitting = true
		return tea.Quit
	}
	if m.pendingQuitBy == "q" {
		m.clearPendingQuit()
		return nil
	}
	// Phase A: Ctrl+C no longer cancels busy agent or compaction.
	// ESC is now the dedicated cancel key. Ctrl+C only initiates quit.
	m.pendingQuitAt = now
	m.pendingQuitBy = "ctrl+c"
	m.pendingQuitGen++
	return clearPendingQuitTick(m.pendingQuitGen)
}

func (m *Model) handleSearchKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "enter":
		query := strings.TrimSpace(m.search.Input.Value())
		prevOffset := m.viewport.offset
		if query != "" {
			m.executeSearchAgainstCurrentTranscript(query)
			if match, ok := m.search.State.CurrentMatch(); ok {
				m.maybeScrollToSearchMatch(match, "search_enter")
			}
		}
		m.search.Input.Blur()
		cmd := m.switchModeWithIME(ModeNormal)
		m.recalcViewportSize()
		return m.refreshInlineImagesIfViewportMoved(prevOffset, cmd)
	case "esc":
		m.clearActiveSearch()
		cmd := m.switchModeWithIME(m.search.PrevMode)
		m.recalcViewportSize()
		return cmd
	default:
		var cmd tea.Cmd
		m.search, cmd = m.search.Update(msg)
		return cmd
	}
}

func (m Model) searchCurrentBlockIndex() int {
	if !m.search.State.Active || m.viewport == nil {
		return -1
	}
	match, ok := m.search.State.CurrentMatch()
	if !ok {
		return -1
	}
	if match.BlockID > 0 {
		for i, block := range m.viewport.visibleBlocks() {
			if block != nil && block.ID == match.BlockID {
				return i
			}
		}
		return -1
	}
	return match.BlockIndex
}

func (m *Model) handleDirectoryKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "ctrl+j", "q":
		cmd := m.switchModeWithIME(ModeNormal)
		m.recalcViewportSize()
		return cmd
	case "j", "down":
		if m.dirList != nil {
			m.dirList.CursorDown()
		}
	case "k", "up":
		if m.dirList != nil {
			m.dirList.CursorUp()
		}
	case "enter":
		cursor := 0
		prevOffset := m.viewport.offset
		if m.dirList != nil {
			cursor = m.dirList.CursorAt()
		}
		if cursor >= 0 && cursor < len(m.dirEntries) {
			entry := m.dirEntries[cursor]
			m.maybeScrollToDirectoryEntry(entry, "directory_enter")
		}
		cmd := m.switchModeWithIME(ModeNormal)
		m.recalcViewportSize()
		return m.refreshInlineImagesIfViewportMoved(prevOffset, cmd)
	case "G":
		if m.dirList != nil {
			m.dirList.CursorToBottom()
		}
	case "g":
		if m.dirList != nil {
			m.dirList.CursorToTop()
		}
	}
	return nil
}
