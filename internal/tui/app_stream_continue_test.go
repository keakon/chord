package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
)

// TestHandleStreamContinueEventRendersStatusNotice verifies that a preserved
// interruption is announced as a productized status card, not as a user message:
// the continuation is a request-scoped overlay, so putting it in the transcript
// would show a message the user never wrote.
func TestHandleStreamContinueEventRendersStatusNotice(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 80, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.StreamContinueEvent{
		Text:    "回复被中断，已保留已输出的内容并从断开处继续。",
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
	plain := stripANSI(strings.Join(blocks[0].Render(80, ""), "\n"))
	if !strings.Contains(plain, "已保留已输出的内容") {
		t.Fatalf("rendered card = %q, want the resume notice", plain)
	}
	if !strings.Contains(plain, streamContinueCardTitle) {
		t.Fatalf("rendered card = %q, want the %q status title", plain, streamContinueCardTitle)
	}
}

// TestHandleStreamContinueEventSettlesInterruptedAssistantCard verifies that
// the preserved partial reply card is settled (kept, not blanked) when the
// continuation arrives, followed by the status notice explaining the resume.
func TestHandleStreamContinueEventSettlesInterruptedAssistantCard(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 24)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{AgentID: "", Text: "partial reply"}})
	cmd := m.handleAgentEvent(agentEventMsg{event: agent.StreamContinueEvent{
		Text:    "回复被中断，已保留已输出的内容并从断开处继续。",
		AgentID: "",
	}})
	applyTestCmd(t, &m, cmd)

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 2 {
		t.Fatalf("visible block count = %d, want 2 (settled partial + notice)", len(blocks))
	}
	if blocks[0].Type != BlockAssistant || blocks[0].Streaming {
		t.Fatalf("first block = type %v streaming=%v, want a settled assistant card", blocks[0].Type, blocks[0].Streaming)
	}
	plain := stripANSI(strings.Join(blocks[0].Render(120, ""), "\n"))
	if !strings.Contains(plain, "partial reply") {
		t.Fatalf("first card = %q, want the preserved partial text kept on screen", plain)
	}
	if blocks[1].Type != BlockStatus {
		t.Fatalf("second block type = %v, want BlockStatus notice", blocks[1].Type)
	}
}
