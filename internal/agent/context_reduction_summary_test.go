package agent

import (
	"slices"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tools"
)

func TestReduceJSONBlobSummaryPreservesScalarValuesAndExactNumbers(t *testing.T) {
	content := `{
		"a_nested": {"child": true},
		"b_nested": [1, 2],
		"c_nested": {"child": true},
		"d_nested": [1, 2],
		"e_nested": {"child": true},
		"f_nested": [1, 2],
		"g_nested": {"child": true},
		"h_nested": [1, 2],
		"request_id": 9007199254740993,
		"status": "ready"
	}`

	got, ok := reduceJSONBlobSummary(requestReductionContext{ToolName: tools.NameShell, Content: content})
	if !ok {
		t.Fatal("reduceJSONBlobSummary() rejected valid object")
	}
	if !strings.Contains(got, `- "request_id": 9007199254740993`) {
		t.Fatalf("summary lost exact integer value: %q", got)
	}
	if !strings.Contains(got, `- "status": "ready"`) {
		t.Fatalf("summary omitted scalar value behind sorted containers: %q", got)
	}
	if strings.Contains(got, "9007199254740992") {
		t.Fatalf("summary rounded integer value: %q", got)
	}
	if _, ok := reduceJSONBlobSummary(requestReductionContext{Content: content + `{}`}); ok {
		t.Fatal("reduceJSONBlobSummary() accepted trailing JSON value")
	}

	largeNumber := strings.Repeat("9", summaryLineSnippetChars*4)
	largeContent := `{"value":` + largeNumber + `}`
	largeSummary, ok := reduceJSONBlobSummary(requestReductionContext{Content: largeContent})
	if !ok {
		t.Fatal("reduceJSONBlobSummary() rejected valid large number")
	}
	if len(largeSummary) >= len(largeContent) {
		t.Fatalf("large-number summary bytes = %d, want less than original %d", len(largeSummary), len(largeContent))
	}
	if strings.Count(largeSummary, "\n") != 1 {
		t.Fatalf("large-number summary broke one-entry-per-line shape: %q", largeSummary)
	}
}

func TestJSONSummaryHelpersBoundKeysAndSampleArrayRange(t *testing.T) {
	longKey := strings.Repeat("k", summaryLineSnippetChars*2) + "\nunsafe: key"
	lines := summarizeJSONObjectEntries(map[string]any{
		longKey: map[string]any{
			strings.Repeat("nested", summaryLineSnippetChars): true,
		},
	})
	if len(lines) != 1 {
		t.Fatalf("object summary lines = %d, want 1: %v", len(lines), lines)
	}
	if got := []rune(lines[0]); len(got) > summaryLineSnippetChars*2+48 {
		t.Fatalf("object summary did not bound long keys: runes=%d line=%q", len(got), lines[0])
	}
	if strings.Contains(lines[0], "\nunsafe") {
		t.Fatalf("object summary emitted an unescaped key newline: %q", lines[0])
	}

	items := summarizeJSONArrayItems([]any{"first", "second", "middle", "fourth", "last"}, 3)
	want := []string{`- [0] "first"`, `- [2] "middle"`, `- [4] "last"`}
	if !slices.Equal(items, want) {
		t.Fatalf("array samples = %v, want %v", items, want)
	}
}

func TestRequestJSONReductionNeverExpandsContent(t *testing.T) {
	ctx := requestReductionContext{
		ToolName: tools.NameShell,
		Meta:     toolCallMeta{Name: tools.NameShell},
		Content:  `{"value":"short"}`,
	}
	got, rule, ok := reduceRequestToolOutput(requestReductionJSON, ctx)
	if ok || got != "" || rule != "" {
		t.Fatalf("reduceRequestToolOutput() = (%q, %q, %t), want no expanding reduction", got, rule, ok)
	}
}

func TestNumberedSourceStatsIncludesBlankAndZeroNumberedLines(t *testing.T) {
	content := "0 \t\n1 package sample\n2 \t\n3 func run() {}\n"
	lines, rangeNote := numberedSourceStats(content)
	if lines != 4 || rangeNote != " range=0-3" {
		t.Fatalf("numberedSourceStats() = (%d, %q), want (4, %q)", lines, rangeNote, " range=0-3")
	}
	if num, ok := parseNumberedSourceLine("2 \t"); !ok || num != 2 {
		t.Fatalf("parseNumberedSourceLine(blank) = (%d, %t), want (2, true)", num, ok)
	}
}
