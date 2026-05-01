package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/agent"
)

func (m Model) hasComposerContent() bool {
	return m.input.Value() != "" || len(m.attachments) > 0 || m.editingQueuedDraftID != "" || m.input.HasInlinePastes()
}

func (m Model) focusedActivity() agent.AgentActivityEvent {
	statusActiveID := m.focusedAgentID
	if statusActiveID == "" {
		statusActiveID = "main"
	}
	return m.activities[statusActiveID]
}

func (m Model) shouldQueueMainDraft() bool {
	if m.focusedAgentID != "" || m.agent == nil {
		return false
	}
	if m.isAgentBusy() {
		return true
	}
	if m.agent != nil && m.agent.LoopKeepsMainBusy() {
		return true
	}
	return m.focusedActivity().Type == agent.ActivityCompacting
}

func (m *Model) nextDraftID() string {
	m.nextQueuedDraftID++
	return fmt.Sprintf("draft-%d", m.nextQueuedDraftID)
}

func (m Model) visibleQueuedDrafts() []queuedDraft {
	// queuedDrafts currently represent the local main-agent queue only. When the
	// user switches to a worker view, the queue must stay isolated to main so the
	// current agent's composer/queue state is not visually mixed with another's.
	if m.focusedAgentID != "" {
		return nil
	}
	return m.queuedDrafts
}

func (m *Model) draftIDForSubmit() string {
	if m.editingQueuedDraftID != "" {
		id := m.editingQueuedDraftID
		m.editingQueuedDraftID = ""
		return id
	}
	return m.nextDraftID()
}

func (m *Model) findQueuedDraftIndex(id string) int {
	if id == "" {
		return -1
	}
	for i := range m.queuedDrafts {
		if m.queuedDrafts[i].ID == id {
			return i
		}
	}
	return -1
}

func (m *Model) removeQueuedDraftAt(idx int) queuedDraft {
	draft := m.queuedDrafts[idx]
	m.queuedDrafts = append(m.queuedDrafts[:idx], m.queuedDrafts[idx+1:]...)
	return draft
}

func (m *Model) syncQueuedDraft(draft queuedDraft) bool {
	if m.agent == nil {
		return false
	}
	ok := m.agent.QueuePendingUserDraft(draft.ID, draft.contentParts())
	if ok {
		m.queueSyncEnabled = true
	}
	return ok
}

func (m Model) queuedDraftsAutoContinue() bool {
	for _, draft := range m.queuedDrafts {
		if draft.Mirrored {
			return true
		}
	}
	return false
}

func (m *Model) dropQueuedDraftFromAgent(draftID string) {
	if m.agent == nil || !m.queueSyncEnabled || draftID == "" {
		return
	}
	m.agent.RemovePendingUserDraft(draftID)
}

func (m *Model) loadQueuedDraftIntoComposer(draft queuedDraft) tea.Cmd {
	m.clearActiveSearch()
	m.editingQueuedDraftID = draft.ID
	text, inlinePastes := displayTextAndInlinePastes(draft.contentParts(), draft.Content)
	nextPasteSeq := 0
	for _, paste := range inlinePastes {
		if paste.Seq > nextPasteSeq {
			nextPasteSeq = paste.Seq
		}
	}
	composerText := composerTextFromParts(draft.contentParts(), draft.Content)
	if len(inlinePastes) > 0 {
		composerText = text
	} else if composerText == "" {
		composerText = text
	}
	m.input.SetDisplayValueAndPastes(composerText, inlinePastes, nextPasteSeq)
	m.input.syncHeight()
	m.attachments = attachmentsFromParts(draft.contentParts())
	m.switchModeWithIME(ModeInsert)
	m.recalcViewportSize()
	return m.input.Focus()
}

func (m *Model) editQueuedDraftAt(idx int) tea.Cmd {
	if idx < 0 || idx >= len(m.queuedDrafts) {
		return nil
	}
	if m.hasComposerContent() {
		return m.enqueueToast("Submit or clear the current draft before editing a queued item", "info")
	}
	draft := m.removeQueuedDraftAt(idx)
	m.dropQueuedDraftFromAgent(draft.ID)
	return tea.Batch(
		m.loadQueuedDraftIntoComposer(draft),
		m.enqueueToast("Queued draft loaded into composer", "info"),
	)
}

func (m *Model) deleteQueuedDraftAt(idx int) tea.Cmd {
	if idx < 0 || idx >= len(m.queuedDrafts) {
		return nil
	}
	draft := m.removeQueuedDraftAt(idx)
	m.dropQueuedDraftFromAgent(draft.ID)
	m.recalcViewportSize()
	return m.enqueueToast("Queued draft removed", "info")
}

const (
	queuedDraftDeleteToken       = "[del]"
	queuedDraftDeleteRightMargin = 2
)

func (m *Model) queuedDraftActionAt(x, y int) (idx int, remove bool, ok bool) {
	if m.layout.queue.Dx() == 0 || m.layout.queue.Dy() == 0 {
		return 0, false, false
	}
	if x < m.layout.queue.Min.X || x >= m.layout.queue.Max.X || y < m.layout.queue.Min.Y || y >= m.layout.queue.Max.Y {
		return 0, false, false
	}
	idx = y - m.layout.queue.Min.Y
	if idx < 0 || idx >= len(m.queuedDrafts) {
		return 0, false, false
	}
	deleteWidth := runewidth.StringWidth(queuedDraftDeleteToken)
	deleteStart := m.layout.queue.Max.X - queuedDraftDeleteRightMargin - deleteWidth
	if deleteStart < m.layout.queue.Min.X {
		deleteStart = m.layout.queue.Min.X
	}
	deleteEnd := deleteStart + deleteWidth
	return idx, x >= deleteStart && x < deleteEnd, true
}
