package tui

import (
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/identity"
)

// focusAgentForRequest orients the viewport to the agent whose confirm or
// question request is about to open. Only a SubAgent asking arrives here:
// "main" (and empty, the unset field) keeps the current view, because the main
// agent is not a separate switchable pane and yanking focus back to it would
// override the user's own navigation. When the asking agent is already
// focused, nothing changes.
func (m *Model) focusAgentForRequest(agentID string) {
	if agentID == "" || agentID == identity.MainAgentID {
		return
	}
	if agentID == m.focusedAgentIDOrMain() {
		return
	}
	m.setFocusedAgent(agentID)
}

func (m *Model) handleConfirmRequest(msg confirmRequestMsg) tea.Cmd {
	m.exitRenderFreeze()
	m.focusAgentForRequest(msg.request.AgentID)
	m.confirm = confirmState{
		request:   &msg.request,
		requestID: msg.request.RequestID,
		prevMode:  m.mode,
	}
	m.terminalTitleRequestSeen = m.displayState == stateForeground
	if msg.request.Timeout > 0 {
		m.confirm.deadline = time.Now().Add(msg.request.Timeout)
	}
	cmd := m.switchModeWithIME(ModeConfirm)
	m.recalcViewportSize()
	idleCmd := m.updateBackgroundIdleSweepState()
	flushCmd := m.requestStreamBoundaryFlush()
	titleCmd := m.syncTerminalTitleState()
	if !m.confirm.deadline.IsZero() {
		if cmd != nil || idleCmd != nil || flushCmd != nil || titleCmd != nil {
			return tea.Batch(cmd, idleCmd, flushCmd, titleCmd, confirmTimeoutTick())
		}
		return confirmTimeoutTick()
	}
	if cmd != nil || idleCmd != nil || flushCmd != nil || titleCmd != nil {
		return tea.Batch(cmd, idleCmd, flushCmd, titleCmd)
	}
	return nil
}

func (m *Model) handleConfirmTimeoutTick() tea.Cmd {
	if m.mode == ModeConfirm && !m.confirm.deadline.IsZero() {
		if time.Now().After(m.confirm.deadline) {
			return m.resolveConfirm(ConfirmResult{Action: ConfirmDeny})
		}
		m.recalcViewportSize()
		return confirmTimeoutTick()
	}
	return nil
}

func (m *Model) handleQuestionRequest(msg questionRequestMsg) tea.Cmd {
	m.exitRenderFreeze()
	m.focusAgentForRequest(msg.request.AgentID)
	ei := newQuestionTextarea(m.width)
	m.question = questionState{
		request:    &msg.request,
		requestID:  msg.requestID,
		responseCh: msg.request.ResponseCh,
		selected:   make(map[int]bool),
		prevMode:   m.mode,
		input:      ei,
	}
	m.terminalTitleRequestSeen = m.displayState == stateForeground
	if msg.request.Timeout > 0 {
		m.question.deadline = time.Now().Add(msg.request.Timeout)
	}
	var focusCmd tea.Cmd
	if len(msg.request.Questions) > 0 && len(msg.request.Questions[0].Options) == 0 {
		focusCmd = m.question.input.Focus()
	}
	cmd := m.switchModeWithIME(ModeQuestion)
	m.recalcViewportSize()
	idleCmd := m.updateBackgroundIdleSweepState()
	flushCmd := m.requestStreamBoundaryFlush()
	titleCmd := m.syncTerminalTitleState()
	if !m.question.deadline.IsZero() {
		return tea.Batch(cmd, focusCmd, idleCmd, flushCmd, titleCmd, questionTimeoutTick())
	}
	if cmd != nil || focusCmd != nil || idleCmd != nil || flushCmd != nil || titleCmd != nil {
		return tea.Batch(cmd, focusCmd, idleCmd, flushCmd, titleCmd)
	}
	return nil
}

func (m *Model) handleQuestionTimeoutTick() tea.Cmd {
	if m.mode == ModeQuestion && !m.question.deadline.IsZero() {
		if time.Now().After(m.question.deadline) {
			return m.cancelQuestion()
		}
		m.recalcViewportSize()
		return questionTimeoutTick()
	}
	return nil
}
