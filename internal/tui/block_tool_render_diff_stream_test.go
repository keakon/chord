package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

func streamingApplyPatchTestBlock(lineCount int) *Block {
	lines := make([]string, 0, lineCount+4)
	lines = append(lines, "*** Begin Patch", "*** Update File: src/demo.go", "@@")
	for i := range lineCount {
		lines = append(lines, fmt.Sprintf("+const value%d = %d", i, i))
	}
	lines = append(lines, "*** End Patch")
	return &Block{
		ID:                 1,
		Type:               BlockToolCall,
		ToolName:           tools.NameApplyPatch,
		RawArgs:            `{"patch":"` + strings.ReplaceAll(strings.Join(lines, "\n"), "\n", `\n`),
		Collapsed:          false,
		ToolExecutionState: agent.ToolCallExecutionStateReceiving,
	}
}

// applyPatchStreamingTestBlock builds a receiving apply_patch block whose args
// carry patch verbatim, the shape the live streaming path sees.
func applyPatchStreamingTestBlock(patch string) *Block {
	return &Block{
		ID:                 1,
		Type:               BlockToolCall,
		ToolName:           tools.NameApplyPatch,
		RawArgs:            `{"patch":"` + strings.ReplaceAll(patch, "\n", `\n`) + `"}`,
		Collapsed:          false,
		ToolExecutionState: agent.ToolCallExecutionStateReceiving,
	}
}

// assertBlockRangeMatchesFullRender pins RenderRange to the cold Render output
// slice for the same block state: whichever path RenderRange takes must be
// indistinguishable from slicing a full render.
func assertBlockRangeMatchesFullRender(t *testing.T, width int, newBlock func() *Block) {
	t.Helper()
	full := newBlock().Render(width, "")
	block := newBlock()
	if got := block.LineCount(width); got != len(full) {
		t.Fatalf("line count = %d, full render = %d", got, len(full))
	}
	got := block.RenderRange(width, "", 0, len(full))
	if strings.Join(got, "\n") != strings.Join(full, "\n") {
		t.Fatalf("range differs from full render:\n got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(full, "\n"))
	}
}

func TestStreamingApplyPatchRangeMatchesFullRender(t *testing.T) {
	fullBlock := streamingApplyPatchTestBlock(1800)
	full := fullBlock.Render(100, "")
	if len(full) < 100 {
		t.Fatalf("full render has %d lines, want a large card", len(full))
	}

	block := streamingApplyPatchTestBlock(1800)
	if got := block.LineCount(100); got != len(full) {
		t.Fatalf("streaming line count = %d, full render = %d", got, len(full))
	}
	if block.lineCache != nil {
		t.Fatal("streaming line count populated the full line cache")
	}

	for _, span := range [][2]int{{0, 12}, {4, 5}, {700, 735}, {len(full) - 8, len(full)}} {
		start, end := span[0], span[1]
		got := block.RenderRange(100, "", start, end)
		want := full[start:end]
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("streaming range [%d:%d] differs from full render:\n got:\n%s\nwant:\n%s", start, end, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}

	partial := streamingApplyPatchTestBlock(1800)
	if got := partial.LineCount(100); got != len(full) {
		t.Fatalf("partial streaming line count = %d, full render = %d", got, len(full))
	}
	if partial.previewHL != nil {
		t.Fatal("line count should not initialize the preview highlighter")
	}
	partial.RenderRange(100, "", 700, 735)
	if partial.previewHL == nil || len(partial.previewHL.renderCache) >= 1800 {
		t.Fatalf("range render highlighted the whole patch: highlighter=%v cached=%d", partial.previewHL != nil, len(partial.previewHL.renderCache))
	}
}

// TestStreamingApplyPatchRangeSuppressesMoveAndDeletePreview covers the cold
// render rule that a patch with only delete or move/rename targets shows no
// "Requested patch" preview. The streaming range path must apply the same
// guard, otherwise the live card gains a section the finished card never had.
func TestStreamingApplyPatchRangeSuppressesMoveAndDeletePreview(t *testing.T) {
	cases := []struct {
		name  string
		patch string
	}{
		{
			name:  "delete-only",
			patch: "*** Begin Patch\n*** Delete File: src/removed.go\n*** End Patch",
		},
		{
			name:  "move-only",
			patch: "*** Begin Patch\n*** Update File: src/demo.go\n*** Move to: src/renamed.go\n*** End Patch",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newBlock := func() *Block { return applyPatchStreamingTestBlock(tc.patch) }
			block := newBlock()
			if !block.streamingApplyPatchRangeEligible() {
				t.Fatal("expected the block to take the streaming apply_patch range path")
			}
			full := newBlock().Render(100, "")
			if strings.Contains(stripANSI(strings.Join(full, "\n")), "Requested patch") {
				t.Fatal("cold render should suppress the Requested patch preview for move/delete-only targets")
			}
			assertBlockRangeMatchesFullRender(t, 100, newBlock)
		})
	}
}

// TestStreamingApplyPatchRangeExcludedStatesMatchFullRender pins the fallback
// contract: any state that is not eligible for the streaming range path must
// still return exactly the full-render slice.
func TestStreamingApplyPatchRangeExcludedStatesMatchFullRender(t *testing.T) {
	base := func() *Block {
		return &Block{
			ID:                 1,
			Type:               BlockToolCall,
			ToolName:           tools.NameApplyPatch,
			RawArgs:            `{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old\n+new line\n*** End Patch"}`,
			ToolExecutionState: agent.ToolCallExecutionStateReceiving,
		}
	}
	cases := []struct {
		name   string
		mutate func(*Block)
	}{
		{"result-done", func(b *Block) { b.ResultDone = true; b.ResultContent = "Done!" }},
		{"error", func(b *Block) { b.ResultStatus = agent.ToolResultStatusError }},
		{"cancelled", func(b *Block) { b.ResultStatus = agent.ToolResultStatusCancelled }},
		{"collapsed", func(b *Block) { b.Collapsed = true }},
		{"other-tool", func(b *Block) { b.ToolName = tools.NameRead; b.RawArgs = ""; b.Content = "file contents" }},
		{"diff-already-available", func(b *Block) { b.Diff = "--- a/src/demo.go\n+++ b/src/demo.go\n" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newBlock := func() *Block {
				b := base()
				tc.mutate(b)
				return b
			}
			if newBlock().streamingApplyPatchRangeEligible() {
				t.Fatal("expected the block to fall back from the streaming range path")
			}
			assertBlockRangeMatchesFullRender(t, 100, newBlock)
		})
	}

	// A non-positive width is normalized to 80 by both Render and the streaming
	// range path, so the two must stay equivalent there as well.
	t.Run("zero-width", func(t *testing.T) {
		block := base()
		if !block.streamingApplyPatchRangeEligible() {
			t.Fatal("expected the block to take the streaming apply_patch range path")
		}
		assertBlockRangeMatchesFullRender(t, 0, base)
	})
}

func TestStreamingApplyPatchRangeKeepsLongLinesEquivalent(t *testing.T) {
	line := "+" + strings.Repeat("x", 240)
	args := `{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n@@\n` + line + `\n*** End Patch`
	newBlock := func() *Block {
		return &Block{
			ID:                 1,
			Type:               BlockToolCall,
			ToolName:           tools.NameApplyPatch,
			RawArgs:            args,
			Collapsed:          false,
			ToolExecutionState: agent.ToolCallExecutionStateReceiving,
		}
	}
	fullBlock := newBlock()
	if !fullBlock.streamingApplyPatchRangeEligible() {
		t.Fatal("block must take the streaming apply_patch range path")
	}
	full := fullBlock.Render(60, "")
	block := newBlock()
	if !block.streamingApplyPatchRangeEligible() {
		t.Fatal("block must take the streaming apply_patch range path")
	}
	if got := block.RenderRange(60, "", 4, 5); strings.Join(got, "\n") != full[4] {
		t.Fatalf("long-line range differs from full render:\n got: %s\nwant: %s", strings.Join(got, "\n"), full[4])
	}
}

// streamingArgsApplyPatchTestBlock builds a receiving apply_patch block whose
// args are still arriving: the JSON document has no closing quote yet, so the
// strict parser cannot read it and the card falls back to the streaming
// preview, exactly like the live card.
func streamingArgsApplyPatchTestBlock(patch string) *Block {
	block := &Block{
		ID:                 1,
		Type:               BlockToolCall,
		ToolName:           tools.NameApplyPatch,
		Collapsed:          false,
		ToolExecutionState: agent.ToolCallExecutionStateReceiving,
	}
	block.RawArgs = `{"patch":"` + strings.ReplaceAll(patch, "\n", `\n`)
	block.Content = block.cachedApplyPatchStreamingArgs(block.RawArgs)
	return block
}

// TestStreamingApplyPatchHighlightsMultiFilePatch pins the lexer seed for a
// live multi-file patch. The header shows a display summary ("path +1 files"),
// and feeding that summary to lexerForFilePath resolves to no lexer at all,
// which used to turn every preview line into plain text for the rest of the
// stream.
func TestStreamingApplyPatchHighlightsMultiFilePatch(t *testing.T) {
	patch := "*** Begin Patch\n" +
		"*** Update File: internal/tools/jobs_tools.go\n" +
		"@@\n" +
		"+func demo() int {\n" +
		"+\treturn 1\n" +
		"+}\n" +
		"*** Update File: internal/tools/jobs_tools_test.go\n" +
		"@@\n" +
		"+func sample() {}\n" +
		"*** End Patch"
	block := streamingArgsApplyPatchTestBlock(patch)
	if !block.streamingApplyPatchRangeEligible() {
		t.Fatal("block must take the streaming apply_patch range path")
	}

	lines := block.RenderRange(100, "", 0, block.LineCount(100))
	joined := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "internal/tools/jobs_tools.go +1 files") {
		t.Fatalf("header must keep the display summary:\n%s", joined)
	}
	bodyLine := ""
	for _, line := range lines {
		if strings.Contains(stripANSI(line), "func demo() int {") {
			bodyLine = line
			break
		}
	}
	if bodyLine == "" {
		t.Fatalf("preview does not render the first file's body:\n%s", joined)
	}

	hl := block.previewHL
	if hl == nil || hl.cachedLexer == nil {
		t.Fatalf("streaming preview resolved no lexer: highlighter=%v", hl)
	}
	code := "func demo() int {"
	highlighted := hl.highlightLine(code, diffAddBg)
	if !strings.Contains(highlighted, "\x1b[38;2;") {
		t.Fatalf("highlighter emitted no syntax colour for %q: %q", code, highlighted)
	}
	// preserveBackground re-inserts the card background after every reset, so the
	// highlighted run is not one contiguous substring of the rendered line; every
	// coloured segment of it must still land in the line.
	for _, segment := range strings.Split(highlighted, "\x1b[m") {
		if segment != "" && !strings.Contains(bodyLine, segment) {
			t.Fatalf("rendered patch line is missing highlighter segment %q:\n%s", segment, bodyLine)
		}
	}
}

// TestDiffToolPathsSeparateDisplaySummaryFromLexerSeed pins the split between
// the header display summary and the path handed to the syntax highlighter.
func TestDiffToolPathsSeparateDisplaySummaryFromLexerSeed(t *testing.T) {
	multi := "*** Begin Patch\n" +
		"*** Update File: src/demo.go\n" +
		"@@\n-a\n+b\n" +
		"*** Update File: src/other.go\n" +
		"@@\n-c\n+d\n" +
		"*** End Patch"
	block := applyPatchStreamingTestBlock(multi)
	display, syntax := block.diffToolPathsWithTargets(block.applyPatchTargets())
	if want := "src/demo.go +1 files"; display != want {
		t.Fatalf("display path = %q, want %q", display, want)
	}
	if want := "src/demo.go"; syntax != want {
		t.Fatalf("syntax path = %q, want %q", syntax, want)
	}
	if lexerForFilePath(display) != nil {
		t.Fatal("a decorated display summary must not resolve to a lexer")
	}
	if lexerForFilePath(syntax) == nil {
		t.Fatal("syntax path must resolve to a lexer")
	}

	rename := applyPatchStreamingTestBlock("*** Begin Patch\n*** Update File: src/demo.go\n*** Move to: src/renamed.go\n@@\n-a\n+b\n*** End Patch")
	renameDisplay, renameSyntax := rename.diffToolPathsWithTargets(rename.applyPatchTargets())
	if !strings.Contains(renameDisplay, "→") {
		t.Fatalf("rename display = %q, want the source → target summary", renameDisplay)
	}
	if want := "src/renamed.go"; renameSyntax != want {
		t.Fatalf("rename syntax path = %q, want %q", renameSyntax, want)
	}

	del := applyPatchStreamingTestBlock("*** Begin Patch\n*** Delete File: src/removed.go\n*** End Patch")
	delDisplay, delSyntax := del.diffToolPathsWithTargets(del.applyPatchTargets())
	if want := "D src/removed.go"; delDisplay != want {
		t.Fatalf("delete display = %q, want %q", delDisplay, want)
	}
	if want := "src/removed.go"; delSyntax != want {
		t.Fatalf("delete syntax path = %q, want %q", delSyntax, want)
	}

	// Args that are still streaming cannot be parsed as targets: the display
	// keeps the summary while the syntax path stays a real path.
	streaming := streamingArgsApplyPatchTestBlock(multi)
	if got := streaming.applyPatchTargets(); len(got) != 0 {
		t.Fatalf("streaming args must not parse into targets, got %#v", got)
	}
	streamDisplay, streamSyntax := streaming.diffToolPathsWithTargets(nil)
	if want := "src/demo.go +1 files"; streamDisplay != want {
		t.Fatalf("streaming display path = %q, want %q", streamDisplay, want)
	}
	if want := "src/demo.go"; streamSyntax != want {
		t.Fatalf("streaming syntax path = %q, want %q", streamSyntax, want)
	}
}
