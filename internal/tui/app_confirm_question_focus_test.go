package tui

import (
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/tools"
)

// collectFollowupMsgs flattens the command batch returned for an interactive
// agent event into its concrete messages.
func TestFocusAgentForRequestSwitchesToAskingSubAgent(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.focusAgentForRequest("agent-1")
	if m.focusedAgentID != "agent-1" {
		t.Fatalf("focusedAgentID = %q, want %q (sub-agent request must switch focus)", m.focusedAgentID, "agent-1")
	}
}

func TestFocusAgentForRequestSwitchesToMainForMainAndUnset(t *testing.T) {
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
			if m.focusedAgentID != "" {
				t.Fatalf("focusedAgentID = %q, want %q (main request must switch to the main view)", m.focusedAgentID, "")
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

func TestConfirmRequestFromMainSwitchesToMainView(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.focusedAgentID = "agent-1"
	m.handleConfirmRequest(confirmRequestMsg{request: ConfirmRequest{
		ToolName:  tools.NameEdit,
		RequestID: "req-1",
		AgentID:   identity.MainAgentID,
	}})
	if m.focusedAgentID != "" {
		t.Fatalf("focusedAgentID = %q, want main view (confirm from main must switch focus)", m.focusedAgentID)
	}
}

func TestQuestionRequestOpensAutomaticallyAndPreservesComposer(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeInsert
	m.input.SetValue("composer draft")
	m.handleQuestionState(questionEventForTest("q-1", "Choice", "Choose", []string{"one"}, nil, false, time.Time{}, "agent-2").Question)
	if m.focusedAgentID != "" || m.mode != ModeQuestion || m.input.Value() != "composer draft" || m.question.request == nil {
		t.Fatal("automatic question lost composer draft or agent focus")
	}
	if m.mode != ModeQuestion || m.question.requestID != "q-1" || m.focusedAgentID != "" {
		t.Fatal("explicit opening must preserve agent focus")
	}

}

func TestQueuedHandoffWithoutTargetsIsSkipped(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeNormal
	m.handleConfirmRequest(confirmRequestMsg{request: ConfirmRequest{ToolName: tools.NameEdit}})
	m.handleHandoffSelectRequest(handoffSelectRequestMsg{planPath: "example.md", requestID: "h-1", agentID: identity.MainAgentID})
	m.handleQuestionState(questionEventForTest("q-1", "Choice", "Choose", nil, nil, false, time.Time{}, "main").Question)
	m.resolveConfirm(ConfirmResult{Action: ConfirmAllow})
	if !m.dialogActive() || m.mode != ModeQuestion {
		t.Fatal("question must open after skipping an unavailable handoff")
	}
	if len(backend.handoffResolutions) != 1 || backend.handoffResolutions[0].Action != "cancel" {
		t.Fatal("targetless handoff must cancel")
	}

}

func TestConfirmAndQuestionEventsCarryAgentIDToRequest(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.handleAgentEvent(agentEventMsg{event: agent.ConfirmRequestEvent{ToolName: tools.NameEdit, RequestID: "req-confirm", AgentID: "agent-1"}})
	if m.confirm.request == nil || m.confirm.request.AgentID != "agent-1" {
		t.Fatal("confirm event must open for its asking agent")
	}
	m.handleAgentEvent(agentEventMsg{event: questionEventForTest("req-question", "", "continue?", nil, nil, false, time.Time{}, "agent-1")})
	if len(m.pendingDialogs) != 1 || m.questionRecords["req-question"].AgentID != "agent-1" {
		t.Fatal("question must queue behind confirm")
	}
	m.handleAgentEvent(agentEventMsg{event: agent.HandoffEvent{PlanPath: "plan.md", RequestID: "h-1", AgentID: "agent-1"}})
	if len(m.pendingDialogs) != 2 || m.pendingDialogs[1].handoff == nil || m.pendingDialogs[1].handoff.agentID != "agent-1" {
		t.Fatal("handoff must retain the asking agent")
	}
}

func TestHandoffRequestSwitchesFocusToAskingAgent(t *testing.T) {
	backend := &sessionControlAgent{availableAgents: []string{"builder", "reviewer"}}
	m := NewModelWithSize(backend, 120, 24)
	m.focusedAgentID = "agent-2"
	m.mode = ModeNormal

	// Handoff is main-only, so the request must switch back to the main view.
	m.handleHandoffSelectRequest(handoffSelectRequestMsg{
		planPath:  "docs/plans/example.md",
		requestID: "h-1",
		agentID:   identity.MainAgentID,
	})
	if m.focusedAgentID != "" {
		t.Fatalf("focusedAgentID = %q, want main view (handoff must switch focus)", m.focusedAgentID)
	}
	if m.mode != ModeHandoffSelect {
		t.Fatalf("mode = %v, want ModeHandoffSelect", m.mode)
	}
}

func TestHandoffRequestQueuesBehindOpenDialog(t *testing.T) {
	backend := &sessionControlAgent{availableAgents: []string{"builder", "reviewer"}}
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeNormal

	m.handleConfirmRequest(confirmRequestMsg{request: ConfirmRequest{ToolName: tools.NameEdit}})
	m.handleHandoffSelectRequest(handoffSelectRequestMsg{
		planPath:  "docs/plans/example.md",
		requestID: "h-1",
		agentID:   identity.MainAgentID,
	})
	if m.mode != ModeConfirm || m.handoffSelect.active() {
		t.Fatalf("mode = %v handoffActive=%v, want the confirm still shown", m.mode, m.handoffSelect.active())
	}
	if len(m.pendingDialogs) != 1 || m.pendingDialogs[0].handoff == nil {
		t.Fatalf("pendingDialogs = %+v, want one queued handoff", m.pendingDialogs)
	}

	// Closing the confirm presents the queued handoff.
	_ = m.resolveConfirm(ConfirmResult{Action: ConfirmAllow})
	if m.mode != ModeHandoffSelect || !m.handoffSelect.active() {
		t.Fatalf("mode = %v handoffActive=%v, want ModeHandoffSelect", m.mode, m.handoffSelect.active())
	}
	if len(m.pendingDialogs) != 0 {
		t.Fatalf("pendingDialogs = %d, want 0 after presenting", len(m.pendingDialogs))
	}
}

func TestDialogRequestsQueueAndPresentInArrivalOrder(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeNormal
	m.handleConfirmRequest(confirmRequestMsg{request: ConfirmRequest{ToolName: tools.NameEdit, AgentID: "agent-1"}})
	m.handleQuestionState(questionEventForTest("q-1", "Choice", "Choose", nil, nil, false, time.Time{}, "agent-2").Question)
	if m.mode != ModeConfirm || len(m.pendingDialogs) != 1 {
		t.Fatal("question replaced or queued behind confirm")
	}
	m.resolveConfirm(ConfirmResult{Action: ConfirmAllow})
	if m.mode != ModeQuestion || m.question.request == nil {
		t.Fatal("confirm close must open the queued question automatically")
	}
	if m.mode != ModeQuestion || m.focusedAgentID != "agent-1" {
		t.Fatal("opening question changed transcript focus")
	}

}

func TestQueuedDialogRestoresBaseModeAfterLastDialog(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeNormal

	m.handleConfirmRequest(confirmRequestMsg{request: ConfirmRequest{ToolName: tools.NameEdit}})
	m.handleConfirmRequest(confirmRequestMsg{request: ConfirmRequest{ToolName: tools.NameShell, AgentID: "agent-1"}})
	if len(m.pendingDialogs) != 1 {
		t.Fatalf("pendingDialogs = %d, want 1", len(m.pendingDialogs))
	}

	_ = m.resolveConfirm(ConfirmResult{Action: ConfirmDeny})
	if !m.dialogActive() || m.mode != ModeConfirm {
		t.Fatalf("second confirm should be presented: active=%v mode=%v", m.dialogActive(), m.mode)
	}
	if m.focusedAgentID != "agent-1" {
		t.Fatalf("focusedAgentID = %q, want %q", m.focusedAgentID, "agent-1")
	}

	_ = m.resolveConfirm(ConfirmResult{Action: ConfirmDeny})
	if m.dialogActive() {
		t.Fatal("no dialog should remain after the queue drains")
	}
	if m.mode != ModeNormal {
		t.Fatalf("mode = %v, want ModeNormal once the queue drains", m.mode)
	}
}

func TestExpiredQueuedConfirmIsDropped(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeNormal

	m.handleConfirmRequest(confirmRequestMsg{request: ConfirmRequest{ToolName: tools.NameShell}})
	m.handleConfirmRequest(confirmRequestMsg{request: ConfirmRequest{ToolName: tools.NameEdit, Timeout: time.Second}})
	m.pendingDialogs[0].arrivedAt = time.Now().Add(-2 * time.Second)

	_ = m.resolveConfirm(ConfirmResult{Action: ConfirmAllow})
	if m.dialogActive() {
		t.Fatal("an already-timed-out queued dialog must not be presented")
	}
	if m.mode != ModeNormal {
		t.Fatalf("mode = %v, want ModeNormal after dropping the expired dialog", m.mode)
	}
	if len(m.pendingDialogs) != 0 {
		t.Fatalf("pendingDialogs = %d, want 0", len(m.pendingDialogs))
	}
}

// flattenCmdMsgs recursively flattens a command's batch messages so a nested
// tea.Batch (for example a toast tick batched inside finishDialog) stays
// visible to the assertion.
func flattenCmdMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var msgs []tea.Msg
		for _, sub := range batch {
			msgs = append(msgs, flattenCmdMsgs(sub)...)
		}
		return msgs
	}
	return []tea.Msg{msg}
}

func countToastTickMsgs(msgs []tea.Msg) int {
	count := 0
	for _, msg := range msgs {
		if _, ok := msg.(toastTickMsg); ok {
			count++
		}
	}
	return count
}

func TestQueuedQuestionUsesAbsoluteDeadline(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeNormal
	deadline := time.Now().Add(time.Minute)
	m.handleQuestionState(questionEventForTest("q-1", "Choice", "Choose", nil, nil, false, deadline, "main").Question)
	if m.question.request == nil {
		t.Fatal("arrival must open automatically")
	}
	if m.question.request == nil || !m.question.deadline.Equal(deadline) {
		t.Fatal("local UI changed Core deadline")
	}

}

func TestQueuedQuestionWithElapsedDeadlineStillPresented(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeNormal
	deadline := time.Now().Add(-time.Second)
	m.handleQuestionState(questionEventForTest("q-1", "Choice", "Choose", nil, nil, false, deadline, "main").Question)
	if m.question.request == nil {
		t.Fatal("arrival must open automatically")
	}
	if m.question.request == nil || !m.question.deadline.Equal(deadline) {
		t.Fatal("local UI changed Core deadline")
	}

}

func TestSessionSwitchStartedClearsActiveAndQueuedDialogs(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeNormal
	m.handleConfirmRequest(confirmRequestMsg{request: ConfirmRequest{ToolName: tools.NameEdit}})
	m.handleQuestionState(questionEventForTest("q-1", "Choice", "Choose", nil, nil, false, time.Time{}, "main").Question)
	m.handleAgentEvent(agentEventMsg{event: agent.SessionSwitchStartedEvent{Kind: "resume", SessionID: "session-2"}})
	if m.dialogActive() || len(m.pendingDialogs) != 0 || m.mode != ModeNormal {
		t.Fatal("switch left outgoing projections")
	}

}

func TestSessionSwitchStartedClearsActiveHandoff(t *testing.T) {
	backend := &sessionControlAgent{availableAgents: []string{"builder", "reviewer"}}
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeNormal

	m.handleHandoffSelectRequest(handoffSelectRequestMsg{planPath: "docs/plans/example.md", requestID: "h-1", agentID: identity.MainAgentID})
	if !m.handoffSelect.active() {
		t.Fatal("setup: handoff modal should be active")
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.SessionSwitchStartedEvent{Kind: "resume", SessionID: "session-2"}})

	if m.dialogActive() {
		t.Fatal("a handoff modal from the outgoing session must not survive the switch")
	}
	if m.mode != ModeNormal {
		t.Fatalf("mode = %v, want ModeNormal", m.mode)
	}
}

func TestHandoffWithoutTargetsReturnsToastCommand(t *testing.T) {
	stubTUITicks(t)

	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeNormal

	cmd := m.handleHandoffSelectRequest(handoffSelectRequestMsg{planPath: "docs/plans/example.md", requestID: "h-1", agentID: identity.MainAgentID})

	if cmd == nil {
		t.Fatal("a targetless handoff must return its toast command instead of dropping it")
	}
	if m.activeToast == nil {
		t.Fatal("expected the no-target warning toast to be active")
	}
	if got := countToastTickMsgs(flattenCmdMsgs(cmd)); got != 1 {
		t.Fatalf("toast tick commands = %d, want the tick that auto-dismisses the toast", got)
	}
}

func TestFinishDialogKeepsSkippedHandoffToastCommand(t *testing.T) {
	stubTUITicks(t)

	backend := &sessionControlAgent{} // no eligible handoff target: the dialog skips itself
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeConfirm
	m.pendingDialogs = append(m.pendingDialogs, pendingDialog{
		handoff:   &handoffSelectRequestMsg{planPath: "docs/plans/example.md", requestID: "h-1"},
		arrivedAt: time.Now(),
	})

	// finishDialog is called directly (not through resolveConfirm) so its
	// returned batch holds no blocking channel re-subscription.
	cmd := m.finishDialog(ModeNormal)

	if m.dialogActive() {
		t.Fatal("the skipped handoff must not leave a modal on screen")
	}
	if m.activeToast == nil {
		t.Fatal("expected the no-target warning toast to be active")
	}
	if got := countToastTickMsgs(flattenCmdMsgs(cmd)); got != 1 {
		t.Fatalf("toast tick commands = %d, want finishDialog to keep the skipped handoff's toast tick", got)
	}
}

func TestQuestionResolvedEventClosesMatchingActiveQuestion(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeNormal

	m.receiveTestQuestion(questionDialog{
		requestID: "q-active",
		request: QuestionRequest{
			Item:    tools.QuestionItem{Header: "pick", Question: "which?"},
			AgentID: "agent-1",
		},
	})
	if m.question.request == nil || m.question.requestID != "q-active" {
		t.Fatalf("setup: question = %+v, want q-active active", m.question)
	}

	// A resolved event for another request must leave the dialog alone.
	m.handleAgentEvent(agentEventMsg{event: agent.QuestionStateEvent{Question: agent.QuestionSnapshot{ID: "q-other", Outcome: tools.QuestionOutcomeSuperseded, Version: 2}}})
	if m.question.request == nil {
		t.Fatal("a resolved event for another request must not close the active dialog")
	}

	// The matching event closes the active dialog and restores the base mode.
	m.handleAgentEvent(agentEventMsg{event: agent.QuestionStateEvent{Question: agent.QuestionSnapshot{ID: "q-active", Outcome: tools.QuestionOutcomeNoResponse, Version: 2}}})
	if m.question.request != nil || m.dialogActive() {
		t.Fatal("the matching resolved event must close the active question")
	}
	if m.mode != ModeNormal {
		t.Fatalf("mode = %v, want the pre-dialog mode", m.mode)
	}
}

func TestQuestionResolvedEventDropsMatchingQueuedQuestion(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeNormal
	m.handleConfirmRequest(confirmRequestMsg{request: ConfirmRequest{ToolName: tools.NameEdit}})
	m.handleQuestionState(questionEventForTest("q-1", "Choice", "Choose", nil, nil, false, time.Time{}, "main").Question)
	m.handleQuestionState(agent.QuestionSnapshot{ID: "other", Version: 2, Outcome: tools.QuestionOutcomeNoResponse})
	if len(m.pendingDialogs) != 1 {
		t.Fatal("unmatched terminal removed question")
	}
	m.handleQuestionState(agent.QuestionSnapshot{ID: "q-1", Version: 2, Outcome: tools.QuestionOutcomeNoResponse})
	m.resolveConfirm(ConfirmResult{Action: ConfirmAllow})
	if m.dialogActive() || len(m.pendingDialogs) != 0 {
		t.Fatal("terminal question remained discoverable")
	}

}

func TestQuestionRequestResolvedInSameEventBatchLeavesNoDialog(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeNormal

	// The request installs its dialog synchronously, so a resolved event later
	// in the same batch always finds the dialog it must close.
	updated, _ := m.Update(agentEventBatchMsg{
		{event: questionEventForTest("q-batch", "", "which?", nil, nil, false, time.Time{}, "agent-1")},
		{event: agent.QuestionStateEvent{Question: agent.QuestionSnapshot{ID: "q-batch", Outcome: tools.QuestionOutcomeNoResponse, Version: 2}}},
	})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if model.question.request != nil || model.dialogActive() {
		t.Fatal("a question resolved within its own event batch must not leave a dialog on screen")
	}
	if model.mode != ModeNormal {
		t.Fatalf("mode = %v, want the pre-dialog mode", model.mode)
	}
}
