package agent

import (
	"strings"
	"testing"
)

func TestCompactWebFetchOutputSummaryStripsResultHeadersFromSnippet(t *testing.T) {
	content := strings.Join([]string{
		"URL: https://example.com/article",
		"Final-URL: https://example.com/final",
		"Content-Type: text/html; charset=utf-8",
		"Charset: utf-8",
		"Title: Example",
		"Extraction-Mode: html-legacy",
		"Content-Quality: ok",
		"Truncated: none",
		"",
		"This is the preserved body snippet.",
	}, "\n")
	summary := compactWebFetchOutputSummary(`{"url":"https://example.com/article","timeout":30}`, content)
	if !strings.Contains(summary, "This is the preserved body snippet.") {
		t.Fatalf("summary = %q, want body snippet", summary)
	}
	if strings.Contains(summary, "Extraction-Mode: html-legacy") {
		t.Fatalf("summary should not preserve web fetch header lines in snippet: %q", summary)
	}
	if strings.Contains(summary, "browser=") {
		t.Fatalf("summary should not mention removed browser arg: %q", summary)
	}
}
