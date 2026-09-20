package tui

import (
	"time"

	tea "github.com/keakon/bubbletea/v2"
)

// pendingDialog is a model-initiated dialog (permission confirm, Done approval,
// a Question, or a Handoff plan prompt) that arrived while another dialog was
// already on screen. The TUI shows one modal at a time, so a request that
// arrives during an open dialog waits here and is presented — against its own
// asking agent's view — in arrival order once the current dialog closes.
type pendingDialog struct {
	confirm   *confirmRequestMsg
	question  *questionDialog
	handoff   *handoffSelectRequestMsg
	arrivedAt time.Time
}

// popPendingDialog returns the longest-waiting dialog that has not already
// outlived its timeout, dropping expired entries so a request the agent has
// auto-resolved is never shown. It reports false when no live dialog remains.
func (m *Model) popPendingDialog() (pendingDialog, bool) {
	for len(m.pendingDialogs) > 0 {
		next := m.pendingDialogs[0]
		m.pendingDialogs = m.pendingDialogs[1:]
		if !next.expired(time.Now()) {
			return next, true
		}
	}
	return pendingDialog{}, false
}

// expired reports whether the request's own timeout elapsed while it queued.
// A zero timeout never expires.
func (d pendingDialog) expired(now time.Time) bool {
	timeout := d.timeout()
	return timeout > 0 && !now.Before(d.arrivedAt.Add(timeout))
}

func (d pendingDialog) timeout() time.Duration {
	switch {
	case d.confirm != nil:
		return d.confirm.request.Timeout
	}
	// A Question never expires locally: its deadline is absolute and the
	// broker closes it with a QuestionResolvedEvent that removes the queued
	// entry. Handoff prompts have no timeout either; the agent waits until the
	// user decides.
	return 0
}

// presentPendingDialog installs a queued dialog. prevMode is the mode that was
// active before the dialog queue started, so chained queued dialogs all restore
// the same base mode instead of depending on the dialog before them.
func (m *Model) presentPendingDialog(d pendingDialog, prevMode Mode) tea.Cmd {
	switch {
	case d.confirm != nil:
		return m.presentConfirmRequest(*d.confirm, prevMode, d.arrivedAt)
	case d.question != nil:
		return m.presentQuestionRequest(*d.question, prevMode)
	case d.handoff != nil:
		return m.openHandoffSelect(d.handoff.planPath, d.handoff.requestID, d.handoff.agentID, prevMode)
	}
	return nil
}

// resetDialogsOnSessionSwitch drops every dialog tied to the outgoing session:
// queued requests plus the active confirm/question/handoff modal. The agent
// cancels the turn on switch, so those request IDs are already dead; keeping
// them on screen would answer a gone request and leave dialogActive() true,
// which suppresses the idle sweep. The mode active before the dropped dialog
// is restored (insert mode re-focuses the composer).
func (m *Model) resetDialogsOnSessionSwitch() tea.Cmd {
	m.pendingDialogs = nil
	prevMode := ModeNormal
	hadDialog := false
	if m.confirm.request != nil {
		prevMode = m.confirm.prevMode
		m.confirm = confirmState{}
		hadDialog = true
	}
	if m.question.request != nil {
		prevMode = m.question.prevMode
		m.question = questionState{}
		hadDialog = true
	}
	if m.handoffSelect.active() {
		prevMode = m.handoffSelect.prevMode
		m.clearHandoffSelect()
		hadDialog = true
	}
	if !hadDialog {
		return nil
	}
	m.terminalTitleRequestSeen = false
	m.recalcViewportSize()
	cmds := []tea.Cmd{m.syncTerminalTitleState(), m.restoreModeWithIME(prevMode)}
	if prevMode == ModeInsert {
		cmds = append(cmds, m.input.Focus())
	}
	return tea.Batch(cmds...)
}

// finishDialog closes the active dialog: it presents the next queued dialog on
// top of prevMode, or restores prevMode (re-focusing the composer in insert
// mode) once the queue drains. extraCmds carry the caller's channel
// re-subscription, toasts, and idle-sweep work, and run alongside either
// branch. A queued request that declines to open (a Handoff with no eligible
// target cancels itself) is skipped and the next one is tried; prevMode is only
// restored when nothing is on screen. Commands a skipped dialog returned (its
// toast tick) are kept instead of dropped.
func (m *Model) finishDialog(prevMode Mode, extraCmds ...tea.Cmd) tea.Cmd {
	for {
		next, ok := m.popPendingDialog()
		if !ok {
			break
		}
		cmd := m.presentPendingDialog(next, prevMode)
		if m.dialogActive() {
			return tea.Batch(append(extraCmds, cmd)...)
		}
		if cmd != nil {
			extraCmds = append(extraCmds, cmd)
		}
	}
	extraCmds = append(extraCmds, m.restoreModeWithIME(prevMode))
	if prevMode == ModeInsert {
		extraCmds = append(extraCmds, m.input.Focus())
	}
	return tea.Batch(extraCmds...)
}
