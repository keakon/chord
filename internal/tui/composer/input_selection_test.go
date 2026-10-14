package composer

import (
	"testing"

	"github.com/keakon/bubbles/v2/textarea"
)

func TestInputSelectionTextSingleLine(t *testing.T) {
	in := New(textarea.DefaultDarkStyles(), nil)
	in.SetWidth(24)
	in.SetValue("hello world")
	in.SyncHeight()
	in.StartSelection(1)
	in.UpdateSelection(5)

	if got := in.SelectionText(); got != "ello" {
		t.Fatalf("SelectionText() = %q, want %q", got, "ello")
	}
}

func TestInputSelectionTextMultiline(t *testing.T) {
	in := New(textarea.DefaultDarkStyles(), nil)
	in.SetWidth(24)
	in.SetValue("hello\nworld")
	in.SyncHeight()
	in.StartSelection(3)
	in.UpdateSelection(9)

	if got := in.SelectionText(); got != "lo\nwor" {
		t.Fatalf("SelectionText() = %q, want %q", got, "lo\nwor")
	}
}
