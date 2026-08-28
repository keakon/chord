package tools

import (
	"os"
	"strings"
	"testing"
)

// TestNormalizePunctWithSpaceFoldingIgnoresInvisibleRunes guards that
// variation selectors (U+FE0F) and zero-width spaces (U+200B) are
// folded out of the comparison sequence (no normalized rune), while
// each is merged into the adjacent span so splicing a match back into the
// file keeps the file's original bytes.

func TestNormalizePunctWithSpaceFoldingIgnoresInvisibleRunes(t *testing.T) {
	norm, spans := normalizePunctWithSpaceFolding([]rune("ab\uFE0Fc\u200Bd"))
	if got := string(norm); got != "abcd" {
		t.Fatalf("norm = %q, want %q (invisible runes folded out)", got, "abcd")
	}
	// b's span absorbs the following VS16; c's absorbs the ZWSP; the
	// original file bytes for both are preserved by the widened spans.
	want := []punctSpan{
		{start: 0, end: 1},
		{start: 1, end: 3},
		{start: 3, end: 5},
		{start: 5, end: 6},
	}
	if len(spans) != len(want) {
		t.Fatalf("len(spans) = %d, want %d", len(spans), len(want))
	}
	for i, got := range spans {
		if got != want[i] {
			t.Fatalf("spans[%d] = %+v, want %+v", i, got, want[i])
		}
	}
}

// TestNormalizePunctWithSpaceFoldingIgnoresLeadingInvisibleRune guards
// the boundary case where an invisible rune leads the text: no previous span
// exists to absorb it, so it is simply dropped from the comparison (and stays
// outside any span, preserved by the untouched prefix when splicing).

func TestNormalizePunctWithSpaceFoldingIgnoresLeadingInvisibleRune(t *testing.T) {
	norm, spans := normalizePunctWithSpaceFolding([]rune("\u200Bab"))
	if got := string(norm); got != "ab" {
		t.Fatalf("norm = %q, want %q (leading ZWSP folded out)", got, "ab")
	}
	if len(spans) != 2 || spans[0] != (punctSpan{start: 1, end: 2}) {
		t.Fatalf("spans = %+v, want [{1,2} {2,3}]", spans)
	}
}

// TestEditToolTolerantMatchIgnoresInvisibleRunes guards the full Edit flow:
// a model copy of the target that leaks a VS16 plus a full-width comma still
// replaces the file's half-width-comma text through the tolerant fallback.

func TestEditToolTolerantMatchIgnoresInvisibleRunes(t *testing.T) {
	dir := t.TempDir()
	file := "ab,cd\n"
	path := writeEditFixture(t, dir, "demo.md", file)
	oldText := "ab\uFE0F\uFF0C cd\n"

	newText := "ab;cd\n"
	out, err := runEdit(t, dir, map[string]any{
		"path": path, "old_string": oldText, "new_string": newText,
	})
	if err != nil {
		t.Fatalf("Execute err = %v, want tolerant success", err)
	}
	if !strings.Contains(out, "punctuation/whitespace-tolerant") {
		t.Fatalf("output = %q, want punctuation/whitespace-tolerant marker", out)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "ab;cd\n" {
		t.Fatalf("file = %q, want %q (VS16 ignored during match)", string(got), "ab;cd\n")
	}
}
