package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tools"
)

// The grep/glob collapsed state is a single header line like Read: the count
// summary is merged into the header and no separate body summary line is
// rendered. The expanded state keeps the summary in the header and renders
// every returned match below.

func TestGrepCollapsedSingleLineFoldShowsSummaryInHeader(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               tools.NameGrep,
		Content:                `{"pattern":"TODO"}`,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
		ResultContent: strings.Join([]string{
			"a.go:1:TODO one",
			"b.go:2:TODO two",
			"Note: pattern was invalid regex; searched as literal text.",
			"grep: skipped path: vendor/blocked: no such file or directory",
			"(showing first 2 matches within 4096 KiB; narrow paths/includes/pattern for more precise results)",
		}, "\n"),
	}

	collapsed := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(collapsed, "✓ ▸ grep TODO · 2 matches shown · 2 files · 1 paths skipped · literal fallback · truncated") {
		t.Fatalf("expected collapsed grep summary in header, got:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "grep: skipped path: vendor/blocked") || strings.Contains(collapsed, "searched as literal text") {
		t.Fatalf("collapsed grep should hide full detail lines, got:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "↳") {
		t.Fatalf("collapsed grep should be a single header line without a body summary, got:\n%s", collapsed)
	}

	block.ToggleAtWidth(120)
	expanded := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	for _, want := range []string{"✓ ▾ grep TODO · 2 matches shown · 2 files · 1 paths skipped · literal fallback · truncated", "a.go:1:TODO one", "Note: pattern was invalid regex; searched as literal text.", "grep: skipped path: vendor/blocked"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("expected expanded grep to contain %q, got:\n%s", want, expanded)
		}
	}
}

func TestGlobCollapsedSingleLineFoldShowsSummaryInHeader(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               tools.NameGlob,
		Content:                `{"patterns":["**/*.go"]}`,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
		ResultContent: strings.Join([]string{
			"a.go",
			"b.go",
			"(showing first 2 results within 4096 KiB; full results saved to /tmp/ws/artifacts/glob-results.log; refine pattern/path to narrow results)",
		}, "\n"),
	}

	collapsed := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(collapsed, "✓ ▸ glob **/*.go · 2 files · truncated · /tmp/ws/artifacts/glob-results.log") {
		t.Fatalf("expected collapsed glob summary in header, got:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "↳") {
		t.Fatalf("collapsed glob should be a single header line without a body summary, got:\n%s", collapsed)
	}

	block.ToggleAtWidth(120)
	expanded := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	for _, want := range []string{"✓ ▾ glob **/*.go · 2 files · truncated · /tmp/ws/artifacts/glob-results.log", "a.go", "b.go", "full results saved to /tmp/ws/artifacts/glob-results.log"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("expected expanded glob to contain %q, got:\n%s", want, expanded)
		}
	}
}

func TestGrepCollapsedSummaryKeepsParamsGluedToPattern(t *testing.T) {
	// Parameters are command invocation: they follow the pattern after a
	// single space, and only the result summary is separated with " · ".
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               tools.NameGrep,
		Content:                `{"pattern":"sanitizeDisplayText|\\control|control.*stream","includes":["internal/tui/**/*_test.go"]}`,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
		ResultContent:          "a.go:1:todo one\nb.go:2:todo two",
	}
	collapsed := stripANSI(strings.Join(block.Render(200, ""), "\n"))
	if strings.Contains(collapsed, " · (includes=") {
		t.Fatalf("grep parameters must follow the pattern with a space, not a separator dot; got:\n%s", collapsed)
	}
	if !strings.Contains(collapsed, "(includes=internal/tui/**/*_test.go) · 2 matches · 2 files") {
		t.Fatalf("expected collapsed grep header to glue params to pattern before the count summary, got:\n%s", collapsed)
	}
	if !strings.Contains(collapsed, "grep sanitizeDisplayText|\\control|control.*stream") {
		t.Fatalf("expected collapsed grep header to keep the full pattern, got:\n%s", collapsed)
	}
}

func TestCollapsedSearchSummaryKeepsCountAndDropsParamsWhenNarrow(t *testing.T) {
	// The count summary (match/file counts, truncation facts) must survive
	// width clipping; the paths/includes parameters are dropped first.
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               tools.NameGrep,
		Content:                `{"pattern":"veryLongPatternThatWraps","paths":["internal/tui"],"includes":["**/*.go"]}`,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
		ResultContent:          "internal/tui/app.go:1:todo\ninternal/tui/block.go:2:todo",
	}
	joined := stripANSI(strings.Join(block.Render(60, ""), "\n"))
	if !strings.Contains(joined, "2 matches · 2 files") {
		t.Fatalf("expected narrow collapsed grep to keep the count summary, got:\n%s", joined)
	}
	if strings.Contains(joined, "paths=") && strings.Contains(joined, "includes=") {
		t.Fatalf("expected narrow collapsed grep to drop the parameters, got:\n%s", joined)
	}
}
