package tui

import (
	"image"
	"image/color"
	"strings"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"
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
	styled := uv.Cell{Content: "─", Width: 1, Style: uv.Style{Fg: uv.ColorFrom(color.RGBA{R: 255, G: 0, B: 0, A: 255})}}
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

	styled := uv.Cell{Content: "X", Width: 1, Style: uv.Style{Fg: uv.ColorFrom(color.RGBA{R: 255, G: 0, B: 0, A: 255})}}
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

func TestDrawCachedRenderableSkipsRowsOutsideCachedContent(t *testing.T) {
	m := NewModelWithSize(nil, 40, 10)
	cache := &cachedRenderable{
		lines: [][]uv.Cell{
			{{Content: "A", Width: 1}},
		},
	}
	scr := newCountingScreen(8, 4)
	area := image.Rect(0, 0, 8, 4)

	m.drawCachedRenderable(scr, area, cache)

	if scr.setCalls != area.Dx() {
		t.Fatalf("drawCachedRenderable should only write cached row width, got %d SetCell calls want %d", scr.setCalls, area.Dx())
	}
}

func TestRenderToCachePreservesAllPlainTextCells(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	var cache cachedRenderable
	text := "hello\nworld"
	m.renderToCache(&cache, text)
	if cache.text != text {
		t.Fatalf("cache.text = %q, want %q", cache.text, text)
	}
	if len(cache.lines) != 2 {
		t.Fatalf("len(cache.lines) = %d, want 2", len(cache.lines))
	}
	var got []string
	for _, line := range cache.lines {
		var sb strings.Builder
		for i := range line {
			if line[i].IsZero() || line[i].Content == "" {
				continue
			}
			sb.WriteString(stripANSI(line[i].Content))
		}
		got = append(got, sb.String())
	}
	if strings.Join(got, "\n") != text {
		t.Fatalf("cached lines = %q, want %q", strings.Join(got, "\n"), text)
	}
}

func TestEnsureScreenBufferReusesExistingBuffer(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.ensureScreenBuffer(80, 24)
	if m.screenBuf.RenderBuffer == nil {
		t.Fatal("ensureScreenBuffer should initialize screen buffer")
	}
	ptr := m.screenBuf.RenderBuffer
	m.ensureScreenBuffer(80, 24)
	if m.screenBuf.RenderBuffer != ptr {
		t.Fatal("ensureScreenBuffer should reuse buffer when size is unchanged")
	}
	m.ensureScreenBuffer(100, 30)
	if m.screenBuf.RenderBuffer != ptr {
		t.Fatal("ensureScreenBuffer should resize existing buffer instead of replacing it")
	}
	if got := m.screenBuf.Bounds(); got.Dx() != 100 || got.Dy() != 30 {
		t.Fatalf("screen buffer bounds = %v, want 100x30", got)
	}
}

func TestStreamingAssistantUsesCheapWrapPath(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{Type: BlockAssistant, Streaming: true, Content: "- bullet one that wraps nicely\n- bullet two that also wraps"}
	lines := block.Render(50, "")
	if len(lines) == 0 {
		t.Fatal("streaming assistant should render lines")
	}
	if block.mdCacheWidth != 50 {
		t.Fatalf("mdCacheWidth = %d, want 50", block.mdCacheWidth)
	}
	if len(block.streamTailSoftWrapContinuations) != len(block.streamTailLines) {
		t.Fatalf("tail soft wrap metadata len = %d, want %d", len(block.streamTailSoftWrapContinuations), len(block.streamTailLines))
	}
	for _, wrapped := range block.streamTailSoftWrapContinuations {
		if wrapped {
			t.Fatal("streaming cheap path should not mark synthetic markdown soft-wrap continuations")
		}
	}
}

func TestHasVisibleInlineImageRequiresVisibleRenderedImage(t *testing.T) {
	v := NewViewport(80, 5)
	block := &Block{
		ID:   1,
		Type: BlockUser,
		ImageParts: []BlockImagePart{{
			RenderRows:      3,
			RenderStartLine: 1,
			RenderEndLine:   3,
		}},
	}
	v.AppendBlock(block)
	if !v.HasVisibleInlineImage() {
		t.Fatal("expected visible rendered image to be detected")
	}
	v.offset = 10
	if v.HasVisibleInlineImage() {
		t.Fatal("off-screen image should not be reported as visible")
	}
	block.ImageParts[0].RenderRows = 0
	v.offset = 0
	if v.HasVisibleInlineImage() {
		t.Fatal("image with no rendered rows should not be visible")
	}
}

func TestNewScreenBufferUsesGraphemeWidth(t *testing.T) {
	canvas := newScreenBuffer(80, 24)

	// This exact sequence appears in session logs and overflows the right panel
	// when counted with wcwidth-style rules instead of grapheme-aware width.
	s := "\u200d\u2640\ufe0f"
	if got := canvas.WidthMethod().StringWidth(s); got != 2 {
		t.Fatalf("screen buffer width(%q) = %d, want 2", s, got)
	}
}

func TestSpaceToggleInvalidatesMainRenderCache(t *testing.T) {
	m := NewModelWithSize(nil, 100, 24)
	m.mode = ModeNormal
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "shell",
		Content:       `{"command":"echo first"}`,
		ResultContent: "first",
		ResultDone:    true,
		Collapsed:     true,
	}
	m.viewport.AppendBlock(block)
	m.recalcViewportSize()

	// Space without a focused block toggles the card under the current offset.
	// The main-area draw cache is keyed on mainRenderKey (which includes the
	// viewport render version); without a version bump after the toggle the
	// cached frame is reused and the change only becomes visible after a scroll.
	keyBefore := m.mainRenderKey(ModeNormal, 100)
	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace}))
	keyAfter := m.mainRenderKey(ModeNormal, 100)
	if keyBefore == keyAfter {
		t.Fatal("Space toggle left mainRenderKey unchanged; cached main frame would be reused until a scroll changes the offset")
	}
	if !block.ToolCallDetailExpanded {
		t.Fatal("Space should expand the shell card (ToolCallDetailExpanded)")
	}
	// Toggling again must advance the render version once more (two-way switch).
	keyAfterFirst := keyAfter
	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace}))
	if got := m.mainRenderKey(ModeNormal, 100); got == keyAfterFirst {
		t.Fatal("second Space toggle left mainRenderKey unchanged")
	}
	if block.ToolCallDetailExpanded {
		t.Fatal("second Space should collapse the shell card again")
	}
}
