package tui

import (
	"strings"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/inputhistory"
)

type inputHistoryClearMsg struct {
	generation uint64
	result     inputhistory.ClearResult
}

func (m *Model) routeInputHistoryCommand(value string, snapshot submittedInput) tea.Cmd {
	if value != historyCommand && value != historyClearCommand {
		return m.enqueueToast("Usage: /history or /history clear", "info")
	}
	if len(m.attachments) > 0 || m.input.HasInlinePastes() {
		return m.enqueueToast("Remove attachments and pasted objects before using /history", "info")
	}
	if m.startupRestorePending || m.sessionSwitch.active() || m.takeoverInputFrozen() {
		return m.enqueueToast("Wait for the session to finish loading or switching", "info")
	}
	if m.inputHistoryStore == nil {
		return m.enqueueToast("Input history is unavailable in this runtime", "info")
	}
	cmd := m.recordSubmittedInput(snapshot)
	m.input.Reset()
	m.slashCompleteSelected = 0
	if strings.EqualFold(value, historyClearCommand) {
		m.inputHistory.token.generation++
		m.inputHistory.confirm = false
		m.mode = ModeInputHistoryClearConfirm
		return cmd
	}
	return tea.Batch(cmd, m.openInputHistory(true))
}
func (m *Model) handleInputHistoryClearKey(msg tea.KeyMsg) tea.Cmd {
	s := &m.inputHistory
	switch msg.String() {
	case "esc", "ctrl+c":
		m.closeInputHistory()
	case "tab", "shift+tab", "left", "right":
		if !s.clearing {
			s.confirm = !s.confirm
		}
	case "enter":
		if s.clearing {
			return nil
		}
		if !s.confirm {
			m.closeInputHistory()
			return nil
		}
		ticket, err := m.inputHistoryStore.TryClear()
		if err != nil {
			return m.enqueueToast(err.Error(), "warn")
		}
		s.clearing = true
		generation := s.token.generation
		return func() tea.Msg { return inputHistoryClearMsg{generation, <-ticket.Result} }
	}
	return nil
}
