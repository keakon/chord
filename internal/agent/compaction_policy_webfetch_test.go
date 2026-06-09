package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tools"
)

func TestCompactGlobOutputSummaryUsesCanonicalToolName(t *testing.T) {
	summary := compactSearchLikeOutputSummary(requestReductionContext{
		ToolName: tools.NameGlob,
		Content:  "internal/agent/compaction_policy.go",
		Meta: toolCallMeta{
			Name: tools.NameGlob,
			Args: `{"patterns":["**/*.go"],"path":"internal"}`,
		},
	})
	if !strings.Contains(summary, "Older "+tools.NameGlob+" results summarized for this request to save context") {
		t.Fatalf("summary = %q, want canonical glob summary", summary)
	}
	if strings.Contains(summary, "Re-run") || strings.Contains(summary, "re-run") {
		t.Fatalf("summary should not include re-run hints: %q", summary)
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
	if !strings.Contains(summary, "Older "+tools.NameWebFetch+" output truncated for this request to save context") {
		t.Fatalf("summary = %q, want canonical web_fetch summary", summary)
	}
	if strings.Contains(summary, "Re-run") || strings.Contains(summary, "re-run") {
		t.Fatalf("summary should not include re-run hints: %q", summary)
	}
}
