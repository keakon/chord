package composer

import (
	"strings"
	"testing"

	"github.com/keakon/bubbles/v2/textarea"
)

func TestInputHistoryRestoresBangLargePasteEntry(t *testing.T) {
	in := New(textarea.DefaultDarkStyles(), nil)
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
	in := New(textarea.DefaultDarkStyles(), nil)
	in.SetBangMode(true)
	raw := strings.Join([]string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"}, "\n")
	if !in.InsertLargePaste(raw) {
		t.Fatal("InsertLargePaste() = false, want true")
	}
	in.PushHistory(HistoryEntry{Display: "echo hi", BangMode: true})

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
