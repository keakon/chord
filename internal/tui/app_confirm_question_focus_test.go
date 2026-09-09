package tui

import (
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/tools"
)

// collectFollowupMsgs flattens the command batch returned for an interactive
// agent event into its concrete messages.
func collectFollowupMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msgs := []tea.Msg{}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, sub := range batch {
			if sub == nil {
				continue
			}
			if subMsg := sub(); subMsg != nil {
				msgs = append(msgs, subMsg)
			}
		}
	} else if msg != nil {
		msgs = append(msgs, msg)
	}
	return msgs
}

func TestFocusAgentForRequestSwitchesToAskingSubAgent(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.focusAgentForRequest("agent-1")
	if m.focusedAgentID != "agent-1" {
		t.Fatalf("focusedAgentID = %q, want %q (sub-agent request must switch focus)", m.focusedAgentID, "agent-1")
	}
}

func TestFocusAgentForRequestKeepsCurrentViewForMainAndUnset(t *testing.T) {
	cases := []struct {
		name    string
		agentID string
	}{
		{name: "main agent request", agentID: identity.MainAgentID},
		{name: "unset agent id", agentID: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModelWithSize(nil, 80, 24)
			m.focusedAgentID = "agent-1"
			m.focusAgentForRequest(tc.agentID)
			if m.focusedAgentID != "agent-1" {
				t.Fatalf("focusedAgentID = %q, want %q (main/unset requests must not yank focus)", m.focusedAgentID, "agent-1")
			}
		})
	}
}

func TestFocusAgentForRequestNoOpWhenAlreadyFocused(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.focusAgentForRequest("agent-1")
	m.focusAgentForRequest("agent-1")
	if m.focusedAgentID != "agent-1" {
		t.Fatalf("focusedAgentID = %q, want %q (already focused request must be a no-op)", m.focusedAgentID, "agent-1")
	}
}

func TestConfirmRequestSwitchesFocusToAskingSubAgent(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	cmd := m.handleConfirmRequest(confirmRequestMsg{request: ConfirmRequest{
		ToolName:  tools.NameEdit,
		RequestID: "req-1",
		AgentID:   "agent-1",
	}})
	if cmd == nil {
		t.Fatal("confirm request should return a followup command")
	}
	if m.focusedAgentID != "agent-1" {
		t.Fatalf("focusedAgentID = %q, want %q (confirm from sub-agent must switch focus)", m.focusedAgentID, "agent-1")
	}
}

func TestConfirmRequestFromMainKeepsCurrentFocus(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.focusedAgentID = "agent-1"
	m.handleConfirmRequest(confirmRequestMsg{request: ConfirmRequest{
		ToolName:  tools.NameEdit,
		RequestID: "req-1",
		AgentID:   identity.MainAgentID,
	}})
	if m.focusedAgentID != "agent-1" {
		t.Fatalf("focusedAgentID = %q, want %q (main confirm must not yank the sub-agent view)", m.focusedAgentID, "agent-1")
	}
}

func TestQuestionRequestSwitchesFocusToAskingSubAgent(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.handleQuestionRequest(questionRequestMsg{
		requestID: "q-1",
		request: QuestionRequest{
			Questions: []tools.QuestionItem{{Header: "pick", Question: "which one?", Options: []tools.QuestionOption{{Label: "one"}, {Label: "two"}}}},
			AgentID:   "agent-2",
		},
	})
	if m.focusedAgentID != "agent-2" {
		t.Fatalf("focusedAgentID = %q, want %q (question from sub-agent must switch focus)", m.focusedAgentID, "agent-2")
	}
}

func TestConfirmAndQuestionEventsCarryAgentIDToRequest(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.ConfirmRequestEvent{
		ToolName:  tools.NameEdit,
		RequestID: "req-confirm",
		AgentID:   "agent-1",
	}})
	var confirm *ConfirmRequest
	for _, msg := range collectFollowupMsgs(cmd) {
		if cr, ok := msg.(confirmRequestMsg); ok {
			confirm = &cr.request
		}
	}
	if confirm == nil {
		t.Fatal("confirm event should produce a confirmRequestMsg followup")
	}
	if confirm.AgentID != "agent-1" {
		t.Fatalf("ConfirmRequest.AgentID = %q, want %q", confirm.AgentID, "agent-1")
	}

	cmd = m.handleAgentEvent(agentEventMsg{event: agent.QuestionRequestEvent{
		RequestID: "req-question",
		Question:  "continue?",
		AgentID:   "agent-1",
	}})
	var question *QuestionRequest
	for _, msg := range collectFollowupMsgs(cmd) {
		if qr, ok := msg.(questionRequestMsg); ok {
			question = &qr.request
		}
	}
	if question == nil {
		t.Fatal("question event should produce a questionRequestMsg followup")
	}
	if question.AgentID != "agent-1" {
		t.Fatalf("QuestionRequest.AgentID = %q, want %q", question.AgentID, "agent-1")
	}
}
