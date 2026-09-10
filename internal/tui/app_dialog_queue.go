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
	question  *questionRequestMsg
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
	case d.question != nil:
		return d.question.request.Timeout
	}
	// Handoff prompts have no timeout: the agent waits until the user decides.
	return 0
}

// presentPendingDialog installs a queued dialog. prevMode is the mode that was
// active before the dialog queue started, so chained queued dialogs all restore
// the same base mode instead of depending on the dialog before them.
func (m *Model) presentPendingDialog(d pendingDialog, prevMode Mode) tea.Cmd {
	switch {
	case d.confirm != nil:
		return m.presentConfirmRequest(*d.confirm, prevMode)
	case d.question != nil:
		return m.presentQuestionRequest(*d.question, prevMode)
	case d.handoff != nil:
		return m.openHandoffSelect(d.handoff.planPath, d.handoff.requestID, d.handoff.agentID, prevMode)
	}
	return nil
}

// finishDialog closes the active dialog: it presents the next queued dialog on
// top of prevMode, or restores prevMode (re-focusing the composer in insert
// mode) once the queue drains. extraCmds carry the caller's channel
// re-subscription, toasts, and idle-sweep work, and run alongside either
// branch. A queued request that declines to open (a Handoff with no eligible
// target cancels itself) is skipped and the next one is tried; prevMode is only
// restored when nothing is on screen.
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
	}
	extraCmds = append(extraCmds, m.restoreModeWithIME(prevMode))
	if prevMode == ModeInsert {
		extraCmds = append(extraCmds, m.input.Focus())
	}
	return tea.Batch(extraCmds...)
}
