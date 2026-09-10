package tui

import (
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

func mailboxQueuedEvent(messageID, agentID, ownerAgentID, summary string) agent.MailboxQueuedEvent {
	return agent.MailboxQueuedEvent{Message: agent.SubAgentMailboxMessage{
		MessageID:    messageID,
		AgentID:      agentID,
		TaskID:       "task-1",
		OwnerAgentID: ownerAgentID,
		Kind:         agent.SubAgentMailboxKindProgress,
		Summary:      summary,
	}}
}

func mailboxTranscriptAppendedEvent(messageID, content, targetAgentID string, index int) agent.MailboxTranscriptAppendedEvent {
	return agent.MailboxTranscriptAppendedEvent{
		Message: message.Message{
			Role:    "user",
			Content: content,
			Mailbox: &message.MailboxMetadata{
				MessageID: messageID,
				AgentID:   "agent-1",
				TaskID:    "task-1",
				Kind:      string(agent.SubAgentMailboxKindProgress),
			},
		},
		TargetAgentID: targetAgentID,
		MessageIndex:  index,
	}
}

func TestMailboxQueuedEventCreatesQueueRow(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	_ = m.handleAgentEvent(agentEventMsg{event: mailboxQueuedEvent("m1", "agent-1", "agent-1", "working")})

	if len(m.mailboxQueue) != 1 {
		t.Fatalf("mailboxQueue length = %d, want 1", len(m.mailboxQueue))
	}
	item := m.mailboxQueue[0]
	if item.MessageID != "m1" || item.AgentID != "agent-1" || item.TargetAgentID != "agent-1" || item.Summary != "working" {
		t.Fatalf("queued row = %#v, want m1/agent-1/agent-1/working", item)
	}
}

func TestMailboxQueuedEventUpdatesSameMessageID(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	_ = m.handleAgentEvent(agentEventMsg{event: mailboxQueuedEvent("m1", "agent-1", "agent-1", "first")})
	_ = m.handleAgentEvent(agentEventMsg{event: mailboxQueuedEvent("m1", "agent-1", "agent-1", "second")})

	if len(m.mailboxQueue) != 1 {
		t.Fatalf("mailboxQueue length = %d, want the same MessageID to update in place", len(m.mailboxQueue))
	}
	if got := m.mailboxQueue[0].Summary; got != "second" {
		t.Fatalf("queued summary = %q, want the latest value", got)
	}
}

func TestMailboxTranscriptAppendedReplacesQueueRowWithCard(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	_ = m.handleAgentEvent(agentEventMsg{event: mailboxQueuedEvent("m1", "agent-1", "agent-1", "working")})
	_ = m.handleAgentEvent(agentEventMsg{event: mailboxTranscriptAppendedEvent("m1", "worker body", "agent-1", 3)})

	if len(m.mailboxQueue) != 0 {
		t.Fatalf("mailboxQueue length = %d, want the queue row removed on transcript append", len(m.mailboxQueue))
	}
	block := m.findBlockByMailboxMessageID(&message.MailboxMetadata{MessageID: "m1"})
	if block == nil {
		t.Fatal("expected a card keyed by the mailbox message ID")
	}
	if block.MailboxMessageID != "m1" || block.MsgIndex != 3 || block.AgentID != "agent-1" {
		t.Fatalf("card = {mailbox:%q msgIndex:%d agent:%q}, want m1/3/agent-1", block.MailboxMessageID, block.MsgIndex, block.AgentID)
	}

	// A durable append delivered twice must not create a second card.
	_ = m.handleAgentEvent(agentEventMsg{event: mailboxTranscriptAppendedEvent("m1", "worker body", "agent-1", 3)})
	cards := 0
	for _, b := range m.viewport.blocks {
		if b != nil && b.MailboxMessageID == "m1" {
			cards++
		}
	}
	if cards != 1 {
		t.Fatalf("cards for message m1 = %d, want 1", cards)
	}
}

func TestSessionSwitchStartedClearsMailboxQueue(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	_ = m.handleAgentEvent(agentEventMsg{event: mailboxQueuedEvent("m1", "agent-1", "agent-1", "working")})
	if len(m.mailboxQueue) != 1 {
		t.Fatalf("mailboxQueue length = %d, want 1 before the switch", len(m.mailboxQueue))
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.SessionSwitchStartedEvent{Kind: "resume", SessionID: "session-2"}})

	if len(m.mailboxQueue) != 0 {
		t.Fatalf("mailboxQueue length = %d, want cleared on session switch", len(m.mailboxQueue))
	}
}

func TestVisibleQueuedMailboxesFiltersByFocusedAgent(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	_ = m.handleAgentEvent(agentEventMsg{event: mailboxQueuedEvent("m1", "agent-1", "agent-1", "for agent-1")})
	_ = m.handleAgentEvent(agentEventMsg{event: mailboxQueuedEvent("m2", "agent-2", "agent-2", "for agent-2")})
	_ = m.handleAgentEvent(agentEventMsg{event: mailboxQueuedEvent("m3", "agent-3", "main", "for main")})

	m.focusedAgentID = "agent-1"
	if visible := m.visibleQueuedMailboxes(); len(visible) != 1 || visible[0].MessageID != "m1" {
		t.Fatalf("visible rows for agent-1 = %#v, want only m1", visible)
	}

	m.focusedAgentID = "agent-2"
	if visible := m.visibleQueuedMailboxes(); len(visible) != 1 || visible[0].MessageID != "m2" {
		t.Fatalf("visible rows for agent-2 = %#v, want only m2", visible)
	}

	// An owner of "main" normalizes to the main focus.
	m.focusedAgentID = ""
	if visible := m.visibleQueuedMailboxes(); len(visible) != 1 || visible[0].MessageID != "m3" {
		t.Fatalf("visible rows for main = %#v, want only m3", visible)
	}
}
