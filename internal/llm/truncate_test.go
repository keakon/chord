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
