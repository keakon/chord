package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/inputhistory"
)

func historyTestModel(t *testing.T) Model {
	t.Helper()
	m := NewModel(&sessionControlAgent{})
	s := inputhistory.New(t.TempDir(), inputhistory.DefaultOptions())
	m.SetInputHistory(s, "project", "sample")
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	})
	return m
}
func waitHistoryReady(t *testing.T, m *Model) {
	t.Helper()
	if m.inputHistory.refreshing {
		// These model-level tests drive the snapshot command explicitly instead of
		// starting Bubble Tea's timer and cursor commands.
		m.inputHistory.refreshing = false
		m.handleInputHistoryMessage(m.requestInputHistorySnapshot(true)())
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for m.inputHistory.loading || m.inputHistory.preparedID == "" {
		select {
		case msg := <-m.inputHistoryRuntime.results:
			m.handleInputHistoryMessage(msg)
		case <-deadline.C:
			t.Fatal("history preparation timed out")
		}
	}
}
func TestInputHistoryStructuredRoundTrip(t *testing.T) {
	entry := inputHistoryEntry{Display: "  [image] ", BangMode: true}
	entry.InlinePastes = []inlineLargePaste{{Kind: inlineTokenImage, Start: 2, End: 9, DisplayText: "[image]"}}
	parts, err := historyTextParts(entry, 1<<20)
	if err != nil || len(parts) != 1 || parts[0].Text != "   " {
		t.Fatal(parts, err)
	}
	raw := "中文 e\u0301 [image 1]\n"
	record := inputhistory.Record{Kind: inputhistory.ShellCommand, TextParts: []inputhistory.TextPart{{Kind: inputhistory.Text, Text: "  [image] "}, {Kind: inputhistory.Paste, Text: raw}, {Kind: inputhistory.Text, Text: " tail "}}}
	restored := historyRestoreEntry(record)
	parts, err = historyTextParts(restored, 1<<20)
	if err != nil || !reflect.DeepEqual(parts, record.TextParts) || !restored.BangMode {
		t.Fatal(parts, restored, err)
	}
	restored.InlinePastes[0].Start++
	if _, err = historyTextParts(restored, 1<<20); err == nil {
		t.Fatal("invalid range accepted")
	}
	if _, err = historyTextParts(inputHistoryEntry{Display: strings.Repeat("x", 100)}, 10); err != inputhistory.ErrTooLarge {
		t.Fatal(err)
	}
}
func TestInputHistorySubmitAndRestoreUndo(t *testing.T) {
	m := historyTestModel(t)
	backend := m.agent.(*sessionControlAgent)
	m.input.SetValue("  sample prompt  ")
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	snap, err := m.inputHistoryStore.Snapshot(context.Background(), false)
	if err != nil || len(snap.Entries) != 1 || snap.Entries[0].Record.TextParts[0].Text != "  sample prompt  " {
		t.Fatal(snap, err)
	}
	m.input.SetValue("draft")
	m.input.SetBangMode(true)
	m.attachments = []Attachment{{FileName: "sample.png", Data: []byte("bytes")}}
	m.editingQueuedDraftID = "queued-1"
	before := m.composerEditSnapshot()
	sent := len(backend.sentMessages)
	m.openInputHistory(true)
	waitHistoryReady(t, &m)
	m.applyInputHistorySelection()
	if m.input.Value() != "  sample prompt  " || m.input.BangMode() || len(m.attachments) != 0 || m.editingQueuedDraftID != "queued-1" || len(backend.sentMessages) != sent {
		t.Fatal("restore mutated send, mode, or queue identity")
	}
	m.undoComposerEdit()
	if !sameComposerContent(before, m.composerEditSnapshot()) {
		t.Fatal("undo did not restore composer")
	}
}
func TestInputHistoryCancelOwnerAndStaleResult(t *testing.T) {
	m := historyTestModel(t)
	m.input.SetValue("draft")
	m.openInputHistory(false)
	token := m.inputHistory.token
	m.closeInputHistory()
	if m.input.Value() != "draft" {
		t.Fatal("cancel changed draft")
	}
	m.openInputHistory(true)
	m.handleInputHistoryMessage(inputHistoryMatchMsg{token: token, result: inputhistory.SearchResult{Hits: []inputhistory.Hit{{ID: "stale"}}}})
	if len(m.inputHistory.results.Hits) != 0 {
		t.Fatal("accepted stale result")
	}
	m.switchModeWithIME(ModeConfirm)
	if m.mode != ModeConfirm || m.inputHistory.token.generation == token.generation {
		t.Fatal("search was not invalidated")
	}
}
func TestInputHistoryCommandsAndShortcuts(t *testing.T) {
	m := historyTestModel(t)
	m.input.SetValue("draft")
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
	if m.mode != ModeInputHistorySearch || m.input.Value() != "draft" {
		t.Fatal("Ctrl+R did not open history")
	}
	m.handleCtrlC()
	if m.mode != ModeInsert || m.quitting {
		t.Fatal("Ctrl+C did not close history")
	}
	m.input.SetValue(historyCommand)
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if m.mode != ModeInputHistorySearch || m.input.Value() != "" || m.inputHistory.query.Value() != "" {
		t.Fatal("history command did not open empty query")
	}
	m.closeInputHistory()
	m.input.SetValue(historyClearCommand)
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if m.mode != ModeInputHistoryClearConfirm || m.inputHistory.confirm {
		t.Fatal("clear must default to cancel")
	}
	m.handleInputHistoryClearKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	snap, _ := m.inputHistoryStore.Snapshot(context.Background(), false)
	if len(snap.Entries) != 2 {
		t.Fatal("cancel removed records", snap)
	}
}
func TestInputHistoryRejectedAttachmentAndQueueIdentity(t *testing.T) {
	m := historyTestModel(t)
	m.input.SetValue("draft")
	m.editingQueuedDraftID = "queued"
	m.attachments = []Attachment{{FileName: "missing.png", ImagePath: "/nonexistent/input-history-test.png"}}
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	snap, _ := m.inputHistoryStore.Snapshot(context.Background(), false)
	if len(snap.Entries) != 0 || m.input.Value() != "draft" || m.editingQueuedDraftID != "queued" {
		t.Fatal("rejected input was accepted or changed")
	}
}
func TestInputHistoryInitialQueryBounded(t *testing.T) {
	entry := historyRestoreEntry(inputhistory.Record{TextParts: []inputhistory.TextPart{{Kind: inputhistory.Paste, Text: strings.Repeat("a", 1<<20)}}})
	if q, limited := historyInitialQuery(entry); len(q) != 256 || !limited {
		t.Fatal(len(q))
	}
}
func BenchmarkInputHistoryRestore(b *testing.B) {
	r := inputhistory.Record{TextParts: []inputhistory.TextPart{{Kind: inputhistory.Text, Text: strings.Repeat("a", 4<<20)}}}
	b.ReportAllocs()
	for b.Loop() {
		_ = historyRestoreEntry(r)
	}
}
func BenchmarkInputHistoryOverlay(b *testing.B) {
	m := NewModelWithSize(nil, 120, 40)
	m.mode = ModeInputHistorySearch
	m.inputHistory.entriesByID = make(map[string]inputhistory.Entry, 10000)
	for i := range 10000 {
		id := fmt.Sprint(i)
		entry := inputhistory.Entry{Record: inputhistory.Record{ID: id, ProjectKey: "project", Kind: inputhistory.Prompt, TextParts: []inputhistory.TextPart{{Kind: inputhistory.Text, Text: "sample prompt"}}}, State: inputhistory.Synced}
		m.inputHistory.snapshot.Entries = append(m.inputHistory.snapshot.Entries, entry)
		m.inputHistory.entriesByID[id] = entry
		if i >= 9900 {
			m.inputHistory.results.Hits = append(m.inputHistory.results.Hits, inputhistory.Hit{ID: id, Summary: "sample prompt"})
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = m.renderInputHistory()
	}
}

func TestInputHistoryProcessRestore(t *testing.T) {
	if dir := os.Getenv("CHORD_TUI_HISTORY_HELPER"); dir != "" {
		m := NewModel(nil)
		m.SetInputHistory(inputhistory.New(dir, inputhistory.DefaultOptions()), "project", "sample")
		defer m.Close()
		m.openInputHistory(true)
		waitHistoryReady(t, &m)
		m.applyInputHistorySelection()
		if m.input.Value() != "original input" || m.mode != ModeInsert {
			t.Fatal("independent process restore failed")
		}
		return
	}
	dir := t.TempDir()
	m := NewModel(&sessionControlAgent{})
	m.SetInputHistory(inputhistory.New(dir, inputhistory.DefaultOptions()), "project", "sample")
	m.activities["main"] = agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityCompacting}
	m.input.SetValue("original input")
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if len(m.queuedDrafts) != 1 || len(m.agent.(*sessionControlAgent).sentMessages) != 0 {
		t.Fatal("input bypassed waiting queue")
	}
	copy := m
	if err := copy.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestInputHistoryProcessRestore$")
	cmd.Env = append(os.Environ(), "CHORD_TUI_HISTORY_HELPER="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v %s", err, output)
	}
}
func TestInputHistoryQueryChangeBlocksRestore(t *testing.T) {
	m := historyTestModel(t)
	m.input.SetValue("sample")
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m.input.SetValue("draft")
	m.openInputHistory(true)
	waitHistoryReady(t, &m)
	m.pasteInputHistoryQuery("missing")
	m.applyInputHistorySelection()
	if m.input.Value() != "draft" || m.mode != ModeInputHistorySearch {
		t.Fatal("stale query restored input")
	}
}
func TestInputHistoryOversizedUndo(t *testing.T) {
	m := historyTestModel(t)
	m.input.SetValue("sample")
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m.attachments = []Attachment{{Data: make([]byte, composerUndoBytes+1)}}
	m.openInputHistory(true)
	waitHistoryReady(t, &m)
	m.applyInputHistorySelection()
	if len(m.attachments) != 1 || m.mode != ModeInputHistorySearch {
		t.Fatal("oversized draft replaced")
	}
}
func TestInputHistorySearchWorkerLatest(t *testing.T) {
	r := newHistorySearchRuntime()
	defer r.close()
	entered, done := make(chan struct{}), make(chan struct{})
	r.submit(historySearchTask{priority: 0, run: func(ctx context.Context) tea.Msg { close(entered); <-ctx.Done(); close(done); return "obsolete" }})
	<-entered
	r.submit(historySearchTask{priority: 0, run: func(context.Context) tea.Msg { return "latest" }})
	<-done
	select {
	case msg := <-r.results:
		if msg != "latest" {
			t.Fatal(msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("latest task did not run")
	}
}

func TestInputHistoryOpeningPreservesSelection(t *testing.T) {
	m := historyTestModel(t)
	m.input.SetValue("sample draft")
	m.input.SelectRuneRange(0, 6)
	selection, version := m.input.SelectionState(), m.input.interactionVersion
	m.openInputHistory(false)
	if m.input.interactionVersion != version || m.input.SelectionState() != selection {
		t.Fatal("opening history changed the composer")
	}
	m.closeInputHistory()
	if m.input.SelectionState() != selection {
		t.Fatal("cancel changed selection")
	}
}
