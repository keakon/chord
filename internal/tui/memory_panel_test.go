package tui

import (
	"context"
	"reflect"
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/memory"
)

type memoryPanelTestAgent struct {
	*sessionControlAgent
	view      *agent.MemoryView
	applied   int
	organized int
}

func (a *memoryPanelTestAgent) ReviewMemory(context.Context) (*agent.MemoryView, error) {
	return a.view, nil
}
func (a *memoryPanelTestAgent) OrganizeMemory(_ context.Context, base *memory.ReviewSnapshot, all bool, _ string) (*memory.ManualDraft, error) {
	a.organized++
	d := memory.NewRemovalDraft(base)
	d.All = all
	return d, nil
}
func (a *memoryPanelTestAgent) ApplyMemory(context.Context, *memory.ManualDraft) error {
	a.applied++
	return nil
}
func (a *memoryPanelTestAgent) UndoMemory(context.Context) error { return nil }

func memoryPanelTestModel() (Model, *memoryPanelTestAgent) {
	first := memory.ReviewItem{Entry: memory.ManagedEntry{ID: "reports--1111111111111111", Summary: "Report style"}, Record: &memory.Record{Type: memory.TypePreference}, Content: "Keep socket conditions in the report.", Path: "record-one.md"}
	second := memory.ReviewItem{Entry: memory.ManagedEntry{ID: "config--2222222222222222", Summary: "Config"}, Record: &memory.Record{Type: memory.TypeFact}, Content: "A different configuration fact.", Path: "record-two.md"}
	idx := &memory.MemoryIndex{Managed: []memory.ManagedEntry{first.Entry, second.Entry}}
	view := &agent.MemoryView{Snapshot: &memory.ReviewSnapshot{ProjectRoot: "sample-project", Index: idx, Items: []memory.ReviewItem{first, second}}, Applied: "Actual older summary", Pending: true}
	a := &memoryPanelTestAgent{sessionControlAgent: &sessionControlAgent{}, view: view}
	m := NewModelWithSize(nil, 100, 25)
	m.agent = a
	m.mode = ModeMemoryPanel
	m.memoryPanel = memoryPanelState{view: view, prevMode: ModeNormal, selected: map[string]bool{}, epoch: m.sessionTranscriptEpoch}
	m.rebuildMemoryList()
	return m, a
}

func TestMemoryPanelSearchBodyAndSelectionSafety(t *testing.T) {
	m, _ := memoryPanelTestModel()
	m.handleMemoryPanelKey(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace}))
	if !m.memoryPanel.selected["reports--1111111111111111"] {
		t.Fatal("space did not select record")
	}
	m.handleMemoryPanelKey(sessionSelectRuneKey("/"))
	m.handleMemoryPanelKey(sessionSelectRuneKey("SOCKET report"))
	if m.memoryPanel.list.Len() != 1 {
		t.Fatal("body search did not find record")
	}
	if len(m.memoryPanel.selected) != 0 {
		t.Fatal("filter kept a hidden selection")
	}
	m.handleMemoryPanelKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if m.memoryPanel.inputFocused || m.memoryPanel.detail {
		t.Fatal("first Enter should leave filter without opening detail")
	}
	m.handleMemoryPanelKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if !m.memoryPanel.detail || m.contentViewer.content != m.memoryPanel.view.Snapshot.Items[0].Content {
		t.Fatal("detail did not show complete record")
	}
	m.handleMemoryPanelKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if m.memoryPanel.detail || m.memoryPanel.query != "SOCKET report" {
		t.Fatal("return did not preserve search")
	}
	full, err := m.memorySelectedSnapshot(true)
	if err != nil || len(full.Items) != 2 {
		t.Fatalf("full organization obeyed filter: %v", err)
	}
}

func TestMemoryPanelCopyCurrentAndMultiple(t *testing.T) {
	m, _ := memoryPanelTestModel()
	old := clipboardWriteAll
	t.Cleanup(func() { clipboardWriteAll = old })
	var copied string
	clipboardWriteAll = func(s string) error { copied = s; return nil }
	cmd := m.handleMemoryPanelKey(sessionSelectRuneKey("y"))
	if cmd == nil {
		t.Fatal("y returned no copy command")
	}
	reflect.ValueOf(cmd()).Index(1).Call(nil)
	if copied != m.memoryPanel.view.Snapshot.Items[0].Content {
		t.Fatalf("copied preview rather than record: %q", copied)
	}
	m.memoryPanel.selected = map[string]bool{"reports--1111111111111111": true, "config--2222222222222222": true}
	cmd = m.handleMemoryPanelKey(sessionSelectRuneKey("y"))
	reflect.ValueOf(cmd()).Index(1).Call(nil)
	if !strings.Contains(copied, "socket conditions") || !strings.Contains(copied, "different configuration") {
		t.Fatal("did not copy selected records")
	}
	if m.mode != ModeMemoryPanel {
		t.Fatal("copy closed panel")
	}
}

func TestMemoryPanelMutationNeedsExplicitPreviewApproval(t *testing.T) {
	m, a := memoryPanelTestModel()
	m.handleMemoryPanelKey(sessionSelectRuneKey("d"))
	if !m.memoryPanel.detail || m.memoryPanel.preview == nil || a.applied != 0 {
		t.Fatal("remove should only open preview")
	}
	m.handleMemoryPanelKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if m.memoryPanel.preview != nil || a.applied != 0 {
		t.Fatal("cancel applied removal")
	}
	m.handleMemoryPanelKey(sessionSelectRuneKey("d"))
	cmd := m.handleMemoryPanelKey(sessionSelectRuneKey("a"))
	if cmd == nil {
		t.Fatal("apply returned no async command")
	}
	msg := cmd().(memoryPanelResultMsg)
	if a.applied != 1 || !msg.changed {
		t.Fatal("explicit apply failed")
	}
}

func TestNormalEnterWithoutCardActionDoesNotOpenMemory(t *testing.T) {
	m, a := memoryPanelTestModel()
	m.mode = ModeNormal
	m.viewport.ReplaceBlocks(nil)
	if hint := m.nextEnterHint(); hint != "" {
		t.Fatalf("next enter hint = %q, want none without a card action", hint)
	}
	m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if m.mode != ModeNormal {
		t.Fatal("Enter without a card action opened the memory panel")
	}
	if len(a.sentMessages) != 0 {
		t.Fatal("Enter created a user turn")
	}
}

func TestMemoryPanelStaleResultDoesNotReopenClosedPanel(t *testing.T) {
	m, a := memoryPanelTestModel()
	m.mode = ModeNormal
	if cmd := m.openMemoryPanel(nil); cmd == nil {
		t.Fatal("openMemoryPanel returned no command")
	}
	seq := m.memoryPanel.seq
	m.closeMemoryPanel()
	m.handleMemoryPanelResult(memoryPanelResultMsg{seq: seq, epoch: m.sessionTranscriptEpoch, view: a.view})
	if m.mode != ModeNormal || m.memoryPanel.view != nil {
		t.Fatal("late read result reopened closed panel")
	}
	m.mode = ModeNormal
	m.focusedBlockID = -1
	m.viewport.AppendBlock(&Block{ID: 92, Type: BlockAssistant, Content: "Streaming reply", Streaming: true})
	m.focusedBlockID = 92
	m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if m.mode != ModeNormal {
		t.Fatal("Enter opened memory for an unfinished action")
	}
}

func TestMemoryPanelEmptySearchDoesNotInvokeMainEnterFallback(t *testing.T) {
	m, _ := memoryPanelTestModel()
	m.memoryPanel.query = "no matching entry"
	m.rebuildMemoryList()
	m.handleMemoryPanelKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if m.mode != ModeMemoryPanel || m.memoryPanel.detail {
		t.Fatal("empty result invoked another Enter action")
	}
	m.memoryPanel.tab = 1
	m.memoryPanel.query = ""
	m.rebuildMemoryList()
	m.handleMemoryPanelKey(sessionSelectRuneKey("d"))
	if m.memoryPanel.preview != nil {
		t.Fatal("session snapshot is writable")
	}
}

func TestMemoryPanelKeepsResultsAndDetailAcrossDialog(t *testing.T) {
	m, a := memoryPanelTestModel()
	m.showMemoryDetail("Record", strings.Repeat("A line of memory text.\n", 80))
	m.renderMemoryPanel()
	m.handleMemoryPanelKey(sessionSelectRuneKey("G"))
	if m.memoryPanel.viewer.scrollOffset == 0 {
		t.Fatal("detail did not scroll")
	}
	savedOffset := m.memoryPanel.viewer.scrollOffset
	m.mode = ModeConfirm
	m.confirm.prevMode = ModeMemoryPanel
	m.contentViewer = contentViewerState{title: "Arguments", content: "other view"}
	m.memoryPanel.loading = true
	m.handleMemoryPanelResult(memoryPanelResultMsg{seq: m.memoryPanel.seq, epoch: m.sessionTranscriptEpoch, view: a.view})
	if m.memoryPanel.pendingResult == nil {
		t.Fatal("dialog dropped result")
	}
	m.switchModeWithIME(ModeMemoryPanel)
	if m.memoryPanel.loading || m.memoryPanel.pendingResult != nil {
		t.Fatal("resume did not consume result")
	}
	m.renderMemoryPanel()
	if m.contentViewer.title != "Record" || m.memoryPanel.viewer.scrollOffset != savedOffset {
		t.Fatal("dialog corrupted memory detail")
	}
}

func TestMemoryPanelPasteAndSessionSwitch(t *testing.T) {
	m, _ := memoryPanelTestModel()
	m.memoryPanel.inputFocused = true
	m.memoryPanel.selected["reports--1111111111111111"] = true
	m.handleNonKeyInputMsg(tea.PasteMsg{Content: "socket\nconditions"})
	if m.memoryPanel.query != "socket conditions" || m.memoryPanel.list.Len() != 1 || len(m.memoryPanel.selected) != 0 {
		t.Fatal("paste did not filter safely")
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.memoryPanel.cancel = cancel
	m.beginSessionSwitch("new", "")
	if ctx.Err() == nil || m.mode == ModeMemoryPanel || m.memoryPanel.view != nil {
		t.Fatal("switch left old panel active")
	}
}

func TestMemoryPanelDetailCopyAndRowClick(t *testing.T) {
	m, _ := memoryPanelTestModel()
	old := clipboardWriteAll
	t.Cleanup(func() { clipboardWriteAll = old })
	var copied string
	clipboardWriteAll = func(s string) error { copied = s; return nil }

	m.handleMemoryPanelKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if !m.memoryPanel.detail {
		t.Fatal("enter did not open the detail")
	}
	cmd := m.handleMemoryPanelKey(sessionSelectRuneKey("y"))
	if cmd == nil {
		t.Fatal("detail y returned no copy command")
	}
	reflect.ValueOf(cmd()).Index(1).Call(nil)
	if copied != m.memoryPanel.view.Snapshot.Items[0].Content {
		t.Fatalf("detail copy = %q", copied)
	}
	m.handleMemoryPanelKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if m.memoryPanel.detail {
		t.Fatal("escape did not return to the list")
	}

	box := m.renderMemoryPanel()
	rect := m.overlayRect(box)
	row := -1
	for i, line := range strings.Split(box, "\n") {
		if strings.Contains(line, "Config") {
			row = i
			break
		}
	}
	if row < 0 {
		t.Fatal("second record row not found in the rendered panel")
	}
	x, y := rect.Min.X+3, rect.Min.Y+row
	m.handleModalMouseMsg(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	if got := m.memoryPanel.list.CursorAt(); got != 1 {
		t.Fatalf("click selected row %d, want 1", got)
	}
	if m.memoryPanel.detail {
		t.Fatal("first click opened the detail")
	}
	m.handleModalMouseMsg(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	if !m.memoryPanel.detail || m.contentViewer.content != m.memoryPanel.view.Snapshot.Items[1].Content {
		t.Fatal("second click did not open the clicked record")
	}
}

func TestMemoryPanelFeedbackAndTabState(t *testing.T) {
	m, _ := memoryPanelTestModel()
	old := clipboardWriteAll
	t.Cleanup(func() { clipboardWriteAll = old })
	calls := 0
	clipboardWriteAll = func(string) error { calls++; return nil }

	// Copying a path outside the project view reports the miss instead of doing nothing.
	m.memoryPanel.tab = 1
	m.rebuildMemoryList()
	m.activeToast = nil
	m.handleMemoryPanelKey(sessionSelectRuneKey("p"))
	if calls != 0 || m.activeToast == nil || !strings.Contains(m.activeToast.Message, "No file path") {
		t.Fatalf("pathless copy: calls=%d toast=%v", calls, m.activeToast)
	}

	// Mutations stay in the project view and say why.
	m.activeToast = nil
	m.handleMemoryPanelKey(sessionSelectRuneKey("d"))
	if m.memoryPanel.preview != nil || m.activeToast == nil || !strings.Contains(m.activeToast.Message, "Switch to project memories") {
		t.Fatalf("remove outside project view: preview=%v toast=%v", m.memoryPanel.preview, m.activeToast)
	}
	m.activeToast = nil
	m.memoryPanel.tab = 0
	m.rebuildMemoryList()
	m.memoryPanel.view.Snapshot.CanUndo = false
	m.handleMemoryPanelKey(sessionSelectRuneKey("u"))
	if m.activeToast == nil || m.activeToast.Message != "Nothing to undo" {
		t.Fatalf("undo feedback: %v", m.activeToast)
	}

	// Tab keeps the multi-selection; editing the filter still clears it.
	m.handleMemoryPanelKey(sessionSelectRuneKey("/"))
	m.handleMemoryPanelKey(sessionSelectRuneKey("socket"))
	m.handleMemoryPanelKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m.handleMemoryPanelKey(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace}))
	if len(m.memoryPanel.selected) != 1 {
		t.Fatalf("selection after space = %v", m.memoryPanel.selected)
	}
	m.handleMemoryPanelKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	m.handleMemoryPanelKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	m.handleMemoryPanelKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	if m.memoryPanel.tab != 0 || len(m.memoryPanel.selected) != 1 {
		t.Fatalf("tab switch dropped selection: tab=%d selected=%v", m.memoryPanel.tab, m.memoryPanel.selected)
	}
	if m.memoryPanel.query != "" {
		t.Fatalf("tab switch kept query %q", m.memoryPanel.query)
	}
	m.handleMemoryPanelKey(sessionSelectRuneKey("/"))
	m.handleMemoryPanelKey(sessionSelectRuneKey("x"))
	if len(m.memoryPanel.selected) != 0 {
		t.Fatal("filter edit kept a hidden selection")
	}
}

func TestMemoryPanelHidesUnavailableHints(t *testing.T) {
	plainHint := func(m *Model) string { return ansi.Strip(m.memoryPanelOverlayConfig().Hint) }

	m, _ := memoryPanelTestModel()
	m.memoryPanel.view.Snapshot.CanUndo = true
	hint := plainHint(&m)
	for _, want := range []string{"[/]", "[j/k]", "[Enter]", "[y]", "[p]", "[Space]", "[d]", "[o]", "[O]", "[u]", "[Tab]", "[Esc]"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("project view hint missing %s: %q", want, hint)
		}
	}

	// A view without file paths does not advertise path copying.
	m.memoryPanel.tab = 1
	m.rebuildMemoryList()
	if hint = plainHint(&m); strings.Contains(hint, "[p]") || strings.Contains(hint, "path") {
		t.Fatalf("pathless view still advertises p: %q", hint)
	}

	// The read-only user-notes row hides record actions, and nothing to undo hides u.
	m.memoryPanel.tab = 0
	m.memoryPanel.view.Snapshot.CanUndo = false
	m.memoryPanel.view.Snapshot.Index.Head = "- keep the sample project readable"
	m.rebuildMemoryList()
	m.memoryPanel.list.SetCursor(m.memoryPanel.list.Len() - 1)
	hint = plainHint(&m)
	for _, hidden := range []string{"[Space]", "[d]", "[o]", "[O]", "[u]", "[p]"} {
		if strings.Contains(hint, hidden) {
			t.Fatalf("read-only row advertises %s: %q", hidden, hint)
		}
	}
	if !strings.Contains(hint, "[r]") {
		t.Fatalf("refresh disappeared: %q", hint)
	}

	// An empty result set keeps only view-level keys.
	m.memoryPanel.query = "zzz"
	m.rebuildMemoryList()
	hint = plainHint(&m)
	for _, hidden := range []string{"[Enter]", "[y]", "[p]", "[Space]", "[d]", "[o]", "[u]"} {
		if strings.Contains(hint, hidden) {
			t.Fatalf("empty result advertises %s: %q", hidden, hint)
		}
	}
	for _, want := range []string{"[/]", "[Tab]", "[r]", "[Esc]"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("empty result lost %s: %q", want, hint)
		}
	}

	// A detail view only offers path copying when the entry has a path.
	m.memoryPanel.tab = 1
	m.memoryPanel.query = ""
	m.rebuildMemoryList()
	m.handleMemoryPanelKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if hint = plainHint(&m); strings.Contains(hint, "[p]") {
		t.Fatalf("pathless detail advertises p: %q", hint)
	}

	// A change preview without changes hides apply.
	m2, _ := memoryPanelTestModel()
	empty := memory.NewRemovalDraft(&memory.ReviewSnapshot{})
	m2.memoryPanel.preview = empty
	m2.showMemoryDetail("Review", empty.Preview())
	if hint = plainHint(&m2); strings.Contains(hint, "[a]") {
		t.Fatalf("empty preview advertises apply: %q", hint)
	}
}
