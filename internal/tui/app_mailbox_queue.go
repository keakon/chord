package tui

import (
	"strings"
	"time"

	"github.com/keakon/chord/internal/agent"
)

type queuedMailbox struct {
	MessageID     string
	AgentID       string
	TaskID        string
	TargetAgentID string
	Kind          string
	Summary       string
	QueuedAt      time.Time
}

func queuedMailboxFromMessage(msg agent.SubAgentMailboxMessage) queuedMailbox {
	target := strings.TrimSpace(msg.OwnerAgentID)
	if target == "main" {
		target = ""
	}
	return queuedMailbox{
		MessageID:     strings.TrimSpace(msg.MessageID),
		AgentID:       strings.TrimSpace(msg.AgentID),
		TaskID:        strings.TrimSpace(msg.TaskID),
		TargetAgentID: target,
		Kind:          strings.TrimSpace(string(msg.Kind)),
		Summary:       strings.TrimSpace(msg.Summary),
		QueuedAt:      msg.CreatedAt,
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

func (m Model) queuedMailboxLineCount() int {
	return len(m.visibleQueuedDrafts()) + len(m.visibleQueuedMailboxes())
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
