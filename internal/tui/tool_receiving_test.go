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

func TestNewToolCallStartCompletesEarlierReceivingCard(t *testing.T) {
	m := NewModelWithSize(nil, 96, 12)

	// First call streams its arguments; the card shows the transient char count.
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID: "call-1", Name: "shell", AgentID: "main", ArgsJSON: `{"command":"ec"}`,
	}})
	block1, ok := m.viewport.FindBlockByToolID("call-1")
	if !ok {
		t.Fatal("expected call-1 tool block")
	}
	if block1.ToolExecutionState != agent.ToolCallExecutionStateReceiving {
		t.Fatalf("call-1 state = %q, want receiving", block1.ToolExecutionState)
	}
	if block1.ToolProgress == nil || !strings.Contains(block1.ToolProgress.Text, "chars received") {
		t.Fatalf("expected transient char count on receiving card, got %+v", block1.ToolProgress)
	}

	// A second call starting streams implies the first call's arguments are
	// complete (chat-completions deltas arrive in generation order).
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID: "call-2", Name: "read", AgentID: "main", ArgsJSON: `{"path":"a.go"}`,
	}})
	if block1.ToolExecutionState != agent.ToolCallExecutionStateQueued {
		t.Fatalf("call-1 state = %q, want queued after call-2 starts", block1.ToolExecutionState)
	}
	if block1.ToolProgress != nil {
		t.Fatalf("expected char count cleared after args complete, got %+v", block1.ToolProgress)
	}
	if joined := stripANSI(strings.Join(block1.Render(96, "●"), "\n")); strings.Contains(joined, "chars received") {
		t.Fatalf("expected completed args to hide char count, got:\n%s", joined)
	}

	// The late ArgsStreamingDone update must not downgrade the queued state.
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID: "call-1", Name: "shell", AgentID: "main", ArgsJSON: `{"command":"echo hi"}`, ArgsStreamingDone: true,
	}})
	if block1.ToolExecutionState != agent.ToolCallExecutionStateQueued {
		t.Fatalf("call-1 state = %q after ArgsStreamingDone, want queued", block1.ToolExecutionState)
	}
}

func TestNewToolCallStartDoesNotTouchOtherAgentsReceivingCards(t *testing.T) {
	m := NewModelWithSize(nil, 96, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID: "sub-1", Name: "shell", AgentID: "sub-agent", ArgsJSON: `{"command":"pwd"}`,
	}})
	subBlock, ok := m.viewport.FindBlockByToolID("sub-1")
	if !ok {
		t.Fatal("expected sub-agent tool block")
	}
	if subBlock.ToolExecutionState != agent.ToolCallExecutionStateReceiving {
		t.Fatalf("sub-agent state = %q, want receiving", subBlock.ToolExecutionState)
	}

	// A main-agent call starting must leave the sub-agent card receiving.
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID: "call-1", Name: "read", AgentID: "main", ArgsJSON: `{"path":"a.go"}`,
	}})
	if subBlock.ToolExecutionState != agent.ToolCallExecutionStateReceiving {
		t.Fatalf("sub-agent state = %q, want untouched receiving", subBlock.ToolExecutionState)
	}
}
