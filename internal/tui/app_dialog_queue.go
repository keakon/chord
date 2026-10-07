package tui

import (
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/tools"
)

const (
	dialogPriorityBlocking = iota
	dialogPriorityCompletion
	dialogPriorityBackground
)

// pendingDialog retains each decision until it is presented or invalidated.
type pendingDialog struct {
	confirm    *confirmRequestMsg
	handoff    *handoffSelectRequestMsg
	questionID string
	arrivedAt  time.Time
}

// popPendingDialog selects the highest-priority live request, preserving FIFO
// within a priority. A request that is already displayed is never preempted.
func (m *Model) popPendingDialog() (pendingDialog, bool) {
	m.prunePendingDialogs()
	best := -1
	for i, d := range m.pendingDialogs {
		if best < 0 || m.compareDialogs(d, m.pendingDialogs[best]) < 0 {
			best = i
		}
	}
	if best < 0 {
		return pendingDialog{}, false
	}
	next := m.pendingDialogs[best]
	m.pendingDialogs = append(m.pendingDialogs[:best], m.pendingDialogs[best+1:]...)
	return next, true
}

func (m *Model) dialogPriority(d pendingDialog) int {
	if d.confirm != nil {
		if toolNameKey(d.confirm.request.ToolName) == tools.NameDone {
			return dialogPriorityCompletion
		}
		return dialogPriorityBlocking
	}
	if d.handoff != nil {
		return dialogPriorityBlocking
	}
	q := m.questionRecords[d.questionID]
	waiting := !q.Async
	if m.questionWaitBinding == q.BindingID && q.BindingID != "" {
		waiting = m.questionWaiting[q.ID]
	}
	if waiting {
		return dialogPriorityBlocking
	}
	return dialogPriorityBackground
}

func (m *Model) compareDialogs(a, b pendingDialog) int {
	if priority := m.dialogPriority(a) - m.dialogPriority(b); priority != 0 {
		return priority
	}
	return a.arrivedAt.Compare(b.arrivedAt)
}

func (m *Model) prunePendingDialogs() {
	kept := m.pendingDialogs[:0]
	for _, d := range m.pendingDialogs {
		if d.expired(time.Now()) {
			continue
		}
		if d.questionID != "" {
			q, ok := m.questionRecords[d.questionID]
			if !ok || !q.Visible || q.Outcome != "" {
				continue
			}
		}
		kept = append(kept, d)
	}
	m.pendingDialogs = kept
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
	// Core closes it with a committed question state update. Handoff prompts
	// have no timeout either; the agent waits until the user decides.
	return 0
}

// presentPendingDialog installs a queued dialog. prevMode is the mode that was
// active before the dialog queue started, so chained queued dialogs all restore
// the same base mode instead of depending on the dialog before them.
func (m *Model) presentPendingDialog(d pendingDialog, prevMode Mode) tea.Cmd {
	switch {
	case d.confirm != nil:
		return m.presentConfirmRequest(*d.confirm, prevMode, d.arrivedAt)
	case d.handoff != nil:
		cmd := m.openHandoffSelect(d.handoff.planPath, d.handoff.requestID, d.handoff.agentID, prevMode)
		if m.handoffSelect.active() {
			m.handoffSelect.arrivedAt = d.arrivedAt
		}
		return cmd
	case d.questionID != "":
		return m.presentQuestionByID(d.questionID, prevMode)
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
	m.questionRecords = nil
	m.questionShown = nil
	m.questionWaiting = nil
	m.questionWaitBinding = ""
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
	if m.mode == ModeContentViewer {
		// A viewer opened over the dropped dialog (such as View args) has
		// nothing to return to; the dialog's own previous mode is restored
		// below instead.
		m.contentViewer = contentViewerState{}
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
	if prevMode != ModeInsert && prevMode != ModeNormal {
		return tea.Batch(append(extraCmds, m.restoreModeWithIME(prevMode))...)
	}
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
	extraCmds = append(extraCmds, m.restoreModeWithIME(prevMode), m.confirmQuestionPresentation())
	if prevMode == ModeInsert {
		extraCmds = append(extraCmds, m.input.Focus())
	}
	return tea.Batch(extraCmds...)
}

func (m *Model) enqueueDialog(d pendingDialog) tea.Cmd {
	m.pendingDialogs = append(m.pendingDialogs, d)
	return m.tryPresentNextDialog()
}

func (m *Model) tryPresentNextDialog() tea.Cmd {
	if len(m.pendingDialogs) == 0 || m.dialogActive() || m.interactionSuppressed() || (m.mode != ModeInsert && m.mode != ModeNormal) {
		return nil
	}
	return m.finishDialog(m.mode)
}
