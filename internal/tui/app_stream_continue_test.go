package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
)

// TestHandleStreamContinueEventCreatesUserCard verifies that an injected
// stream-continuation prompt renders as a real user message card (it is real
// history the model will see) rather than a toast or status card.
func TestHandleStreamContinueEventCreatesUserCard(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 80, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.StreamContinueEvent{
		Text:    "继续",
		AgentID: "",
	}})
	applyTestCmd(t, &m, cmd)

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 {
		t.Fatalf("visible block count = %d, want 1", len(blocks))
	}
	if blocks[0].Type != BlockUser {
		t.Fatalf("block type = %v, want BlockUser", blocks[0].Type)
	}
	if blocks[0].MsgIndex != -1 {
		t.Fatalf("MsgIndex = %d, want -1 for a synthetic (non-echoed) user card", blocks[0].MsgIndex)
	}
	plain := stripANSI(strings.Join(blocks[0].Render(80, ""), "\n"))
	if !strings.Contains(plain, "继续") {
		t.Fatalf("rendered card = %q, want the continuation prompt", plain)
	}
}

// TestHandleStreamContinueEventSettlesInterruptedAssistantCard verifies that
// the preserved partial reply card is settled (kept, not blanked) when the
// continuation prompt arrives, and the prompt opens its own user card.
func TestHandleStreamContinueEventSettlesInterruptedAssistantCard(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 24)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{AgentID: "", Text: "partial reply"}})
	cmd := m.handleAgentEvent(agentEventMsg{event: agent.StreamContinueEvent{
		Text:    "继续",
		AgentID: "",
	}})
	applyTestCmd(t, &m, cmd)

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 2 {
		t.Fatalf("visible block count = %d, want 2 (settled partial + continuation)", len(blocks))
	}
	if blocks[0].Type != BlockAssistant || blocks[0].Streaming {
		t.Fatalf("first block = type %v streaming=%v, want a settled assistant card", blocks[0].Type, blocks[0].Streaming)
	}
	plain := stripANSI(strings.Join(blocks[0].Render(120, ""), "\n"))
	if !strings.Contains(plain, "partial reply") {
		t.Fatalf("first card = %q, want the preserved partial text kept on screen", plain)
	}
	if blocks[1].Type != BlockUser {
		t.Fatalf("second block type = %v, want BlockUser continuation prompt", blocks[1].Type)
	}
}
