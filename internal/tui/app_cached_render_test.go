package tui

import (
	"image/color"
	"testing"
	"time"

	uv "github.com/keakon/ultraviolet"
)

func TestInvalidateDrawCachesPreservesRuntimeState(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	deferred := &startupDeferredTranscriptState{
		startedAt:              time.Now(),
		originalViewportBudget: m.viewport.maxHotBytes,
	}
	startedAt := time.Now().Add(-3 * time.Second)
	m.animRunning = true
	m.statusBarTickGeneration = 11
	m.statusBarTickScheduled = true
	m.terminalTitleTickRunning = true
	m.terminalTitleTickGeneration = 13
	m.terminalTitleRequestBlinkOff = true
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, UserLocalShellCmd: "echo hi", UserLocalShellPending: true, StartedAt: startedAt})
	m.startupDeferredTranscript = deferred
	m.startupDeferredPreheatGeneration = 17

	m.cachedMainKey = "main-cache"
	m.cachedMainRender = cachedRenderable{text: "cached main"}
	m.cachedMainSearchBlockIndex = 42
	m.cachedInputKey = "input-cache"
	m.cachedStatusKey = "status-cache"
	m.cachedInfoPanelOut = "panel-cache"
	m.infoPanelHitBoxes = []infoPanelSectionHitBox{{startY: 1}}
	m.statusBarAgentSnapshotDirty = false

	m.invalidateDrawCaches()

	if !m.animRunning {
		t.Fatal("animRunning should survive draw-cache invalidation")
	}
	if m.statusBarTickGeneration != 11 || !m.statusBarTickScheduled {
		t.Fatalf("status bar tick state = generation %d scheduled %t, want generation 11 scheduled true", m.statusBarTickGeneration, m.statusBarTickScheduled)
	}
	if m.terminalTitleTickGeneration != 13 || !m.terminalTitleTickRunning || !m.terminalTitleRequestBlinkOff {
		t.Fatalf("terminal title ticker state = generation %d running %t blinkOff %t, want generation 13 running true blinkOff true", m.terminalTitleTickGeneration, m.terminalTitleTickRunning, m.terminalTitleRequestBlinkOff)
	}
	if got, ok := m.viewport.LatestVisiblePendingUserLocalShellStartedAt(); !ok || !got.Equal(startedAt) {
		t.Fatalf("pending terminal start = %v ok=%t, want %v true", got, ok, startedAt)
	}
	if m.startupDeferredTranscript != deferred || m.startupDeferredPreheatGeneration != 17 {
		t.Fatalf("startup deferred state = %p generation %d, want %p generation 17", m.startupDeferredTranscript, m.startupDeferredPreheatGeneration, deferred)
	}

	if m.cachedMainKey != "" || m.cachedMainRender.text != "" || m.cachedInputKey != "" || m.cachedStatusKey != "" || m.cachedInfoPanelOut != "" {
		t.Fatalf("render cache was not cleared: mainKey=%q mainText=%q inputKey=%q statusKey=%q infoOut=%q", m.cachedMainKey, m.cachedMainRender.text, m.cachedInputKey, m.cachedStatusKey, m.cachedInfoPanelOut)
	}
	if m.cachedMainSearchBlockIndex != -1 {
		t.Fatalf("cachedMainSearchBlockIndex = %d, want -1", m.cachedMainSearchBlockIndex)
	}
	if !m.statusBarAgentSnapshotDirty {
		t.Fatal("statusBarAgentSnapshotDirty should be set after draw-cache invalidation")
	}
	if m.infoPanelHitBoxes != nil {
		t.Fatalf("infoPanelHitBoxes = %#v, want nil", m.infoPanelHitBoxes)
	}
}

func TestMainRenderKeyIncludesSearchQuery(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	base := m.mainRenderKey(m.mode, 80)
	m.search.State.Query = "needle"
	if got := m.mainRenderKey(m.mode, 80); got == base {
		t.Fatalf("mainRenderKey() did not change when search query changed; base=%q", base)
	}
}

func TestSetThemePreservesDeferredStartupTranscriptRuntimeState(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	deferred := &startupDeferredTranscriptState{
		startedAt:              time.Now(),
		originalViewportBudget: m.viewport.maxHotBytes,
	}
	m.startupDeferredTranscript = deferred
	m.startupDeferredPreheatGeneration = 23
	m.viewport.maxHotBytes = startupDeferredTranscriptAggressiveHotBytes

	theme := DefaultTheme()
	theme.Name = "runtime-preserving-test"
	m.SetTheme(theme)

	if m.startupDeferredTranscript != deferred {
		t.Fatalf("startupDeferredTranscript = %p, want %p after SetTheme", m.startupDeferredTranscript, deferred)
	}
	if m.startupDeferredPreheatGeneration != 23 {
		t.Fatalf("startupDeferredPreheatGeneration = %d, want 23 after SetTheme", m.startupDeferredPreheatGeneration)
	}
	if m.viewport.maxHotBytes != startupDeferredTranscriptAggressiveHotBytes {
		t.Fatalf("viewport maxHotBytes = %d, want %d after SetTheme", m.viewport.maxHotBytes, startupDeferredTranscriptAggressiveHotBytes)
	}
}

func BenchmarkApplyWheelScrollDeltaLargeTranscript(b *testing.B) {
	m := NewModelWithSize(nil, 120, 40)
	m.viewport = benchmarkLargeViewport(5000)
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		m.viewport.offset = 1000
		m.applyWheelScrollDelta(600)
	}
}

func TestApplyWheelScrollDeltaBulkPathMatchesDirectViewportScroll(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.viewport = benchmarkLargeViewport(200)
	m.viewport.offset = 50
	m.applyWheelScrollDelta(37)
	if got, want := m.viewport.offset, 87; got != want {
		t.Fatalf("offset after bulk scroll down = %d, want %d", got, want)
	}
	m.applyWheelScrollDelta(-25)
	if got, want := m.viewport.offset, 62; got != want {
		t.Fatalf("offset after bulk scroll up = %d, want %d", got, want)
	}
}

func TestDrawCachedRenderableToClearedAreaClearsStaleCellsWhenSourceLineEmpty(t *testing.T) {
	m := NewModel(nil)
	width := 20
	buf := uv.NewScreenBuffer(width, 1)

	// Seed the row with styled content that should be cleared.
	styled := uv.Cell{Content: "─", Width: 1, Style: uv.Style{Fg: color.RGBA{R: 255, G: 0, B: 0, A: 255}}}
	for x := range width {
		buf.SetCell(x, 0, &styled)
	}

	cache := cachedRenderable{lines: [][]uv.Cell{nil}}
	m.drawCachedRenderableToClearedArea(buf, buf.Bounds(), &cache)

	line := buf.Line(0)
	for x := range width {
		if !line[x].Equal(&uv.EmptyCell) {
			t.Fatalf("cell[%d] = %#v, want EmptyCell", x, line[x])
		}
	}
}

func TestDrawCachedRenderableToClearedAreaClearsStaleCellsBeyondSourceHeight(t *testing.T) {
	m := NewModel(nil)
	width := 10
	height := 3
	buf := uv.NewScreenBuffer(width, height)

	styled := uv.Cell{Content: "X", Width: 1, Style: uv.Style{Fg: color.RGBA{R: 255, G: 0, B: 0, A: 255}}}
	for y := range height {
		for x := range width {
			buf.SetCell(x, y, &styled)
		}
	}

	// Only provide one source line; the remaining rows must be cleared.
	cache := cachedRenderable{lines: [][]uv.Cell{{{Content: "A", Width: 1}}}}
	m.drawCachedRenderableToClearedArea(buf, buf.Bounds(), &cache)

	// First row should be overwritten (starts with "A"), remaining rows empty.
	row0 := buf.Line(0)
	if row0[0].Content != "A" {
		t.Fatalf("row0[0].Content = %q, want %q", row0[0].Content, "A")
	}
	for y := 1; y < height; y++ {
		row := buf.Line(y)
		for x := range width {
			if !row[x].Equal(&uv.EmptyCell) {
				t.Fatalf("cell[%d,%d] = %#v, want EmptyCell", x, y, row[x])
			}
		}
	}
}
