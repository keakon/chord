package tui

import (
	"context"
	"reflect"
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

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
	m.handleMemoryPanelKey(sessionSelectRuneKey("y"))
	cmd := m.handleMemoryPanelKey(sessionSelectRuneKey("y"))
	if cmd == nil {
		t.Fatal("yy returned no copy command")
	}
	reflect.ValueOf(cmd()).Index(1).Call(nil)
	if copied != m.memoryPanel.view.Snapshot.Items[0].Content {
		t.Fatalf("copied preview rather than record: %q", copied)
	}
	m.memoryPanel.selected = map[string]bool{"reports--1111111111111111": true, "config--2222222222222222": true}
	m.handleMemoryPanelKey(sessionSelectRuneKey("y"))
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
