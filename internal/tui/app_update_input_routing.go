package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

func (m *Model) handleNonKeyInputMsg(msg tea.Msg) tea.Cmd {
	switch m.mode {
	case ModeInsert:
		// PasteMsg (bracket paste via cmd+v): if content is empty the clipboard
		// likely holds an image, so try image paste instead of inserting nothing.
		if pm, ok := msg.(tea.PasteMsg); ok {
			if strings.TrimSpace(pm.Content) == "" {
				return m.pasteFromClipboard()
			}
			m.input.ClearSelection()
			if m.input.InsertLargePaste(pm.Content) {
				m.input.syncHeight()
				if m.atMentionOpen {
					m.syncAtMentionQuery()
				}
				m.recalcViewportSize()
				return nil
			}
		}
		cmd := m.input.Update(msg)
		// PasteMsg may insert multiple lines; sync the input height so all
		// pasted content is visible.
		if _, ok := msg.(tea.PasteMsg); ok {
			m.input.syncHeight()
			if m.atMentionOpen {
				m.syncAtMentionQuery()
			}
			m.recalcViewportSize()
		}
		return cmd
	case ModeConfirm:
		if m.confirm.editing {
			var cmd tea.Cmd
			m.confirm.editInput, cmd = m.confirm.editInput.Update(msg)
			return cmd
		}
	case ModeQuestion:
		if m.question.custom || (m.question.request != nil &&
			m.question.currentQ < len(m.question.request.Questions) &&
			len(m.question.request.Questions[m.question.currentQ].Options) == 0) {
			var cmd tea.Cmd
			m.question.input, cmd = m.question.input.Update(msg)
			return cmd
		}
	case ModeSearch:
		var cmd tea.Cmd
		m.search, cmd = m.search.Update(msg)
		return cmd
	}
	return nil
}
