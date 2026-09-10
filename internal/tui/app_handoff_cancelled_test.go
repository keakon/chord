package tui

import (
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/tools"
)

// TestHandoffCancelledClosesActiveSelector covers the common case: the wait was
// shown (the selector is on screen) and the runtime discards it before the
// user decides. The selector must close, the pre-dialog mode return, and a
// warning toast name the reason — without sending a decision the runtime has
// already cancelled.
func TestHandoffCancelledClosesActiveSelector(t *testing.T) {
	backend := &sessionControlAgent{availableAgents: []string{"builder", "reviewer"}}
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeInsert

	m.handleHandoffSelectRequest(handoffSelectRequestMsg{planPath: "docs/plans/example.md", requestID: "h-1", agentID: identity.MainAgentID})
	if m.mode != ModeHandoffSelect || !m.handoffSelect.active() {
		t.Fatalf("setup: mode=%v active=%v, want ModeHandoffSelect", m.mode, m.handoffSelect.active())
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.HandoffCancelledEvent{RequestID: "h-1", Reason: "superseded"}})

	if m.handoffSelect.active() {
		t.Fatal("the cancelled handoff selector must close")
	}
	if m.mode != ModeInsert {
		t.Fatalf("mode = %v, want the pre-dialog ModeInsert restored", m.mode)
	}
	if len(backend.handoffResolutions) != 0 {
		t.Fatalf("handoff resolutions = %+v, want none (the runtime already cancelled)", backend.handoffResolutions)
	}
	if m.activeToast == nil {
		t.Fatal("expected a warning toast for the cancelled handoff")
	}
	if m.activeToast.Level != "warn" {
		t.Fatalf("toast level = %q, want warn", m.activeToast.Level)
	}
	if !strings.Contains(m.activeToast.Message, "superseded") {
		t.Fatalf("toast = %q, want it to mention the reason", m.activeToast.Message)
	}
}

// TestHandoffCancelledDropsQueuedSelector covers a wait that had not been
// promoted yet: the queued entry is dropped so the next finishDialog cannot
// present the stale modal, and the drop also warns.
func TestHandoffCancelledDropsQueuedSelector(t *testing.T) {
	backend := &sessionControlAgent{availableAgents: []string{"builder", "reviewer"}}
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeNormal

	m.handleConfirmRequest(confirmRequestMsg{request: ConfirmRequest{ToolName: tools.NameEdit}})
	m.handleHandoffSelectRequest(handoffSelectRequestMsg{planPath: "docs/plans/example.md", requestID: "h-1", agentID: identity.MainAgentID})
	if len(m.pendingDialogs) != 1 || m.pendingDialogs[0].handoff == nil {
		t.Fatalf("setup: pendingDialogs = %+v, want one queued handoff", m.pendingDialogs)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.HandoffCancelledEvent{RequestID: "h-1", Reason: "superseded"}})

	if m.mode != ModeConfirm || m.confirm.request == nil {
		t.Fatalf("mode = %v confirm=%v, want the confirm still on screen", m.mode, m.confirm.request != nil)
	}
	if len(m.pendingDialogs) != 0 {
		t.Fatalf("pendingDialogs = %+v, want the queued handoff dropped", m.pendingDialogs)
	}
	if m.activeToast == nil {
		t.Fatal("dropping an about-to-be-shown handoff should warn")
	}

	_ = m.resolveConfirm(ConfirmResult{Action: ConfirmAllow})
	if m.mode == ModeHandoffSelect || m.handoffSelect.active() {
		t.Fatalf("mode = %v active=%v, want the dropped selector never presented", m.mode, m.handoffSelect.active())
	}
	if m.mode != ModeNormal {
		t.Fatalf("mode = %v, want ModeNormal once the confirm closes", m.mode)
	}
}

// TestHandoffCancelledIgnoresUnmatchedRequestID pins the no-side-effect rule:
// a cancel for an unrelated request leaves the active selector and the queue
// untouched and stays silent.
func TestHandoffCancelledIgnoresUnmatchedRequestID(t *testing.T) {
	backend := &sessionControlAgent{availableAgents: []string{"builder", "reviewer"}}
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeNormal

	m.handleHandoffSelectRequest(handoffSelectRequestMsg{planPath: "docs/plans/example.md", requestID: "h-1", agentID: identity.MainAgentID})
	m.handleHandoffSelectRequest(handoffSelectRequestMsg{planPath: "docs/plans/other.md", requestID: "h-2", agentID: identity.MainAgentID})
	if !m.handoffSelect.active() || len(m.pendingDialogs) != 1 {
		t.Fatalf("setup: active=%v queued=%d, want h-1 active with h-2 queued", m.handoffSelect.active(), len(m.pendingDialogs))
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.HandoffCancelledEvent{RequestID: "h-3", Reason: "superseded"}})

	if !m.handoffSelect.active() || strings.TrimSpace(m.handoffSelect.requestID) != "h-1" {
		t.Fatalf("active selector = %+v, want h-1 untouched", m.handoffSelect.requestID)
	}
	if len(m.pendingDialogs) != 1 || m.pendingDialogs[0].handoff == nil || m.pendingDialogs[0].handoff.requestID != "h-2" {
		t.Fatalf("pendingDialogs = %+v, want the unrelated h-2 kept", m.pendingDialogs)
	}
	if m.activeToast != nil {
		t.Fatalf("an unmatched cancel must stay silent, got toast %q", m.activeToast.Message)
	}
}

// TestHandoffCancelledClosesViewerAboveSelector covers the plan viewer opened
// on top of the selector: both layers must close, landing on the selector's
// pre-dialog mode instead of ModeHandoffSelect.
func TestHandoffCancelledClosesViewerAboveSelector(t *testing.T) {
	backend := &sessionControlAgent{availableAgents: []string{"builder", "reviewer"}}
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeNormal

	m.handleHandoffSelectRequest(handoffSelectRequestMsg{planPath: "docs/plans/example.md", requestID: "h-1", agentID: identity.MainAgentID})
	_ = m.handleHandoffSelectKey(tea.KeyPressMsg(tea.Key{Text: "v", Code: 'v'}))
	if m.mode != ModeContentViewer || m.contentViewer.prevMode != ModeHandoffSelect {
		t.Fatalf("setup: mode=%v prevMode=%v, want the viewer over the selector", m.mode, m.contentViewer.prevMode)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.HandoffCancelledEvent{RequestID: "h-1", Reason: "superseded"}})

	if m.mode != ModeNormal {
		t.Fatalf("mode = %v, want the selector's pre-dialog ModeNormal", m.mode)
	}
	if m.contentViewer.title != "" {
		t.Fatalf("content viewer = %+v, want it cleared", m.contentViewer.title)
	}
	if m.handoffSelect.active() {
		t.Fatal("the selector under the viewer must close too")
	}
	if len(backend.handoffResolutions) != 0 {
		t.Fatalf("handoff resolutions = %+v, want none", backend.handoffResolutions)
	}
	if m.activeToast == nil {
		t.Fatal("closing a shown handoff should warn")
	}
}

// TestHandoffCancelledWhileDenyingWithReason covers the deny-reason sub-state:
// the sub-mode must be dropped along with the selector, leaving no residue.
func TestHandoffCancelledWhileDenyingWithReason(t *testing.T) {
	backend := &sessionControlAgent{availableAgents: []string{"builder", "reviewer"}}
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeNormal

	m.handleHandoffSelectRequest(handoffSelectRequestMsg{planPath: "docs/plans/example.md", requestID: "h-1", agentID: identity.MainAgentID})
	_ = m.handleHandoffSelectKey(tea.KeyPressMsg(tea.Key{Text: "r", Code: 'r'}))
	if !m.handoffSelect.denyingWithReason {
		t.Fatal("setup: expected the handoff deny-reason sub-mode")
	}
	m.handoffSelect.denyReasonInput.SetValue("reviewer should go first")

	_ = m.handleAgentEvent(agentEventMsg{event: agent.HandoffCancelledEvent{RequestID: "h-1", Reason: "superseded"}})

	if m.mode != ModeNormal {
		t.Fatalf("mode = %v, want ModeNormal", m.mode)
	}
	if m.handoffSelect.active() {
		t.Fatal("the selector must close")
	}
	if m.handoffSelect.denyingWithReason {
		t.Fatal("the deny-reason sub-mode must not survive")
	}
	if got := m.handoffSelect.denyReasonInput.Value(); got != "" {
		t.Fatalf("deny reason input = %q, want cleared", got)
	}
	if len(backend.handoffResolutions) != 0 {
		t.Fatalf("handoff resolutions = %+v, want none (no deny is sent on cancel)", backend.handoffResolutions)
	}
	if m.activeToast == nil {
		t.Fatal("closing a shown handoff should warn")
	}
}
