package composer

import (
	"reflect"
	"testing"

	"github.com/keakon/bubbles/v2/textarea"
	tea "github.com/keakon/bubbletea/v2"
)

func TestKeyboardSelectionKeepsSingleOwner(t *testing.T) {
	in := New(textarea.DefaultDarkStyles(), nil)
	in.SetValue("alpha beta")
	in.SelectRuneRange(2, 5)
	_, handled := in.HandleSelectionKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight, Mod: tea.ModShift}))
	if !handled || in.SelectionText() != "pha " || in.textarea.HasSelection() {
		t.Fatal("keyboard extension did not preserve sole selection ownership")
	}
	in.Update(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	if in.SelectionText() != in.Value() || in.textarea.HasSelection() {
		t.Fatal("select-all did not preserve sole selection ownership")
	}
}

func TestStateRestoresHistoryNavigationAndIndependentDraft(t *testing.T) {
	in := New(textarea.DefaultDarkStyles(), nil)
	in.SetWidth(80)
	in.AddHistory("earlier entry")
	in.SetValue("sample ")
	in.CursorEnd()
	in.InsertImagePlaceholder(1)
	in.SetBangMode(true)
	want := in.Snapshot()
	in.HistoryUp()
	saved := in.SaveState()
	in.Reset()
	in.SetValue("another draft")
	in.RestoreState(saved)
	if in.Value() != "earlier entry" {
		t.Fatalf("restored history entry = %q", in.Value())
	}
	in.HistoryDown()
	if got := in.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("restored draft = %#v, want %#v", got, want)
	}
	in.CursorEnd()
	if _, ok := in.RemoveInlinePasteAtCursor(); !ok {
		t.Fatal("expected restored inline object at cursor")
	}
	in.RestoreState(saved)
	in.HistoryDown()
	if got := in.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("editing restored draft changed saved state: got %#v, want %#v", got, want)
	}
}
