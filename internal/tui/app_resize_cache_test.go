package tui

import (
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	tea "github.com/keakon/bubbletea/v2"
	uv "github.com/keakon/ultraviolet"
)

func assertOnlyResizeCacheSweep(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	msgs := testCmdMsgs(cmd)
	if _, ok := testFindMsg[resizeCacheSweepTickMsg](msgs); !ok || len(msgs) != 1 {
		t.Fatalf("immediate layout must schedule only cache cleanup, got %v", msgs)
	}
}

func TestResizeCacheSweepCoalescesWithoutPostponing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewModelWithSize(nil, 120, 40)
		cmd := m.handleWindowSizeUpdate(tea.WindowSizeMsg{Width: 150, Height: 50})
		if cmd == nil || !m.resizeCacheSweepScheduled {
			t.Fatal("applied resize did not schedule cleanup")
		}
		results := make(chan tea.Msg, 1)
		go func() { results <- cmd() }()
		synctest.Wait()
		time.Sleep(15 * time.Second)
		if next := m.handleWindowSizeUpdate(tea.WindowSizeMsg{Width: 160, Height: 55}); next != nil {
			t.Fatal("second resize scheduled another command")
		}
		select {
		case <-results:
			t.Fatal("cleanup fired before 30 seconds")
		default:
		}
		time.Sleep(15 * time.Second)
		synctest.Wait()
		var tick resizeCacheSweepTickMsg
		select {
		case msg := <-results:
			var ok bool
			tick, ok = msg.(resizeCacheSweepTickMsg)
			if !ok {
				t.Fatalf("timer returned %T", msg)
			}
		default:
			t.Fatal("later resize postponed cleanup")
		}
		_, next := m.Update(tick)
		if next != nil || m.resizeCacheSweepScheduled {
			t.Fatal("cleanup rescheduled without a new resize")
		}
		if m.width != 160 || m.height != 55 {
			t.Fatal("cleanup used the earlier dimensions")
		}
		cmd = m.handleWindowSizeUpdate(tea.WindowSizeMsg{Width: 140, Height: 45})
		if cmd == nil || m.resizeCacheSweepGeneration == tick.generation {
			t.Fatal("new resize did not schedule an independent sweep")
		}
		m.handleResizeCacheSweep(tick)
		if !m.resizeCacheSweepScheduled {
			t.Fatal("stale timer consumed a newer sweep")
		}
	})
}

func TestResizeCacheSweepRequiresAppliedSizeChange(t *testing.T) {
	stubTUITicks(t)
	m := NewModelWithSize(nil, 120, 40)
	for _, msg := range []tea.WindowSizeMsg{{Width: 120, Height: 40}, {Width: 0, Height: 40}} {
		if cmd := m.handleWindowSizeUpdate(msg); cmd != nil || m.resizeCacheSweepScheduled {
			t.Fatal("unchanged or invalid size scheduled cleanup")
		}
	}
	msgs := testCmdMsgs(m.handleWindowSizeUpdate(tea.WindowSizeMsg{Width: 119, Height: 39}))
	apply, ok := testFindMsg[applyResizeMsg](msgs)
	if !ok || m.resizeCacheSweepScheduled {
		t.Fatal("pending shrink must wait for layout before scheduling cleanup")
	}
	// A superseded shrink must not clean a layout that was never applied.
	m.handleWindowSizeUpdate(tea.WindowSizeMsg{Width: 120, Height: 40})
	if cmd := m.handleApplyResize(apply); cmd != nil || m.resizeCacheSweepScheduled {
		t.Fatal("reverted shrink scheduled cleanup")
	}
	msgs = testCmdMsgs(m.handleWindowSizeUpdate(tea.WindowSizeMsg{Width: 119, Height: 39}))
	apply, _ = testFindMsg[applyResizeMsg](msgs)
	msgs = testCmdMsgs(m.handleApplyResize(apply))
	if _, ok := testFindMsg[resizeCacheSweepTickMsg](msgs); !ok || !m.resizeCacheSweepScheduled {
		t.Fatal("applied debounced resize did not schedule cleanup")
	}
}

func resizeCacheTestViewport(t testing.TB) *Viewport {
	t.Helper()
	v := NewViewport(182, 55)
	t.Cleanup(func() { _ = v.Close() })
	for i := range 20 {
		v.AppendBlock(&Block{ID: i + 1, Type: BlockAssistant, Content: strings.Repeat("Sample paragraph with **styled text** for layout inspection.\n\n", 12)})
	}
	v.sticky = false
	v.offset = 0
	v.Render("", nil, -1, 0, "")
	v.sticky = true
	v.scrollToEnd()
	v.SetSize(166, 47)
	v.Render("", nil, -1, 0, "")
	return v
}

func TestResizeCacheSweepPreservesVisibleContentAndProtectedCards(t *testing.T) {
	v := resizeCacheTestViewport(t)
	for _, setup := range []struct {
		index int
		apply func(*Block)
	}{
		{1, func(b *Block) { b.Focused = true }},
		{2, func(b *Block) { b.Streaming = true }},
		{3, func(b *Block) { b.UserLocalShellPending = true }},
		{4, func(b *Block) { b.Type = BlockToolCall; b.ResultDone = false }},
	} {
		setup.apply(v.blocks[setup.index])
	}
	first := v.blocks[0]
	if first.viewportCacheWidth != 182 || first.lineCacheWidth != 166 {
		t.Fatal("fixture must retain a stale viewport cache after resize")
	}
	before := v.Render("", nil, -1, 0, "")
	starts := append([]int(nil), v.blockStarts()...)
	total, offset, resident := v.TotalLines(), v.offset, v.ResidentBytes()
	protected := make([][]string, 4)
	for i := range protected {
		protected[i] = v.blocks[i+1].mdCache
	}
	m := &Model{viewport: v, resizeCacheSweepScheduled: true, resizeCacheSweepGeneration: 1}
	m.handleResizeCacheSweep(resizeCacheSweepTickMsg{generation: 1})
	if first.viewportCache != nil || first.mdCache != nil || first.Content == "" {
		t.Fatal("offscreen derived caches were not released independently of content")
	}
	for i, cache := range protected {
		if cache == nil || !reflect.DeepEqual(cache, v.blocks[i+1].mdCache) {
			t.Fatalf("protected card %d lost its cache", i+1)
		}
	}
	if v.ResidentBytes() >= resident {
		t.Fatal("cleanup did not reduce resident cache estimates")
	}
	if v.TotalLines() != total || v.offset != offset || !reflect.DeepEqual(starts, v.blockStarts()) || v.Render("", nil, -1, 0, "") != before {
		t.Fatal("cleanup changed visible content or scroll geometry")
	}
	// A duplicate delivery cannot clear caches created after the sweep.
	first.mdCache = []string{"rebuilt"}
	m.handleResizeCacheSweep(resizeCacheSweepTickMsg{generation: 1})
	if first.mdCache == nil {
		t.Fatal("duplicate timer swept without a new resize")
	}
}

func TestResizeCacheSweepUsesReplacementViewport(t *testing.T) {
	stubTUITicks(t)
	m := NewModelWithSize(nil, 215, 61)
	msgs := testCmdMsgs(m.handleWindowSizeUpdate(tea.WindowSizeMsg{Width: 199, Height: 51}))
	tick, ok := testFindMsg[resizeCacheSweepTickMsg](msgs)
	if !ok {
		t.Fatal("missing cleanup tick")
	}
	previous := m.viewport
	previous.AppendBlock(&Block{ID: 100, Type: BlockAssistant, Content: "Previous session"})
	previous.blocks[0].mdCache = []string{"previous cache"}
	m.viewport = resizeCacheTestViewport(t)
	m.handleResizeCacheSweep(tick)
	if previous.blocks[0].mdCache == nil || m.viewport.blocks[0].mdCache != nil {
		t.Fatal("cleanup did not exclusively use the current viewport")
	}
}

func TestResizeCacheSweepShrinksCanvasWithoutChangingCells(t *testing.T) {
	m := &Model{width: 199, height: 51, resizeCacheSweepScheduled: true, resizeCacheSweepGeneration: 1}
	m.ensureScreenBuffer(215, 61)
	m.screenBuf.Lines[0][0].Content = "visible"
	m.screenBuf.Lines[60][214].Content = "hidden"
	m.ensureScreenBuffer(199, 51)
	m.renderScratchBuffer(215)
	before := cloneScreenLines(nil, m.screenBuf.Lines)
	oldCell := &m.screenBuf.Lines[0][0]
	m.handleResizeCacheSweep(resizeCacheSweepTickMsg{generation: 1})
	if oldCell == &m.screenBuf.Lines[0][0] || !screenLinesEqual(before, m.screenBuf.Lines) {
		t.Fatal("canvas was not compacted while preserving cells")
	}
	if cap(m.screenBuf.Lines) != 51 || cap(m.screenBuf.Lines[0]) != 199 || m.renderScratchOK || m.renderScratch.RenderBuffer != nil {
		t.Fatal("oversized storage retained")
	}
	cell := &m.screenBuf.Lines[0][0]
	m.shrinkResizeScreenBuffer()
	if cell != &m.screenBuf.Lines[0][0] {
		t.Fatal("exact-sized canvas was needlessly reallocated")
	}
	m.ensureScreenBuffer(215, 61)
	if !m.screenBuf.Lines[60][214].Equal(&uv.EmptyCell) {
		t.Fatal("growing the compacted canvas restored hidden content")
	}
}

func TestResizeCacheSweepPastZeroHeightBlock(t *testing.T) {
	v := resizeCacheTestViewport(t)
	v.blocks[0].Content = ""
	v.blocks[0].InvalidateCache()
	v.recalcTotalLines()
	if v.blockSpansCache[0] != 0 {
		t.Fatal("fixture must have a zero-height leading block")
	}
	m := &Model{viewport: v, resizeCacheSweepScheduled: true, resizeCacheSweepGeneration: 1}
	m.handleResizeCacheSweep(resizeCacheSweepTickMsg{generation: 1})
	if v.blocks[1].mdCache != nil {
		t.Fatal("zero-height block prevented cleanup of later offscreen caches")
	}
}

func BenchmarkResizeCacheSweep(b *testing.B) {
	for _, sweep := range []bool{false, true} {
		name := "retain"
		if sweep {
			name = "release"
		}
		b.Run(name, func(b *testing.B) {
			v := resizeCacheTestViewport(b)
			m := &Model{viewport: v}
			b.ReportAllocs()
			for b.Loop() {
				width := 182
				if v.width == width {
					width = 166
				}
				v.SetSize(width, 47)
				v.Render("", nil, -1, 0, "")
				if sweep {
					m.resizeCacheSweepScheduled = true
					m.handleResizeCacheSweep(resizeCacheSweepTickMsg{})
				}
			}
			b.ReportMetric(float64(v.ResidentBytes()), "resident-B")
		})
	}
}
