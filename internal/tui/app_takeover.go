package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/composer"
)

const takeoverCommand = "/takeover"

type takeoverRuntimeState struct {
	handler      func(context.Context, string, *time.Duration) (recovery.SessionPauseStatus, error)
	waiting      bool
	activating   bool
	sourceFrozen bool
	status       recovery.SessionPauseStatus
	sequence     uint64
	ctx          context.Context
	cancel       context.CancelFunc
	// startedAt anchors the elapsed time shown while waiting. The wait is
	// anchored here rather than at the handler's deadline so the status bar can
	// render progress without the observation budget (which lives inside the
	// handler goroutine) and without any cross-package plumbing.
	startedAt time.Time
	// observing marks a transfer whose observation budget ended while the
	// source was still handing off. It is not a terminal state: the source
	// keeps releasing, so the status bar must keep reporting it.
	observing bool
	// statusHandler queries an admitted transfer without starting one. The
	// wait handler cannot serve this role: every call mints a new transfer
	// identity, and a source that already admitted a transfer answers a second
	// start with transfer_busy.
	statusHandler func(context.Context, string, string) (recovery.SessionPauseStatus, error)
	// observeFailures counts consecutive status queries that neither the
	// source nor its durable checkpoint could answer. The banner retires
	// instead of ticking forever against a source that stopped answering.
	observeFailures int
	// lastObserveAt paces the status queries. The status bar repaints every
	// second while observing, so the query interval must be slower than the
	// repaint it rides on.
	lastObserveAt  time.Time
	observePending bool
}

const (
	// takeoverObserveInterval paces status queries for an observed transfer.
	takeoverObserveInterval = 2 * time.Second
	// takeoverObserveQueryTimeout bounds one status query. The durable query
	// answers from the checkpoint after the source exits, so a query that
	// cannot finish in this budget is treated as unanswered.
	takeoverObserveQueryTimeout = 5 * time.Second
	// takeoverObserveFailureLimit bounds how long the banner trusts a source
	// that stopped answering. The durable query still reports a saved or
	// released transfer after the source exits, so consecutive failures mean
	// neither the source nor its checkpoint is reachable.
	takeoverObserveFailureLimit = 3
)

type takeoverResultMsg struct {
	sequence uint64
	status   recovery.SessionPauseStatus
	err      error
}

type takeoverWaitMsg struct {
	sequence uint64
	status   recovery.SessionPauseStatus
	err      error
}

type takeoverObserveMsg struct {
	sequence uint64
	status   recovery.SessionPauseStatus
	err      error
}

type takeoverPreparedMsg struct {
	event       agent.SessionTransferPreparingEvent
	mirroredIDs []string
	err         error
}

func takeoverFailureMessage(status recovery.SessionPauseStatus, err error) string {
	text := err.Error()
	if status.TransferID != "" && !strings.Contains(text, status.TransferID) {
		return fmt.Sprintf("%s · Transfer %s · Session %s", text, status.TransferID, status.SessionID)
	}
	return text
}

// SetTakeoverHandler connects the UI to the local runtime coordinator. The
// callback waits for source release; the Core separately validates activation.
func (m *Model) SetTakeoverHandler(fn func(context.Context, string, *time.Duration) (recovery.SessionPauseStatus, error)) {
	m.takeover.handler = fn
}

// SetTakeoverStatusHandler connects the UI to the read-only status query. It
// observes a transfer whose wait budget already expired: the coordinator stops
// watching when the budget ends, so without this the banner would report a
// hand-off nobody is still watching.
func (m *Model) SetTakeoverStatusHandler(fn func(context.Context, string, string) (recovery.SessionPauseStatus, error)) {
	m.takeover.statusHandler = fn
}

func (m *Model) takeoverInputFrozen() bool {
	return m.takeover.waiting || m.takeover.observing || m.takeover.sourceFrozen
}

func (m *Model) cancelTargetTakeover() {
	if m.takeover.cancel != nil {
		m.takeover.cancel()
	}
	m.takeover.ctx = nil
	m.takeover.cancel = nil
	m.takeover.waiting = false
	m.takeover.activating = false
	m.takeover.observing = false
	m.takeover.observePending = false
	m.takeover.sequence++
}

func (m *Model) hasOtherComposerDrafts(ignoreCurrentCommand bool) bool {
	if m.clipboardAttachmentPending || len(m.attachments) > 0 || m.editingQueuedDraftID != "" || m.input.HasInlinePastes() || (!ignoreCurrentCommand && m.input.Value() != "") {
		return true
	}
	for id, state := range m.agentComposerStates {
		if id == m.focusedAgentID {
			continue
		}
		if state.input.HasContent() || len(state.attachments) > 0 || state.editingQueuedDraftID != "" {
			return true
		}
	}
	return false
}

func parseTakeoverCommand(value string) (string, *time.Duration, error) {
	args := strings.Fields(value)
	if len(args) != 2 && len(args) != 4 {
		return "", nil, fmt.Errorf("usage: /takeover <session-id> [--timeout <duration>]")
	}
	if len(args) == 2 {
		return args[1], nil, nil
	}
	if args[2] != "--timeout" {
		return "", nil, fmt.Errorf("usage: /takeover <session-id> [--timeout <duration>]")
	}
	d, err := time.ParseDuration(args[3])
	if err != nil || d < 0 {
		return "", nil, fmt.Errorf("timeout must be a non-negative duration, such as 0s or 30s")
	}
	return args[1], new(d), nil
}

func (m *Model) startTakeoverCommand(value string) tea.Cmd {
	sid, timeout, err := parseTakeoverCommand(value)
	if err != nil {
		return m.enqueueToast(err.Error(), "warn")
	}
	if m.takeover.handler == nil {
		return m.enqueueToast("Session takeover is unavailable in this runtime", "warn")
	}
	if m.takeoverInputFrozen() || m.agent == nil || m.currentSessionID() == sid {
		return m.enqueueToast("Choose another session while this session is idle", "warn")
	}
	if m.hasOtherComposerDrafts(true) || len(m.queuedDrafts) > 0 || len(m.mailboxQueue) > 0 || m.inflightDraft != nil || len(m.activeJobs()) > 0 || m.agent.LoopKeepsMainBusy() || m.sessionSwitch.active() || m.mode == ModeConfirm || m.mode == ModeQuestion || (m.viewport != nil && m.viewport.HasUserLocalShellPending()) {
		return m.enqueueToast("Finish active work and save or clear all drafts before taking over a session", "warn")
	}
	for _, activity := range m.activities {
		if runtimeActivityBusy(activity) {
			return m.enqueueToast("Wait for all agents to become idle before taking over a session", "warn")
		}
	}
	for _, sub := range m.agent.GetSubAgents() {
		if sub.State == string(agent.SubAgentStateRunning) {
			return m.enqueueToast("Wait for all agents to become idle before taking over a session", "warn")
		}
	}
	_, ok := m.agent.(interface {
		ResumePausedSession(context.Context, string, recovery.TransferIdentity) error
	})
	if !ok {
		return m.enqueueToast("Session takeover is unavailable in this runtime", "warn")
	}
	historyCmd := m.recordSubmittedInput(m.captureSubmittedInput())
	m.input.Reset()
	m.slashCompleteSelected = 0
	m.cancelTargetTakeover()
	m.takeover.ctx, m.takeover.cancel = context.WithCancel(context.Background())
	m.takeover.waiting = true
	m.takeover.status = recovery.SessionPauseStatus{SessionID: sid}
	m.takeover.observeFailures = 0
	m.takeover.lastObserveAt = time.Time{}
	m.takeover.startedAt = time.Now()
	seq, handler, ctx := m.takeover.sequence, m.takeover.handler, m.takeover.ctx
	return tea.Batch(historyCmd, func() tea.Msg {
		if err := ctx.Err(); err != nil {
			return takeoverWaitMsg{sequence: seq, err: err}
		}
		status, err := handler(ctx, sid, timeout)
		if err == nil && (status.SessionID != sid || status.TransferID == "") {
			err = fmt.Errorf("source returned a different transfer identity")
		}
		if err == nil && !status.WaitExpired && status.State != recovery.SessionPauseReleased {
			err = fmt.Errorf("transfer %s is %s; query status with --session %s --transfer-id %s", status.TransferID, status.State, sid, status.TransferID)
		}
		return takeoverWaitMsg{sequence: seq, status: status, err: err}
	})
}

func (m *Model) applyTakeoverWait(msg takeoverWaitMsg) tea.Cmd {
	if msg.sequence != m.takeover.sequence || !m.takeover.waiting || m.takeover.activating {
		return nil
	}
	if msg.err != nil {
		m.cancelTargetTakeover()
		m.takeover.status = recovery.SessionPauseStatus{}
		return m.enqueueToast(takeoverFailureMessage(msg.status, msg.err), "warn")
	}
	m.takeover.status = msg.status
	if msg.status.State == recovery.SessionPauseReleased {
		return m.finishTakeoverActivation(msg.sequence, msg.status)
	}
	m.takeover.waiting = false
	m.takeover.observing = true
	m.takeover.observeFailures = 0
	m.takeover.lastObserveAt = time.Time{}
	return m.enqueueToast("Observation budget ended; the source keeps transferring. Query with chord takeover status --session "+msg.status.SessionID+" --transfer-id "+msg.status.TransferID, "info")
}

func (m *Model) applyTakeoverResult(msg takeoverResultMsg) tea.Cmd {
	if msg.sequence != m.takeover.sequence || !m.takeover.activating {
		return nil
	}
	m.cancelTargetTakeover()
	if msg.err != nil {
		m.takeover.status = recovery.SessionPauseStatus{}
		return m.enqueueToast(takeoverFailureMessage(msg.status, msg.err), "warn")
	}
	m.restoreTakeoverPendingInputs()
	m.recalcViewportSize()
	return m.enqueueToast("Session restored paused; send a message or Continue when ready", "info")
}

func (m *Model) prepareSourceTakeover(evt agent.SessionTransferPreparingEvent) tea.Cmd {
	m.closeInputHistory()
	if m.takeover.waiting || m.takeover.observing || evt.Identity.SessionID != m.currentSessionID() {
		evt.Reply <- fmt.Errorf("target_not_ready: session changed or another takeover is active")
		return nil
	}
	if m.hasOtherComposerDrafts(false) {
		m.takeover.sourceFrozen = false
		evt.Reply <- fmt.Errorf("target_not_ready: save or clear unsubmitted composer drafts first")
		return m.enqueueToast("Save or clear unsubmitted drafts before transferring; your drafts remain here", "warn")
	}
	m.takeover.sourceFrozen = true
	drafts := append([]queuedDraft(nil), m.queuedDrafts...)
	backend := m.agent
	return func() tea.Msg {
		result := takeoverPreparedMsg{event: evt}
		if backend == nil {
			result.err = fmt.Errorf("input mirror unavailable")
			return result
		}
		focus := backend.FocusedAgentID()
		defer backend.SwitchFocus(focus)
		owners := map[string]bool{"": true, identity.MainAgentID: true}
		for _, sub := range backend.GetSubAgents() {
			if sub.State == string(agent.SubAgentStateRunning) {
				owners[sub.InstanceID] = true
			}
		}
		for _, draft := range drafts {
			if draft.DeliveryManaged {
				if !draft.Mirrored {
					result.err = fmt.Errorf("wait for pending input saves before transferring")
					break
				}
				continue
			}
			if draft.Mirrored {
				continue
			}
			if !owners[draft.AgentID] {
				result.err = fmt.Errorf("queued input owner is unavailable; keep the source session open")
				break
			}
			backend.SwitchFocus(draft.AgentID)
			if !backend.QueueSubAgentDraft(draft.ID, draft.contentParts()) {
				result.err = fmt.Errorf("queued input could not be accepted; keep the source session open")
				break
			}
			result.mirroredIDs = append(result.mirroredIDs, draft.ID)
		}
		return result
	}
}

func (m *Model) handleTakeoverAgentEvent(evt agent.AgentEvent) (bool, agentEventEffects) {
	effects := agentEventEffects{}
	switch evt := evt.(type) {
	case agent.SessionPendingInputsUpdatedEvent:
		retired := make(map[string]bool, len(evt.InputIDs))
		for _, id := range evt.InputIDs {
			retired["restored:"+id] = true
		}
		remaining := m.queuedDrafts[:0]
		removed := 0
		for _, draft := range m.queuedDrafts {
			if retired[draft.ID] {
				removed++
				continue
			}
			remaining = append(remaining, draft)
		}
		m.queuedDrafts = remaining
		m.takeover.status.PendingInputCount = max(0, m.takeover.status.PendingInputCount-removed)
		m.recalcViewportSize()
	case agent.SessionTransferPreparingEvent:
		effects.addFollowup(m.prepareSourceTakeover(evt))
	case agent.SessionTransferEvent:
		if evt.Status.SessionID != m.currentSessionID() {
			return true, effects
		}
		m.takeover.status = evt.Status
		switch evt.Status.State {
		case recovery.SessionPausePreparing, recovery.SessionPauseDraining, recovery.SessionPauseSaving, recovery.SessionPauseSaveFailed:
			m.takeover.sourceFrozen = true
			if evt.Status.State == recovery.SessionPauseSaveFailed {
				text := "Session save failed; the source keeps ownership. Check storage and session history before restarting and resuming"
				if evt.Status.RecoverableAction == recovery.SessionPauseActionRetrySave {
					text = "Session save failed; the source keeps ownership. Use takeover retry-save to retry"
				}
				effects.addFollowup(m.enqueueToast(text, "warn"))
			}
		case recovery.SessionPauseSaved:
			m.takeover.sourceFrozen = true
			// The runtime releases ownership after this dedicated exit. Ordinary
			// cancellation and shutdown must not write to the sealed checkpoint.
			effects.addFollowup(tea.Quit)
		case recovery.SessionPauseReady, recovery.SessionPausePrepareFailed, "running":
			m.takeover.sourceFrozen = false
			m.takeover.observing = false
		}
		if evt.Status.State == recovery.SessionPauseReleased {
			m.takeover.observing = false
		}
	default:
		return false, effects
	}
	return true, effects
}

func (m *Model) applyTakeoverPrepared(msg takeoverPreparedMsg) tea.Cmd {
	for _, id := range msg.mirroredIDs {
		if i := m.findQueuedDraftIndex(id); i >= 0 {
			m.queuedDrafts[i].Mirrored = true
		}
	}
	if len(msg.mirroredIDs) > 0 {
		m.queueSyncEnabled = true
	}
	msg.event.Reply <- msg.err
	if msg.err != nil {
		m.takeover.sourceFrozen = false
		return m.enqueueToast(msg.err.Error(), "warn")
	}
	return nil
}

func (m *Model) restoreTakeoverPendingInputs() {
	reader, ok := m.agent.(interface {
		PendingSessionInputs() []recovery.PendingInput
		CurrentSessionPauseStatus() recovery.SessionPauseStatus
	})
	if !ok {
		return
	}
	m.takeover.status = reader.CurrentSessionPauseStatus()
	m.takeover.sourceFrozen = false
	owners := make(map[string]string)
	for _, sub := range m.agent.GetSubAgents() {
		owners[sub.TaskID] = sub.InstanceID
	}
	for _, p := range reader.PendingSessionInputs() {
		if p.OwnerTaskID == identity.MainAgentID && p.DeliveryPolicy != "" {
			continue
		}
		id := "restored:" + p.InputID
		if m.findQueuedDraftIndex(id) >= 0 {
			continue
		}
		owner := ""
		if p.OwnerTaskID != identity.MainAgentID {
			owner = owners[p.OwnerTaskID]
			if owner == "" {
				owner = p.OwnerTaskID
			}
		}
		draft := queuedDraft{ID: id, AgentID: owner, Content: p.Content, Parts: p.Parts, Mirrored: true, DisplayContent: composer.DisplayTextFromParts(p.Parts, p.Content), FileRefs: fileRefsFromParts(p.Parts)}
		m.queuedDrafts = append(m.queuedDrafts, draft)
	}
	m.queueSyncEnabled = len(m.queuedDrafts) > 0
	m.applyInputQueueSnapshot(m.agent.InputQueueSnapshot())
}

// takeoverElapsedSuffix renders how long this runtime has been waiting. It is
// intentionally elapsed-only: --timeout is a budget the user just typed, so the
// number they are counting is "how long have I waited", not a countdown they
// have to translate. tools.FormatElapsed keeps the granularity consistent with
// every other status bar lane.
func (m *Model) takeoverElapsedSuffix(now time.Time) string {
	return tools.FormatElapsed(now.Sub(m.takeover.startedAt))
}

func (m *Model) takeoverStatusTextAt(now time.Time) string {
	if m.takeover.activating {
		return "Restoring paused session · " + m.takeoverElapsedSuffix(now)
	}
	if m.takeover.waiting {
		return "Waiting for session transfer · " + m.takeoverElapsedSuffix(now)
	}
	if m.takeover.observing {
		// The budget expired but the source still owns the session. Returning an
		// empty string here would drop the status bar back to idle and hide a
		// transfer that is still running, so keep it visible until the source
		// reports a terminal state.
		return "Transfer still handing off · " + m.takeoverElapsedSuffix(now) + " · query takeover status"
	}
	s := m.takeover.status
	switch s.State {
	case recovery.SessionPausePreparing:
		return "Preparing session transfer"
	case recovery.SessionPauseDraining:
		return fmt.Sprintf("Waiting for %d active operations", s.BlockerCount)
	case recovery.SessionPauseSaving:
		return "Saving paused session"
	case recovery.SessionPauseSaveFailed:
		if s.RecoverableAction != recovery.SessionPauseActionRetrySave {
			return "Save failed; check session recovery"
		}
		return "Save failed; retry-save is available"
	case recovery.SessionPauseSaved:
		return "Session saved; transferring"
	case recovery.SessionPauseReady:
		if s.PendingInputCount == 0 {
			return ""
		}
		return fmt.Sprintf("Paused · %d pending inputs · Send or continue", s.PendingInputCount)
	default:
		return ""
	}
}

func (m *Model) takeoverStatusText() string {
	return m.takeoverStatusTextAt(time.Now())
}

// pollTakeoverObservation rides on the status bar tick. While a transfer is
// observed past its wait budget, it queries the source at a slower pace than
// the repaint it shares the tick with, and returns nothing at all once the
// observation ends.
func (m *Model) pollTakeoverObservation(now time.Time) tea.Cmd {
	if m == nil || !m.takeover.observing || m.takeover.observePending || m.takeover.statusHandler == nil {
		return nil
	}
	if !m.takeover.lastObserveAt.IsZero() && now.Sub(m.takeover.lastObserveAt) < takeoverObserveInterval {
		return nil
	}
	m.takeover.lastObserveAt = now
	m.takeover.observePending = true
	seq, handler := m.takeover.sequence, m.takeover.statusHandler
	sid, transferID := m.takeover.status.SessionID, m.takeover.status.TransferID
	parent := m.takeover.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, takeoverObserveQueryTimeout)
		defer cancel()
		if err := ctx.Err(); err != nil {
			return takeoverObserveMsg{sequence: seq, err: err}
		}
		status, err := handler(ctx, sid, transferID)
		if err == nil && (status.SessionID != sid || status.TransferID != transferID) {
			err = fmt.Errorf("source returned a different transfer identity")
		}
		return takeoverObserveMsg{sequence: seq, status: status, err: err}
	}
}

// applyTakeoverObservation keeps the observed banner honest. A released source
// completes the same validated activation the wait path runs; a save failure
// stays observable so a retry can still finish the transfer; a source that
// stops answering retires the banner after a bounded number of failures,
// because by then neither the source nor its checkpoint can be reached.
func (m *Model) applyTakeoverObservation(msg takeoverObserveMsg) tea.Cmd {
	if msg.sequence != m.takeover.sequence || !m.takeover.observing {
		return nil
	}
	m.takeover.observePending = false
	m.takeover.lastObserveAt = time.Now()
	if msg.err != nil {
		m.takeover.observeFailures++
		if m.takeover.observeFailures < takeoverObserveFailureLimit {
			return nil
		}
		m.cancelTargetTakeover()
		return m.enqueueToast(fmt.Sprintf(
			"Session transfer could not be confirmed; the source may have exited. Query with chord takeover status --session %s --transfer-id %s",
			m.takeover.status.SessionID, m.takeover.status.TransferID), "warn")
	}
	m.takeover.observeFailures = 0
	failedSave := m.takeover.status.State != recovery.SessionPauseSaveFailed && msg.status.State == recovery.SessionPauseSaveFailed
	m.takeover.status = msg.status
	switch msg.status.State {
	case recovery.SessionPauseReleased:
		return m.finishTakeoverActivation(msg.sequence, msg.status)
	case recovery.SessionPauseSaveFailed:
		if !failedSave {
			return nil
		}
		text := "Session save failed; the source keeps ownership. Check storage and session history before restarting and resuming"
		if msg.status.RecoverableAction == recovery.SessionPauseActionRetrySave {
			text = "Session save failed; the source keeps ownership. Use takeover retry-save to retry"
		}
		return m.enqueueToast(text, "warn")
	}
	return nil
}

// finishTakeoverActivation runs the validated activation for a released
// transfer and reports through the same message the wait path uses, so both
// paths share the restore and failure handling.
func (m *Model) finishTakeoverActivation(seq uint64, status recovery.SessionPauseStatus) tea.Cmd {
	m.takeover.observing = false
	m.takeover.waiting = true
	m.takeover.activating = true
	ctx := m.takeover.ctx
	resume, ok := m.agent.(interface {
		ResumePausedSession(context.Context, string, recovery.TransferIdentity) error
	})
	return func() tea.Msg {
		err := ctx.Err()
		if err == nil {
			if !ok {
				err = fmt.Errorf("session takeover is unavailable in this runtime")
			} else {
				err = resume.ResumePausedSession(ctx, status.SessionID, status.TransferIdentity)
			}
		}
		return takeoverResultMsg{sequence: seq, status: status, err: err}
	}
}

func (m *Model) takeoverDraftActionBlocked(idx int) bool {
	return m.takeoverInputFrozen() || (idx >= 0 && idx < len(m.queuedDrafts) && strings.HasPrefix(m.queuedDrafts[idx].ID, "restored:"))
}
