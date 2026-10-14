package tui

import (
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/convformat"
)

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

func TestHandleInsertKeyEmptyBangMarksLocalShellCard(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeInsert
	m.input.SetBangMode(true)

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 {
		t.Fatalf("viewport block count = %d, want 1", len(blocks))
	}
	if !blocks[0].IsUserLocalShell() {
		t.Fatal("empty bang card should retain local-shell identity")
	}
	if blocks[0].UserLocalShellPending {
		t.Fatal("empty bang card should not be pending")
	}
}

func TestTerminalContextMessageKeepsReadablePartsAndPersistedContent(t *testing.T) {
	msg := localShellContextMessage("!echo hi", "echo hi", "ok", nil)
	readable := convformat.BlockString(convformat.LabelUser, convformat.UserShellReadableBody("echo hi", "ok", false))
	persisted := convformat.BlockString(convformat.LabelUser, convformat.UserShellPersistedBody("!echo hi", "echo hi", "ok", false))

	if msg.Content != persisted {
		t.Fatalf("message content = %q, want persisted body", msg.Content)
	}
	if len(msg.Parts) != 1 || msg.Parts[0].Text != readable {
		t.Fatalf("message parts = %#v, want readable body", msg.Parts)
	}
}
