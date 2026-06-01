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
	m.layout = m.generateLayout(m.width, m.height)
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
	m.openContentViewer("Preview", "\n# Title\n\nBody\n")

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
	if copied != "\n# Title\n\nBody\n" {
		t.Fatalf("copied content = %q", copied)
	}
}

func TestContentViewerCopySelectionThenFullWithYY(t *testing.T) {
	origWrite := clipboardWriteAll
	var copied []string
	clipboardWriteAll = func(text string) error {
		copied = append(copied, text)
		return nil
	}
	defer func() { clipboardWriteAll = origWrite }()

	runCmdTree := func(cmd tea.Cmd) {
		var walk func(any)
		walk = func(v any) {
			if v == nil {
				return
			}
			rv := reflect.ValueOf(v)
			if !rv.IsValid() {
				return
			}
			if rv.Kind() == reflect.Func {
				walk(rv.Call(nil)[0].Interface())
				return
			}
			if rv.Kind() == reflect.Slice {
				for i := 0; i < rv.Len(); i++ {
					walk(rv.Index(i).Interface())
				}
			}
		}
		walk(cmd())
	}

	m := NewModelWithSize(nil, 100, 20)
	m.openContentViewer("Preview", "alpha beta gamma")
	m.layout = m.generateLayout(m.width, m.height)
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
	runCmdTree(cmd)
	if len(copied) != 1 || copied[0] != "alpha" {
		t.Fatalf("copied content history = %#v, want [alpha]", copied)
	}
	if !m.chord.active() || m.chord.op != chordY {
		t.Fatalf("chord state = %#v, want pending y chord", m.chord)
	}

	cmd = m.handleContentViewerKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}))
	if cmd == nil {
		t.Fatal("yy should return clipboard command")
	}
	runCmdTree(cmd)
	if len(copied) != 2 || copied[1] != "alpha beta gamma" {
		t.Fatalf("copied content history = %#v, want [alpha alpha beta gamma]", copied)
	}
}

func TestContentViewerDoubleClickSelectsWord(t *testing.T) {
	m := NewModelWithSize(nil, 100, 20)
	m.openContentViewer("Preview", "alpha beta gamma")
	m.layout = m.generateLayout(m.width, m.height)
	_ = m.renderContentViewer()

	marginX := contentViewerHorizontalMargin(m.viewport.width)
	marginY := contentViewerVerticalMargin(m.viewport.height)
	click := tea.MouseClickMsg{
		X:      m.layout.main.Min.X + marginX + len("alpha ") + 1,
		Y:      m.layout.main.Min.Y + marginY + 2,
		Button: tea.MouseLeft,
	}
	_, handled := m.handleModalMouseMsg(click)
	if !handled {
		t.Fatal("first content viewer click was not handled")
	}
	_, handled = m.handleModalMouseMsg(click)
	if !handled {
		t.Fatal("second content viewer click was not handled")
	}

	if got := m.selectedContentViewerText(); got != "beta" {
		t.Fatalf("selected text = %q, want beta", got)
	}
	if m.contentViewer.selecting {
		t.Fatal("double-click selection should not remain in dragging state")
	}
}

func TestContentViewerTripleClickSelectsLine(t *testing.T) {
	m := NewModelWithSize(nil, 100, 20)
	m.openContentViewer("Preview", "alpha beta gamma")
	m.layout = m.generateLayout(m.width, m.height)
	_ = m.renderContentViewer()

	marginX := contentViewerHorizontalMargin(m.viewport.width)
	marginY := contentViewerVerticalMargin(m.viewport.height)
	click := tea.MouseClickMsg{
		X:      m.layout.main.Min.X + marginX + len("alpha ") + 1,
		Y:      m.layout.main.Min.Y + marginY + 2,
		Button: tea.MouseLeft,
	}
	for i := range 3 {
		_, handled := m.handleModalMouseMsg(click)
		if !handled {
			t.Fatalf("content viewer click %d was not handled", i+1)
		}
	}

	if got := m.selectedContentViewerText(); got != "alpha beta gamma" {
		t.Fatalf("selected text = %q, want full line", got)
	}
	if m.contentViewer.selecting {
		t.Fatal("triple-click selection should not remain in dragging state")
	}
}

func TestContentViewerEscRestoresPreviousMode(t *testing.T) {
	m := NewModelWithSize(nil, 100, 20)
	m.mode = ModeConfirm
	m.confirm.request = &ConfirmRequest{ToolName: "done", DoneReport: "report"}
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
