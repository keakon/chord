package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tools"
)

func TestCompactGlobOutputSummaryUsesCanonicalToolName(t *testing.T) {
	summary := compactGlobOutputSummary(`{"pattern":"**/*.go","path":"internal"}`, "internal/agent/compaction_policy.go")
	if !strings.Contains(summary, "Re-run "+tools.NameGlob+"(") {
		t.Fatalf("summary = %q, want canonical glob re-run hint", summary)
	}
	if strings.Contains(summary, "Re-run Glob(") {
		t.Fatalf("summary should not use legacy Glob tool name: %q", summary)
	}
}

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
	if !strings.Contains(summary, "Re-run "+tools.NameWebFetch+"(") {
		t.Fatalf("summary = %q, want canonical web_fetch re-run hint", summary)
	}
	if strings.Contains(summary, "Re-run WebFetch(") {
		t.Fatalf("summary should not use legacy WebFetch tool name: %q", summary)
	}
}
