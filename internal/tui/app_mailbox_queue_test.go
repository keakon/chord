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

func mailboxDeliveryDroppedEvent(messageID string) agent.MailboxDeliveryDroppedEvent {
	return agent.MailboxDeliveryDroppedEvent{MessageID: messageID}
}

func TestMailboxDeliveryDroppedEventRemovesQueueRow(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	_ = m.handleAgentEvent(agentEventMsg{event: mailboxQueuedEvent("m1", "agent-1", "agent-1", "working")})
	_ = m.handleAgentEvent(agentEventMsg{event: mailboxQueuedEvent("m2", "agent-2", "agent-2", "other")})

	_ = m.handleAgentEvent(agentEventMsg{event: mailboxDeliveryDroppedEvent("m1")})

	if len(m.mailboxQueue) != 1 || m.mailboxQueue[0].MessageID != "m2" {
		t.Fatalf("mailboxQueue = %#v, want only m2 left after m1 was dropped", m.mailboxQueue)
	}
}

func TestMailboxDeliveryDroppedEventUnknownIDIsNoop(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	_ = m.handleAgentEvent(agentEventMsg{event: mailboxQueuedEvent("m1", "agent-1", "agent-1", "working")})

	_ = m.handleAgentEvent(agentEventMsg{event: mailboxDeliveryDroppedEvent("missing")})

	if len(m.mailboxQueue) != 1 || m.mailboxQueue[0].MessageID != "m1" {
		t.Fatalf("mailboxQueue = %#v, want m1 untouched for an unknown drop id", m.mailboxQueue)
	}
}

func TestQueuedMailboxLineCountMatchesVisibleRows(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	_ = m.handleAgentEvent(agentEventMsg{event: mailboxQueuedEvent("m1", "agent-1", "agent-1", "first")})
	_ = m.handleAgentEvent(agentEventMsg{event: mailboxQueuedEvent("m2", "agent-2", "agent-2", "second")})

	for _, focus := range []string{"", "agent-1", "agent-2"} {
		m.focusedAgentID = focus
		want := len(m.visibleQueuedDrafts()) + len(m.visibleQueuedMailboxes())
		if got := m.queuedMailboxLineCount(); got != want {
			t.Fatalf("focus=%q queuedMailboxLineCount() = %d, want the materialized visible-row count %d", focus, got, want)
		}
	}
}

func TestMessagesToBlocksRestoresDurableSubAgentMailboxCard(t *testing.T) {
	content := "<system-reminder>\nSubAgent mailbox update:\n- agent_id: worker-1\n- task_id: task-1\n- kind: completed\n- summary: finished review\n</system-reminder>"
	msgs := []message.Message{{
		Role:    "user",
		Content: content,
		Kind:    message.KindSubAgentMailbox,
		Mailbox: &message.MailboxMetadata{
			MessageID: "worker-1-1",
			AgentID:   "worker-1",
			TaskID:    "task-1",
			Kind:      "completed",
		},
	}}
	nextID := 1
	blocks := messagesToBlocks(msgs, &nextID)
	if len(blocks) != 1 {
		t.Fatalf("block count = %d, want 1", len(blocks))
	}
	block := blocks[0]
	if block.Type != BlockStatus || block.StatusTitle != "AGENT COMPLETE" {
		t.Fatalf("block = %#v, want AGENT COMPLETE status", block)
	}
	if block.Content != content {
		t.Fatalf("block content = %q, want exact model message %q", block.Content, content)
	}
	if block.LinkedAgentID != "worker-1" || block.LinkedTaskID != "task-1" {
		t.Fatalf("block links = (%q, %q), want worker-1/task-1", block.LinkedAgentID, block.LinkedTaskID)
	}
}

// The live event path and the session-restore path build the same card from
// different data shapes — an AgentNotifyEvent versus a persisted
// MailboxMetadata. They used to hand-roll the Block and drifted, so a resumed
// session badged a risk alert "AGENT RISK" where the run had shown "AGENT
// BLOCKED". Both now share one constructor; this pins the fields that must
// agree no matter which path produced the card.
func TestSubAgentMailboxCardAgreesAcrossLiveAndRestore(t *testing.T) {
	live := newSubAgentMailboxBlock(1, "risk_alert", "", "worker-1", "adhoc-1", "worker cannot continue", "")
	meta := &message.MailboxMetadata{AgentID: "worker-1", TaskID: "adhoc-1", Kind: "risk_alert"}
	restored := newSubAgentMailboxBlock(1, meta.Kind, "", meta.AgentID, meta.TaskID, "<persisted body>", "")

	if live.StatusTitle != restored.StatusTitle {
		t.Fatalf("title live %q vs restored %q", live.StatusTitle, restored.StatusTitle)
	}
	if live.StatusFrom != restored.StatusFrom {
		t.Fatalf("from live %q vs restored %q", live.StatusFrom, restored.StatusFrom)
	}
	if live.StatusKind != restored.StatusKind {
		t.Fatalf("kind live %q vs restored %q", live.StatusKind, restored.StatusKind)
	}
	if live.LinkedTaskID != restored.LinkedTaskID {
		t.Fatalf("task live %q vs restored %q", live.LinkedTaskID, restored.LinkedTaskID)
	}
	if live.StatusTitle != "AGENT BLOCKED" {
		t.Fatalf("risk alert title = %q, want AGENT BLOCKED per docs/tools.md", live.StatusTitle)
	}
	if got := subAgentMailboxCardTitle("completed", ""); got != "AGENT COMPLETE" {
		t.Fatalf("completed title = %q, want AGENT COMPLETE", got)
	}
	if got := subAgentMailboxCardTitle("progress", ""); got != "AGENT MESSAGE" {
		t.Fatalf("progress title = %q, want AGENT MESSAGE", got)
	}
	if got := subAgentMailboxCardTitle("risk_alert", agent.SubAgentStallResolvedSubtype); got != "AGENT BLOCKED RESOLVED" {
		t.Fatalf("stall-resolved title = %q, want AGENT BLOCKED RESOLVED", got)
	}
}
