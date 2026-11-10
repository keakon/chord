package tui

import (
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

func TestConfirmResolvedDropsQueuedRequest(t *testing.T) {
	for _, outcome := range []string{
		agent.ConfirmOutcomeApproved, agent.ConfirmOutcomeDenied,
		agent.ConfirmOutcomeTimeout, agent.ConfirmOutcomeCancelled,
	} {
		t.Run(outcome, func(t *testing.T) {
			backend := &sessionControlAgent{}
			m := NewModelWithSize(backend, 120, 24)
			m.mode = ModeNormal
			m.handleQuestionState(questionEventForTest("q-1", "Choice", "Choose", nil, nil, false, time.Time{}, "main").Question)
			m.handleAgentEvent(agentEventMsg{event: agent.ConfirmRequestEvent{
				RequestID: "req-1", ToolName: tools.NameEdit, AgentID: "main",
			}})
			m.handleAgentEvent(agentEventMsg{event: agent.ConfirmRequestEvent{
				RequestID: "req-2", ToolName: tools.NameShell, AgentID: "main",
			}})
			m.handleQuestionState(questionEventForTest("q-2", "Choice", "Choose", nil, nil, false, time.Time{}, "main").Question)
			if m.question.requestID != "q-1" || len(m.pendingDialogs) != 3 {
				t.Fatal("expected an active question with two confirms and another question queued")
			}

			m.handleAgentEvent(agentEventMsg{event: agent.ConfirmResolvedEvent{
				RequestID: "req-1", ToolName: tools.NameEdit, AgentID: "main", Outcome: outcome,
			}})
			if m.mode != ModeQuestion || m.question.requestID != "q-1" {
				t.Fatal("settling a queued confirm changed the active question")
			}
			if len(m.pendingDialogs) != 2 || m.pendingDialogs[0].confirm == nil ||
				m.pendingDialogs[0].confirm.request.RequestID != "req-2" || m.pendingDialogs[1].questionID != "q-2" {
				t.Fatalf("queue = %+v, want only the unrelated confirm and question", m.pendingDialogs)
			}
			// A repeated terminal event must leave the surviving requests alone.
			m.handleAgentEvent(agentEventMsg{event: agent.ConfirmResolvedEvent{RequestID: "req-1", Outcome: outcome}})
			m.handleQuestionState(agent.QuestionSnapshot{ID: "q-1", Version: 2, Outcome: tools.QuestionOutcomeNoResponse})
			if m.confirm.request == nil || m.confirm.requestID != "req-2" {
				t.Fatalf("presented confirm = %q, want req-2", m.confirm.requestID)
			}
			m.handleAgentEvent(agentEventMsg{event: agent.ConfirmResolvedEvent{RequestID: "req-2", Outcome: outcome}})
			if m.question.request == nil || m.question.requestID != "q-2" {
				t.Fatal("remaining question was lost")
			}
			m.handleQuestionState(agent.QuestionSnapshot{ID: "q-2", Version: 2, Outcome: tools.QuestionOutcomeNoResponse})
			if m.dialogActive() || len(m.pendingDialogs) != 0 || m.mode != ModeNormal {
				t.Fatalf("resolved dialog reappeared: active=%v queued=%d mode=%v", m.dialogActive(), len(m.pendingDialogs), m.mode)
			}
			if len(backend.confirmResolutions) != 0 || m.activeToast != nil {
				t.Fatal("terminal events must not send another answer or show a toast")
			}
		})
	}
}

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
