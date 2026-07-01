package llm

import (
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

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
			got := TruncateStringRunes(tc.in, 200, "...")
			if got != tc.want {
				t.Fatalf("TruncateStringRunes() = %q, want %q", got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("TruncateStringRunes() returned invalid UTF-8: %q", got)
			}
		})
	}
}

func TestParseHTTPErrorRawBodyTruncationPreservesUTF8(t *testing.T) {
	testBodies := []struct {
		name string
		body string
	}{
		{name: "chinese", body: strings.Repeat("a", 199) + "试tail"},
		{name: "emoji", body: strings.Repeat("a", 199) + "🙂tail"},
	}
	parsers := []struct {
		name string
		fn   func(int, http.Header, []byte) *APIError
	}{
		{name: "openai", fn: parseOpenAIHTTPErrorFromBytes},
		{name: "anthropic", fn: parseHTTPErrorFromBytes},
		{name: "gemini", fn: parseGeminiHTTPErrorFromBytes},
	}
	for _, parser := range parsers {
		for _, body := range testBodies {
			t.Run(parser.name+"/"+body.name, func(t *testing.T) {
				err := parser.fn(http.StatusForbidden, nil, []byte(body.body))
				if err == nil {
					t.Fatal("parser returned nil error")
				}
				if !utf8.ValidString(err.Message) {
					t.Fatalf("message is invalid UTF-8: %q", err.Message)
				}
				if !strings.HasSuffix(err.Message, "...") {
					t.Fatalf("message = %q, want truncated suffix", err.Message)
				}
			})
		}
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
			got := TruncateStringFirstRunes(tc.in, tc.n)
			if got != tc.want {
				t.Fatalf("TruncateStringFirstRunes(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
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
			got := TruncateStringLastRunes(tc.in, tc.n)
			if got != tc.want {
				t.Fatalf("TruncateStringLastRunes(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
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
			got := TruncateStringHeadTail(tc.in, tc.head, tc.tail, tc.sep)
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
				t.Fatalf("TruncateStringHeadTail() = %q, want %q", got, want)
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
			got := TruncateStringBytes(tc.in, tc.maxBytes)
			if got != tc.want {
				t.Fatalf("TruncateStringBytes(%q, %d) = %q, want %q", tc.in, tc.maxBytes, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("result is invalid UTF-8: %q", got)
			}
		})
	}
}
