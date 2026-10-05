package tui

import tea "github.com/keakon/bubbletea/v2"

func (m *Model) switchMainRoleFromView() tea.Cmd {
	if m.focusedAgentID != "" {
		return m.enqueueToast("Switch to the main agent view to change its role", "info")
	}
	return m.handleSwitchRole()
}

func (m *Model) stopCurrentOperation() tea.Cmd {
	m.clearPendingQuit()
	m.clearChordState()
	if m.agent == nil {
		return nil
	}
	stoppedLoop := false
	if m.agent.CurrentLoopState() != "" {
		m.agent.DisableLoopMode()
		stoppedLoop = true
	}
	cmd := m.cancelBusyAgent()
	if m.agent.IsCompactionRunning() && m.agent.CancelCompaction() {
		return tea.Batch(cmd, m.enqueueToast("Cancelling context compaction...", "info"))
	}
	if cmd != nil {
		return cmd
	}
	if stoppedLoop {
		return m.enqueueToast("Loop disabled.", "info")
	}
	return m.enqueueToast("No active operation to stop", "info")
}
