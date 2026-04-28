package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/convformat"
)

func TestInputHistoryRestoresBangLargePasteEntry(t *testing.T) {
	in := NewInput()
	in.SetBangMode(true)
	raw := strings.Join([]string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}, "\n")
	if !in.InsertLargePaste(raw) {
		t.Fatal("InsertLargePaste() = false, want true")
	}

	in.AddCurrentToHistory()
	in.Reset()
	in.HistoryUp()

	if !in.BangMode() {
		t.Fatal("BangMode() = false, want true")
	}
	if !in.HasInlinePastes() {
		t.Fatal("HasInlinePastes() = false, want true")
	}
	if got := in.Value(); got != "[Pasted text #1 +11 lines]" {
		t.Fatalf("input value = %q, want inline placeholder", got)
	}
	if got := strings.Join(in.InlinePasteRawContents(), "\n"); got != raw {
		t.Fatalf("raw pasted content = %q, want original content", got)
	}
}

func TestInputHistoryDraftRoundTripRestoresBangLargePaste(t *testing.T) {
	in := NewInput()
	in.SetBangMode(true)
	raw := strings.Join([]string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"}, "\n")
	if !in.InsertLargePaste(raw) {
		t.Fatal("InsertLargePaste() = false, want true")
	}
	in.AddHistoryEntry(inputHistoryEntry{Display: "echo hi", BangMode: true})

	in.HistoryUp()
	if got := in.Value(); got != "echo hi" {
		t.Fatalf("history value = %q, want echo hi", got)
	}

	in.HistoryDown()

	if !in.BangMode() {
		t.Fatal("BangMode() = false, want true after restoring draft")
	}
	if !in.HasInlinePastes() {
		t.Fatal("HasInlinePastes() = false, want true after restoring draft")
	}
	if got := in.Value(); got != "[Pasted text #1 +11 lines]" {
		t.Fatalf("restored draft value = %q, want inline placeholder", got)
	}
	if got := strings.Join(in.InlinePasteRawContents(), "\n"); got != raw {
		t.Fatalf("restored raw pasted content = %q, want original content", got)
	}
}

func TestHandleInsertKeyBangLargePasteUsesRawCommand(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeInsert
	m.input.SetBangMode(true)
	raw := strings.Join([]string{
		"curl --request POST \\",
		"  --url https://example.com \\",
		"  --header 'Content-Type: application/json' \\",
		"  --data '{",
		`    "a": 1,`,
		`    "b": 2`,
		"  }'",
		"echo done 1",
		"echo done 2",
		"echo done 3",
		"echo done 4",
	}, "\n")
	if !m.input.InsertLargePaste(raw) {
		t.Fatal("InsertLargePaste() = false, want true")
	}

	cmd := m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if cmd == nil {
		t.Fatal("handleInsertKey(Enter) returned nil cmd, want shell command")
	}

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 {
		t.Fatalf("viewport block count = %d, want 1", len(blocks))
	}
	if got := blocks[0].UserLocalShellCmd; got != raw {
		t.Fatalf("UserLocalShellCmd = %q, want original raw command", got)
	}
	if !blocks[0].UserLocalShellPending {
		t.Fatal("UserLocalShellPending = false, want true")
	}
	if got := blocks[0].Content; got != "![Pasted text #1 +11 lines]" {
		t.Fatalf("block content = %q, want placeholder user line", got)
	}
}

func TestLocalShellContextMessageKeepsReadablePartsAndPersistedContent(t *testing.T) {
	msg := localShellContextMessage("!echo hi", "echo hi", "ok", nil)
	readable := convformat.BlockString(convformat.LabelUser, convformat.UserShellReadableBody("!echo hi", "echo hi", "ok", false))
	persisted := convformat.BlockString(convformat.LabelUser, convformat.UserShellPersistedBody("!echo hi", "echo hi", "ok", false))

	if msg.Content != persisted {
		t.Fatalf("message content = %q, want persisted body", msg.Content)
	}
	if len(msg.Parts) != 1 || msg.Parts[0].Text != readable {
		t.Fatalf("message parts = %#v, want readable body", msg.Parts)
	}
}
