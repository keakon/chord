package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

// streamContinueTestText mirrors the agent's durable KindStreamContinue message
// content: the same text is persisted, sent to the model, and delivered in the
// StreamContinueEvent, so the card body always equals what the model was told.
const streamContinueTestText = "Your previous reply was interrupted before it completed. The text already written is preserved above; continue directly from the interruption point without apologizing or restating what was written."

// TestHandleStreamContinueEventRendersStatusNotice verifies that a preserved
// interruption with an appended continuation message renders the real message
// content as a productized status card — never a display-only notice invented
// for the UI.
func TestHandleStreamContinueEventRendersStatusNotice(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 80, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.StreamContinueEvent{
		Text:    streamContinueTestText,
		AgentID: "",
	}})
	applyTestCmd(t, &m, cmd)

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 {
		t.Fatalf("visible block count = %d, want 1", len(blocks))
	}
	if blocks[0].Type != BlockStatus {
		t.Fatalf("block type = %v, want BlockStatus (not a user card)", blocks[0].Type)
	}
	if !blocks[0].Collapsed {
		t.Fatalf("%q must start collapsed", streamContinueCardTitle)
	}
	collapsed := stripANSI(strings.Join(blocks[0].Render(80, ""), "\n"))
	if !strings.Contains(collapsed, streamContinueCardTitle) || !strings.Contains(collapsed, "▸") {
		t.Fatalf("rendered card = %q, want a collapsed %q notice", collapsed, streamContinueCardTitle)
	}
	if !blocks[0].ToggleAtWidth(80) || blocks[0].Collapsed {
		t.Fatalf("toggling must expand the %q notice", streamContinueCardTitle)
	}
	plain := stripANSI(strings.Join(blocks[0].Render(80, ""), "\n"))
	if !strings.Contains(plain, "continue directly from the interruption point") {
		t.Fatalf("rendered card = %q, want the continuation message text", plain)
	}
	if !strings.Contains(plain, streamContinueCardTitle) {
		t.Fatalf("rendered card = %q, want the %q status title", plain, streamContinueCardTitle)
	}
}

// TestHandleStreamContinueEventSettlesInterruptedAssistantCard verifies that
// the preserved partial reply card is settled (kept, not blanked) when the
// continuation arrives, followed by the card for the continuation message.
func TestHandleStreamContinueEventSettlesInterruptedAssistantCard(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 24)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{AgentID: "", Text: "partial reply"}})
	cmd := m.handleAgentEvent(agentEventMsg{event: agent.StreamContinueEvent{
		Text:    streamContinueTestText,
		AgentID: "",
	}})
	applyTestCmd(t, &m, cmd)

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 2 {
		t.Fatalf("visible block count = %d, want 2 (settled partial + continuation card)", len(blocks))
	}
	if blocks[0].Type != BlockAssistant || blocks[0].Streaming {
		t.Fatalf("first block = type %v streaming=%v, want a settled assistant card", blocks[0].Type, blocks[0].Streaming)
	}
	plain := stripANSI(strings.Join(blocks[0].Render(120, ""), "\n"))
	if !strings.Contains(plain, "partial reply") {
		t.Fatalf("first card = %q, want the preserved partial text kept on screen", plain)
	}
	if blocks[1].Type != BlockStatus {
		t.Fatalf("second block type = %v, want BlockStatus continuation card", blocks[1].Type)
	}
}

// TestHandleStreamContinueEventEmptyTextSettlesWithoutCard verifies the
// prefill-capable-pool path: when no continuation message was appended (the
// model resumes from the trailing interrupted assistant turn), the event still
// settles the interrupted card but renders no extra status card.
func TestHandleStreamContinueEventEmptyTextSettlesWithoutCard(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 24)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{AgentID: "", Text: "partial reply"}})
	cmd := m.handleAgentEvent(agentEventMsg{event: agent.StreamContinueEvent{Text: "", AgentID: ""}})
	applyTestCmd(t, &m, cmd)

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 {
		t.Fatalf("visible block count = %d, want 1 (settled partial only, no notice card)", len(blocks))
	}
	if blocks[0].Type != BlockAssistant || blocks[0].Streaming {
		t.Fatalf("block = type %v streaming=%v, want a settled assistant card", blocks[0].Type, blocks[0].Streaming)
	}
}

// TestMessagesToBlocksRendersStreamContinueAsStatusCard verifies that a
// restored session renders a durable KindStreamContinue user message with the
// same status card chrome used live, so the card survives a restore.
func TestMessagesToBlocksRendersStreamContinueAsStatusCard(t *testing.T) {
	nextID := 1
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "keep going please"},
		{Role: message.RoleAssistant, Content: "partial reply", StopReason: "interrupted"},
		{Role: message.RoleUser, Content: streamContinueTestText, Kind: message.KindStreamContinue},
	}

	blocks := messagesToBlocks(msgs, &nextID)
	if len(blocks) != 3 {
		t.Fatalf("len(blocks) = %d, want 3", len(blocks))
	}
	card := blocks[2]
	if card.Type != BlockStatus || card.StatusTitle != streamContinueCardTitle || card.Content != streamContinueTestText {
		t.Fatalf("restored continuation card = %#v, want BlockStatus %q with the message content", card, streamContinueCardTitle)
	}
}
