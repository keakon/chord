package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func (m *Model) openSessionDeleteConfirm() tea.Cmd {
	if m.mode != ModeSessionSelect || len(m.sessionSelect.options) == 0 {
		return nil
	}
	sel, ok := m.sessionSelectCurrentOption()
	if !ok {
		return nil
	}
	m.sessionDeleteConfirm = sessionDeleteConfirmState{
		session:  &sel,
		prevMode: m.mode,
	}
	cmd := m.switchModeWithIME(ModeSessionDeleteConfirm)
	m.recalcViewportSize()
	return cmd
}

func (m *Model) closeSessionDeleteConfirm() tea.Cmd {
	prevMode := m.sessionDeleteConfirm.prevMode
	if prevMode == 0 {
		prevMode = ModeSessionSelect
	}
	m.sessionDeleteConfirm = sessionDeleteConfirmState{}
	cmd := m.restoreModeWithIME(prevMode)
	m.recalcViewportSize()
	return cmd
}

func (m *Model) handleSessionDeleteConfirmKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "n", "N":
		return m.closeSessionDeleteConfirm()
	case "y", "Y", "enter":
		return m.confirmSessionDeletion()
	default:
		return nil
	}
}

func (m *Model) confirmSessionDeletion() tea.Cmd {
	target := m.sessionDeleteConfirm.session
	closeCmd := m.closeSessionDeleteConfirm()
	if target == nil || m.agent == nil {
		return closeCmd
	}
	if err := m.agent.DeleteSession(target.ID); err != nil {
		return tea.Batch(closeCmd, m.enqueueToast(err.Error(), "error"))
	}
	m.removeSessionSelectOptionByID(target.ID)
	msg := fmt.Sprintf("Deleted session %s", target.ID)
	return tea.Batch(closeCmd, m.enqueueToast(msg, "info"))
}

func (m *Model) removeSessionSelectOptionByID(sessionID string) {
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return
	}
	for i, option := range m.sessionSelect.options {
		if option.ID == id {
			m.removeSessionSelectOptionAt(i)
			return
		}
	}
}

func (m *Model) removeSessionSelectOptionAt(idx int) {
	if idx < 0 || idx >= len(m.sessionSelect.options) {
		return
	}
	m.sessionSelect.options = append(m.sessionSelect.options[:idx], m.sessionSelect.options[idx+1:]...)
	m.sessionSelect.searchCorpus = buildSessionSearchCorpus(m.sessionSelect.options)
	m.rebuildSessionSelectFilteredView(false)
	m.recalcViewportSize()
}

func (m *Model) renderSessionDeleteConfirmDialog() string {
	target := m.sessionDeleteConfirm.session
	if target == nil {
		return ""
	}
	preview := strings.TrimSpace(target.OriginalFirstUserMessage)
	if preview == "" {
		preview = strings.TrimSpace(target.FirstUserMessage)
	}
	if preview == "" {
		preview = "(no first message)"
	}
	if m.sessionDeleteConfirm.renderCacheText != "" &&
		m.sessionDeleteConfirm.renderCacheWidth == m.width &&
		m.sessionDeleteConfirm.renderCacheTheme == m.theme.Name &&
		m.sessionDeleteConfirm.renderCacheID == target.ID &&
		m.sessionDeleteConfirm.renderCacheForked == target.ForkedFrom &&
		m.sessionDeleteConfirm.renderCacheMsg == preview {
		return m.sessionDeleteConfirm.renderCacheText
	}
	const maxDialogWidth = 90
	maxWidth := m.width - 6
	if maxWidth > maxDialogWidth {
		maxWidth = maxDialogWidth
	}
	if maxWidth < 40 {
		maxWidth = 40
	}
	innerWidth := maxWidth - 2
	if innerWidth < 20 {
		innerWidth = 20
	}
	previewLines := wrapText(preview, max(10, innerWidth-2))
	for i := range previewLines {
		previewLines[i] = DimStyle.Render(previewLines[i])
	}
	lines := []string{
		ConfirmSeparatorStyle.Render("⚠ Delete Session?"),
		"",
		ConfirmToolStyle.Render("Session ID: " + target.ID),
	}
	if target.ForkedFrom != "" {
		lines = append(lines, ConfirmToolStyle.Render("Forked from: "+target.ForkedFrom))
	}
	lines = append(lines,
		ConfirmDenyStyle.Render("This permanently deletes the selected session directory."),
		"",
		ConfirmToolStyle.Render("First message preview:"),
	)
	lines = append(lines, previewLines...)
	lines = append(lines,
		"",
		lipgloss.JoinHorizontal(lipgloss.Left,
			ConfirmAllowStyle.Render("[y] Delete"),
			DimStyle.Render("  "),
			ConfirmDenyStyle.Render("[n/esc] Cancel"),
		),
	)
	body := strings.Join(lines, "\n")
	out := DirectoryBorderStyle.Width(maxWidth).Render(body)
	m.sessionDeleteConfirm.renderCacheWidth = m.width
	m.sessionDeleteConfirm.renderCacheTheme = m.theme.Name
	m.sessionDeleteConfirm.renderCacheID = target.ID
	m.sessionDeleteConfirm.renderCacheForked = target.ForkedFrom
	m.sessionDeleteConfirm.renderCacheMsg = preview
	m.sessionDeleteConfirm.renderCacheText = out
	return out
}
