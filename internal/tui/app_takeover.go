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
)

const takeoverCommand = "/takeover"

type takeoverRuntimeState struct {
	handler      func(context.Context, string, *time.Duration) (agent.SessionPauseStatus, error)
	waiting      bool
	sourceFrozen bool
	status       agent.SessionPauseStatus
	sequence     uint64
}

type takeoverResultMsg struct {
	sequence uint64
	status   agent.SessionPauseStatus
	err      error
}

type takeoverPreparedMsg struct {
	event       agent.SessionTransferPreparingEvent
	mirroredIDs []string
	err         error
}

func takeoverFailureMessage(status agent.SessionPauseStatus, err error) string {
	text := err.Error()
	if status.TransferID != "" && !strings.Contains(text, status.TransferID) {
		return fmt.Sprintf("%s · Transfer %s · Session %s", text, status.TransferID, status.SessionID)
	}
	return text
}

// SetTakeoverHandler connects the UI to the local runtime coordinator. The
// callback waits for source release; the Core separately validates activation.
func (m *Model) SetTakeoverHandler(fn func(context.Context, string, *time.Duration) (agent.SessionPauseStatus, error)) {
	m.takeover.handler = fn
}

func (m *Model) takeoverInputFrozen() bool {
	return m.takeover.waiting || m.takeover.sourceFrozen
}

func (m *Model) hasOtherComposerDrafts(ignoreCurrentCommand bool) bool {
	if m.clipboardAttachmentPending || len(m.attachments) > 0 || m.editingQueuedDraftID != "" || m.input.HasInlinePastes() || (!ignoreCurrentCommand && m.input.Value() != "") {
		return true
	}
	for id, state := range m.agentComposerStates {
		if id == m.focusedAgentID {
			continue
		}
		if state.draft.Entry.Display != "" || len(state.draft.Entry.InlinePastes) > 0 || len(state.attachments) > 0 || state.editingQueuedDraftID != "" {
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
	resume, ok := m.agent.(interface {
		ResumePausedSession(context.Context, string, recovery.TransferIdentity) error
	})
	if !ok {
		return m.enqueueToast("Session takeover is unavailable in this runtime", "warn")
	}
	m.input.AddCurrentToHistory()
	m.input.Reset()
	m.slashCompleteSelected = 0
	m.takeover.waiting = true
	m.takeover.sequence++
	seq, handler := m.takeover.sequence, m.takeover.handler
	return func() tea.Msg {
		status, err := handler(context.Background(), sid, timeout)
		if err == nil && status.State != agent.SessionPauseReleased {
			err = fmt.Errorf("transfer %s is %s; query status with --session %s --transfer-id %s", status.TransferID, status.State, sid, status.TransferID)
		}
		if err == nil && (status.SessionID != sid || status.TransferID == "") {
			err = fmt.Errorf("source returned a different transfer identity")
		}
		if err == nil {
			err = resume.ResumePausedSession(context.Background(), sid, status.TransferIdentity)
		}
		return takeoverResultMsg{sequence: seq, status: status, err: err}
	}
}

func (m *Model) prepareSourceTakeover(evt agent.SessionTransferPreparingEvent) tea.Cmd {
	if m.takeover.waiting || evt.Identity.SessionID != m.currentSessionID() {
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
			if draft.Mirrored {
				continue
			}
			if !owners[draft.AgentID] {
				result.err = fmt.Errorf("queued input owner is unavailable; keep the source session open")
				break
			}
			backend.SwitchFocus(draft.AgentID)
			if !backend.QueuePendingUserDraft(draft.ID, draft.contentParts()) {
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
		case agent.SessionPausePreparing, agent.SessionPauseDraining, agent.SessionPauseSaving, agent.SessionPauseSaveFailed:
			m.takeover.sourceFrozen = true
			if evt.Status.State == agent.SessionPauseSaveFailed {
				text := "Session save failed; the source keeps ownership. Check storage and session history before restarting and resuming"
				if evt.Status.RecoverableAction == agent.SessionPauseActionRetrySave {
					text = "Session save failed; the source keeps ownership. Use takeover retry-save to retry"
				}
				effects.addFollowup(m.enqueueToast(text, "warn"))
			}
		case agent.SessionPauseSaved:
			m.takeover.sourceFrozen = true
			// The runtime releases ownership after this dedicated exit. Ordinary
			// cancellation and shutdown must not write to the sealed checkpoint.
			effects.addFollowup(tea.Quit)
		case agent.SessionPauseReady, agent.SessionPausePrepareFailed, "running":
			m.takeover.sourceFrozen = false
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
		CurrentSessionPauseStatus() agent.SessionPauseStatus
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
		draft := queuedDraft{ID: id, AgentID: owner, Content: p.Content, Parts: p.Parts, Mirrored: true, DisplayContent: displayTextFromParts(p.Parts, p.Content), FileRefs: fileRefsFromParts(p.Parts)}
		m.queuedDrafts = append(m.queuedDrafts, draft)
	}
	m.queueSyncEnabled = len(m.queuedDrafts) > 0
}

func (m *Model) takeoverStatusText() string {
	if m.takeover.waiting {
		return "Waiting for session transfer"
	}
	s := m.takeover.status
	switch s.State {
	case agent.SessionPausePreparing:
		return "Preparing session transfer"
	case agent.SessionPauseDraining:
		return fmt.Sprintf("Waiting for %d active operations", s.BlockerCount)
	case agent.SessionPauseSaving:
		return "Saving paused session"
	case agent.SessionPauseSaveFailed:
		if s.RecoverableAction != agent.SessionPauseActionRetrySave {
			return "Save failed; check session recovery"
		}
		return "Save failed; retry-save is available"
	case agent.SessionPauseSaved:
		return "Session saved; transferring"
	case agent.SessionPauseReady:
		if s.PendingInputCount == 0 {
			return ""
		}
		return fmt.Sprintf("Paused · %d pending inputs · Send or continue", s.PendingInputCount)
	default:
		return ""
	}
}

func (m *Model) takeoverDraftActionBlocked(idx int) bool {
	return m.takeoverInputFrozen() || (idx >= 0 && idx < len(m.queuedDrafts) && strings.HasPrefix(m.queuedDrafts[idx].ID, "restored:"))
}
