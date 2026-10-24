package tui

import (
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

// TestConfirmResolvedClosesDialogWithoutAnswering covers the race where the
// runtime settles the wait (auto-deny timeout or cancellation) while the
// dialog is still on screen: the dialog must close and restore the pre-dialog
// mode without sending another answer for a request nobody waits on.
func TestConfirmResolvedClosesDialogWithoutAnswering(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeInsert

	m.handleConfirmRequest(confirmRequestMsg{request: ConfirmRequest{ToolName: tools.NameEdit, RequestID: "req-1"}})
	if m.mode != ModeConfirm || m.confirm.request == nil {
		t.Fatalf("setup: mode=%v, want the confirm dialog on screen", m.mode)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ConfirmResolvedEvent{
		RequestID: "req-1", ToolName: tools.NameEdit, AgentID: "main", Outcome: agent.ConfirmOutcomeTimeout,
	}})

	if m.mode != ModeInsert {
		t.Fatalf("mode = %v, want the pre-dialog ModeInsert restored", m.mode)
	}
	if m.confirm.request != nil {
		t.Fatal("the resolved confirm dialog must close")
	}
	if len(backend.confirmResolutions) != 0 {
		t.Fatalf("confirm resolutions = %+v, want none (the wait already ended)", backend.confirmResolutions)
	}
}

// TestConfirmResolvedIgnoresUnmatchedRequestID pins the no-side-effect rule: a
// resolution for another request leaves the open dialog untouched and silent.
func TestConfirmResolvedIgnoresUnmatchedRequestID(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeNormal

	m.handleConfirmRequest(confirmRequestMsg{request: ConfirmRequest{ToolName: tools.NameEdit, RequestID: "req-1"}})
	if m.mode != ModeConfirm || m.confirm.request == nil {
		t.Fatalf("setup: mode=%v", m.mode)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ConfirmResolvedEvent{
		RequestID: "req-9", Outcome: agent.ConfirmOutcomeTimeout,
	}})

	if m.mode != ModeConfirm || m.confirm.request == nil {
		t.Fatal("an unmatched resolution must leave the dialog untouched")
	}
	if m.activeToast != nil {
		t.Fatalf("an unmatched resolution must stay silent, got toast %q", m.activeToast.Message)
	}
}
