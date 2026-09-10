package tui

import (
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/identity"
)

// focusAgentForRequest orients the viewport to the agent whose confirm or
// question request is about to open. Every model-initiated dialog — permission
// confirm, Done approval, and the Question tool — must be shown against the
// transcript that produced it, so the view switches to the asking agent,
// including the main agent (whose view is the empty focus id). Nothing changes
// when that agent is already focused.
func (m *Model) focusAgentForRequest(agentID string) {
	target := agentID
	if target == identity.MainAgentID {
		target = ""
	}
	if target == m.focusedAgentID {
		return
	}
	m.setFocusedAgent(target)
}

// dialogActive reports whether a model-initiated modal dialog is on screen.
// The TUI renders a single modal at a time, so a request that arrives while one
// is open waits in pendingDialogs instead of replacing the visible dialog.
// Handoff counts as active while its plan-content viewer is open on top of it:
// the decision is still pending.
func (m *Model) dialogActive() bool {
	return m.confirm.request != nil || m.question.request != nil || m.handoffSelect.active()
}

func (m *Model) handleConfirmRequest(msg confirmRequestMsg) tea.Cmd {
	if m.dialogActive() {
		m.pendingDialogs = append(m.pendingDialogs, pendingDialog{confirm: &msg, arrivedAt: time.Now()})
		return nil
	}
	return m.presentConfirmRequest(msg, m.mode, time.Now())
}

// presentConfirmRequest installs a confirmation dialog (permission ask, Done
// approval) as the active modal. prevMode is restored once the dialog closes;
// it is passed in rather than read from m.mode so a queued dialog restores the
// mode that was active before the queue started. arrivedAt is when the request
// reached the TUI: the agent counts its timeout from request creation, so the
// displayed deadline anchors there rather than resetting to show a full timeout
// the agent may already have auto-resolved.
func (m *Model) presentConfirmRequest(msg confirmRequestMsg, prevMode Mode, arrivedAt time.Time) tea.Cmd {
	m.exitRenderFreeze()
	m.focusAgentForRequest(msg.request.AgentID)
	m.confirm = confirmState{
		request:   &msg.request,
		requestID: msg.request.RequestID,
		prevMode:  prevMode,
	}
	m.terminalTitleRequestSeen = m.displayState == stateForeground
	var timeoutCmd tea.Cmd
	if msg.request.Timeout > 0 {
		m.confirm.deadline = arrivedAt.Add(msg.request.Timeout)
		timeoutCmd = confirmTimeoutTick()
	}
	cmd := m.switchModeWithIME(ModeConfirm)
	m.recalcViewportSize()
	idleCmd := m.updateBackgroundIdleSweepState()
	flushCmd := m.requestStreamBoundaryFlush()
	titleCmd := m.syncTerminalTitleState()
	return tea.Batch(cmd, idleCmd, flushCmd, titleCmd, timeoutCmd)
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
	if m.dialogActive() {
		m.pendingDialogs = append(m.pendingDialogs, pendingDialog{question: &msg, arrivedAt: time.Now()})
		return nil
	}
	return m.presentQuestionRequest(msg, m.mode, time.Now())
}

// presentQuestionRequest installs a Question dialog as the active modal.
// prevMode and arrivedAt handling match presentConfirmRequest.
func (m *Model) presentQuestionRequest(msg questionRequestMsg, prevMode Mode, arrivedAt time.Time) tea.Cmd {
	m.exitRenderFreeze()
	m.focusAgentForRequest(msg.request.AgentID)
	ei := newQuestionTextarea(m.width)
	m.question = questionState{
		request:    &msg.request,
		requestID:  msg.requestID,
		responseCh: msg.request.ResponseCh,
		selected:   make(map[int]bool),
		prevMode:   prevMode,
		input:      ei,
	}
	m.terminalTitleRequestSeen = m.displayState == stateForeground
	var timeoutCmd tea.Cmd
	if msg.request.Timeout > 0 {
		m.question.deadline = arrivedAt.Add(msg.request.Timeout)
		timeoutCmd = questionTimeoutTick()
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
	return tea.Batch(cmd, focusCmd, idleCmd, flushCmd, titleCmd, timeoutCmd)
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
