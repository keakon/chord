package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

func TestTryContinueSkipsQuestionStateTail(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{
		{Role: message.RoleAssistant, Content: "asked something", StopReason: "stop"},
		{Role: message.RoleUser, Content: "queued input"},
		{Role: message.RoleUser, Kind: message.KindQuestionState},
		{Role: message.RoleSystem, Kind: message.KindQuestionState, Content: `{"op":"pause"}`},
	}}
	m := NewModelWithSize(backend, 120, 24)

	m.tryContinue()

	if backend.continueCalls != 1 || len(backend.sentMessages) != 0 {
		t.Fatalf("continue calls = %d sent = %#v, want ContinueFromContext without a draft", backend.continueCalls, backend.sentMessages)
	}
}

func TestTryContinueWithoutContinuableMessageShowsToast(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{
		{Role: message.RoleSystem, Kind: message.KindQuestionState, Content: `{"op":"pause"}`},
	}}
	m := NewModelWithSize(backend, 120, 24)

	m.tryContinue()

	if m.activeToast == nil || !strings.Contains(m.activeToast.Message, "Nothing to continue") {
		t.Fatalf("active toast = %#v, want nothing-to-continue notice", m.activeToast)
	}
}

func TestFocusedTryContinueSkipsQuestionStateTail(t *testing.T) {
	backend := &targetedConversationAgent{
		focused:   "agent-1",
		subAgents: []agent.SubAgentInfo{{InstanceID: "agent-1", TaskID: "task-1"}},
		messagesByTask: map[string][]message.Message{"task-1": {
			{Role: message.RoleUser, Content: "work"},
			{Role: message.RoleSystem, Kind: message.KindQuestionState},
		}},
	}
	m := NewModelWithSize(backend, 120, 24)
	m.focusedAgentID = "agent-1"
	m.viewport.SetFilter("agent-1")

	var action focusedContinueActionMsg
	for _, msg := range runCmdTree(m.tryContinue()) {
		if candidate, ok := msg.(focusedContinueActionMsg); ok {
			action = candidate
			break
		}
	}
	if action.action != focusedContinueFromContext || action.target.TaskID != "task-1" {
		t.Fatalf("focused continue action = %#v, want continue from context", action)
	}
}

func TestRestoredInterruptedTailShowsContinueHint(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{
		{Role: message.RoleAssistant, Content: "earlier reply", StopReason: "stop"},
		{Role: message.RoleUser, Content: "queued input"},
		{Role: message.RoleSystem, Kind: message.KindQuestionState},
	}}
	m := NewModelWithSize(backend, 120, 24)

	m.Update(sessionRestoredRebuildMsg{reason: transcriptRestoreReasonSession})

	if m.activeToast == nil || !strings.Contains(m.activeToast.Message, "press Enter to continue") {
		t.Fatalf("active toast = %#v, want interrupted-tail hint", m.activeToast)
	}
}

func TestRestoredAnsweredTailShowsNoContinueHint(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{
		{Role: message.RoleUser, Content: "asked"},
		{Role: message.RoleAssistant, Content: "answered", StopReason: "stop"},
		{Role: message.RoleSystem, Kind: message.KindQuestionState},
	}}
	m := NewModelWithSize(backend, 120, 24)

	m.Update(sessionRestoredRebuildMsg{reason: transcriptRestoreReasonSession})

	if m.activeToast != nil {
		t.Fatalf("active toast = %#v, want no hint for an answered tail", m.activeToast)
	}
}
