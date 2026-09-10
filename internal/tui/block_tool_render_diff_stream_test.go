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

func TestStreamingApplyPatchRangeKeepsLongLinesEquivalent(t *testing.T) {
	line := "+" + strings.Repeat("x", 240)
	args := `{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n@@\n` + line + `\n*** End Patch`
	fullBlock := &Block{
		ID:        1,
		Type:      BlockToolCall,
		ToolName:  tools.NameApplyPatch,
		RawArgs:   args,
		Collapsed: false,
	}
	full := fullBlock.Render(60, "")
	block := &Block{
		ID:        1,
		Type:      BlockToolCall,
		ToolName:  tools.NameApplyPatch,
		RawArgs:   args,
		Collapsed: false,
	}
	if got := block.RenderRange(60, "", 4, 5); strings.Join(got, "\n") != full[4] {
		t.Fatalf("long-line range differs from full render:\n got: %s\nwant: %s", strings.Join(got, "\n"), full[4])
	}
}
