package llm

import (
	"math"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBoundedTruncationMatchesRuneSelection(t *testing.T) {
	for _, text := range []string{"", "abc", strings.Repeat("source line\n", 300), strings.Repeat("甲🙂b\n", 300)} {
		runes := []rune(text)
		for _, sep := range []string{"", "...", "\n…🙂\n"} {
			for _, head := range []int{-2, 0, 1, 5, 500, math.MaxInt} {
				prefix := string(runes[:min(max(head, 0), len(runes))])
				wantPrefix := prefix
				if len(prefix) < len(text) {
					wantPrefix += sep
				}
				if got := TruncateStringRunes(text, head, sep); got != wantPrefix {
					t.Fatalf("prefix n=%d: got %q, want %q", head, got, wantPrefix)
				}
				for _, tail := range []int{-2, 0, 1, 5, 500, math.MaxInt} {
					keptTail := min(max(tail, 0), len(runes))
					wantTail := string(runes[len(runes)-keptTail:])
					if got := TruncateStringLastRunes(text, tail); got != wantTail {
						t.Fatalf("suffix n=%d: got %q, want %q", tail, got, wantTail)
					}
					remaining := len(runes) - min(max(head, 0), len(runes))
					remaining -= min(max(tail, 0), remaining)
					want := text
					if remaining > utf8.RuneCountInString(sep) {
						want = prefix + sep + wantTail
					}
					if got := TruncateStringHeadTail(text, head, tail, sep); got != want {
						t.Fatalf("head=%d tail=%d sep=%q: got %q, want %q", head, tail, sep, got, want)
					}
				}
			}
		}
	}
}
