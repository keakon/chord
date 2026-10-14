package llm

import (
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

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
