package texttruncate

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func BenchmarkTruncateStringHeadTail(b *testing.B) {
	for _, tc := range []struct {
		name string
		text string
	}{
		{name: "ascii", text: strings.Repeat("source line from tool output\n", 100)},
		{name: "unicode", text: strings.Repeat("工具输出内容🙂\n", 300)},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = HeadTail(tc.text, 500, 500, "\n...\n")
			}
		})
	}
}

func TestTruncateStringHeadTailASCIIWithUnicodeSeparator(t *testing.T) {
	got := HeadTail("abcdefghij", 3, 2, "…")
	if got != "abc…ij" {
		t.Fatalf("HeadTail() = %q, want %q", got, "abc…ij")
	}
}

func TestTruncateStringHeadTailASCIIAllocsGuard(t *testing.T) {
	text := strings.Repeat("source line from tool output\n", 100)
	allocs := testing.AllocsPerRun(100, func() {
		_ = HeadTail(text, 500, 500, "\n...\n")
	})
	const maxAllocs = 1
	if allocs > maxAllocs {
		t.Fatalf("ASCII head-tail truncation allocs = %.0f, want ≤%d", allocs, maxAllocs)
	}
}

func TestTruncateStringRunesPreservesUTF8(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{
			name: "chinese at boundary",
			in:   strings.Repeat("a", 199) + "试tail",
			want: strings.Repeat("a", 199) + "试...",
		},
		{
			name: "emoji at boundary",
			in:   strings.Repeat("a", 199) + "🙂tail",
			want: strings.Repeat("a", 199) + "🙂...",
		},
		{
			name: "short string unchanged",
			in:   "异常状态🙂",
			want: "异常状态🙂",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Runes(tc.in, 200, "...")
			if got != tc.want {
				t.Fatalf("Runes() = %q, want %q", got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("Runes() returned invalid UTF-8: %q", got)
			}
		})
	}
}

func TestTruncateStringFirstRunes(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		n    int
		want string
	}{
		{name: "empty", in: "", n: 5, want: ""},
		{name: "zero", in: "厂房", n: 0, want: ""},
		{name: "negative", in: "厂房", n: -1, want: ""},
		{name: "whole", in: "厂房", n: 5, want: "厂房"},
		{name: "cut on boundary", in: "厂房设备", n: 2, want: "厂房"},
		{name: "ascii", in: "abcdef", n: 3, want: "abc"},
		{name: "emoji", in: "🙂🙂🙂", n: 2, want: "🙂🙂"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := FirstRunes(tc.in, tc.n)
			if got != tc.want {
				t.Fatalf("FirstRunes(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("result is invalid UTF-8: %q", got)
			}
		})
	}
}

func TestTruncateStringLastRunes(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		n    int
		want string
	}{
		{name: "empty", in: "", n: 5, want: ""},
		{name: "zero", in: "厂房", n: 0, want: ""},
		{name: "negative", in: "厂房", n: -1, want: ""},
		{name: "whole", in: "厂房", n: 5, want: "厂房"},
		{name: "cut on boundary", in: "厂房设备", n: 2, want: "设备"},
		{name: "ascii", in: "abcdef", n: 3, want: "def"},
		{name: "emoji", in: "🙂🙂🙂", n: 2, want: "🙂🙂"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := LastRunes(tc.in, tc.n)
			if got != tc.want {
				t.Fatalf("LastRunes(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("result is invalid UTF-8: %q", got)
			}
		})
	}
}

func TestTruncateStringHeadTailPreservesUTF8(t *testing.T) {
	// Craft a string where the naive byte split point lands in the middle of a
	// multi-byte rune. "厂" is 3 bytes (e5 8e 82); this is the exact corruption
	// pattern that produced invalid UTF-8 in exported history files.
	for _, tc := range []struct {
		name              string
		in                string
		head, tail        int
		sep               string
		wantWhole         bool
		wantHead, wantTai string
	}{
		{
			name:      "whole fits",
			in:        "厂房设备",
			head:      3,
			tail:      3,
			sep:       "...",
			wantWhole: true,
		},
		{
			name:     "middle elision on rune boundary",
			in:       "厂房设备安装调试运行",
			head:     2,
			tail:     2,
			sep:      "\n...\n",
			wantHead: "厂房",
			wantTai:  "运行",
		},
		{
			name:     "emoji middle elision",
			in:       "🙂😀😁😂🤣😃😄😅",
			head:     2,
			tail:     2,
			sep:      "...",
			wantHead: "🙂😀",
			wantTai:  "😄😅",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := HeadTail(tc.in, tc.head, tc.tail, tc.sep)
			if !utf8.ValidString(got) {
				t.Fatalf("result is invalid UTF-8: %q", got)
			}
			if tc.wantWhole {
				if got != tc.in {
					t.Fatalf("expected whole string %q, got %q", tc.in, got)
				}
				return
			}
			want := tc.wantHead + tc.sep + tc.wantTai
			if got != want {
				t.Fatalf("HeadTail() = %q, want %q", got, want)
			}
		})
	}
}

func TestTruncateStringBytesPreservesUTF8(t *testing.T) {
	// "厂" is 3 bytes. A byte budget landing inside it must back off to the
	// preceding rune boundary rather than emit a half-encoded rune.
	for _, tc := range []struct {
		name     string
		in       string
		maxBytes int
		want     string
	}{
		{name: "empty budget", in: "厂房", maxBytes: 0, want: ""},
		{name: "negative budget", in: "厂房", maxBytes: -1, want: ""},
		{name: "whole fits", in: "厂房", maxBytes: 100, want: "厂房"},
		{name: "split mid-rune backs off", in: "厂房", maxBytes: 4, want: "厂"},
		{name: "split mid-rune backs off further", in: "厂房", maxBytes: 5, want: "厂"},
		{name: "exact rune boundary", in: "厂房", maxBytes: 3, want: "厂"},
		{name: "ascii", in: "abcdef", maxBytes: 3, want: "abc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Bytes(tc.in, tc.maxBytes)
			if got != tc.want {
				t.Fatalf("Bytes(%q, %d) = %q, want %q", tc.in, tc.maxBytes, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("result is invalid UTF-8: %q", got)
			}
		})
	}
}
