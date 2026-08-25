package tui

import (
	"strconv"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tools"
)

// benchmarkBashLargeStdout builds a ~1.66MB shell stdout shaped like a large
// diff transcript (12000 hunks), matching the worst-case expanded shell card
// described in the handoff scenario.
func benchmarkBashLargeStdout() string {
	var sb strings.Builder
	for hunk := range 12000 {
		sb.WriteString("stdout line ")
		sb.WriteString(strconv.Itoa(hunk))
		sb.WriteString(" with enough words to wrap several times at card width: lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor incididunt\n")
	}
	return sb.String()
}

// BenchmarkViewportRenderLargeExpandedToolAfterSpill measures the actual cold
// restore path: loading a spilled tool block and rendering the visible window
// after its in-memory render caches have been discarded.
func BenchmarkViewportRenderLargeExpandedToolAfterSpill(b *testing.B) {
	ApplyTheme(DefaultTheme())
	v := NewViewport(100, 44)
	block := benchmarkBashLargeBlock()
	v.AppendBlock(block)
	if !v.spillBlock(block) {
		b.Fatal("spillBlock() failed")
	}
	ref := *block.spillRef
	lineCounts := block.spillLineCounts
	v.maxHotBytes = 1 << 62
	v.sticky = false
	v.offset = 0

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		cold := &Block{
			ID:                     block.ID,
			Type:                   block.Type,
			ToolName:               block.ToolName,
			ToolCallDetailExpanded: true,
			Collapsed:              block.Collapsed,
			spillCold:              true,
			spillStore:             v.spill,
			spillRef:               &ref,
			spillLineCounts:        lineCounts,
			spillRecover:           v.recoverSpilledBlock,
		}
		v.blocks[0] = cold
		v.visibleBlocksCache = []*Block{cold}
		v.visibleBlocksDirty = false
		v.hotBytes = 0
		v.hotBudgetDirty = false
		v.hotBytesDirty = false
		_ = v.Render("", nil, -1, -1, "")
	}
}

func benchmarkBashLargeBlock() *Block {
	return &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               tools.NameShell,
		Content:                `{"command":"git diff --stat && git diff"}`,
		ResultContent:          benchmarkBashLargeStdout(),
		ResultDone:             true,
		ToolCallDetailExpanded: true,
	}
}

// BenchmarkRenderBashLargeCardCollapsed measures the collapsed default state:
// only the header and the one-line summary are assembled, never the body.
func BenchmarkRenderBashLargeCardCollapsed(b *testing.B) {
	ApplyTheme(DefaultTheme())
	block := benchmarkBashLargeBlock()
	block.ToolCallDetailExpanded = false
	block.Collapsed = true
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		block.InvalidateCache()
		_ = block.Render(100, "")
	}
}

// BenchmarkRenderBashLargeCardExpandedCold measures the one-time cost of
// expanding (or restoring after spill): the whole body is wrapped and styled.
func BenchmarkRenderBashLargeCardExpandedCold(b *testing.B) {
	ApplyTheme(DefaultTheme())
	block := benchmarkBashLargeBlock()
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		block.InvalidateCache()
		_ = block.Render(100, "")
	}
}

// BenchmarkRenderBashLargeCardExpandedWarmWindow measures the steady-state hot
// path: after the first full render, a visible window must be served from the
// cached line slice instead of re-assembling the whole card every frame.
func BenchmarkRenderBashLargeCardExpandedWarmWindow(b *testing.B) {
	ApplyTheme(DefaultTheme())
	block := benchmarkBashLargeBlock()
	_ = block.LineCount(100) // warm lineCache (Render alone does not populate it)
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		lines := block.RenderRange(100, "", 0, 44)
		if len(lines) != 44 {
			b.Fatalf("window render returned %d lines, want 44", len(lines))
		}
	}
}

// BenchmarkViewportRenderLargeExpandedToolWindow measures a full viewport frame
// whose visible window lies inside one large expanded tool card, with the card
// already hot in memory. This guards the "per-frame full re-render" regression
// that a partial-window viewport cache miss used to risk.
func BenchmarkViewportRenderLargeExpandedToolWindow(b *testing.B) {
	ApplyTheme(DefaultTheme())
	v := NewViewport(100, 44)
	for i := range 260 {
		v.AppendBlock(&Block{ID: 100 + i, Type: BlockAssistant, Content: strings.Repeat("filler ", 20)})
	}
	v.AppendBlock(benchmarkBashLargeBlock())
	v.ScrollToBottom()
	_ = v.Render("", nil, -1, -1, "") // warm line + block-position caches
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_ = v.Render("", nil, -1, -1, "")
	}
}
