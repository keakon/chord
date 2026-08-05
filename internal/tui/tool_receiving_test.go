package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
)

func TestToolCallReceivingUsesStaticReceivingIcon(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	args := `{"todos":[{"id":"1","content":"a","status":"pending"}]}`

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-receiving-1",
		Name:     "todo_write",
		ArgsJSON: args,
	}})

	block, ok := m.viewport.FindBlockByToolID("call-receiving-1")
	if !ok {
		t.Fatal("expected tool block")
	}
	if block.ToolExecutionState != agent.ToolCallExecutionStateReceiving {
		t.Fatalf("tool state = %q, want receiving", block.ToolExecutionState)
	}
	frameA := stripANSI(strings.Join(block.Render(96, "▖"), "\n"))
	frameB := stripANSI(strings.Join(block.Render(96, "▘"), "\n"))
	if frameA != frameB {
		t.Fatalf("receiving tool card should not animate\nframe A:\n%s\n\nframe B:\n%s", frameA, frameB)
	}
	if !strings.Contains(frameA, "◌ todo_write") {
		t.Fatalf("expected receiving icon, got:\n%s", frameA)
	}
}
