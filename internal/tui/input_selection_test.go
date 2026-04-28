package tui

import "testing"

func TestInputSelectionTextSingleLine(t *testing.T) {
	in := NewInput()
	in.SetWidth(24)
	in.SetValue("hello world")
	in.syncHeight()
	in.StartSelection(0, 1)
	in.UpdateSelection(0, 5)

	if got := in.SelectionText(); got != "ello" {
		t.Fatalf("SelectionText() = %q, want %q", got, "ello")
	}
}

func TestInputSelectionTextMultiline(t *testing.T) {
	in := NewInput()
	in.SetWidth(24)
	in.SetValue("hello\nworld")
	in.syncHeight()
	in.StartSelection(0, 3)
	in.UpdateSelection(1, 3)

	if got := in.SelectionText(); got != "lo\nwor" {
		t.Fatalf("SelectionText() = %q, want %q", got, "lo\nwor")
	}
}

func TestTruncateMiddleDisplay(t *testing.T) {
	got := truncateMiddleDisplay("/home/user/projects/myapp", 16)
	if got != "/home/…/myapp" {
		t.Fatalf("truncateMiddleDisplay() = %q, want %q", got, "/home/…/myapp")
	}
}
