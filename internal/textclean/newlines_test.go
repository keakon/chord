package textclean

import "testing"

func TestNormalizeNewlines(t *testing.T) {
	got := NormalizeNewlines("a\r\nb\rc")
	if got != "a\nb\nc" {
		t.Fatalf("NormalizeNewlines = %q, want %q", got, "a\nb\nc")
	}
}
