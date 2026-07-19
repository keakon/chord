package agent

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestCompactTextSnippetPreservesUTF8 is a regression test for a corruption bug
// where compactTextSnippet sliced strings on byte boundaries (s[:keepHead] and
// s[len(s)-keepTail:]). When a boundary fell inside a multi-byte rune such as
// "厂" (0xE5 0x8E 0x82) the head kept an orphan lead byte and the tail started
// on an orphan continuation byte, producing invalid UTF-8 that was written to
// history-N.md and later rejected by the read tool.
func TestCompactTextSnippetPreservesUTF8(t *testing.T) {
	// Every byte offset must be exercised so a boundary lands mid-rune for at
	// least one input, regardless of the 2/3 head split.
	base := strings.Repeat("石药中诺厂家分类", 40)
	for extra := range 8 {
		in := strings.Repeat("a", extra) + base
		for _, maxChars := range []int{10, 33, 64, 100, 200} {
			got := compactTextSnippet(in, maxChars)
			if !utf8.ValidString(got) {
				t.Fatalf("compactTextSnippet(extra=%d,maxChars=%d) returned invalid UTF-8: %q", extra, maxChars, got)
			}
		}
	}
}

func TestCompactTextSnippetShortStringUnchanged(t *testing.T) {
	in := "石药中诺厂家"
	if got := compactTextSnippet(in, 100); got != in {
		t.Fatalf("compactTextSnippet() = %q, want %q", got, in)
	}
}

func TestCompactTextSnippetKeepsHeadAndTail(t *testing.T) {
	in := strings.Repeat("头", 50) + "MIDDLE" + strings.Repeat("尾", 50)
	got := compactTextSnippet(in, 60)
	if !utf8.ValidString(got) {
		t.Fatalf("compactTextSnippet returned invalid UTF-8: %q", got)
	}
	if !strings.Contains(got, "\n...\n") {
		t.Fatalf("expected middle-elision separator, got %q", got)
	}
	if !strings.HasPrefix(got, "头") {
		t.Fatalf("expected head preserved, got %q", got)
	}
	if !strings.HasSuffix(got, "尾") {
		t.Fatalf("expected tail preserved, got %q", got)
	}
}
