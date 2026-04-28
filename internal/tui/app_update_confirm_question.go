package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

func (m *Model) handleConfirmRequest(msg confirmRequestMsg) tea.Cmd {
	m.exitRenderFreeze()
	m.confirm = confirmState{
		request:   &msg.request,
		requestID: msg.request.RequestID,
		prevMode:  m.mode,
	}
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
	ei := newQuestionTextarea(m.width)
	m.question = questionState{
		request:    &msg.request,
		requestID:  msg.requestID,
		responseCh: msg.request.ResponseCh,
		selected:   make(map[int]bool),
		prevMode:   m.mode,
		input:      ei,
	}
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
