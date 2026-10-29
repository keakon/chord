package tui

import (
	"fmt"
	"image"
	"slices"
	"strconv"
	"strings"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

type inputQueueAction struct {
	editing    *queuedDraft
	generation uint64
	draft      queuedDraft
	kind       string
}
type inputQueueUI struct {
	cursor   int
	prevMode Mode
	snapshot *agent.InputQueueSnapshot
	pending  map[string]inputQueueAction
	sequence uint64
	editing  *queuedDraft
	// projectionStale records that the queued drafts derived from snapshot were
	// dropped without a newer snapshot arriving — a session-restore rebuild
	// clears them while the durable queue is unchanged. The next snapshot at the
	// same ViewVersion must still be re-projected, otherwise the queue stays
	// invisible until an unrelated operation raises the version.
	projectionStale bool
}

func (m *Model) nextInputQueueOperation(action inputQueueAction) string {
	action.generation = m.input.Revision().Interaction
	action.editing = m.inputQueue.editing
	m.inputQueue.sequence++
	id := fmt.Sprintf("tui-input-%d", m.inputQueue.sequence)
	if m.inputQueue.pending == nil {
		m.inputQueue.pending = make(map[string]inputQueueAction)
	}
	m.inputQueue.pending[id] = action
	return id
}
func sameInputQueueBinding(a, b agent.InputTarget) bool {
	return a.RuntimeID == b.RuntimeID && a.SessionID == b.SessionID && a.SessionEpoch == b.SessionEpoch
}

func (m *Model) submitManagedDraft(draft queuedDraft) tea.Cmd {
	draft.DeliveryManaged = true
	draft.DeliveryStatus = "Saving"
	target := m.agent.InputDeliveryTarget()
	op := m.nextInputQueueOperation(inputQueueAction{draft: draft, kind: "submit"})
	var admitted bool
	if editing := m.inputQueue.editing; editing != nil {
		// Saving an edit keeps the row's policy whichever key submitted it;
		// the queue panel's n/a keys are the explicit way to switch it.
		draft.DeliveryPolicy = editing.DeliveryPolicy
		admitted = m.agent.UpdateQueuedInput(agent.UpdateInput{OperationID: op, Target: target, InputID: editing.InputID, ExpectedRevision: editing.Revision, DeliveryPolicy: draft.DeliveryPolicy, Content: new(draft.Content), Parts: draft.contentParts()})
	} else {
		admitted = m.agent.SubmitUserInput(agent.SubmitInput{OperationID: op, DraftID: draft.ID, Target: target, DeliveryPolicy: draft.DeliveryPolicy, Content: draft.Content, Parts: draft.contentParts()})
	}
	if !admitted {
		delete(m.inputQueue.pending, op)
		return tea.Batch(m.loadQueuedDraftIntoComposer(draft), m.enqueueToast("Input was not accepted; the draft was preserved", "warn"))
	}
	// Submission owns the captured edit from here; subsequent composer input
	// must not update that same item while its acknowledgement is in flight.
	m.inputQueue.editing = nil
	m.editingQueuedDraftID = ""
	m.queuedDrafts = slices.DeleteFunc(m.queuedDrafts, func(d queuedDraft) bool { return d.DeliveryManaged && d.InputID == "" && d.ID == draft.ID })
	m.queuedDrafts = append(m.queuedDrafts, draft)
	m.queueSyncEnabled = true
	m.recalcViewportSize()
	return nil
}

func (m *Model) applyInputQueueSnapshot(snapshot *agent.InputQueueSnapshot) {
	if snapshot == nil || m.agent == nil || !sameInputQueueBinding(snapshot.Target, m.agent.InputDeliveryTarget()) {
		return
	}
	previous := m.inputQueue.snapshot
	if previous != nil && !sameInputQueueBinding(previous.Target, snapshot.Target) {
		m.queuedDrafts = slices.DeleteFunc(m.queuedDrafts, func(d queuedDraft) bool { return d.DeliveryManaged })
		m.inputQueue.pending = nil
		m.inputQueue.editing = nil
	}
	if previous != nil && sameInputQueueBinding(previous.Target, snapshot.Target) && snapshot.ViewVersion <= previous.ViewVersion && !m.inputQueue.projectionStale {
		return
	}
	m.inputQueue.projectionStale = false
	m.inputQueue.snapshot = snapshot.Clone()
	retained := m.queuedDrafts[:0]
	for _, draft := range m.queuedDrafts {
		if !draft.DeliveryManaged || draft.InputID == "" {
			retained = append(retained, draft)
		}
	}
	m.queuedDrafts = retained
	if snapshot.State != nil {
		for _, input := range snapshot.State.Inputs {
			// A committed identity replaces its saving copy, irrespective of text.
			m.queuedDrafts = slices.DeleteFunc(m.queuedDrafts, func(d queuedDraft) bool {
				return d.DeliveryManaged && d.InputID == input.InputID && d.DeliveryStatus == "Saving"
			})
			// Started inputs remain durable for recovery, but are no longer
			// composer drafts. A later snapshot must not resurrect consumed input.
			if input.Stage == recovery.PendingInputStageStarted {
				continue
			}
			draft := queuedDraftFromParts(input.Parts)
			draft.ID, draft.InputID, draft.Revision = input.DraftID, input.InputID, input.Revision
			draft.Content, draft.DisplayContent = input.Content, input.Content
			draft.DeliveryManaged, draft.Mirrored = true, true
			draft.DeliveryPolicy, draft.DeliveryStage = input.DeliveryPolicy, input.Stage
			draft.EditHeld = slices.Contains(snapshot.State.EditHolds, input.InputID)
			draft.DeliveryRunnable = input.Stage == recovery.PendingInputStageQueued && !draft.EditHeld && snapshot.Blocked == ""
			draft.DeliveryStatus = "Next request"
			if input.DeliveryPolicy == recovery.InputDeliveryAfterCompletion {
				draft.DeliveryStatus = "After completion"
				if reason := afterCompletionWaitReason(snapshot.State, input); reason != "" {
					draft.DeliveryStatus += " · Waiting: " + reason
					draft.DeliveryRunnable = false
				}
			}
			if snapshot.RestoredHeld && input.Stage == recovery.PendingInputStageQueued {
				draft.DeliveryStatus += " · Restored: continue or send a message to run it"
				draft.DeliveryRunnable = false
			}
			if snapshot.Blocked != "" {
				draft.DeliveryStatus += " · Blocked: " + snapshot.Blocked
				draft.DeliveryRunnable = false
			}
			if draft.EditHeld {
				draft.DeliveryStatus += " · Editing"
			}
			if input.Stage != recovery.PendingInputStageQueued {
				draft.DeliveryStatus += " · " + input.Stage
			}
			m.queuedDrafts = append(m.queuedDrafts, draft)
		}
	}
	m.queueSyncEnabled = true
	m.recalcViewportSize()
}

func (m *Model) handleInputQueueResult(result agent.InputQueueOperationResultEvent) tea.Cmd {
	if m.agent == nil || !sameInputQueueBinding(result.Target, m.agent.InputDeliveryTarget()) {
		return nil
	}
	action, ok := m.inputQueue.pending[result.OperationID]
	if !ok {
		return nil
	}
	delete(m.inputQueue.pending, result.OperationID)
	if result.Result != agent.InputQueueCommitted {
		if action.kind == "submit" {
			if idx := m.findQueuedDraftIndex(action.draft.ID); idx >= 0 {
				m.queuedDrafts[idx].DeliveryStatus = "Not saved"
				if result.Result == agent.InputQueueUnknown {
					m.queuedDrafts[idx].DeliveryStatus = "Result unknown — check before resending"
				}
			}
			if result.Result != agent.InputQueueUnknown && !m.hasComposerContent() && action.generation == m.input.Revision().Interaction {
				m.inputQueue.editing = action.editing
				m.loadQueuedDraftIntoComposer(action.draft)
			}
		}
		m.applyInputQueueSnapshot(m.agent.InputQueueSnapshot())
		return m.enqueueToast(result.Reason, "warn")
	}
	switch action.kind {
	case "hold":
		draft := action.draft
		draft.Revision = result.ItemRevision
		if m.hasComposerContent() || action.generation != m.input.Revision().Interaction {
			return m.enqueueToast("Input held for editing; your current draft was preserved", "info")
		}
		m.inputQueue.editing = &draft
		return m.loadQueuedDraftIntoComposer(draft)
	case "withdraw":
		if m.hasComposerContent() || action.generation != m.input.Revision().Interaction {
			draft := action.draft
			draft = queuedDraft{ID: m.nextDraftID(), AgentID: draft.AgentID, Content: draft.Content, DisplayContent: draft.DisplayContent, Parts: draft.contentParts(), FileRefs: draft.FileRefs, Withdrawn: true, DeliveryStatus: "Withdrawn"}
			m.queuedDrafts = append(m.queuedDrafts, draft)
			return m.enqueueToast("Input withdrawn; draft copy preserved", "info")
		}
		return m.loadQueuedDraftIntoComposer(action.draft)
	case "submit":
		m.queuedDrafts = slices.DeleteFunc(m.queuedDrafts, func(d queuedDraft) bool {
			return d.DeliveryManaged && d.ID == action.draft.ID && d.DeliveryStatus == "Saving"
		})
	case "release":
		// Reopening even the same item starts a new local editing session.
		if action.editing != nil && m.inputQueue.editing == action.editing {
			m.inputQueue.editing = nil
			if m.editingQueuedDraftID == action.draft.ID {
				m.editingQueuedDraftID = ""
			}
		}
	}
	m.applyInputQueueSnapshot(m.agent.InputQueueSnapshot())
	return nil
}

// afterCompletionWaitReason names why a queued after-completion input has not
// fired yet: the work it waits behind is still running, or settled without
// success and no newer work has started. An empty result means the input
// fires at the next idle.
func afterCompletionWaitReason(state *recovery.InputDeliveryState, input recovery.PendingInput) string {
	switch state.AfterCompletionWait(input) {
	case "", recovery.DeliveryWorkCompleted:
		return ""
	case recovery.DeliveryWorkActive:
		return "task in progress"
	case recovery.DeliveryWorkFailed:
		return "last task failed; send a message to run it next"
	case recovery.DeliveryWorkCancelled:
		return "last task cancelled; send a message to run it next"
	default:
		// Unknown is unreachable like failed/cancelled: the next turn
		// re-points the wait, so the remedy is the same.
		return "last task outcome unknown; send a message to run it next"
	}
}

func (m *Model) editManagedDraft(draft queuedDraft) tea.Cmd {
	if m.hasComposerContent() {
		return m.enqueueToast("Save or clear the current draft before editing", "warn")
	}
	if draft.EditHeld && draft.DeliveryStage == recovery.PendingInputStageQueued {
		m.inputQueue.editing = &draft
		return m.loadQueuedDraftIntoComposer(draft)
	}
	if draft.DeliveryStage != recovery.PendingInputStageQueued {
		return m.enqueueToast("This input has already started", "info")
	}
	op := m.nextInputQueueOperation(inputQueueAction{draft: draft, kind: "hold"})
	if !m.agent.ControlInputQueue(agent.QueueControl{OperationID: op, Target: m.agent.InputDeliveryTarget(), Action: agent.InputQueueHoldEdit, InputID: draft.InputID, ExpectedRevision: draft.Revision}) {
		delete(m.inputQueue.pending, op)
		return m.enqueueToast("Could not request editing", "warn")
	}
	return nil
}
func (m *Model) deleteManagedDraft(draft queuedDraft, withdraw bool) tea.Cmd {
	if draft.DeliveryStage != recovery.PendingInputStageQueued {
		return m.enqueueToast("This input has already started", "info")
	}
	kind := "delete"
	if withdraw {
		kind = "withdraw"
	}
	op := m.nextInputQueueOperation(inputQueueAction{draft: draft, kind: kind})
	if !m.agent.DeleteQueuedInput(agent.DeleteInput{OperationID: op, Target: m.agent.InputDeliveryTarget(), InputID: draft.InputID, ExpectedRevision: draft.Revision}) {
		delete(m.inputQueue.pending, op)
		return m.enqueueToast("Could not delete queued input", "warn")
	}
	return nil
}

func (m *Model) handleInputQueueCommand(content string) tea.Cmd {
	if m.agent == nil {
		return nil
	}
	fields := strings.Fields(content)
	snapshot := m.agent.InputQueueSnapshot()
	m.applyInputQueueSnapshot(snapshot)
	if len(fields) == 1 {
		m.input.Reset()
		m.inputQueue.prevMode = m.mode
		m.mode = ModeInputQueue
		m.recalcViewportSize()
		return nil
	}
	action := fields[1]
	if action == "cancel-edit" {
		draft := m.inputQueue.editing
		if draft == nil {
			return nil
		}
		op := m.nextInputQueueOperation(inputQueueAction{draft: *draft, kind: "release"})
		m.agent.ControlInputQueue(agent.QueueControl{OperationID: op, Target: m.agent.InputDeliveryTarget(), Action: agent.InputQueueReleaseEdit, InputID: draft.InputID, ExpectedRevision: draft.Revision})
	} else if action == "next" || action == "after" || action == "withdraw" {
		if len(fields) != 3 {
			return m.enqueueToast("Specify a queue item number", "warn")
		}
		index, err := strconv.Atoi(fields[2])
		drafts := m.visibleQueuedDrafts()
		if err != nil || index < 1 || index > len(drafts) || !drafts[index-1].DeliveryManaged {
			return m.enqueueToast("Queue item not found", "warn")
		}
		draft := drafts[index-1]
		if action == "withdraw" {
			sequence := m.inputQueue.sequence
			cmd := m.deleteManagedDraft(draft, true)
			if m.inputQueue.sequence != sequence {
				id := fmt.Sprintf("tui-input-%d", m.inputQueue.sequence)
				if pending, ok := m.inputQueue.pending[id]; ok {
					m.input.Reset()
					pending.generation = m.input.Revision().Interaction
					m.inputQueue.pending[id] = pending
				}
			}
			return cmd
		}
		policy := recovery.InputDeliveryNextRequest
		if action == "after" {
			policy = recovery.InputDeliveryAfterCompletion
		}
		op := m.nextInputQueueOperation(inputQueueAction{draft: draft, kind: "policy"})
		m.agent.UpdateQueuedInput(agent.UpdateInput{OperationID: op, Target: m.agent.InputDeliveryTarget(), InputID: draft.InputID, ExpectedRevision: draft.Revision, DeliveryPolicy: policy})
	} else {
		if action != agent.InputQueueClear {
			return m.enqueueToast("Unknown queue action", "warn")
		}
		revision := uint64(0)
		if snapshot != nil && snapshot.State != nil {
			revision = snapshot.State.Revision
		}
		op := m.nextInputQueueOperation(inputQueueAction{kind: "control"})
		m.agent.ControlInputQueue(agent.QueueControl{OperationID: op, Target: m.agent.InputDeliveryTarget(), Action: action, ExpectedQueueRevision: revision})
	}
	m.input.Reset()
	return nil
}

func (m *Model) handleInputQueueKey(msg tea.KeyMsg) tea.Cmd {
	drafts := m.visibleQueuedDrafts()
	m.inputQueue.cursor = min(m.inputQueue.cursor, max(len(drafts)-1, 0))
	key := msg.String()
	switch key {
	case "esc", "q":
		m.mode = m.inputQueue.prevMode
		m.recalcViewportSize()
		return nil
	case "up", "k":
		m.inputQueue.cursor = max(0, m.inputQueue.cursor-1)
		return nil
	case "down", "j":
		m.inputQueue.cursor = min(max(0, len(drafts)-1), m.inputQueue.cursor+1)
		return nil
	case "c":
		return m.handleInputQueueCommand("/queue clear")
	case "x":
		if m.inputQueue.editing == nil && len(drafts) > 0 && drafts[m.inputQueue.cursor].EditHeld {
			draft := drafts[m.inputQueue.cursor]
			m.inputQueue.editing = &draft
		}
		return m.handleInputQueueCommand("/queue cancel-edit")
	}
	if len(drafts) == 0 {
		return nil
	}
	draft := drafts[m.inputQueue.cursor]
	if !draft.DeliveryManaged {
		index := m.queuedDraftIndexForVisibleIndex(m.inputQueue.cursor)
		switch key {
		case "enter", "e", "w":
			return m.editQueuedDraftAt(index)
		case "d", "delete":
			return m.deleteQueuedDraftAt(index)
		}
		return nil
	}
	switch key {
	case "enter", "e":
		return m.editManagedDraft(draft)
	case "d", "delete":
		return m.deleteManagedDraft(draft, false)
	case "w":
		return m.deleteManagedDraft(draft, true)
	case "n":
		return m.handleInputQueueCommand(fmt.Sprintf("/queue next %d", m.inputQueue.cursor+1))
	case "a":
		return m.handleInputQueueCommand(fmt.Sprintf("/queue after %d", m.inputQueue.cursor+1))
	}
	return nil
}
func (m *Model) renderInputQueuePanel() string {
	width := min(max(m.width-4, 1), 100)
	drafts := m.visibleQueuedDrafts()
	lines := []string{}
	if snapshot := m.inputQueue.snapshot; snapshot != nil {
		if snapshot.Blocked != "" {
			lines = append(lines, "Storage blocked: "+snapshot.Blocked)
		}
	}
	start := max(0, m.inputQueue.cursor-max(1, m.height-10)/2)
	for i := start; i < min(len(drafts), start+max(1, m.height-10)); i++ {
		draft := drafts[i]
		prefix := "  "
		if i == m.inputQueue.cursor {
			prefix = "> "
		}
		lines = append(lines, terminaltext.TruncateOneLine(fmt.Sprintf("%s%d. %s · %s", prefix, i+1, draft.DeliveryStatus, draft.DisplayContent), width))
	}
	if len(drafts) == 0 {
		lines = append(lines, "No queued inputs")
	}
	cfg := OverlayConfig{Title: "Input queue", MaxWidth: width, MinContentHeight: 1, Hint: "Enter edit · d delete · w withdraw · n next · a after · c clear · x cancel edit · Esc close"}
	out, _ := RenderOverlay(cfg, strings.Join(lines, "\n"), image.Rect(0, 0, m.width, m.height))
	return out
}
