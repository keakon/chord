package lsp

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTruncateBytesAtRuneLeavesValidUTF8 guards the invariant the byte budget
// must not break: cutting through a multi-byte rune would leave invalid UTF-8
// in a log line or an LSP sidebar row. Both callers do receive non-ASCII text -
// localized server messages and non-ASCII workspace paths.
func TestTruncateBytesAtRuneLeavesValidUTF8(t *testing.T) {
	// "界" is three bytes; place it so the budget lands inside it.
	head := strings.Repeat("x", 10)
	got := truncateBytesAtRune(head+"界"+strings.Repeat("y", 20), 11, "...")
	if !utf8.ValidString(got) {
		t.Fatalf("truncateBytesAtRune produced invalid UTF-8: %q", got)
	}
	if want := head + "..."; got != want {
		t.Fatalf("truncateBytesAtRune = %q, want %q", got, want)
	}
}

func TestTruncateBytesAtRuneKeepsShortStrings(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		max  int
	}{
		{"empty", "", 8},
		{"exact fit", "12345678", 8},
		{"well under", "abc", 8},
	} {
		if got := truncateBytesAtRune(tc.in, tc.max, "..."); got != tc.in {
			t.Fatalf("%s: truncateBytesAtRune(%q) = %q, want unchanged", tc.name, tc.in, got)
		}
	}
}

func TestTruncateBytesAtRuneCutsASCIIAtBudget(t *testing.T) {
	if got, want := truncateBytesAtRune(strings.Repeat("x", 20), 5, "..."), "xxxxx..."; got != want {
		t.Fatalf("truncateBytesAtRune = %q, want %q", got, want)
	}
}

// TestTruncateBytesAtRuneZeroBudget pins the degenerate budget: nothing is
// retained, but a non-empty suffix still marks the cut.
func TestTruncateBytesAtRuneZeroBudget(t *testing.T) {
	if got, want := truncateBytesAtRune("abc", 0, "..."), "..."; got != want {
		t.Fatalf("truncateBytesAtRune with a zero budget = %q, want %q", got, want)
	}
	if got, want := truncateBytesAtRune("", 0, "..."), ""; got != want {
		t.Fatalf("truncateBytesAtRune of an empty string = %q, want %q", got, want)
	}
}
