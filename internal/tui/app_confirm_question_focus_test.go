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

func TestQueuedHandoffWithoutTargetsIsSkipped(t *testing.T) {
	// No available handoff targets: the request cancels itself and the queue
	// continues to the next dialog instead of leaving a stale mode behind.
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeNormal

	m.handleConfirmRequest(confirmRequestMsg{request: ConfirmRequest{ToolName: tools.NameEdit}})
	m.handleHandoffSelectRequest(handoffSelectRequestMsg{
		planPath:  "docs/plans/example.md",
		requestID: "h-1",
		agentID:   identity.MainAgentID,
	})
	m.handleQuestionRequest(questionRequestMsg{request: QuestionRequest{
		Questions: []tools.QuestionItem{{Header: "pick", Question: "which?"}},
	}})
	if len(m.pendingDialogs) != 2 {
		t.Fatalf("pendingDialogs = %d, want 2", len(m.pendingDialogs))
	}

	_ = m.resolveConfirm(ConfirmResult{Action: ConfirmAllow})
	if m.mode != ModeQuestion || m.question.request == nil {
		t.Fatalf("mode = %v, want the question after the targetless handoff is skipped", m.mode)
	}
	if len(m.pendingDialogs) != 0 {
		t.Fatalf("pendingDialogs = %d, want 0", len(m.pendingDialogs))
	}
	if len(backend.handoffResolutions) != 1 || backend.handoffResolutions[0].Action != "cancel" {
		t.Fatalf("handoff resolutions = %+v, want the targetless handoff cancelled", backend.handoffResolutions)
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

	cmd = m.handleAgentEvent(agentEventMsg{event: agent.HandoffEvent{
		PlanPath:  "docs/plans/example.md",
		RequestID: "req-handoff",
		AgentID:   "agent-1",
	}})
	var handoff *handoffSelectRequestMsg
	for _, msg := range collectFollowupMsgs(cmd) {
		if hr, ok := msg.(handoffSelectRequestMsg); ok {
			handoff = &hr
		}
	}
	if handoff == nil {
		t.Fatal("handoff event should produce a handoffSelectRequestMsg followup")
	}
	if handoff.agentID != "agent-1" {
		t.Fatalf("handoffSelectRequestMsg.agentID = %q, want %q", handoff.agentID, "agent-1")
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

	m.handleConfirmRequest(confirmRequestMsg{request: ConfirmRequest{
		ToolName: tools.NameEdit,
		AgentID:  "agent-1",
	}})
	if !m.dialogActive() || m.mode != ModeConfirm || m.focusedAgentID != "agent-1" {
		t.Fatalf("first dialog: active=%v mode=%v focus=%q", m.dialogActive(), m.mode, m.focusedAgentID)
	}

	// A second request must wait instead of replacing the visible dialog.
	m.handleQuestionRequest(questionRequestMsg{
		requestID: "req-question",
		request: QuestionRequest{
			Questions: []tools.QuestionItem{{Header: "pick", Question: "which?", Options: []tools.QuestionOption{{Label: "one"}}}},
			AgentID:   "agent-2",
		},
	})
	if m.question.request != nil {
		t.Fatal("question must not replace the open confirm dialog")
	}
	if len(m.pendingDialogs) != 1 || m.pendingDialogs[0].question == nil {
		t.Fatalf("pendingDialogs = %+v, want one queued question", m.pendingDialogs)
	}
	if m.mode != ModeConfirm {
		t.Fatalf("mode = %v, want ModeConfirm while the confirm is still shown", m.mode)
	}

	// Closing the confirm presents the queued question against its own agent.
	_ = m.resolveConfirm(ConfirmResult{Action: ConfirmAllow})
	if m.question.request == nil {
		t.Fatal("queued question should be presented once the confirm closes")
	}
	if m.mode != ModeQuestion {
		t.Fatalf("mode = %v, want ModeQuestion", m.mode)
	}
	if m.focusedAgentID != "agent-2" {
		t.Fatalf("focusedAgentID = %q, want %q (queued dialog switches to its agent)", m.focusedAgentID, "agent-2")
	}
	if len(m.pendingDialogs) != 0 {
		t.Fatalf("pendingDialogs = %d, want 0 after presenting", len(m.pendingDialogs))
	}
	if m.question.prevMode != ModeNormal {
		t.Fatalf("queued question prevMode = %v, want ModeNormal (base mode, not the previous dialog mode)", m.question.prevMode)
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

func TestExpiredQueuedDialogIsDropped(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeNormal

	m.handleConfirmRequest(confirmRequestMsg{request: ConfirmRequest{ToolName: tools.NameEdit}})
	m.handleQuestionRequest(questionRequestMsg{
		request: QuestionRequest{
			Questions: []tools.QuestionItem{{Header: "pick", Question: "which?"}},
			Timeout:   time.Second,
			AgentID:   "agent-2",
		},
	})
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

func TestQueuedDialogDeadlineAnchorsToArrival(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeNormal

	m.handleConfirmRequest(confirmRequestMsg{request: ConfirmRequest{ToolName: tools.NameEdit}})
	arrivedAt := time.Now().Add(-2 * time.Second)
	m.handleQuestionRequest(questionRequestMsg{
		request: QuestionRequest{
			Questions: []tools.QuestionItem{{Header: "pick", Question: "which?"}},
			Timeout:   5 * time.Second,
			AgentID:   "agent-2",
		},
	})
	m.pendingDialogs[0].arrivedAt = arrivedAt

	_ = m.resolveConfirm(ConfirmResult{Action: ConfirmAllow})

	if m.question.request == nil {
		t.Fatal("a queued dialog whose own timeout has not elapsed must still be presented")
	}
	want := arrivedAt.Add(5 * time.Second)
	if !m.question.deadline.Equal(want) {
		t.Fatalf("deadline = %v, want %v (anchored to arrival + timeout, not reset to now + timeout)", m.question.deadline, want)
	}
	if now := time.Now(); !m.question.deadline.Before(now.Add(5 * time.Second)) {
		t.Fatalf("deadline = %v must leave less than a full timeout from now (%v)", m.question.deadline, now)
	}
}

func TestQueuedDialogAnsweredAfterOwnTimeoutIsNotPresented(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeNormal

	m.handleConfirmRequest(confirmRequestMsg{request: ConfirmRequest{ToolName: tools.NameEdit}})
	m.handleQuestionRequest(questionRequestMsg{
		request: QuestionRequest{
			Questions: []tools.QuestionItem{{Header: "pick", Question: "which?"}},
			Timeout:   2 * time.Second,
			AgentID:   "agent-2",
		},
	})
	// Answer the open confirm only after the queued question's own
	// arrivedAt+timeout window has already closed.
	m.pendingDialogs[0].arrivedAt = time.Now().Add(-3 * time.Second)

	_ = m.resolveConfirm(ConfirmResult{Action: ConfirmAllow})

	if m.dialogActive() {
		t.Fatal("a queued dialog whose own timeout already elapsed must not be shown")
	}
	if m.question.request != nil {
		t.Fatal("the expired queued question must not be answerable")
	}
	if m.mode != ModeNormal {
		t.Fatalf("mode = %v, want the base mode restored", m.mode)
	}
	if len(m.pendingDialogs) != 0 {
		t.Fatalf("pendingDialogs = %d, want 0", len(m.pendingDialogs))
	}
}

func TestSessionSwitchStartedClearsActiveAndQueuedDialogs(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeNormal

	m.handleConfirmRequest(confirmRequestMsg{request: ConfirmRequest{ToolName: tools.NameEdit}})
	m.handleQuestionRequest(questionRequestMsg{request: QuestionRequest{
		Questions: []tools.QuestionItem{{Header: "pick", Question: "which?"}},
		AgentID:   "agent-2",
	}})
	if !m.dialogActive() || len(m.pendingDialogs) != 1 {
		t.Fatalf("setup: active=%v queued=%d, want an active confirm and one queued question", m.dialogActive(), len(m.pendingDialogs))
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.SessionSwitchStartedEvent{Kind: "resume", SessionID: "session-2"}})

	if m.dialogActive() {
		t.Fatal("the outgoing session's active dialog must be dropped on switch")
	}
	if len(m.pendingDialogs) != 0 {
		t.Fatalf("pendingDialogs = %d, want cleared on session switch", len(m.pendingDialogs))
	}
	if m.mode != ModeNormal {
		t.Fatalf("mode = %v, want the pre-dialog mode restored", m.mode)
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
