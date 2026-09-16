package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
)

func TestStatusSessionDoubleClickCopiesWholeSessionID(t *testing.T) {
	backend := &sessionControlAgent{sessionSummary: &agent.SessionSummary{ID: "1775115074902"}}
	m := NewModelWithSize(backend, 160, 40)
	m.workingDir = "/home/user/projects/myapp"
	m.layout = m.generateLayout(m.width, m.height)
	_ = m.renderStatusBar()
	if m.statusSession.display == "" {
		t.Fatal("expected status session id to render")
	}
	clickX := m.statusSession.startX + 2
	if displayWidth := ansi.StringWidth(m.statusSession.display); displayWidth > 0 {
		clickX = m.statusSession.startX + min(2, displayWidth-1)
	}
	clickY := m.layout.status.Min.Y

	updated, cmd := m.Update(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	model := updated.(*Model)
	if cmd != nil {
		t.Fatal("first click should not copy session id")
	}

	updated, cmd = model.Update(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	_ = updated.(*Model)
	if cmd == nil {
		t.Fatal("double click should trigger session id copy")
	}
}

func TestStatusPathDoubleClickCopiesWholePath(t *testing.T) {
	m := NewModelWithSize(nil, 160, 40)
	m.workingDir = "/home/user/projects/myapp"
	m.layout = m.generateLayout(m.width, m.height)
	_ = m.renderStatusBar()
	if m.statusPath.display == "" {
		t.Fatal("expected status path to render")
	}
	clickX := m.statusPath.startX + 2
	if displayWidth := ansi.StringWidth(m.statusPath.display); displayWidth > 0 {
		clickX = m.statusPath.startX + min(2, displayWidth-1)
	}
	clickY := m.layout.status.Min.Y

	updated, cmd := m.Update(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	model := updated.(*Model)
	if cmd != nil {
		t.Fatal("first click should not copy path")
	}

	updated, cmd = model.Update(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	_ = updated.(*Model)
	if cmd == nil {
		t.Fatal("double click should trigger path copy")
	}
}

func TestStatusPathDoubleClickSelectsWholePath(t *testing.T) {
	m := NewModelWithSize(nil, 160, 40)
	m.workingDir = "/home/user/projects/myapp"
	m.layout = m.generateLayout(m.width, m.height)
	_ = m.renderStatusBar()
	if m.statusPath.display == "" {
		t.Fatal("expected status path to render")
	}
	clickX := m.statusPath.startX + 2
	if displayWidth := ansi.StringWidth(m.statusPath.display); displayWidth > 0 {
		clickX = m.statusPath.startX + min(2, displayWidth-1)
	}
	clickY := m.layout.status.Min.Y

	updated, cmd := m.Update(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	model := updated.(*Model)
	if cmd != nil {
		t.Fatal("first click should not trigger path copy")
	}

	updated, cmd = model.Update(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	_ = updated.(*Model)
	if cmd == nil {
		t.Fatal("double click should trigger path copy")
	}
}

func TestNormalYStartsChordWithoutCopyingStatusPath(t *testing.T) {
	m := NewModel(nil)
	m.statusPath.value = "/home/user/projects/myapp"
	m.statusPath.display = "~/projects/myapp"

	cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}))
	if cmd == nil {
		t.Fatal("y should start a pending chord")
	}
	if m.chord.op != chordY {
		t.Fatalf("chord op = %v, want chordY", m.chord.op)
	}
	if m.chord.count != 0 {
		t.Fatalf("chord count = %d, want 0", m.chord.count)
	}
}

func TestWriteStatusSessionClipboardCmdUsesSessionMessage(t *testing.T) {
	cmd := writeStatusSessionClipboardCmd("1775115074902")
	if cmd == nil {
		t.Fatal("expected clipboard command for non-empty session id")
	}
	msg := cmd()
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Len() != 2 {
		t.Fatalf("clipboard command msg = %T, want 2-command sequence", msg)
	}
	second := v.Index(1).Call(nil)[0].Interface().(clipboardWriteResultMsg)
	if second.success != "Session ID copied to clipboard" {
		t.Fatalf("clipboard success = %q, want %q", second.success, "Session ID copied to clipboard")
	}
}

func TestMouseWheelScrollTriggersInlineImageRefresh(t *testing.T) {
	ApplyTheme(DefaultTheme())
	caps := TerminalImageCapabilities{Backend: ImageBackendITerm2, SupportsInline: true, SupportsFullscreen: true}
	setCurrentTerminalImageCapabilities(caps)
	t.Cleanup(func() {
		setCurrentTerminalImageCapabilities(TerminalImageCapabilities{Backend: ImageBackendNone})
	})

	m := NewModelWithSize(nil, 80, 12)
	m.imageCaps = caps
	m.mode = ModeNormal

	m.viewport.AppendBlock(&Block{
		ID:         1,
		Type:       BlockUser,
		ImageCount: 1,
		ImageParts: []BlockImagePart{{
			FileName: "sample.png",
			MimeType: "image/png",
			Data:     makeTestPNG(t),
		}},
	})
	for i := range 3 {
		m.viewport.AppendBlock(&Block{ID: 2 + i, Type: BlockAssistant, Content: strings.Repeat("alpha ", 40)})
	}

	m.layout = m.generateLayout(m.width, m.height)
	m.viewport.ScrollToTop()
	_ = m.viewport.Render("", nil, -1, -1, "")
	m.viewport.ScrollDown(1)
	if m.viewport.offset == 0 {
		t.Fatal("expected setup to produce a non-zero scroll offset")
	}

	updated, cmd := m.Update(tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelUp})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd == nil {
		t.Fatal("mouse wheel scroll should schedule a scroll flush command")
	}
	if model.viewport.offset != 1 {
		t.Fatalf("mouse wheel event should defer offset change until flush, got %d", model.viewport.offset)
	}
	if model.pendingScrollDelta != -mouseWheelScrollStep {
		t.Fatalf("pendingScrollDelta = %d, want %d", model.pendingScrollDelta, -mouseWheelScrollStep)
	}
	cmd = model.consumeScrollFlush(scrollFlushTickMsg{generation: model.scrollFlushGeneration})
	if cmd == nil {
		t.Fatal("scroll flush with inline images should trigger image refresh command")
	}
	if model.viewport.offset >= 1 {
		t.Fatalf("expected scroll flush to decrease offset, got %d", model.viewport.offset)
	}
}

func TestMouseWheelScrollMovesViewportWhileCompacting(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	m.mode = ModeInsert
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityCompacting, AgentID: "main"}
	for i := range 6 {
		m.viewport.AppendBlock(&Block{ID: i + 1, Type: BlockAssistant, Content: strings.Repeat("alpha ", 40)})
	}
	m.layout = m.generateLayout(m.width, m.height)
	m.viewport.ScrollDown(2)
	startOffset := m.viewport.offset
	if startOffset == 0 {
		t.Fatal("expected setup to produce a non-zero scroll offset")
	}

	updated, cmd := m.Update(tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelUp})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd == nil {
		t.Fatal("mouse wheel during compacting should still schedule a scroll flush command")
	}
	if model.pendingScrollDelta != -mouseWheelScrollStep {
		t.Fatalf("pendingScrollDelta = %d, want %d", model.pendingScrollDelta, -mouseWheelScrollStep)
	}
	model.consumeScrollFlush(scrollFlushTickMsg{generation: model.scrollFlushGeneration})
	if model.viewport.offset >= startOffset {
		t.Fatalf("expected compacting scroll flush to decrease offset, got start=%d end=%d", startOffset, model.viewport.offset)
	}
}

func TestMouseWheelScrollMovesViewportWhileConfirmDialogOpen(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	m.mode = ModeConfirm
	m.confirm.request = &ConfirmRequest{ToolName: "done", ArgsJSON: `{}`}
	for i := range 6 {
		m.viewport.AppendBlock(&Block{ID: i + 1, Type: BlockAssistant, Content: strings.Repeat("alpha ", 40)})
	}
	m.layout = m.generateLayout(m.width, m.height)
	m.viewport.ScrollDown(2)
	startOffset := m.viewport.offset
	if startOffset == 0 {
		t.Fatal("expected setup to produce a non-zero scroll offset")
	}

	updated, cmd := m.Update(tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelUp})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd == nil {
		t.Fatal("mouse wheel during confirm dialog should still schedule a scroll flush command")
	}
	if model.pendingScrollDelta != -mouseWheelScrollStep {
		t.Fatalf("pendingScrollDelta = %d, want %d", model.pendingScrollDelta, -mouseWheelScrollStep)
	}
	model.consumeScrollFlush(scrollFlushTickMsg{generation: model.scrollFlushGeneration})
	if model.viewport.offset >= startOffset {
		t.Fatalf("expected confirm-dialog scroll flush to decrease offset, got start=%d end=%d", startOffset, model.viewport.offset)
	}
}

func TestConfirmDialogArrowKeysScrollViewport(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	m.mode = ModeConfirm
	m.confirm.request = &ConfirmRequest{ToolName: "done", ArgsJSON: `{}`}
	for i := range 6 {
		m.viewport.AppendBlock(&Block{ID: i + 1, Type: BlockAssistant, Content: strings.Repeat("alpha ", 40)})
	}
	m.layout = m.generateLayout(m.width, m.height)
	m.viewport.ScrollDown(2)
	startOffset := m.viewport.offset
	if startOffset == 0 {
		t.Fatal("expected setup to produce a non-zero scroll offset")
	}

	cmd := m.handleKeyMsg(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	if cmd != nil {
		t.Fatalf("confirm-dialog arrow scroll should not schedule extra cmd, got %T", cmd)
	}
	if m.viewport.offset >= startOffset {
		t.Fatalf("expected confirm-dialog key scroll to decrease offset, got start=%d end=%d", startOffset, m.viewport.offset)
	}
}

func TestHandleStatusCopyClickHitsSessionBeforePathWhenRegionsOverlap(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.layout = m.generateLayout(m.width, m.height)
	m.statusSession = statusBarCopyRegionState{value: "session-value", display: "session", startX: 10, endX: 20}
	m.statusPath = statusBarCopyRegionState{value: "/tmp/path", display: "path", startX: 10, endX: 20}
	x, y := 12, m.layout.status.Min.Y

	cmd, handled := m.handleStatusCopyClick(x, y)
	if !handled || cmd != nil {
		t.Fatalf("first click handled/cmd = %v/%#v, want handled no command", handled, cmd)
	}
	cmd, handled = m.handleStatusCopyClick(x, y)
	if !handled || cmd == nil {
		t.Fatalf("second click handled/cmd = %v/%#v, want copy command", handled, cmd)
	}
	msg := cmd()
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Len() != 2 {
		t.Fatalf("copy command msg = %T, want 2-command sequence", msg)
	}
	second := v.Index(1).Call(nil)[0].Interface().(clipboardWriteResultMsg)
	if second.success != "Session ID copied to clipboard" {
		t.Fatalf("clipboard success = %q, want session copy", second.success)
	}
}

func TestHandleStatusCopyClickIgnoresNonStatusPoint(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.layout = m.generateLayout(m.width, m.height)
	m.statusPath = statusBarCopyRegionState{value: "/tmp/path", display: "path", startX: 10, endX: 20}
	cmd, handled := m.handleStatusCopyClick(0, m.layout.status.Min.Y)
	if handled || cmd != nil {
		t.Fatalf("non-status click handled/cmd = %v/%#v, want false/nil", handled, cmd)
	}
}
