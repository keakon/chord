package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

// The grep/glob collapsed state is a single header line like Read: only the
// key match/file counts are merged into the header. Expanded state renders
// every returned match and diagnostic below.

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
	if !strings.Contains(collapsed, "✓ ▸ grep TODO · 2 matches · 2 files") {
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
	for _, want := range []string{"✓ ▾ grep TODO · 2 matches · 2 files", "a.go:1:TODO one", "Note: pattern was invalid regex; searched as literal text.", "grep: skipped path: vendor/blocked"} {
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
	if !strings.Contains(collapsed, "✓ ▸ glob **/*.go · 2 files") {
		t.Fatalf("expected collapsed glob summary in header, got:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "↳") {
		t.Fatalf("collapsed glob should be a single header line without a body summary, got:\n%s", collapsed)
	}

	block.ToggleAtWidth(120)
	expanded := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	for _, want := range []string{"✓ ▾ glob **/*.go · 2 files", "a.go", "b.go", "full results saved to /tmp/ws/artifacts/glob-results.log"} {
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

func TestSearchHeaderKeepsPatternWholeAndCompressesParamsWhenNarrow(t *testing.T) {
	// The pattern is the primary argument: when the header cannot fit the
	// full pattern + counts + parameters, the parameters are compressed
	// (middle-truncated) instead of stealing the pattern's width.
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               tools.NameGrep,
		Content:                `{"pattern":"Audit|EffectiveArgs|AnotherVeryLongAlternation","paths":["internal/tui/app_agent_events_tool.go"],"includes":["*.go"]}`,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
		ResultContent:          "internal/tui/app.go:1:match one\ninternal/tui/block.go:2:match two",
	}
	joined := stripANSI(strings.Join(block.Render(118, ""), "\n"))
	if !strings.Contains(joined, "Audit|EffectiveArgs|AnotherVeryLongAlternation") {
		t.Fatalf("expected grep header to keep the full pattern before compressing params, got:\n%s", joined)
	}
	if !strings.Contains(joined, "2 matches · 2 files") {
		t.Fatalf("expected grep header to keep the count summary, got:\n%s", joined)
	}
	if strings.Contains(joined, "(paths=internal/tui/app_agent_events_tool.go, includes=*.go)") {
		t.Fatalf("expected grep header to compress the parameters, got:\n%s", joined)
	}
	if !strings.Contains(joined, "(paths=") {
		t.Fatalf("expected compressed grep header to keep the parameter head, got:\n%s", joined)
	}
}

func TestSearchHeaderKeepsPatternWholeWithoutResultSummary(t *testing.T) {
	// Error and cancelled results have no count summary. Their parameters
	// must remain secondary instead of taking the summary's priority slot.
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               tools.NameGrep,
		Content:                `{"pattern":"Audit|EffectiveArgs|AnotherVeryLongAlternation","paths":["internal/tui/app_agent_events_tool.go"],"includes":["*.go"]}`,
		ResultDone:             true,
		ResultStatus:           agent.ToolResultStatusError,
		ToolCallDetailExpanded: false,
		ResultContent:          "search failed",
	}
	joined := stripANSI(strings.Join(block.Render(118, ""), "\n"))
	if !strings.Contains(joined, "Audit|EffectiveArgs|AnotherVeryLongAlternation") {
		t.Fatalf("expected error grep header to keep the full pattern before compressing params, got:\n%s", joined)
	}
	if strings.Contains(joined, "(paths=internal/tui/app_agent_events_tool.go, includes=*.go)") {
		t.Fatalf("expected error grep header to compress the parameters, got:\n%s", joined)
	}
	if !strings.Contains(joined, "(paths=") {
		t.Fatalf("expected compressed error grep header to keep the parameter head, got:\n%s", joined)
	}
}
