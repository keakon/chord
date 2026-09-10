package tui

import (
	"strings"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/identity"
)

type queuedMailbox struct {
	MessageID     string
	AgentID       string
	TargetAgentID string
	Summary       string
}

func queuedMailboxFromMessage(msg agent.SubAgentMailboxMessage) queuedMailbox {
	target := strings.TrimSpace(msg.OwnerAgentID)
	if target == identity.MainAgentID {
		target = ""
	}
	return queuedMailbox{
		MessageID:     strings.TrimSpace(msg.MessageID),
		AgentID:       strings.TrimSpace(msg.AgentID),
		TargetAgentID: target,
		Summary:       strings.TrimSpace(msg.Summary),
	}
}

func (m Model) visibleQueuedMailboxes() []queuedMailbox {
	agentID := normalizeDraftAgentID(m.focusedAgentID)
	visible := make([]queuedMailbox, 0, len(m.mailboxQueue))
	for _, item := range m.mailboxQueue {
		if normalizeDraftAgentID(item.TargetAgentID) == agentID {
			visible = append(visible, item)
		}
	}
	return visible
}

// visibleQueuedDraftCount and visibleQueuedMailboxCount count what
// queuedMailboxLineCount needs without materializing a slice each call, since
// the layout pass asks for the line count several times per frame.
func (m Model) visibleQueuedDraftCount() int {
	agentID := normalizeDraftAgentID(m.focusedAgentID)
	count := 0
	for _, draft := range m.queuedDrafts {
		if normalizeDraftAgentID(draft.AgentID) == agentID {
			count++
		}
	}
	return count
}

func (m Model) visibleQueuedMailboxCount() int {
	agentID := normalizeDraftAgentID(m.focusedAgentID)
	count := 0
	for _, item := range m.mailboxQueue {
		if normalizeDraftAgentID(item.TargetAgentID) == agentID {
			count++
		}
	}
	return count
}

func (m Model) queuedMailboxLineCount() int {
	return m.visibleQueuedDraftCount() + m.visibleQueuedMailboxCount()
}

func (m *Model) upsertQueuedMailbox(msg agent.SubAgentMailboxMessage) {
	item := queuedMailboxFromMessage(msg)
	if item.MessageID == "" {
		return
	}
	for i := range m.mailboxQueue {
		if m.mailboxQueue[i].MessageID == item.MessageID {
			m.mailboxQueue[i] = item
			return
		}
	}
	m.mailboxQueue = append(m.mailboxQueue, item)
	m.recalcViewportSize()
	m.invalidateDrawCaches()
}

func (m *Model) removeQueuedMailbox(messageID string) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return
	}
	for i := range m.mailboxQueue {
		if m.mailboxQueue[i].MessageID == messageID {
			m.mailboxQueue = append(m.mailboxQueue[:i], m.mailboxQueue[i+1:]...)
			m.recalcViewportSize()
			m.invalidateDrawCaches()
			return
		}
	}
}
