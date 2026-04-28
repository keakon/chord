package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tui/markdownutil"
)

func TestSplitAssistantMarkdownSegmentsSupportsTildeOuterFence(t *testing.T) {
	input := "before\n~~~md\nouter\n```go\nfunc main() {}\n```\nafter inner\n~~~\npost\n"
	segments := splitAssistantMarkdownSegments(input)
	if len(segments) != 3 {
		t.Fatalf("len=%d want 3", len(segments))
	}
	if !segments[1].code {
		t.Fatal("middle segment should be code")
	}
	if segments[1].fenceMarker != '~' {
		t.Fatalf("fenceMarker=%q want ~", segments[1].fenceMarker)
	}
	if got := segments[1].fenceLen; got != 3 {
		t.Fatalf("fenceLen=%d want 3", got)
	}
	if !strings.Contains(segments[1].raw, "```go\nfunc main() {}\n```") {
		t.Fatalf("nested inner backtick fence missing from tilde segment: %q", segments[1].raw)
	}
}

func TestSplitAssistantMarkdownSegmentsSupportsLongOuterFence(t *testing.T) {
	input := "before\n````md\nouter\n```go\nfunc main() {}\n```\nafter inner\n````\npost\n"
	segments := splitAssistantMarkdownSegments(input)
	if len(segments) != 3 {
		t.Fatalf("len=%d want 3", len(segments))
	}
	if !segments[1].code {
		t.Fatal("middle segment should be code")
	}
	if segments[1].fenceLang != "md" {
		t.Fatalf("fenceLang=%q want md", segments[1].fenceLang)
	}
	if got := segments[1].fenceLen; got != 4 {
		t.Fatalf("fenceLen=%d want 4", got)
	}
	if !strings.Contains(segments[1].raw, "```go\nfunc main() {}\n```") {
		t.Fatalf("nested inner fence missing from code segment: %q", segments[1].raw)
	}
}

func TestRepairMarkdownForDisplayClosesUnterminatedFence(t *testing.T) {
	input := "## Sample\n\n```md\nbody\n"
	got := markdownutil.RepairForDisplay(input)
	if !strings.HasSuffix(got, "\n```") {
		t.Fatalf("expected repaired markdown to append closing fence, got %q", got)
	}
}

func TestRepairMarkdownForDisplayClosesUnterminatedTildeFence(t *testing.T) {
	input := "## Sample\n\n~~~md\nbody\n"
	got := markdownutil.RepairForDisplay(input)
	if !strings.HasSuffix(got, "\n~~~") {
		t.Fatalf("expected repaired markdown to append closing tilde fence, got %q", got)
	}
}

func TestRenderMarkdownContentRepairsUnterminatedFence(t *testing.T) {
	ApplyTheme(DefaultTheme())
	resetMarkdownRenderer()
	lines := renderMarkdownContent("## Sample\n\n```md\nbody\n", 40)
	joined := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "Sample") {
		t.Fatalf("expected heading text, got %q", joined)
	}
	if !strings.Contains(joined, "body") {
		t.Fatalf("expected fenced content text, got %q", joined)
	}
}

func TestRenderMarkdownContentInlinesTableEmailLinks(t *testing.T) {
	ApplyTheme(DefaultTheme())
	resetMarkdownRenderer()
	content := "| Account ID | Email | Expiration Date |\n|------------|-------|-----------------|\n| a20158b8-... | gfwgfwgfwgfw@gmail.com | 2026-04-02 |"
	lines := renderMarkdownContent(content, 80)
	joinedANSI := strings.Join(lines, "\n")
	if strings.Contains(joinedANSI, "[1]:") {
		t.Fatalf("expected table links to stay inline, got footer list:\n%s", joinedANSI)
	}
	joinedPlain := stripANSI(stripOSC8(joinedANSI))
	if !strings.Contains(joinedPlain, "gfwgfwgfwgfw@gmail.com") {
		t.Fatalf("expected email text to remain inside rendered table, got %q", joinedPlain)
	}
}

func TestRenderAssistantMarkdownContentKeepsNestedFenceInsideOuterMarkdownExample(t *testing.T) {
	ApplyTheme(DefaultTheme())
	var hl *codeHighlighter
	content := "````md\nHi!\n\n## Proposed API\n\n```go\nfunc main() {}\n```\n\nAfter.\n````"
	lines, _, _ := renderAssistantMarkdownContent(content, content, 48, 2, &hl)
	joinedPlain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(joinedPlain, "MD") {
		t.Fatalf("expected outer markdown block badge, got %q", joinedPlain)
	}
	if !strings.Contains(joinedPlain, "```go") {
		t.Fatalf("expected inner fence text to stay inside rendered code block, got %q", joinedPlain)
	}
	if !strings.Contains(joinedPlain, "After.") {
		t.Fatalf("expected trailing text inside outer markdown example, got %q", joinedPlain)
	}
}

func TestRenderCompactionSummaryKeepsNestedFenceInsideMarkdownExample(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		Type:                   BlockCompactionSummary,
		Collapsed:              false,
		CompactionPreviewLines: 10,
		Content:                "~~~md\nouter\n```go\nfunc main() {}\n```\nafter inner\n~~~",
	}
	lines := block.Render(60, "")
	joinedPlain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(joinedPlain, "MD") {
		t.Fatalf("expected outer markdown badge in compaction summary, got %q", joinedPlain)
	}
	if !strings.Contains(joinedPlain, "```go") {
		t.Fatalf("expected inner fence text to remain inside summary code block, got %q", joinedPlain)
	}
	if !strings.Contains(joinedPlain, "after inner") {
		t.Fatalf("expected trailing inner text to remain inside summary code block, got %q", joinedPlain)
	}
}

func TestToolResultMarkdownRepairsUnterminatedFence(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		Type:                   BlockToolCall,
		ToolName:               "WebFetch",
		Content:                `{"url":"https://example.com"}`,
		ResultDone:             true,
		ResultStatus:           agent.ToolResultStatusSuccess,
		ResultContent:          "## Ready\n\n```md\nbody\n",
		ToolCallDetailExpanded: true,
	}
	lines := block.Render(80, "")
	joined := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "Ready") {
		t.Fatalf("expected heading text in tool result, got:\n%s", joined)
	}
	if !strings.Contains(joined, "body") {
		t.Fatalf("expected repaired fenced content in tool result, got:\n%s", joined)
	}
}

func TestToolResultMarkdownRepairsUnterminatedTildeFence(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		Type:                   BlockToolCall,
		ToolName:               "WebFetch",
		Content:                `{"url":"https://example.com"}`,
		ResultDone:             true,
		ResultStatus:           agent.ToolResultStatusSuccess,
		ResultContent:          "## Ready\n\n~~~md\nbody\n",
		ToolCallDetailExpanded: true,
	}
	lines := block.Render(80, "")
	joined := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "Ready") {
		t.Fatalf("expected heading text in tool result, got:\n%s", joined)
	}
	if !strings.Contains(joined, "body") {
		t.Fatalf("expected repaired tilde-fenced content in tool result, got:\n%s", joined)
	}
}

// ---------------------------------------------------------------------------
// findStreamingMarkdownSettledFrontier tests
// ---------------------------------------------------------------------------

func TestFindStreamingMarkdownSettledFrontier_NoNewline(t *testing.T) {
	// Content with no newline at all — no stable boundary yet.
	if got := markdownutil.FindStreamingSettledFrontier("hello world"); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

func TestFindStreamingMarkdownSettledFrontier_SingleNewline(t *testing.T) {
	// Single newline — not a paragraph boundary, frontier stays 0.
	// (We only advance on blank lines, not every newline.)
	content := "hello\nworld"
	if got := markdownutil.FindStreamingSettledFrontier(content); got != 0 {
		t.Fatalf("single newline should not advance frontier, got %d", got)
	}
}

func TestFindStreamingMarkdownSettledFrontier_BlankLine(t *testing.T) {
	// A blank line (double newline) marks a paragraph boundary.
	// Frontier points at the start of the blank run so the tail preserves the
	// visible separator during streaming.
	content := "paragraph one\n\nstill coming"
	frontier := markdownutil.FindStreamingSettledFrontier(content)
	want := len("paragraph one\n")
	if frontier != want {
		t.Fatalf("want frontier=%d, got %d (content=%q)", want, frontier, content)
	}
}

func TestFindStreamingMarkdownSettledFrontier_MultipleBlankLines(t *testing.T) {
	// Frontier should be at the last stable blank-run start before the tail.
	content := "para one\n\npara two\n\nstill streaming"
	frontier := markdownutil.FindStreamingSettledFrontier(content)
	want := len("para one\n\npara two\n")
	if frontier != want {
		t.Fatalf("want frontier=%d, got %d", want, frontier)
	}
}

func TestFindStreamingMarkdownSettledFrontier_BlankRunStartsAtFirstBlankLine(t *testing.T) {
	content := "para one\n\n\nstill streaming"
	frontier := markdownutil.FindStreamingSettledFrontier(content)
	want := len("para one\n")
	if frontier != want {
		t.Fatalf("want frontier=%d, got %d", want, frontier)
	}
}

func TestFindStreamingMarkdownSettledFrontier_UnclosedFence(t *testing.T) {
	// An open fence blocks frontier advancement inside it.
	content := "intro\n\n```go\nfunc main() {\n"
	frontier := markdownutil.FindStreamingSettledFrontier(content)
	// Frontier should point to the start of the blank run before the fence so
	// the tail keeps the separator visible.
	want := len("intro\n")
	if frontier != want {
		t.Fatalf("want frontier=%d (blank line before fence), got %d", want, frontier)
	}
}

func TestFindStreamingMarkdownSettledFrontier_ClosedFence(t *testing.T) {
	// A fully closed fence advances the frontier past its closing line.
	content := "intro\n\n```go\nfunc main() {}\n```\nafter"
	frontier := markdownutil.FindStreamingSettledFrontier(content)
	want := len("intro\n\n```go\nfunc main() {}\n```\n")
	if frontier != want {
		t.Fatalf("want frontier=%d, got %d", want, frontier)
	}
}

func TestFindStreamingMarkdownSettledFrontier_TildeFence(t *testing.T) {
	// Tilde fences should also close the frontier.
	content := "intro\n\n~~~sh\necho hello\n~~~\nmore"
	frontier := markdownutil.FindStreamingSettledFrontier(content)
	want := len("intro\n\n~~~sh\necho hello\n~~~\n")
	if frontier != want {
		t.Fatalf("want frontier=%d, got %d", want, frontier)
	}
}

func TestFindStreamingMarkdownSettledFrontier_EmptyContent(t *testing.T) {
	if got := markdownutil.FindStreamingSettledFrontier(""); got != 0 {
		t.Fatalf("empty content: want 0, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// Streaming incremental markdown rendering integration tests
// ---------------------------------------------------------------------------

func TestStreamingAssistantBlock_SettledPrefixRenderedAsMarkdown(t *testing.T) {
	ApplyTheme(DefaultTheme())
	// Content: one fully settled paragraph (separated by \n\n), then a tail.
	content := "## Hello\n\nstill streaming..."
	block := &Block{
		Type:      BlockAssistant,
		Content:   content,
		Streaming: true,
	}
	lines := block.Render(80, "")
	joined := stripANSI(strings.Join(lines, "\n"))
	// Settled prefix includes the heading — markdown rendering should produce it.
	if !strings.Contains(joined, "Hello") {
		t.Fatalf("expected heading text in streaming block, got:\n%s", joined)
	}
	if !strings.Contains(joined, "still streaming") {
		t.Fatalf("expected tail text in streaming block, got:\n%s", joined)
	}
}

func TestStreamingAssistantBlock_MetadataLengthConsistent(t *testing.T) {
	ApplyTheme(DefaultTheme())
	content := "First paragraph.\n\nSecond still streaming"
	block := &Block{
		Type:      BlockAssistant,
		Content:   content,
		Streaming: true,
	}
	block.Render(80, "")
	// After render the render-metadata slices should be consistent.
	if len(block.renderSyntheticPrefixWidths) != len(block.renderSoftWrapContinuations) {
		t.Fatalf("render metadata length mismatch: syntheticPrefixWidths=%d softWrapContinuations=%d",
			len(block.renderSyntheticPrefixWidths), len(block.renderSoftWrapContinuations))
	}
}

func TestStreamingAssistantBlock_PreservesParagraphSeamBlankLine(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		Type:      BlockAssistant,
		Content:   "First paragraph.\n\nSecond still streaming",
		Streaming: true,
	}
	block.Render(80, "")
	if len(block.mdCache) < 3 {
		t.Fatalf("expected at least 3 content lines, got %d: %#v", len(block.mdCache), block.mdCache)
	}
	if !strings.Contains(stripANSI(block.mdCache[0]), "First paragraph.") {
		t.Fatalf("first content line = %q, want first paragraph", stripANSI(block.mdCache[0]))
	}
	if block.mdCache[1] != "" {
		t.Fatalf("expected preserved blank seam line, got %q", block.mdCache[1])
	}
	if !strings.Contains(stripANSI(block.mdCache[2]), "Second still streaming") {
		t.Fatalf("third content line = %q, want second paragraph", stripANSI(block.mdCache[2]))
	}
}

func TestStreamingAssistantBlock_NoFrontierNoMarkdown(t *testing.T) {
	ApplyTheme(DefaultTheme())
	// Content without any paragraph boundary — no markdown rendering yet.
	content := "just plain text still coming"
	block := &Block{
		Type:      BlockAssistant,
		Content:   content,
		Streaming: true,
	}
	block.Render(80, "")
	if block.streamSettledLineCount != 0 {
		t.Fatalf("expected no settled lines when no frontier, got %d", block.streamSettledLineCount)
	}
}

func TestStreamingAssistantBlock_CacheReuseWhenFrontierUnchanged(t *testing.T) {
	ApplyTheme(DefaultTheme())
	content := "Settled para.\n\ntail"
	block := &Block{
		Type:      BlockAssistant,
		Content:   content,
		Streaming: true,
	}
	block.Render(80, "")
	if len(block.streamSettledLines) == 0 {
		t.Fatal("expected settled cache after first render")
	}
	// Capture settled lines pointer after first render.
	firstSettledPtr := &block.streamSettledLines[0]

	// Append to tail without advancing frontier, then invalidate the normal
	// render caches like the real streaming path does.
	block.Content = "Settled para.\n\ntail extended"
	block.InvalidateCache()
	block.Render(80, "")

	if len(block.streamSettledLines) == 0 {
		t.Fatal("expected settled cache after second render")
	}
	if firstSettledPtr != &block.streamSettledLines[0] {
		t.Fatalf("expected settled cache to be reused when frontier unchanged")
	}
}

func TestStreamingAssistantBlock_RebuildsSettledCacheWhenPrefixChangesAtSameFrontier(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		Type:      BlockAssistant,
		Content:   "Alpha\n\ntail",
		Streaming: true,
	}
	block.Render(80, "")
	if len(block.streamSettledLines) == 0 {
		t.Fatal("expected settled cache after first render")
	}
	firstSettledPtr := &block.streamSettledLines[0]

	// Same frontier length, different settled prefix text.
	block.Content = "Bravo\n\ntail"
	block.InvalidateCache()
	block.Render(80, "")

	if len(block.streamSettledLines) == 0 {
		t.Fatal("expected settled cache after second render")
	}
	if firstSettledPtr == &block.streamSettledLines[0] {
		t.Fatalf("expected settled cache to rebuild when settled prefix text changes")
	}
	if got := block.streamSettledRaw; got != "Bravo\n" {
		t.Fatalf("streamSettledRaw=%q want %q", got, "Bravo\n")
	}
}

func TestStreamingAssistantBlock_NoExtraEmptyTailLineWhenAllContentSettled(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		Type:      BlockAssistant,
		Content:   "intro\n\n```go\nfunc main() {}\n```\n",
		Streaming: true,
	}
	block.Render(80, "")
	if block.streamSettledLineCount == 0 {
		t.Fatal("expected settled prefix to be rendered")
	}
	if got, want := len(block.mdCache), block.streamSettledLineCount; got != want {
		t.Fatalf("mdCache lines=%d want %d (no extra tail lines when fully settled)", got, want)
	}
}

func TestStreamingAssistantBlock_SettledAfterStreamingMatchesFullRender(t *testing.T) {
	ApplyTheme(DefaultTheme())
	content := "## Title\n\nSome body text."
	// First render in streaming mode.
	streaming := &Block{
		Type:      BlockAssistant,
		Content:   content,
		Streaming: true,
	}
	streamingLines := streaming.Render(80, "")

	// Then settled (full markdown).
	settled := &Block{
		Type:      BlockAssistant,
		Content:   content,
		Streaming: false,
	}
	settledLines := settled.Render(80, "")

	// Both should contain the heading text — they may differ in ANSI details
	// during streaming but both must surface the content.
	joined := func(lines []string) string { return stripANSI(strings.Join(lines, "\n")) }
	if !strings.Contains(joined(streamingLines), "Title") {
		t.Fatalf("streaming render missing heading: %s", joined(streamingLines))
	}
	if !strings.Contains(joined(settledLines), "Title") {
		t.Fatalf("settled render missing heading: %s", joined(settledLines))
	}
}
