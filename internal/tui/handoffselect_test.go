package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestHandoffSelectOptionIndexAtUsesListBaseRow(t *testing.T) {
	backend := &sessionControlAgent{
		availableAgents: []string{"builder", "reviewer", "qa"},
	}
	m := NewModelWithSize(backend, 120, 24)
	m.openHandoffSelect("docs/plans/example.md")
	if m.mode != ModeHandoffSelect {
		t.Fatalf("mode after open = %v, want ModeHandoffSelect", m.mode)
	}
	if m.handoffSelect.selector.list == nil {
		t.Fatal("expected handoff overlay list")
	}
	_ = m.renderHandoffSelectDialog()

	dialogRect := m.overlayRect(m.renderHandoffSelectDialog())
	x := dialogRect.Min.X + 2
	y := dialogRect.Min.Y + 1 + m.handoffSelect.selector.listBaseRow
	idx, ok := m.handoffSelectOptionIndexAt(x, y)
	if !ok {
		t.Fatal("expected hit test to resolve first list row")
	}
	if idx != 0 {
		t.Fatalf("hit-test index = %d, want 0", idx)
	}
}

func TestHandoffSelectOptionIndexAtAccountsForScrollWindowStart(t *testing.T) {
	agents := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		agents = append(agents, fmt.Sprintf("agent-%02d", i))
	}
	backend := &sessionControlAgent{availableAgents: agents}

	// Height chosen so handoffSelectMaxVisible() clamps to 3.
	m := NewModelWithSize(backend, 120, 16)
	m.openHandoffSelect("docs/plans/example.md")
	if m.handoffSelect.selector.list == nil {
		t.Fatal("expected handoff overlay list")
	}
	m.handoffSelect.selector.list.SetCursor(11)
	_ = m.renderHandoffSelectDialog()

	start, end := m.handoffSelect.selector.list.WindowRange()
	if end-start != 3 {
		t.Fatalf("visible window = %d, want 3", end-start)
	}
	if start == 0 {
		t.Fatal("expected list to be scrolled")
	}

	dialogRect := m.overlayRect(m.renderHandoffSelectDialog())
	x := dialogRect.Min.X + 2
	y := dialogRect.Min.Y + 1 + m.handoffSelect.selector.listBaseRow
	idx, ok := m.handoffSelectOptionIndexAt(x, y)
	if !ok {
		t.Fatal("expected hit test to resolve first visible list row")
	}
	if idx != start {
		t.Fatalf("hit-test index = %d, want %d (window start)", idx, start)
	}
}

func TestHandoffSelectModalMouseWheelScrollsPlanPreview(t *testing.T) {
	backend := &sessionControlAgent{availableAgents: []string{"builder", "reviewer", "qa"}}
	m := NewModelWithSize(backend, 120, 24)
	m.openHandoffSelect("docs/plans/example.md")
	m.handoffSelect.planErr = ""
	m.handoffSelect.planText = strings.Repeat("This handoff plan preview should wrap into several visible lines. ", 40)
	m.layout = m.generateLayout(m.width, m.height)

	cmd, handled := m.handleModalMouseMsg(tea.MouseWheelMsg{X: 1, Y: 1, Button: tea.MouseWheelDown})
	if !handled {
		t.Fatal("handoff select wheel was not handled")
	}
	if cmd != nil {
		t.Fatalf("wheel returned cmd %#v, want nil", cmd)
	}
	if got := m.handoffSelect.scroll; got != mouseWheelScrollStep {
		t.Fatalf("plan scroll after wheel down = %d, want %d", got, mouseWheelScrollStep)
	}
	if got := m.handoffSelect.selector.list.CursorAt(); got != 0 {
		t.Fatalf("cursor after plan wheel = %d, want unchanged 0", got)
	}

	_ = m.renderHandoffSelectDialog()
	if got := m.handoffSelect.scroll; got != 3 {
		t.Fatalf("clamped plan scroll = %d, want 3", got)
	}
}

func TestHandoffSelectViewOpensContentViewer(t *testing.T) {
	backend := &sessionControlAgent{availableAgents: []string{"builder"}}
	m := NewModelWithSize(backend, 120, 24)
	m.openHandoffSelect("docs/plans/example.md")
	m.handoffSelect.planErr = ""
	m.handoffSelect.planText = "# Plan\n\nDo the work."

	cmd := m.handleHandoffSelectKey(tea.KeyPressMsg(tea.Key{Text: "v", Code: 'v'}))
	if cmd != nil {
		_ = cmd()
	}
	if m.mode != ModeContentViewer {
		t.Fatalf("mode after Handoff view = %v, want ModeContentViewer", m.mode)
	}
	if m.contentViewer.prevMode != ModeHandoffSelect {
		t.Fatalf("viewer prevMode = %v, want ModeHandoffSelect", m.contentViewer.prevMode)
	}
	if !strings.Contains(m.contentViewer.content, "Do the work.") {
		t.Fatalf("viewer content = %q", m.contentViewer.content)
	}
}

func TestHandoffViewYankCopiesFullPlan(t *testing.T) {
	origWrite := clipboardWriteAll
	var copied string
	clipboardWriteAll = func(text string) error {
		copied = text
		return nil
	}
	defer func() { clipboardWriteAll = origWrite }()

	backend := &sessionControlAgent{availableAgents: []string{"builder"}}
	m := NewModelWithSize(backend, 120, 24)
	m.openHandoffSelect("docs/plans/example.md")
	m.handoffSelect.planErr = ""
	m.handoffSelect.planText = "# Plan\n\nDo the work."

	cmd := m.handleHandoffSelectKey(tea.KeyPressMsg(tea.Key{Text: "v", Code: 'v'}))
	if cmd != nil {
		_ = cmd()
	}
	if m.mode != ModeContentViewer {
		t.Fatalf("mode after Handoff view = %v, want ModeContentViewer", m.mode)
	}

	_ = m.handleContentViewerKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}))
	cmd = m.handleContentViewerKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}))
	if cmd == nil {
		t.Fatal("Handoff view yy should return clipboard command")
	}
	msg := cmd()
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Len() != 2 {
		t.Fatalf("clipboard command msg = %T, want 2-command sequence", msg)
	}
	second := v.Index(1).Call(nil)[0].Interface().(clipboardWriteResultMsg)
	if second.success != "View content copied to clipboard" {
		t.Fatalf("clipboard success = %q", second.success)
	}
	want := "Plan path: docs/plans/example.md\n\n---\n\n# Plan\n\nDo the work."
	if copied != want {
		t.Fatalf("copied content = %q, want %q", copied, want)
	}
	if backend.executePlanCalls != 0 {
		t.Fatalf("view yy should not confirm handoff, ExecutePlan calls = %d", backend.executePlanCalls)
	}
}

func TestHandoffSelectEscClosesWithoutExecutingPlan(t *testing.T) {
	backend := &sessionControlAgent{availableAgents: []string{"builder", "reviewer"}}
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeNormal
	m.openHandoffSelect("docs/plans/example.md")

	cmd := m.handleHandoffSelectKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if cmd != nil {
		_ = cmd()
	}
	if m.mode != ModeNormal {
		t.Fatalf("mode after close = %v, want ModeNormal", m.mode)
	}
	if backend.executePlanCalls != 0 {
		t.Fatalf("ExecutePlan calls = %d, want 0", backend.executePlanCalls)
	}
	if backend.continueCalls != 0 || len(backend.contextMessages) != 0 {
		t.Fatalf("Esc should not continue or append context, continue=%d messages=%d", backend.continueCalls, len(backend.contextMessages))
	}
}

func TestHandoffSelectDenyWithReasonContinuesFromContext(t *testing.T) {
	backend := &sessionControlAgent{availableAgents: []string{"builder", "reviewer"}}
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeNormal
	m.openHandoffSelect("docs/plans/example.md")

	_ = m.handleHandoffSelectKey(tea.KeyPressMsg(tea.Key{Text: "r", Code: 'r'}))
	if !m.handoffSelect.denyingWithReason {
		t.Fatal("expected handoff deny reason mode")
	}
	m.handoffSelect.denyReasonInput.SetValue("use reviewer first")
	cmd := m.handleHandoffSelectKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if cmd != nil {
		_ = cmd()
	}

	if backend.executePlanCalls != 0 {
		t.Fatalf("ExecutePlan calls = %d, want 0", backend.executePlanCalls)
	}
	if backend.continueCalls != 1 {
		t.Fatalf("ContinueFromContext calls = %d, want 1", backend.continueCalls)
	}
	if len(backend.contextMessages) != 1 {
		t.Fatalf("context messages = %d, want 1", len(backend.contextMessages))
	}
	msg := backend.contextMessages[0]
	if msg.Role != "user" || !strings.Contains(msg.Content, "Handoff rejected: use reviewer first") || !strings.Contains(msg.Content, "Plan path: docs/plans/example.md") {
		t.Fatalf("unexpected context message: %+v", msg)
	}
}

func TestHandoffSelectConfirmExecutesSelectedPlan(t *testing.T) {
	backend := &sessionControlAgent{availableAgents: []string{"builder", "reviewer", "qa"}}
	m := NewModelWithSize(backend, 120, 24)
	m.openHandoffSelect("docs/plans/example.md")
	m.handoffSelect.selector.list.SetCursor(1)

	cmd := m.confirmHandoff()
	if cmd != nil {
		_ = cmd()
	}
	if backend.executePlanCalls != 1 {
		t.Fatalf("ExecutePlan calls = %d, want 1", backend.executePlanCalls)
	}
	if backend.executePlanPath != "docs/plans/example.md" || backend.executePlanAgent != "reviewer" {
		t.Fatalf("ExecutePlan = (%q, %q), want plan path and reviewer", backend.executePlanPath, backend.executePlanAgent)
	}
}

func TestHandoffSelectDenyReasonMouseClickDoesNotApprove(t *testing.T) {
	backend := &sessionControlAgent{availableAgents: []string{"builder", "reviewer", "qa"}}
	m := NewModelWithSize(backend, 120, 24)
	m.openHandoffSelect("docs/plans/example.md")
	m.layout = m.generateLayout(m.width, m.height)
	_ = m.handleHandoffSelectKey(tea.KeyPressMsg(tea.Key{Text: "r", Code: 'r'}))
	_ = m.renderHandoffSelectDialog()
	dialogRect := m.overlayRect(m.renderHandoffSelectDialog())
	clickX := dialogRect.Min.X + 2
	clickY := dialogRect.Min.Y + 1 + m.handoffSelect.selector.listBaseRow + 1

	cmd, handled := m.handleModalMouseMsg(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	if !handled {
		t.Fatal("handoff deny reason click was not handled")
	}
	if cmd != nil {
		t.Fatalf("deny reason click returned cmd %#v, want nil", cmd)
	}
	if backend.executePlanCalls != 0 {
		t.Fatalf("ExecutePlan calls = %d, want 0", backend.executePlanCalls)
	}
	if !m.handoffSelect.denyingWithReason {
		t.Fatal("deny reason mode should remain active")
	}
}

func TestHandoffDenyReasonAcceptsPasteMsg(t *testing.T) {
	backend := &sessionControlAgent{availableAgents: []string{"builder"}}
	m := NewModelWithSize(backend, 120, 24)
	m.openHandoffSelect("docs/plans/example.md")
	_ = m.handleHandoffSelectKey(tea.KeyPressMsg(tea.Key{Text: "r", Code: 'r'}))

	cmd := m.handleNonKeyInputMsg(tea.PasteMsg{Content: "because pasted\nwith details"})
	if cmd != nil {
		t.Fatalf("PasteMsg returned cmd %#v, want nil", cmd)
	}
	if got := m.handoffSelect.denyReasonInput.Value(); got != "because pasted\nwith details" {
		t.Fatalf("deny reason input = %q", got)
	}
}

func TestHandoffSelectModalMouseClickUpdatesCursorAndReturnsCommand(t *testing.T) {
	backend := &sessionControlAgent{availableAgents: []string{"builder", "reviewer", "qa"}}
	m := NewModelWithSize(backend, 120, 24)
	m.openHandoffSelect("docs/plans/example.md")
	m.layout = m.generateLayout(m.width, m.height)
	_ = m.renderHandoffSelectDialog()
	dialogRect := m.overlayRect(m.renderHandoffSelectDialog())
	clickX := dialogRect.Min.X + 2
	clickY := dialogRect.Min.Y + 1 + m.handoffSelect.selector.listBaseRow + 1

	cmd, handled := m.handleModalMouseMsg(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	if !handled {
		t.Fatal("handoff select click was not handled")
	}
	_ = cmd
	if got := m.handoffSelect.selector.list.CursorAt(); got != 1 {
		t.Fatalf("cursor after click = %d, want 1", got)
	}
	if backend.executePlanCalls != 1 {
		t.Fatalf("ExecutePlan calls after click = %d, want 1", backend.executePlanCalls)
	}
}
