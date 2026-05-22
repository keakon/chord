package tui

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestContentViewerScrollsWithKeysAndMouseWheel(t *testing.T) {
	m := NewModelWithSize(nil, 100, 12)
	m.openContentViewer("Preview", strings.Repeat("Scrollable markdown preview content. ", 80))
	if m.mode != ModeContentViewer {
		t.Fatalf("mode = %v, want ModeContentViewer", m.mode)
	}
	_ = m.renderContentViewer()
	if m.contentViewerMaxScroll(m.viewport.width) == 0 {
		t.Fatal("expected content viewer to have scrollable content")
	}

	_ = m.handleContentViewerKey(tea.KeyPressMsg(tea.Key{Text: "j", Code: 'j'}))
	if got := m.contentViewer.scrollOffset; got != 1 {
		t.Fatalf("scroll after j = %d, want 1", got)
	}

	cmd, handled := m.handleModalMouseMsg(tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelDown})
	if !handled {
		t.Fatal("content viewer wheel was not handled")
	}
	if cmd != nil {
		t.Fatalf("wheel returned cmd %#v, want nil", cmd)
	}
	if got := m.contentViewer.scrollOffset; got != 1+mouseWheelScrollStep {
		t.Fatalf("scroll after wheel = %d, want %d", got, 1+mouseWheelScrollStep)
	}
}

func TestContentViewerRendersWithMargins(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	m.openContentViewer("Preview", "# Title\n\nBody")

	view := m.renderContentViewer()
	lines := strings.Split(view, "\n")
	if len(lines) < 3 {
		t.Fatalf("rendered lines = %d, want at least 3", len(lines))
	}
	if strings.TrimSpace(lines[0]) != "" {
		t.Fatalf("top margin line = %q, want blank", lines[0])
	}
	if !strings.HasPrefix(lines[1], "  ") {
		t.Fatalf("content line missing left margin: %q", lines[1])
	}
}

func TestContentViewerCopyWritesRawContent(t *testing.T) {
	origWrite := clipboardWriteAll
	var copied string
	clipboardWriteAll = func(text string) error {
		copied = text
		return nil
	}
	defer func() { clipboardWriteAll = origWrite }()

	m := NewModelWithSize(nil, 100, 20)
	m.openContentViewer("Preview", "# Title\n\nBody")

	_ = m.handleContentViewerKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}))
	cmd := m.handleContentViewerKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}))
	if cmd == nil {
		t.Fatal("copy should return clipboard command")
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
	if copied != "# Title\n\nBody" {
		t.Fatalf("copied content = %q", copied)
	}
}

func TestContentViewerCopySelectionClearsHighlight(t *testing.T) {
	origWrite := clipboardWriteAll
	var copied string
	clipboardWriteAll = func(text string) error {
		copied = text
		return nil
	}
	defer func() { clipboardWriteAll = origWrite }()

	m := NewModelWithSize(nil, 100, 20)
	m.openContentViewer("Preview", "alpha beta gamma")
	_ = m.renderContentViewer()
	m.contentViewer.selStartLine = 2
	m.contentViewer.selStartCol = 0
	m.contentViewer.selEndLine = 2
	m.contentViewer.selEndCol = 4
	m.contentViewer.selEndInclusiveForCopy = true

	cmd := m.handleContentViewerKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}))
	if cmd == nil {
		t.Fatal("copy selection should return clipboard command")
	}
	msg := cmd()
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Len() != 2 {
		t.Fatalf("clipboard command msg = %T, want 2-command sequence", msg)
	}
	second := v.Index(1).Call(nil)[0].Interface().(clipboardWriteResultMsg)
	if second.success != "Selection copied to clipboard" {
		t.Fatalf("clipboard success = %q", second.success)
	}
	if copied != "alpha" {
		t.Fatalf("copied content = %q", copied)
	}
	if m.contentViewerHasSelection() {
		t.Fatal("selection should be cleared after y copies it")
	}
}

func TestContentViewerEscRestoresPreviousMode(t *testing.T) {
	m := NewModelWithSize(nil, 100, 20)
	m.mode = ModeConfirm
	m.confirm.request = &ConfirmRequest{ToolName: "Done", DoneReport: "report"}
	m.openContentViewer("Done report", "report")

	cmd := m.handleContentViewerKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if cmd != nil {
		_ = cmd()
	}
	if m.mode != ModeConfirm {
		t.Fatalf("mode after close = %v, want ModeConfirm", m.mode)
	}
	if m.confirm.request == nil {
		t.Fatal("confirm request should remain active after closing viewer")
	}
}
