package tui

import (
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"
)

func TestComposerUndoTypingAndNavigation(t *testing.T) {
	m := NewModel(nil)
	for _, r := range "abc" {
		m.handleInsertKey(tea.KeyPressMsg(tea.Key{Text: string(r), Code: r}))
	}
	m.undoComposerEdit()
	if m.input.Value() != "" {
		t.Fatalf("merged typing undo = %q", m.input.Value())
	}
	for _, r := range "abc" {
		m.handleInsertKey(tea.KeyPressMsg(tea.Key{Text: string(r), Code: r}))
	}
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}))
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Text: "x", Code: 'x'}))
	m.undoComposerEdit()
	if m.input.Value() != "abc" || m.input.Column() != 2 {
		t.Fatalf("navigation undo = %q at %d", m.input.Value(), m.input.Column())
	}
}

func TestComposerUndoRestoresPasteBangAndWrappedCursor(t *testing.T) {
	m := NewModel(nil)
	m.input.SetWidth(8)
	m.input.SetValue("long wrapped first line\nnext")
	m.input.rebuildDisplay(m.input.Value(), len([]rune("long wrapped first line\nne")))
	m.input.SetBangMode(true)
	before := m.input.draftSnapshot()
	m.insertComposerText(strings.Repeat("sample\n", 12))
	withPaste := m.input.ContentParts()
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl}))
	m.undoComposerEdit()
	if !m.input.HasInlinePastes() || len(m.input.ContentParts()) != len(withPaste) || !m.input.BangMode() {
		t.Fatal("clear undo lost paste or bang mode")
	}
	m.undoComposerEdit()
	if m.input.Value() != before.Entry.Display || m.input.Line() != before.Row || m.input.Column() != before.Col {
		t.Fatalf("cursor restore = %q %d:%d, want %d:%d", m.input.Value(), m.input.Line(), m.input.Column(), before.Row, before.Col)
	}
}

func TestComposerUndoRestoresAttachmentAtomically(t *testing.T) {
	m := NewModel(nil)
	m.clipboardAttachmentPending = true
	m.clipboardAttachmentSeq = 1
	m.handleClipboardAttachmentReady(clipboardAttachmentReadyMsg{requestID: 1, data: []byte("sample"), mimeType: "image/png"})
	if len(m.attachments) != 1 || !m.input.HasInlinePastes() {
		t.Fatal("attachment not added")
	}
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	if len(m.attachments) != 0 {
		t.Fatal("attachment not removed")
	}
	m.undoComposerEdit()
	if len(m.attachments) != 1 || !m.input.HasInlinePastes() || string(m.attachments[0].Data) != "sample" {
		t.Fatal("undo restored dangling placeholder")
	}
	m.undoComposerEdit()
	if len(m.attachments) != 0 || m.input.Value() != "" {
		t.Fatal("attachment insertion was not atomic")
	}
}

func TestComposerUndoBoundariesAndBudget(t *testing.T) {
	m := NewModel(nil)
	for range composerUndoEntries + 10 {
		m.insertComposerText("sample")
	}
	if len(m.composerUndo.entries) != composerUndoEntries || m.composerUndo.bytes > composerUndoBytes {
		t.Fatal("undo history not bounded")
	}
	m.input.Reset()
	m.undoComposerEdit()
	if m.input.Value() != "" || len(m.composerUndo.entries) != 0 {
		t.Fatal("undo crossed submit boundary")
	}
	m.insertComposerText("draft")
	m.saveComposerStateForAgent("")
	m.restoreComposerStateForAgent("worker")
	m.undoComposerEdit()
	if m.input.Value() != "" {
		t.Fatal("undo crossed agent boundary")
	}
	m.attachments = []Attachment{{Data: make([]byte, composerUndoBytes)}}
	m.insertComposerText("extra")
	if len(m.composerUndo.entries) != 0 {
		t.Fatal("oversized snapshot retained")
	}
	m.input.Reset()
	m.attachments = nil
	m.insertComposerText("draft")
	m.input.AddHistory("previous")
	m.input.HistoryUp()
	m.undoComposerEdit()
	if m.input.Value() != "previous" {
		t.Fatal("undo crossed history boundary")
	}
	finish := m.beginComposerEdit(false)
	m.beginSessionSwitch("new", "")
	finish()
	if m.composerUndo.depth != 0 || len(m.composerUndo.entries) != 0 {
		t.Fatal("session switch retained an edit transaction")
	}
}

func TestComposerUndoUsesConfiguredBinding(t *testing.T) {
	m := NewModel(nil)
	m.keyMap = KeyMapFromConfig(map[string][]string{"insert_undo": {"alt+u"}})
	m.insertComposerText("draft")
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModAlt}))
	if m.input.Value() != "" {
		t.Fatal("configured undo ignored")
	}
}

func TestComposerUndoReportsOversizedDraft(t *testing.T) {
	m := NewModel(nil)
	m.attachments = []Attachment{{Data: make([]byte, composerUndoBytes)}}
	m.insertComposerText("draft")
	if m.activeToast != nil {
		t.Fatal("editing an oversized draft must not show a toast")
	}
	if cmd := m.undoComposerEdit(); cmd == nil || m.activeToast == nil || !strings.Contains(m.activeToast.Message, "8 MiB") {
		t.Fatal("undo did not explain the draft size limit")
	}
	if m.input.Value() != "draft" || len(m.attachments) != 1 {
		t.Fatal("unavailable undo changed the draft")
	}

	m.activeToast = nil
	m.input.Reset()
	m.attachments = nil
	if cmd := m.undoComposerEdit(); cmd != nil || m.activeToast != nil {
		t.Fatal("undo size warning crossed a composer boundary")
	}
	m.insertComposerText("small")
	m.undoComposerEdit()
	if m.input.Value() != "" || m.activeToast != nil {
		t.Fatal("normal undo did not recover")
	}
}
