package tui

import (
	"strings"
	"testing"
)

func TestLargePasteCursorRestoresAfterSoftWrap(t *testing.T) {
	in := NewInput()
	in.SetWidth(12)

	// Create a soft-wrapped first logical line.
	in.SetValue(strings.Repeat("x", 40))
	in.syncHeight()
	in.CursorEnd()

	// Add a newline and some short text so that there is a second logical line.
	in.InsertString("\n")
	in.InsertString("hi")
	in.CursorEnd()

	// Cursor should be at the end of line 1 (0-based logical line index).
	if got := in.Line(); got != 1 {
		t.Fatalf("precondition: input.Line() = %d, want 1", got)
	}
	if got := in.Column(); got != 2 {
		t.Fatalf("precondition: input.Column() = %d, want 2", got)
	}

	// Insert a large paste placeholder at the cursor. This path rebuilds the
	// textarea display value via SetValue() and must restore the cursor reliably
	// even when previous content is soft-wrapped.
	paste := strings.Join([]string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}, "\n")
	if !in.InsertLargePaste(paste) {
		t.Fatal("InsertLargePaste() = false, want true")
	}

	// Cursor should be placed immediately after the inserted placeholder.
	if got := in.Line(); got != 1 {
		t.Fatalf("input.Line() after large paste = %d, want 1", got)
	}
	wantCol := len([]rune("hi")) + len([]rune("[Pasted text #1 +11 lines]"))
	if got := in.Column(); got != wantCol {
		t.Fatalf("input.Column() after large paste = %d, want %d", got, wantCol)
	}
	if got := in.Value(); !strings.Contains(got, "hi[Pasted text #1 +11 lines]") {
		t.Fatalf("input value = %q, want placeholder inserted after existing text", got)
	}
}
