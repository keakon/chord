package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestMain(m *testing.M) {
	origClipboardWriteAll := clipboardWriteAll
	clipboardWriteAll = func(string) error { return nil }
	code := m.Run()
	clipboardWriteAll = origClipboardWriteAll
	os.Exit(code)
}

type countingScreen struct {
	*uv.Buffer
	method   ansi.Method
	setCalls int
}

func newCountingScreen(width, height int) *countingScreen {
	return &countingScreen{
		Buffer: uv.NewBuffer(width, height),
		method: ansi.WcWidth,
	}
}

func (s *countingScreen) Bounds() uv.Rectangle {
	return s.Buffer.Bounds()
}

func (s *countingScreen) CellAt(x, y int) *uv.Cell {
	return s.Buffer.CellAt(x, y)
}

func (s *countingScreen) SetCell(x, y int, c *uv.Cell) {
	s.setCalls++
	s.Buffer.SetCell(x, y, c)
}

func (s *countingScreen) WidthMethod() uv.WidthMethod {
	return s.method
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

func TestScheduleStreamFlushCoalescesUntilConsumed(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	first := m.scheduleStreamFlush(0)
	if first == nil {
		t.Fatal("first scheduleStreamFlush should return a tick command")
	}
	if !m.streamFlushScheduled {
		t.Fatal("scheduleStreamFlush should mark flush as scheduled")
	}
	gen := m.streamFlushGeneration
	if second := m.scheduleStreamFlush(0); second != nil {
		t.Fatal("second scheduleStreamFlush before consume should be coalesced")
	}
	if !m.consumeStreamFlush(streamFlushTickMsg{generation: gen}) {
		t.Fatal("consumeStreamFlush should accept current generation")
	}
	if m.streamFlushScheduled {
		t.Fatal("consumeStreamFlush should clear scheduled flag")
	}
	if third := m.scheduleStreamFlush(0); third == nil {
		t.Fatal("scheduleStreamFlush should schedule again after consume")
	}
}

func TestScheduleStreamFlushRejectsStaleGeneration(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	_ = m.scheduleStreamFlush(0)
	if m.consumeStreamFlush(streamFlushTickMsg{generation: m.streamFlushGeneration - 1}) {
		t.Fatal("consumeStreamFlush should reject stale generation")
	}
	if !m.streamFlushScheduled {
		t.Fatal("stale generation should not clear scheduled flag")
	}
}

func TestConsumeScrollFlushCoalescesUntilConsumed(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	first := m.scheduleScrollFlush(16 * time.Millisecond)
	if first == nil {
		t.Fatal("first scheduleScrollFlush should return a tick command")
	}
	if !m.scrollFlushScheduled {
		t.Fatal("scheduleScrollFlush should mark flush as scheduled")
	}
	gen := m.scrollFlushGeneration
	if second := m.scheduleScrollFlush(16 * time.Millisecond); second != nil {
		t.Fatal("second scheduleScrollFlush before consume should be coalesced")
	}
	if cmd := m.consumeScrollFlush(scrollFlushTickMsg{generation: gen - 1}); cmd != nil {
		t.Fatal("consumeScrollFlush should ignore stale generation")
	}
	if !m.scrollFlushScheduled {
		t.Fatal("stale generation should not clear scheduled flag")
	}
	m.pendingScrollDelta = 6
	m.viewport = nil
	m.SetFocusResizeFreezeEnabled(false)
	if cmd := m.consumeScrollFlush(scrollFlushTickMsg{generation: gen}); cmd != nil {
		t.Fatal("consumeScrollFlush without viewport should not emit redraw cmd")
	}
	if m.scrollFlushScheduled {
		t.Fatal("consumeScrollFlush should clear scheduled flag")
	}
	if m.pendingScrollDelta != 0 {
		t.Fatal("consumeScrollFlush should clear pendingScrollDelta")
	}
	if third := m.scheduleScrollFlush(16 * time.Millisecond); third == nil {
		t.Fatal("scheduleScrollFlush should schedule again after consume")
	}
}

func TestHostRedrawCmdDisabledWithoutFreezeWorkaround(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.SetFocusResizeFreezeEnabled(false)
	if cmd := m.hostRedrawCmd("scroll-flush"); cmd != nil {
		t.Fatal("hostRedrawCmd should be disabled when freeze workaround is off")
	}
	if m.lastHostRedrawReason != "" {
		t.Fatalf("lastHostRedrawReason = %q, want empty", m.lastHostRedrawReason)
	}
}

func TestHostRedrawCmdRequiresNotFrozen(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.SetFocusResizeFreezeEnabled(true)
	m.focusResizeFrozen = true
	if cmd := m.hostRedrawCmd("scroll-flush"); cmd != nil {
		t.Fatal("hostRedrawCmd should skip while focus resize freeze is active")
	}
	if m.lastHostRedrawReason != "" {
		t.Fatalf("lastHostRedrawReason = %q, want empty", m.lastHostRedrawReason)
	}
}

func TestHostRedrawCmdThrottlesRepeatedRequests(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.SetFocusResizeFreezeEnabled(true)
	first := m.hostRedrawCmd("scroll-flush")
	if first == nil {
		t.Fatal("first hostRedrawCmd should emit redraw command")
	}
	if second := m.hostRedrawCmd("stream-flush"); second != nil {
		t.Fatal("second hostRedrawCmd inside throttle window should be coalesced")
	}
}

func TestConsumeScrollFlushAddsHostRedrawForFreezeWorkaround(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.SetFocusResizeFreezeEnabled(true)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: strings.Repeat("hello\n", 80)})
	m.width = 40
	m.viewport.width = 40
	m.viewport.height = 5
	m.recalcViewportSize()
	m.viewport.ScrollToTop()
	m.pendingScrollDelta = 20
	m.scrollFlushScheduled = true
	m.scrollFlushGeneration = 1
	cmd := m.consumeScrollFlush(scrollFlushTickMsg{generation: 1})
	if cmd == nil {
		t.Fatal("consumeScrollFlush should emit redraw command when viewport moved")
	}
	if m.viewport.offset == 0 {
		t.Fatal("expected consumeScrollFlush to move viewport down")
	}
	if m.lastHostRedrawReason != "scroll-flush" {
		t.Fatalf("lastHostRedrawReason = %q, want scroll-flush", m.lastHostRedrawReason)
	}
}

func TestStreamBoundaryFlushTriggersContentBoundaryRedrawForFreezeWorkaround(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.SetFocusResizeFreezeEnabled(true)

	cmd := m.requestStreamBoundaryFlush()
	if cmd == nil {
		t.Fatal("stream boundary flush should schedule commands")
	}
	if m.lastHostRedrawReason != "content-boundary" {
		t.Fatalf("lastHostRedrawReason = %q, want content-boundary", m.lastHostRedrawReason)
	}
}

func TestContentBoundaryRedrawThrottlesAgainstRecentHostRedraw(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.SetFocusResizeFreezeEnabled(true)
	m.lastHostRedrawAt = time.Now().Add(-contentBoundaryHostRedrawMinInterval / 2)
	m.lastHostRedrawReason = "scroll-flush"

	if cmd := m.hostRedrawForContentBoundaryCmd("content-boundary"); cmd != nil {
		t.Fatal("content boundary redraw should be throttled after a recent host redraw")
	}
	if m.lastHostRedrawReason != "scroll-flush" {
		t.Fatalf("lastHostRedrawReason = %q, want scroll-flush", m.lastHostRedrawReason)
	}
}

func TestSendDraftTriggersLiveAppendRedrawForFreezeWorkaround(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.SetFocusResizeFreezeEnabled(true)

	cmd := m.sendDraft(queuedDraft{Content: "hello", QueuedAt: time.Now()})
	if cmd == nil {
		t.Fatal("sendDraft should schedule live append redraw command")
	}
	if m.lastHostRedrawReason != "live-append" {
		t.Fatalf("lastHostRedrawReason = %q, want live-append", m.lastHostRedrawReason)
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

func TestMessagesToBlocksRendersLoopNoticeAsStatusCard(t *testing.T) {
	msgs := []message.Message{{Role: "user", Content: "LOOP\n\nTarget:\n- finish current task", Kind: "loop_notice"}}
	nextID := 1
	blocks := messagesToBlocks(msgs, &nextID)
	if len(blocks) != 1 {
		t.Fatalf("block count = %d, want 1", len(blocks))
	}
	if blocks[0].Type != BlockStatus {
		t.Fatalf("block type = %v, want BlockStatus", blocks[0].Type)
	}
	if blocks[0].StatusTitle != "LOOP" {
		t.Fatalf("StatusTitle = %q, want %q", blocks[0].StatusTitle, "LOOP")
	}
	plain := stripANSI(strings.Join(blocks[0].Render(80, ""), "\n"))
	if !strings.Contains(plain, "LOOP") || !strings.Contains(plain, "finish current task") {
		t.Fatalf("rendered status card = %q, want persisted loop card content", plain)
	}
	if !strings.Contains(plain, "  Target:") || !strings.Contains(plain, "  • finish current task") {
		t.Fatalf("rendered status card = %q, want indented loop body", plain)
	}
}

func TestParseDiagnosticsBundleCommand(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{input: "/diagnostics", want: "/diagnostics", ok: true},
		{input: " /diagnostics ", want: "/diagnostics", ok: true},
		{input: "/DIAGNOSTICS", want: "/DIAGNOSTICS", ok: true},
		{input: "/diagnostics extra", want: "/diagnostics extra", ok: true},
		{input: "/stats", want: "", ok: false},
	}
	for _, tt := range tests {
		got, ok := parseDiagnosticsBundleCommand(tt.input)
		if got != tt.want || ok != tt.ok {
			t.Fatalf("parseDiagnosticsBundleCommand(%q) = (%q, %t), want (%q, %t)", tt.input, got, ok, tt.want, tt.ok)
		}
	}
}

func TestAppendLocalStatusCardDefersWhileAssistantStreamIsActive(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	block := &Block{ID: m.nextBlockID, Type: BlockAssistant, Content: "streaming", Streaming: true}
	m.nextBlockID++
	m.currentAssistantBlock = block
	m.assistantBlockAppended = true
	m.appendViewportBlock(block)

	m.appendLocalStatusCard("EXPORT", "written")

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 {
		t.Fatalf("visible block count = %d, want 1 while streaming", len(blocks))
	}
	if got := len(m.pendingLocalStatusCards); got != 1 {
		t.Fatalf("pendingLocalStatusCards = %d, want 1", got)
	}
	if m.currentAssistantBlock != block {
		t.Fatal("currentAssistantBlock should remain active")
	}
}

func TestFinalizeTurnFlushesDeferredLocalStatusCards(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	block := &Block{ID: m.nextBlockID, Type: BlockAssistant, Content: "streaming", Streaming: true}
	m.nextBlockID++
	m.currentAssistantBlock = block
	m.assistantBlockAppended = true
	m.appendViewportBlock(block)
	m.pendingLocalStatusCards = []localStatusCard{{title: "DIAGNOSTICS", content: "bundle ready"}}

	m.finalizeTurn()

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 2 {
		t.Fatalf("visible block count = %d, want 2 after finalize", len(blocks))
	}
	last := blocks[len(blocks)-1]
	if last.StatusTitle != "DIAGNOSTICS" || last.Content != "bundle ready" {
		t.Fatalf("last block = %#v, want diagnostics status card", last)
	}
	if got := len(m.pendingLocalStatusCards); got != 0 {
		t.Fatalf("pendingLocalStatusCards = %d, want 0", got)
	}
	if m.currentAssistantBlock != nil {
		t.Fatal("currentAssistantBlock should be finalized")
	}
}

func TestTUIDiagnosticRingBufferKeepsNewestEvents(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	for i := 0; i < maxTUIDiagnosticEvents+5; i++ {
		m.recordTUIDiagnostic("event", "n=%d", i)
	}
	events := m.snapshotTUIDiagnosticEvents()
	if len(events) != maxTUIDiagnosticEvents {
		t.Fatalf("len(events) = %d, want %d", len(events), maxTUIDiagnosticEvents)
	}
	if !strings.Contains(events[0].Detail, "n=5") {
		t.Fatalf("first event = %q, want n=5", events[0].Detail)
	}
	if !strings.Contains(events[len(events)-1].Detail, "n=132") {
		t.Fatalf("last event = %q, want n=132", events[len(events)-1].Detail)
	}
}

func TestTUIDiagnosticCoalescesConsecutiveIdenticalEvents(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.recordTUIDiagnostic("focus", "gained=true")
	for i := 0; i < 200; i++ {
		m.recordTUIDiagnostic("host-redraw-skip", "reason=stream-flush disallowed=true")
	}
	m.recordTUIDiagnostic("focus-settle", "generation=1")

	events := m.snapshotTUIDiagnosticEvents()
	if len(events) != 3 {
		t.Fatalf("len(events) = %d, want 3 (coalesced)", len(events))
	}
	if events[0].Kind != "focus" {
		t.Fatalf("events[0].Kind = %q, want focus", events[0].Kind)
	}
	skip := events[1]
	if skip.Kind != "host-redraw-skip" {
		t.Fatalf("events[1].Kind = %q, want host-redraw-skip", skip.Kind)
	}
	if skip.RepeatCount != 199 {
		t.Fatalf("events[1].RepeatCount = %d, want 199", skip.RepeatCount)
	}
	if skip.FirstAt.IsZero() {
		t.Fatal("events[1].FirstAt should not be zero")
	}
	if !skip.At.After(skip.FirstAt) {
		t.Fatalf("events[1].At (%v) should be after FirstAt (%v)", skip.At, skip.FirstAt)
	}
	if events[2].Kind != "focus-settle" {
		t.Fatalf("events[2].Kind = %q, want focus-settle", events[2].Kind)
	}
}

func TestTUIDiagnosticCoalesceDoesNotMergeDifferentDetail(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.recordTUIDiagnostic("host-redraw-skip", "reason=stream-flush")
	m.recordTUIDiagnostic("host-redraw-skip", "reason=scroll-flush")
	m.recordTUIDiagnostic("host-redraw-skip", "reason=stream-flush")

	events := m.snapshotTUIDiagnosticEvents()
	if len(events) != 3 {
		t.Fatalf("len(events) = %d, want 3 (different details not coalesced)", len(events))
	}
}

func TestTUIDiagnosticCoalescesToolCallUpdateLengthChanges(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.recordTUIDiagnostic("focus", "focused=true")
	for i := 0; i < maxTUIDiagnosticEvents+10; i++ {
		m.recordTUIDiagnostic("tool-call-update", "tool=TodoWrite id=call-1 block=79 len=%d->%d", i, i+1)
	}
	m.recordTUIDiagnostic("host-redraw", "reason=scroll-flush")

	events := m.snapshotTUIDiagnosticEvents()
	if len(events) != 3 {
		t.Fatalf("len(events) = %d, want 3 (tool-call-update coalesced)", len(events))
	}
	if events[0].Kind != "focus" {
		t.Fatalf("events[0].Kind = %q, want focus", events[0].Kind)
	}
	update := events[1]
	if update.Kind != "tool-call-update" {
		t.Fatalf("events[1].Kind = %q, want tool-call-update", update.Kind)
	}
	if update.RepeatCount != maxTUIDiagnosticEvents+9 {
		t.Fatalf("events[1].RepeatCount = %d, want %d", update.RepeatCount, maxTUIDiagnosticEvents+9)
	}
	if !strings.Contains(update.Detail, "len=137->138") {
		t.Fatalf("events[1].Detail = %q, want latest length", update.Detail)
	}
	if events[2].Kind != "host-redraw" {
		t.Fatalf("events[2].Kind = %q, want host-redraw", events[2].Kind)
	}
}

func TestBuildTUIDiagnosticDumpIncludesKeySections(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, Content: "hello"})
	m.recordTUIDiagnostic("test", "something happened")
	m.lastImageProtocolReason = "focus-settle:inline-replay"
	m.lastImageProtocolSummary = "backend=kitty visible_inline=false kitty_visible_parts=0 kitty_seq_bytes=0 cmds=0"

	path, dump, err := m.buildDiagnosticDump(time.Date(2026, 3, 30, 12, 34, 56, 0, time.UTC), "/debug tui-dump")
	if err != nil {
		t.Fatalf("buildTUIDiagnosticDump error: %v", err)
	}
	if !strings.Contains(path, filepath.Join("logs", "tui-dumps")) {
		t.Fatalf("dump path = %q, want runtime tui-dumps dir", path)
	}
	for _, want := range []string{
		"[model]",
		"[layout]",
		"[viewport]",
		"[recent_events]",
		"[blocks.visible]",
		"[blocks.rendered]",
		"[viewport_render]",
		"[screen_buffer]",
		"something happened",
		"hello",
		"last_image_protocol_reason",
		"last_host_redraw",
		"kitty_placement_cache_len",
		"display_state",
		"background_idle_since",
		"idle_sweep_generation",
		"hot_budget_dirty",
		"hot_bytes_dirty",
		"max_hot_bytes",
	} {
		if !strings.Contains(dump, want) {
			t.Fatalf("dump missing %q\n%s", want, dump)
		}
	}
}

func TestBuildTUIDiagnosticDumpTruncatesHugeRenderedSectionsInMiddle(t *testing.T) {
	rows := make([]string, 0, 400)
	for i := 0; i < 400; i++ {
		rows = append(rows, fmt.Sprintf("%03d line-%03d", i+1, i))
	}
	content := strings.Join(rows, "\n")
	if len(strings.Split(content, "\n")) <= tuiDiagnosticDumpSectionMaxLines {
		t.Fatal("test fixture must exceed dump section limit")
	}

	var sb strings.Builder
	writeDiagnosticDumpSection(&sb, content)
	dump := sb.String()
	if !strings.Contains(dump, "... truncated ") {
		t.Fatalf("section missing truncation marker\n%s", dump)
	}
	if !strings.Contains(dump, "line-000") {
		t.Fatalf("section missing head content\n%s", dump)
	}
	if !strings.Contains(dump, "line-399") {
		t.Fatalf("section missing tail content\n%s", dump)
	}
}

func TestHandleInsertDiagnosticsCommandReturnsCmd(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeInsert
	m.input.SetValue("/diagnostics")

	cmd := m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if cmd == nil {
		t.Fatal("expected diagnostics export command")
	}
}

func TestDiagnosticsBundleSuccessTriggersStatusCardAndRedraw(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.SetFocusResizeFreezeEnabled(true)

	updated, cmd := m.Update(diagnosticsBundleMsg{path: "/tmp/chord-diagnostics.zip"})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd == nil {
		t.Fatal("successful diagnostics export should schedule toast and redraw commands")
	}
	if model.lastHostRedrawReason != "diagnostics-bundle" {
		t.Fatalf("lastHostRedrawReason = %q, want diagnostics-bundle", model.lastHostRedrawReason)
	}
	blocks := model.viewport.visibleBlocks()
	if len(blocks) == 0 {
		t.Fatal("expected diagnostics status card")
	}
	last := blocks[len(blocks)-1]
	if last.StatusTitle != "DIAGNOSTICS" {
		t.Fatalf("StatusTitle = %q, want DIAGNOSTICS", last.StatusTitle)
	}
	if !strings.Contains(last.Content, "Before sharing it") {
		t.Fatalf("status content = %q, want sensitive-content reminder", last.Content)
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
	if len(block.mdCacheSoftWrapContinuations) != len(block.mdCache) {
		t.Fatalf("soft wrap metadata len = %d, want %d", len(block.mdCacheSoftWrapContinuations), len(block.mdCache))
	}
	for _, wrapped := range block.mdCacheSoftWrapContinuations {
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

func TestRenderStatusBarShowsRulesMode(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.mode = ModeRules
	m.layout = m.generateLayout(m.width, m.height)
	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "RULES") {
		t.Fatalf("status bar should include RULES pill, got %q", plain)
	}
}

func TestRenderStatusBarShowsSessionIDOnRight(t *testing.T) {
	backend := &sessionControlAgent{sessionSummary: &agent.SessionSummary{ID: "1775115074902"}}
	m := NewModelWithSize(backend, 180, 24)
	m.workingDir = "/home/user/projects/myapp"
	m.layout = m.generateLayout(m.width, m.height)

	got := stripANSI(m.renderStatusBar())
	if !strings.Contains(got, "SID 1775115074902") {
		t.Fatalf("status bar should show prefixed session id, got %q", got)
	}
	if m.statusSession.display == "" || m.statusSession.value != "1775115074902" {
		t.Fatalf("status session region = %+v, want visible session id", m.statusSession)
	}
}

func TestRulesModeConsumesMouseEventsAndDoesNotPassThrough(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.mode = ModeRules
	m.layout = m.generateLayout(m.width, m.height)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "hello"})

	updated, cmd := m.Update(tea.MouseClickMsg{X: 1, Y: 1, Button: tea.MouseLeft})
	model := updated.(*Model)
	if cmd != nil {
		t.Fatalf("rules overlay click should not schedule command, got %#v", cmd)
	}
	if model.focusedBlockID != 0 && model.focusedBlockID != -1 {
		// focusedBlockID defaults to 0 in some constructors; ensure it didn't switch to block 1.
		if model.focusedBlockID == 1 {
			t.Fatalf("rules overlay should consume clicks; focusedBlockID=%d", model.focusedBlockID)
		}
	}
	if model.focusedBlockID == 1 {
		t.Fatalf("rules overlay should consume clicks; focusedBlockID=%d", model.focusedBlockID)
	}
	blk := model.viewport.GetFocusedBlock(1)
	if blk != nil && blk.Focused {
		t.Fatal("rules overlay should not change underlying block focus")
	}
}

func TestRenderStatusBarHidesSessionIDWhenNarrow(t *testing.T) {
	backend := &sessionControlAgent{sessionSummary: &agent.SessionSummary{ID: "1775115074902"}}
	m := NewModelWithSize(backend, 70, 24)
	m.layout = m.generateLayout(m.width, m.height)

	got := stripANSI(m.renderStatusBar())
	if strings.Contains(got, "1775115074902") {
		t.Fatalf("status bar should hide session id when narrow, got %q", got)
	}
	if m.statusSession.display != "" {
		t.Fatalf("status session region should be hidden, got %+v", m.statusSession)
	}
}

func TestRenderStatusBarUsesForegroundOnlyStatusElements(t *testing.T) {
	m := NewModel(nil)
	m.width = 140
	m.workingDir = "/home/user/projects/myapp"

	got := m.renderStatusBar()
	if m.statusPath.display == "" {
		t.Fatal("status path should be rendered")
	}

	plain := stripANSI(got)
	if !strings.Contains(plain, m.statusPath.display) {
		t.Fatalf("status bar plain text %q does not contain path %q", plain, m.statusPath.display)
	}

	pathSegment := StatusBarPathStyle.Render(m.statusPath.display)
	if !strings.Contains(got, pathSegment) {
		t.Fatalf("status bar should include styled path segment %q; got %q", pathSegment, got)
	}

	if strings.Contains(got, "48;5;") {
		t.Fatalf("status bar should not include background ANSI sequences; got %q", got)
	}
}

func TestRenderStatusBarShowsLatestCumulativeProgressAfterMultipleAgentEvents(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 17_869, Events: 105}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 18_041, Events: 106}})
	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "↓ 18 KB · 106 events · 0s") {
		t.Fatalf("status bar should show latest cumulative progress, got %q", plain)
	}
}

func TestRequestCycleStartedResetsProgressForNewRequest(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 300 * 1024, Events: 120}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestCycleStartedEvent{AgentID: "main", TurnID: 2}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 5 * 1024, Events: 2}})
	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "↓ 5.0 KB · 2 events") {
		t.Fatalf("new request cycle should reset progress state before next bytes arrive, got %q", plain)
	}
}

func TestPendingDraftConsumedResetsProgressBaselineForNewRequest(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	m.mode = ModeNormal
	m.requestProgress["main"] = requestProgressState{VisibleBytes: 300 * 1024, VisibleEvents: 120}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.PendingDraftConsumedEvent{DraftID: "draft-1", Parts: []message.ContentPart{{Type: "text", Text: "continue"}}, AgentID: "main"}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{Type: agent.ActivityConnecting, AgentID: "main"}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 305 * 1024, Events: 122}})
	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "↓ 5.0 KB · 2 events") {
		t.Fatalf("pending draft consumed should reset request progress baseline for the next request, got %q", plain)
	}
}

func TestRequestProgressStartsFromZeroOnFirstStreamTextAssistantCard(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.requestProgress["main"] = requestProgressState{VisibleBytes: 200 * 1024, VisibleEvents: 80}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{AgentID: "main", Text: "hi"}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 205 * 1024, Events: 82}})
	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "↓ 5.0 KB · 2 events · 0s") {
		t.Fatalf("assistant stream should start from zero baseline on first text, got %q", plain)
	}
}

func TestRequestProgressStartsFromZeroForNewAssistantCard(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.requestProgress["main"] = requestProgressState{VisibleBytes: 200 * 1024, VisibleEvents: 80}
	m.markRequestProgressBaseline("main")
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 205 * 1024, Events: 82}})
	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "↓ 5.0 KB · 2 events · 0s") {
		t.Fatalf("new assistant card progress should start from zero baseline, got %q", plain)
	}
}

func TestRequestProgressResetsPerCardAcrossAssistantToolAssistant(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 200 * 1024, Events: 80}})
	m.markRequestProgressBaseline("main")
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 220 * 1024, Events: 90}})
	plain1 := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain1, "↓ 20 KB · 10 events · 0s") {
		t.Fatalf("first assistant card progress = %q, want delta from card baseline", plain1)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallExecutionEvent{ID: "tool-1", Name: "Bash", AgentID: "main", State: agent.ToolCallExecutionStateRunning}})
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityExecuting, AgentID: "main"}
	m.activityStartTime["main"] = time.Now().Add(-3 * time.Second)
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 225 * 1024, Events: 93}})
	plain2 := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain2, "⚙ 3s") {
		t.Fatalf("tool card should use executing style, got %q", plain2)
	}

	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.markRequestProgressBaseline("main")
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 230 * 1024, Events: 95}})
	plain3 := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain3, "↓ 5.0 KB · 2 events") {
		t.Fatalf("second assistant card progress should restart from zero, got %q", plain3)
	}
}

func TestRenderStatusBarShowsZeroByteDownloadForWaitingDownloadStates(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityWaitingHeaders, AgentID: "main"}
	m.activityStartTime["main"] = time.Now()
	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "↓ 0 B · 0s") {
		t.Fatalf("waiting_headers should render zero-byte download state, got %q", plain)
	}

	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityWaitingToken, AgentID: "main"}
	plain = stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "↓ 0 B · 0s") {
		t.Fatalf("waiting_token should render zero-byte download state, got %q", plain)
	}
}

func TestRenderStatusBarShowsRequestProgressForMainRuntimeID(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main-1", Bytes: 128 * 1024, Events: 42}})
	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "↓ 128 KB · 42 events · 0s") {
		t.Fatalf("status bar should map main runtime agent id to main progress lane, got %q", plain)
	}
}

func TestRenderStatusBarShowsRequestProgressAfterAgentEventInjection(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 128 * 1024, Events: 42}})
	plain := stripANSI(m.renderStatusBar())
	if strings.Contains(plain, "Streaming") {
		t.Fatalf("status bar should not show legacy streaming label after progress injection, got %q", plain)
	}
	if !strings.Contains(plain, "↓ 128 KB · 42 events · 0s") {
		t.Fatalf("status bar should show request progress summary after progress injection, got %q", plain)
	}
}

func TestRenderStatusBarShowsRequestProgressInsteadOfStreamingLabel(t *testing.T) {
	m := NewModel(nil)
	m.width = 180
	m.workingDir = "/home/user/projects/myapp"
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.requestProgress["main"] = requestProgressState{VisibleBytes: 128 * 1024, VisibleEvents: 42}

	got := stripANSI(m.renderStatusBar())
	if strings.Contains(got, "Streaming") {
		t.Fatalf("status bar should not show legacy streaming label when request progress exists; got %q", got)
	}
	if !strings.Contains(got, "↓ 128 KB · 42 events · 0s") {
		t.Fatalf("status bar should show request progress summary in new icon style; got %q", got)
	}
}

func TestRequestProgressDoneImmediatelyStopsShowingPreviousRequestLength(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 128 * 1024, Events: 42}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 128 * 1024, Events: 42, Done: true}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{Type: agent.ActivityCompacting, AgentID: "main"}})
	plain := stripANSI(m.renderStatusBar())
	if strings.Contains(plain, "128 KB") || strings.Contains(plain, "42 events") {
		t.Fatalf("status bar should drop finished request length immediately when switching request, got %q", plain)
	}
	if !strings.Contains(plain, "■") && !strings.Contains(plain, "▪") {
		t.Fatalf("status bar should switch to compacting immediately after previous request done, got %q", plain)
	}
}

func TestLeavingRequestActivityClearsPreviousRequestProgress(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 64 * 1024, Events: 10}})
	// Switching to ActivityExecuting should NOT clear request progress yet —
	// tool arg streaming may still be in flight and RequestProgressEvent{Done:true}
	// has not arrived. The status bar will show executing state (⚙) because
	// buildStatusBarActivityDisplay checks activity type first, but the progress
	// data is retained until explicitly done or a new cycle starts.
	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{Type: agent.ActivityExecuting, AgentID: "main"}})
	if _, ok := m.requestProgress["main"]; !ok {
		t.Fatal("request progress should be retained during ActivityExecuting until Done event arrives")
	}
	plain := stripANSI(m.renderStatusBar())
	// Status bar shows executing icon, not download progress, because activity type is Executing
	if !strings.Contains(plain, "⚙") {
		t.Fatalf("status bar should show executing state, got %q", plain)
	}
	// Now simulate RequestProgressEvent{Done:true} — this should clear the progress
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 64 * 1024, Events: 10, Done: true}})
	if _, ok := m.requestProgress["main"]; ok {
		t.Fatal("request progress should be cleared after Done event")
	}
}

func TestRequestDoneThenNextConnectingStartsAtZero(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 128 * 1024, Events: 42}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 128 * 1024, Events: 42, Done: true}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{Type: agent.ActivityConnecting, AgentID: "main"}})
	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "↓ 0 B · 0s") {
		t.Fatalf("next request connecting should start from zero immediately after previous request done, got %q", plain)
	}
	if strings.Contains(plain, "128 KB") || strings.Contains(plain, "42 events") {
		t.Fatalf("next request connecting should not inherit previous request length, got %q", plain)
	}
}

func TestRenderStatusBarShowsLoopStateImmediatelyAfterEnableEvent(t *testing.T) {
	backend := &sessionControlAgent{currentRole: "builder"}
	m := NewModelWithSize(backend, 120, 24)

	backend.loopState = agent.LoopStateExecuting
	backend.loopTarget = "finish current task"
	backend.loopIteration = 1
	backend.loopMaxIterations = 10
	_ = m.handleAgentEvent(agentEventMsg{event: agent.LoopStateChangedEvent{}})

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "LOOP 1/10") {
		t.Fatalf("status bar = %q, want immediate loop pill after enable event", plain)
	}
}

func TestRenderStatusBarShowsLoopStateAfterRuntimeEnable(t *testing.T) {
	backend := &sessionControlAgent{currentRole: "builder"}
	backend.loopState = ""
	backend.loopTarget = "finish current task"
	backend.loopIteration = 1
	backend.loopMaxIterations = 10
	backend.loopState = agent.LoopStateExecuting
	m := NewModelWithSize(backend, 120, 24)
	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "LOOP 1/10") {
		t.Fatalf("status bar = %q, want loop pill after runtime enable", plain)
	}
}

func TestRenderStatusBarShowsLoopStateForMainAgent(t *testing.T) {
	backend := &sessionControlAgent{currentRole: "builder", loopState: agent.LoopStateExecuting, loopTarget: "finish current task", loopIteration: 1, loopMaxIterations: 10}
	m := NewModelWithSize(backend, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityExecuting, AgentID: "main"}

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "LOOP 1/10") {
		t.Fatalf("status bar = %q, want loop pill with iteration info", plain)
	}
	if strings.Contains(plain, "finish current task") {
		t.Fatalf("status bar = %q, should not expose loop target text", plain)
	}
}

func TestRenderStatusBarShowsLoopIterationWithoutLimitWhenUnlimited(t *testing.T) {
	backend := &sessionControlAgent{currentRole: "builder", loopState: agent.LoopStateExecuting, loopIteration: 1, loopMaxSet: true, loopMaxIterations: 0}
	m := NewModelWithSize(backend, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityExecuting, AgentID: "main"}

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "LOOP 1") {
		t.Fatalf("status bar = %q, want loop iteration without limit", plain)
	}
	if strings.Contains(plain, "LOOP 1/") {
		t.Fatalf("status bar = %q, should not show max iteration when unlimited", plain)
	}
}

func TestRenderStatusBarDoesNotShowLoopWhenDisabled(t *testing.T) {
	backend := &sessionControlAgent{currentRole: "builder"}
	m := NewModelWithSize(backend, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityExecuting, AgentID: "main"}

	plain := stripANSI(m.renderStatusBar())
	if strings.Contains(plain, "Loop") {
		t.Fatalf("status bar = %q, should not show loop text when loop is disabled", plain)
	}
}

func TestRenderStatusBarLoopPillNotSqueezedByEscHint(t *testing.T) {
	// When loop is active, nextEscHint() returns "" so the LOOP pill is not
	// pushed past the activity center and clipped by renderStatusBarPlacedLine.
	backend := &sessionControlAgent{currentRole: "builder", loopState: agent.LoopStateExecuting, loopIteration: 1, loopMaxIterations: 10}
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityExecuting, AgentID: "main"}

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "LOOP 1/10") {
		t.Fatalf("status bar = %q, want LOOP pill visible", plain)
	}
	if strings.Contains(plain, "disable loop") {
		t.Fatalf("status bar = %q, should not contain esc hint 'disable loop' that would squeeze LOOP pill", plain)
	}
}

func TestSlashCompletionHidesLoopCommandsWhenSubAgentFocused(t *testing.T) {
	m := NewModelWithSize(&sessionControlAgent{}, 100, 30)
	m.focusedAgentID = "sub-1"
	matches := m.getSlashCompletions("/l")
	joined := make([]string, 0, len(matches))
	for _, item := range matches {
		joined = append(joined, item.Cmd)
	}
	got := strings.Join(joined, "\n")
	if strings.Contains(got, "/loop on") || strings.Contains(got, "/loop off") {
		t.Fatalf("slash completions = %q, should not show /loop commands when subagent is focused", got)
	}
}

func TestSlashCompletionShowsLoopCommandsForPrefixL(t *testing.T) {
	m := NewModel(nil)
	matches := m.getSlashCompletions("/l")
	joined := make([]string, 0, len(matches))
	for _, item := range matches {
		joined = append(joined, item.Cmd)
	}
	got := strings.Join(joined, "\n")
	if !strings.Contains(got, "/loop on") {
		t.Fatalf("slash completions = %q, want /loop on", got)
	}
	if strings.Contains(got, "/loop off") {
		t.Fatalf("slash completions = %q, should not show /loop off when loop is disabled", got)
	}
}

func TestSlashCompletionShowsLoopOffWhenLoopEnabled(t *testing.T) {
	backend := &sessionControlAgent{loopState: agent.LoopStateExecuting}
	m := NewModel(backend)
	matches := m.getSlashCompletions("/l")
	joined := make([]string, 0, len(matches))
	for _, item := range matches {
		joined = append(joined, item.Cmd)
	}
	got := strings.Join(joined, "\n")
	if !strings.Contains(got, "/loop off") {
		t.Fatalf("slash completions = %q, want /loop off", got)
	}
	if strings.Contains(got, "/loop on") {
		t.Fatalf("slash completions = %q, should not show /loop on when loop is enabled", got)
	}
}

func TestSlashCompletionShowsLoopOnAfterLoopStops(t *testing.T) {
	m := NewModel(&sessionControlAgent{})
	matches := m.getSlashCompletions("/l")
	joined := make([]string, 0, len(matches))
	for _, item := range matches {
		joined = append(joined, item.Cmd)
	}
	got := strings.Join(joined, "\n")
	if !strings.Contains(got, "/loop on") {
		t.Fatalf("slash completions = %q, want /loop on after loop stops", got)
	}
	if strings.Contains(got, "/loop off") {
		t.Fatalf("slash completions = %q, should not show /loop off after loop stops", got)
	}
}

func TestRenderStatusBarKeepsLoopTextWhileCompacting(t *testing.T) {
	backend := &sessionControlAgent{currentRole: "builder", loopState: agent.LoopStateExecuting, loopTarget: "finish current task"}
	m := NewModelWithSize(backend, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityCompacting, AgentID: "main"}

	rendered := m.renderStatusBar()
	plain := stripANSI(rendered)
	if !strings.Contains(plain, "LOOP") {
		t.Fatalf("status bar = %q, want loop pill while compacting", plain)
	}
	if strings.Contains(plain, "finish current task") {
		t.Fatalf("status bar = %q, should not expose loop target text while compacting", plain)
	}
	if strings.Contains(plain, "compacting") {
		t.Fatalf("status bar = %q, should not duplicate compacting in loop text", plain)
	}
}

func TestRenderStatusBarCentersActivityAwayFromPath(t *testing.T) {
	m := NewModel(nil)
	m.width = 180
	m.workingDir = "/home/user/projects/myapp"
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	got := stripANSI(m.renderStatusBar())
	path := displayWorkingDir(m.workingDir)
	pathIdx := strings.Index(got, path)
	if pathIdx < 0 {
		t.Fatalf("status bar should include path %q; got %q", path, got)
	}
	if strings.Contains(got, "Streaming") {
		t.Fatalf("status bar should not render legacy streaming label; got %q", got)
	}
	if strings.Contains(got, statusBarActivityPathGap+path) {
		t.Fatalf("path should not be directly joined to activity gap %q; got %q", statusBarActivityPathGap, got)
	}
}

func TestRenderStatusBarOmitsShortHelp(t *testing.T) {
	m := NewModel(nil)
	m.width = 140

	got := stripANSI(m.renderStatusBar())
	if strings.Contains(got, "esc: normal") || strings.Contains(got, "enter: send/continue") {
		t.Fatalf("status bar should not render short help hints; got %q", got)
	}
}

func TestFormatContextPillOmitsZeroCurrent(t *testing.T) {
	if got := formatContextPill(0, 0); got != "" {
		t.Fatalf("formatContextPill(0,0) = %q, want empty", got)
	}
	if got := formatContextPill(0, 128_000); got != "" {
		t.Fatalf("formatContextPill(0,limit) = %q, want empty", got)
	}
	if got := formatContextPill(1000, 128_000); got == "" || !strings.Contains(got, "1.0k") {
		t.Fatalf("formatContextPill(1000,128000) = %q, want non-empty with token count", got)
	}
}

// When the right panel is hidden (narrow terminal), the status bar mirrors info-panel metrics;
// zero token IO and zero cost should not consume space. Scroll-position pills are not shown.
func TestNarrowStatusBarOmitsZeroUsageAndBottomLabel(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeNormal
	m.updateRightPanelVisible()
	if m.rightPanelVisible {
		t.Fatal("rightPanelVisible should be false at width 80")
	}

	plain := stripANSI(m.renderStatusBar())
	if strings.Contains(plain, "$0.00") {
		t.Fatalf("status bar should omit zero cost; got %q", plain)
	}
	if strings.Contains(plain, "↓0") || strings.Contains(plain, "↑0") {
		t.Fatalf("status bar should omit zero token IO; got %q", plain)
	}

	m.viewport.totalLines = 100
	m.viewport.height = 10
	m.viewport.offset = 90 // scrolled to bottom
	plain = stripANSI(m.renderStatusBar())
	if strings.Contains(plain, "BOT") {
		t.Fatalf("status bar should omit BOT at bottom; got %q", plain)
	}

}

func TestNarrowStatusBarTokenPillMatchesInfoPanelSemantics(t *testing.T) {
	backend := &sessionControlAgent{
		providerModelRef: "anthropic/claude-opus-4.7",
		tokenUsage:       message.TokenUsage{InputTokens: 29_900_000, OutputTokens: 143_100},
		sidebarUsage:     analytics.SessionStats{EstimatedCost: 1.2345},
	}
	m := NewModelWithSize(backend, 80, 24)
	m.mode = ModeNormal
	m.updateRightPanelVisible()
	if m.rightPanelVisible {
		t.Fatal("rightPanelVisible should be false at width 80")
	}

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "↑ 29.9M  ↓ 143.1k") {
		t.Fatalf("status bar token pill should use client-view arrows/spaces; got %q", plain)
	}
	if !strings.Contains(plain, "$1.23") {
		t.Fatalf("status bar cost pill should keep cost visible after token pill sync; got %q", plain)
	}
	if strings.Contains(plain, "↓29.9M") || strings.Contains(plain, "↑143.1k") {
		t.Fatalf("status bar should not use legacy compact token arrows; got %q", plain)
	}
}

func TestRenderAnimatedInputSeparatorUsesBusyColorsWhenAgentActive(t *testing.T) {
	m := NewModel(nil)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	got := m.renderAnimatedInputSeparator(24)
	plain := stripANSI(got)
	if plain != strings.Repeat(SectionSeparator, 24) {
		t.Fatalf("separator plain text = %q, want %q", plain, strings.Repeat(SectionSeparator, 24))
	}
	if !strings.Contains(got, "38;5;") && !strings.Contains(got, "38;2;") {
		t.Fatalf("busy separator should include ANSI foreground styling; got %q", got)
	}
	if got == InputSeparatorStyle.Render(strings.Repeat(SectionSeparator, 24)) {
		t.Fatalf("busy separator should differ from static insert separator; got %q", got)
	}
}

func TestRenderAnimatedInputSeparatorUsesDimmedStyleWhenIdle(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeNormal

	got := m.renderAnimatedInputSeparator(12)
	want := InputSeparatorDimmedStyle.Render(strings.Repeat(SectionSeparator, 12))
	if got != want {
		t.Fatalf("idle separator = %q, want %q", got, want)
	}
}

func TestRenderAnimatedInputSeparatorSetThemeInvalidatesCache(t *testing.T) {
	m := NewModel(nil)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.theme = DefaultTheme()

	first := m.renderAnimatedInputSeparator(24)
	if first == "" {
		t.Fatal("expected initial separator render to be non-empty")
	}
	if m.cachedSepTheme != "dark" {
		t.Fatalf("cached separator theme = %q, want dark", m.cachedSepTheme)
	}
	if m.cachedSepResult == "" {
		t.Fatal("expected cachedSepResult to be populated after render")
	}

	// SetTheme must clear the separator cache — this is the single
	// invalidation hook callers rely on whenever the palette is reloaded.
	m.SetTheme(DefaultTheme())
	if m.cachedSepTheme != "" || m.cachedSepResult != "" || m.cachedSepFrame != 0 {
		t.Fatalf("SetTheme should clear separator cache, got theme=%q frame=%d result=%q", m.cachedSepTheme, m.cachedSepFrame, m.cachedSepResult)
	}

	if second := m.renderAnimatedInputSeparator(24); second == "" {
		t.Fatal("expected separator render after SetTheme to be non-empty")
	}
	if m.cachedSepResult == "" {
		t.Fatal("expected separator render to repopulate the cache")
	}
}

func TestClickInfoPanelSectionHeaderTogglesCollapse(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.todos = []tools.TodoItem{{ID: "1", Content: "Investigate spacing", Status: "in_progress"}}
	m := NewModelWithSize(backend, 140, 24)
	m.layout = m.generateLayout(m.width, m.height)
	_ = m.renderInfoPanel(m.layout.infoPanel.Dx(), m.viewport.height)

	clickX := m.layout.infoPanel.Min.X + 1
	clickY := m.layout.infoPanel.Min.Y
	for _, hit := range m.infoPanelHitBoxes {
		if hit.section == infoPanelSectionTodos {
			clickY = m.layout.infoPanel.Min.Y + hit.startY
			break
		}
	}

	updated, cmd := m.Update(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd != nil {
		t.Fatalf("info panel click should not schedule command, got %#v", cmd)
	}
	if !model.infoPanelCollapsedSections[infoPanelSectionTodos] {
		t.Fatal("clicking TODOS header should collapse section")
	}

	plain := stripANSI(model.renderInfoPanel(model.layout.infoPanel.Dx(), model.viewport.height))
	if !strings.Contains(plain, "▶ TODOS") || !strings.Contains(plain, "0/1") {
		t.Fatalf("collapsed info panel should show collapsed TODOS header with progress, got %q", plain)
	}
}

func TestClickInfoPanelLSPHeaderTogglesCollapse(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.lspRows = []agent.LSPServerDisplay{{Name: "gopls", OK: true}, {Name: "pyright", Pending: true}}
	m := NewModelWithSize(backend, 140, 24)
	m.layout = m.generateLayout(m.width, m.height)
	_ = m.renderInfoPanel(m.layout.infoPanel.Dx(), m.viewport.height)

	clickX := m.layout.infoPanel.Min.X + 1
	clickY := m.layout.infoPanel.Min.Y
	for _, hit := range m.infoPanelHitBoxes {
		if hit.section == infoPanelSectionLSP {
			clickY = m.layout.infoPanel.Min.Y + hit.startY
			break
		}
	}

	updated, cmd := m.Update(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd != nil {
		t.Fatalf("info panel click should not schedule command, got %#v", cmd)
	}
	if !model.infoPanelCollapsedSections[infoPanelSectionLSP] {
		t.Fatal("clicking LSP header should collapse section")
	}

	plain := stripANSI(model.renderInfoPanel(model.layout.infoPanel.Dx(), model.viewport.height))
	if !strings.Contains(plain, "▶ LSP") || !strings.Contains(plain, "2") {
		t.Fatalf("collapsed info panel should show collapsed LSP header with count, got %q", plain)
	}
}

func TestClickInfoPanelMCPHeaderTogglesCollapse(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "exa", OK: true}, {Name: "browser", Pending: true}}
	m := NewModelWithSize(backend, 140, 24)
	m.layout = m.generateLayout(m.width, m.height)
	_ = m.renderInfoPanel(m.layout.infoPanel.Dx(), m.viewport.height)

	clickX := m.layout.infoPanel.Min.X + 1
	clickY := m.layout.infoPanel.Min.Y
	for _, hit := range m.infoPanelHitBoxes {
		if hit.section == infoPanelSectionMCP {
			clickY = m.layout.infoPanel.Min.Y + hit.startY
			break
		}
	}

	updated, cmd := m.Update(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd != nil {
		t.Fatalf("info panel click should not schedule command, got %#v", cmd)
	}
	if !model.infoPanelCollapsedSections[infoPanelSectionMCP] {
		t.Fatal("clicking MCP header should collapse section")
	}

	plain := stripANSI(model.renderInfoPanel(model.layout.infoPanel.Dx(), model.viewport.height))
	if !strings.Contains(plain, "▶ MCP") || !strings.Contains(plain, "2") {
		t.Fatalf("collapsed info panel should show collapsed MCP header with count, got %q", plain)
	}
}

func TestClickInfoPanelAgentRowSwitchesFocus(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.subAgents = []agent.SubAgentInfo{{InstanceID: "agent-1", TaskDesc: "ship tests"}}
	m := NewModelWithSize(backend, 140, 24)
	m.refreshSidebar()
	m.layout = m.generateLayout(m.width, m.height)
	_ = m.renderInfoPanel(m.layout.infoPanel.Dx(), m.viewport.height)

	clickX := m.layout.infoPanel.Min.X + 1
	clickY := -1
	for _, hit := range m.infoPanelHitBoxes {
		if hit.agentID == "agent-1" {
			clickY = m.layout.infoPanel.Min.Y + hit.startY
			break
		}
	}
	if clickY < 0 {
		t.Fatal("expected AGENTS row hitbox for agent-1")
	}

	updated, _ := m.Update(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if model.focusedAgentID != "agent-1" {
		t.Fatalf("focusedAgentID after AGENTS click = %q, want agent-1", model.focusedAgentID)
	}
	if backend.focused != "agent-1" {
		t.Fatalf("backend focused agent after AGENTS click = %q, want agent-1", backend.focused)
	}
}

func TestClickOutsideViewportClearsFocusedBlock(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "hello"})

	updated, cmd := m.Update(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd != nil {
		t.Fatalf("viewport click should not schedule command, got %#v", cmd)
	}
	if model.focusedBlockID != 1 {
		t.Fatalf("focusedBlockID after viewport click = %d, want 1", model.focusedBlockID)
	}
	block := model.viewport.GetFocusedBlock(1)
	if block == nil || !block.Focused {
		t.Fatal("expected block to be focused after viewport click")
	}

	rightPanelX := model.viewport.width
	updated, cmd = model.Update(tea.MouseClickMsg{X: rightPanelX, Y: 0, Button: tea.MouseLeft})
	model, ok = updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd != nil {
		t.Fatalf("outside click should not schedule command, got %#v", cmd)
	}
	if model.focusedBlockID != -1 {
		t.Fatalf("focusedBlockID after outside click = %d, want -1", model.focusedBlockID)
	}
	block = model.viewport.GetFocusedBlock(1)
	if block == nil {
		t.Fatal("expected block to remain in viewport")
	}
	if block.Focused {
		t.Fatal("expected outside click to clear block focus")
	}
}

func TestClickBlockErrorDoesNotFocus(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockError, Content: "boom"})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: "ok"})

	updated, cmd := m.Update(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd != nil {
		t.Fatalf("viewport click should not schedule command, got %#v", cmd)
	}
	if model.focusedBlockID != -1 {
		t.Fatalf("focusedBlockID after clicking BlockError = %d, want -1", model.focusedBlockID)
	}
	if errBlock := model.viewport.GetFocusedBlock(1); errBlock == nil {
		t.Fatal("expected error block to remain in viewport")
	} else if errBlock.Focused {
		t.Fatal("BlockError should not become focused")
	}
}

func TestStatusSessionDoubleClickCopiesWholeSessionID(t *testing.T) {
	backend := &sessionControlAgent{sessionSummary: &agent.SessionSummary{ID: "1775115074902"}}
	m := NewModelWithSize(backend, 160, 40)
	m.workingDir = "/home/user/projects/myapp"
	m.layout = m.generateLayout(m.width, m.height)
	_ = m.renderStatusBar()
	if m.statusSession.display == "" {
		t.Fatal("expected status session id to render")
	}
	clickX := m.statusSession.startX + 2
	if displayWidth := ansi.StringWidth(m.statusSession.display); displayWidth > 0 {
		clickX = m.statusSession.startX + min(2, displayWidth-1)
	}
	clickY := m.layout.status.Min.Y

	updated, cmd := m.Update(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	model := updated.(*Model)
	if cmd != nil {
		t.Fatal("first click should not copy session id")
	}

	updated, cmd = model.Update(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	_ = updated.(*Model)
	if cmd == nil {
		t.Fatal("double click should trigger session id copy")
	}
}

func TestStatusPathDoubleClickCopiesWholePath(t *testing.T) {
	m := NewModelWithSize(nil, 160, 40)
	m.workingDir = "/home/user/projects/myapp"
	m.layout = m.generateLayout(m.width, m.height)
	_ = m.renderStatusBar()
	if m.statusPath.display == "" {
		t.Fatal("expected status path to render")
	}
	clickX := m.statusPath.startX + 2
	if displayWidth := ansi.StringWidth(m.statusPath.display); displayWidth > 0 {
		clickX = m.statusPath.startX + min(2, displayWidth-1)
	}
	clickY := m.layout.status.Min.Y

	updated, cmd := m.Update(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	model := updated.(*Model)
	if cmd != nil {
		t.Fatal("first click should not copy path")
	}

	updated, cmd = model.Update(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	_ = updated.(*Model)
	if cmd == nil {
		t.Fatal("double click should trigger path copy")
	}
}

func TestStatusPathDoubleClickSelectsWholePath(t *testing.T) {
	m := NewModelWithSize(nil, 160, 40)
	m.workingDir = "/home/user/projects/myapp"
	m.layout = m.generateLayout(m.width, m.height)
	_ = m.renderStatusBar()
	if m.statusPath.display == "" {
		t.Fatal("expected status path to render")
	}
	clickX := m.statusPath.startX + 2
	if displayWidth := ansi.StringWidth(m.statusPath.display); displayWidth > 0 {
		clickX = m.statusPath.startX + min(2, displayWidth-1)
	}
	clickY := m.layout.status.Min.Y

	updated, cmd := m.Update(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	model := updated.(*Model)
	if cmd != nil {
		t.Fatal("first click should not trigger path copy")
	}

	updated, cmd = model.Update(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	_ = updated.(*Model)
	if cmd == nil {
		t.Fatal("double click should trigger path copy")
	}
}

func TestNormalYStartsChordWithoutCopyingStatusPath(t *testing.T) {
	m := NewModel(nil)
	m.statusPath.value = "/home/user/projects/myapp"
	m.statusPath.display = "~/projects/myapp"

	cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}))
	if cmd == nil {
		t.Fatal("y should start a pending chord")
	}
	if m.chord.op != chordY {
		t.Fatalf("chord op = %v, want chordY", m.chord.op)
	}
	if m.chord.count != 0 {
		t.Fatalf("chord count = %d, want 0", m.chord.count)
	}
}

func TestWriteStatusSessionClipboardCmdUsesSessionMessage(t *testing.T) {
	cmd := writeStatusSessionClipboardCmd("1775115074902")
	if cmd == nil {
		t.Fatal("expected clipboard command for non-empty session id")
	}
	msg := cmd()
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Len() != 2 {
		t.Fatalf("clipboard command msg = %T, want 2-command sequence", msg)
	}
	second := v.Index(1).Call(nil)[0].Interface().(clipboardWriteResultMsg)
	if second.success != "Session ID copied to clipboard" {
		t.Fatalf("clipboard success = %q, want %q", second.success, "Session ID copied to clipboard")
	}
}

func TestMouseWheelScrollTriggersInlineImageRefresh(t *testing.T) {
	ApplyTheme(DefaultTheme())
	caps := TerminalImageCapabilities{Backend: ImageBackendITerm2, SupportsInline: true, SupportsFullscreen: true}
	setCurrentTerminalImageCapabilities(caps)
	t.Cleanup(func() {
		setCurrentTerminalImageCapabilities(TerminalImageCapabilities{Backend: ImageBackendNone})
	})

	m := NewModelWithSize(nil, 80, 12)
	m.imageCaps = caps
	m.mode = ModeNormal

	m.viewport.AppendBlock(&Block{
		ID:         1,
		Type:       BlockUser,
		ImageCount: 1,
		ImageParts: []BlockImagePart{{
			FileName: "sample.png",
			MimeType: "image/png",
			Data:     makeTestPNG(t),
		}},
	})
	for i := 0; i < 3; i++ {
		m.viewport.AppendBlock(&Block{ID: 2 + i, Type: BlockAssistant, Content: strings.Repeat("alpha ", 40)})
	}

	m.layout = m.generateLayout(m.width, m.height)
	m.viewport.ScrollToTop()
	_ = m.viewport.Render("", nil, -1)
	m.viewport.ScrollDown(1)
	if m.viewport.offset == 0 {
		t.Fatal("expected setup to produce a non-zero scroll offset")
	}

	updated, cmd := m.Update(tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelUp})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd == nil {
		t.Fatal("mouse wheel scroll should schedule a scroll flush command")
	}
	if model.viewport.offset != 1 {
		t.Fatalf("mouse wheel event should defer offset change until flush, got %d", model.viewport.offset)
	}
	if model.pendingScrollDelta != -3 {
		t.Fatalf("pendingScrollDelta = %d, want -3", model.pendingScrollDelta)
	}
	cmd = model.consumeScrollFlush(scrollFlushTickMsg{generation: model.scrollFlushGeneration})
	if cmd == nil {
		t.Fatal("scroll flush with inline images should trigger image refresh command")
	}
	if model.viewport.offset >= 1 {
		t.Fatalf("expected scroll flush to decrease offset, got %d", model.viewport.offset)
	}
}

func TestMouseWheelScrollMovesViewportWhileCompacting(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	m.mode = ModeInsert
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityCompacting, AgentID: "main"}
	for i := 0; i < 6; i++ {
		m.viewport.AppendBlock(&Block{ID: i + 1, Type: BlockAssistant, Content: strings.Repeat("alpha ", 40)})
	}
	m.layout = m.generateLayout(m.width, m.height)
	m.viewport.ScrollDown(2)
	startOffset := m.viewport.offset
	if startOffset == 0 {
		t.Fatal("expected setup to produce a non-zero scroll offset")
	}

	updated, cmd := m.Update(tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelUp})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd == nil {
		t.Fatal("mouse wheel during compacting should still schedule a scroll flush command")
	}
	if model.pendingScrollDelta != -3 {
		t.Fatalf("pendingScrollDelta = %d, want -3", model.pendingScrollDelta)
	}
	model.consumeScrollFlush(scrollFlushTickMsg{generation: model.scrollFlushGeneration})
	if model.viewport.offset >= startOffset {
		t.Fatalf("expected compacting scroll flush to decrease offset, got start=%d end=%d", startOffset, model.viewport.offset)
	}
}

func TestToolCallExecutionEventMarksToolQueuedWithoutAnimating(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-q1",
		Name:     "Bash",
		AgentID:  "",
		ArgsJSON: `{"command":"command -v benchstat || true"}`,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallExecutionEvent{
		ID:       "call-q1",
		Name:     "Bash",
		ArgsJSON: `{"command":"command -v benchstat || true"}`,
		State:    agent.ToolCallExecutionStateQueued,
		AgentID:  "",
	}})

	block, ok := m.viewport.FindBlockByToolID("call-q1")
	if !ok {
		t.Fatal("expected queued tool block")
	}
	if block.ToolExecutionState != agent.ToolCallExecutionStateQueued {
		t.Fatalf("ToolExecutionState = %q, want %q", block.ToolExecutionState, agent.ToolCallExecutionStateQueued)
	}
	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if !strings.Contains(joined, "Queued") {
		t.Fatalf("expected queued header badge; got:\n%s", joined)
	}
	for _, seg := range activeToolSpinnerSegments {
		if strings.Contains(joined, seg) {
			t.Fatalf("queued tool should not render spinner segment %q; got:\n%s", seg, joined)
		}
	}
	if !block.StartedAt.IsZero() {
		t.Fatalf("queued tool StartedAt = %v, want zero", block.StartedAt)
	}
}

func TestToolCallUpdateEventArgsStreamingDoneMarksQueuedBeforeExecution(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	args := `{"todos":[{"id":"1","content":"a","status":"pending"}]}`

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-todo-queued-1",
		Name:     "TodoWrite",
		AgentID:  "",
		ArgsJSON: args,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID:                "call-todo-queued-1",
		Name:              "TodoWrite",
		AgentID:           "",
		ArgsJSON:          args,
		ArgsStreamingDone: true,
	}})

	block, ok := m.viewport.FindBlockByToolID("call-todo-queued-1")
	if !ok {
		t.Fatal("expected todo tool block")
	}
	if block.ToolExecutionState != agent.ToolCallExecutionStateQueued {
		t.Fatalf("ToolExecutionState = %q, want %q", block.ToolExecutionState, agent.ToolCallExecutionStateQueued)
	}
	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if !strings.Contains(joined, "Queued") {
		t.Fatalf("expected queued header badge; got:\n%s", joined)
	}
	for _, seg := range activeToolSpinnerSegments {
		if strings.Contains(joined, seg) {
			t.Fatalf("queued todo tool should not render spinner segment %q; got:\n%s", seg, joined)
		}
	}
}

func TestToolCallElapsedFooterStartsAtRunningAndShowsAfterFiveSeconds(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-elapsed-running",
		Name:     "Bash",
		AgentID:  "",
		ArgsJSON: `{"command":"sleep 10"}`,
	}})
	block, ok := m.viewport.FindBlockByToolID("call-elapsed-running")
	if !ok {
		t.Fatal("expected tool block")
	}
	if !block.StartedAt.IsZero() {
		t.Fatalf("speculative tool StartedAt = %v, want zero", block.StartedAt)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallExecutionEvent{
		ID:       "call-elapsed-running",
		Name:     "Bash",
		ArgsJSON: `{"command":"sleep 10"}`,
		State:    agent.ToolCallExecutionStateRunning,
		AgentID:  "",
	}})
	if block.StartedAt.IsZero() {
		t.Fatal("running tool should record StartedAt")
	}
	block.StartedAt = time.Now().Add(-6 * time.Second)

	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if strings.Contains(joined, "⏱") {
		t.Fatalf("did not expect elapsed footer while tool still running; got:\n%s", joined)
	}

	block.StartedAt = time.Now().Add(-4 * time.Second)
	joined = stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if strings.Contains(joined, "⏱") {
		t.Fatalf("did not expect elapsed footer before completion; got:\n%s", joined)
	}

	block.StartedAt = time.Now().Add(-7 * time.Second)
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "call-elapsed-running",
		Name:     "Bash",
		ArgsJSON: `{"command":"sleep 10"}`,
		Result:   "done",
		Status:   agent.ToolResultStatusSuccess,
		AgentID:  "",
	}})
	if block.SettledAt.IsZero() {
		t.Fatal("finished tool should record SettledAt")
	}
	joined = stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if !strings.Contains(joined, "⏱ 7s") {
		t.Fatalf("expected finished tool elapsed footer; got:\n%s", joined)
	}
}

func TestToolCallUpdateEventCreatesMissingToolBlock(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID:       "call-missing-update-1",
		Name:     "Bash",
		AgentID:  "",
		ArgsJSON: `{"command":"echo hello"}`,
	}})

	block, ok := m.viewport.FindBlockByToolID("call-missing-update-1")
	if !ok {
		t.Fatal("expected ToolCallUpdateEvent to create a missing tool block")
	}
	if block.ToolExecutionState != agent.ToolCallExecutionStateRunning {
		t.Fatalf("ToolExecutionState = %q, want %q", block.ToolExecutionState, agent.ToolCallExecutionStateRunning)
	}
	if block.ToolProgress == nil {
		t.Fatal("expected streaming arg progress on created tool block")
	}
}

func TestToolCallExecutionEventCreatesMissingToolBlock(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallExecutionEvent{
		ID:       "call-missing-exec-1",
		Name:     "Read",
		AgentID:  "",
		ArgsJSON: `{"path":"go.mod"}`,
		State:    agent.ToolCallExecutionStateQueued,
	}})

	block, ok := m.viewport.FindBlockByToolID("call-missing-exec-1")
	if !ok {
		t.Fatal("expected ToolCallExecutionEvent to create a missing tool block")
	}
	if block.ToolExecutionState != agent.ToolCallExecutionStateQueued {
		t.Fatalf("ToolExecutionState = %q, want %q", block.ToolExecutionState, agent.ToolCallExecutionStateQueued)
	}
	if block.ToolProgress != nil {
		t.Fatalf("expected no transient arg progress on execution-state fallback block, got %+v", *block.ToolProgress)
	}
}

func TestToolProgressEventUpdatesRunningToolCard(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-progress-1",
		Name:     "Delete",
		AgentID:  "",
		ArgsJSON: `{"paths":["a.txt","b.txt","c.txt"],"reason":"cleanup"}`,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolProgressEvent{
		CallID:  "call-progress-1",
		Name:    "Delete",
		AgentID: "",
		Progress: agent.ToolProgressSnapshot{
			Label:   "paths",
			Current: 3,
			Total:   8,
		},
	}})

	block, ok := m.viewport.FindBlockByToolID("call-progress-1")
	if !ok {
		t.Fatal("expected running tool block")
	}
	if block.ToolProgress == nil {
		t.Fatal("expected tool progress on running block")
	}
	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if !strings.Contains(joined, "3 / 8 paths") {
		t.Fatalf("expected tool progress in rendered card; got:\n%s", joined)
	}
}

func TestToolArgRenderRefreshRespectsCadenceAndByteGrowth(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	now := time.Now()
	m.recordToolArgRender("call-1", `{"command":"echo"}`, now)

	if m.shouldRefreshToolArgRender("call-1", `{"command":"echo hi"}`, now.Add(100*time.Millisecond)) {
		t.Fatal("did not expect refresh before cadence delay elapses")
	}
	if m.shouldRefreshToolArgRender("call-1", `{"command":"echo"}`, now.Add(200*time.Millisecond)) {
		t.Fatal("did not expect refresh when byte length did not grow")
	}
	if !m.shouldRefreshToolArgRender("call-1", `{"command":"echo hi"}`, now.Add(200*time.Millisecond)) {
		t.Fatal("expected refresh after cadence delay and byte growth")
	}
}

func TestAnyToolShowsReceivedCharCountFromStreamingArgs(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	startArgs := `{"command":"ec"}`
	updateArgs := `{"command":"echo hello world"}`

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-bash-progress-1",
		Name:     "Bash",
		AgentID:  "",
		ArgsJSON: startArgs,
	}})
	m.toolArgRenderState["call-bash-progress-1"] = toolArgRenderState{
		lastBytes: len(startArgs),
		lastAt:    time.Now().Add(-200 * time.Millisecond),
	}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID:       "call-bash-progress-1",
		Name:     "Bash",
		AgentID:  "",
		ArgsJSON: updateArgs,
	}})

	block, ok := m.viewport.FindBlockByToolID("call-bash-progress-1")
	if !ok {
		t.Fatal("expected tool block")
	}
	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if !strings.Contains(joined, "30 chars received") {
		t.Fatalf("expected generic arg char count progress; got:\n%s", joined)
	}
}

func TestToolCallUpdateDoesNotMutateContentBeforeArgRenderCadence(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	startArgs := `{"todos":[{"content":"one"}]}`
	updateArgs := `{"todos":[{"content":"one"},{"content":"two"}]}`
	now := time.Now()

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-todo-throttle-1",
		Name:     "TodoWrite",
		AgentID:  "",
		ArgsJSON: startArgs,
	}})
	block, ok := m.viewport.FindBlockByToolID("call-todo-throttle-1")
	if !ok {
		t.Fatal("expected tool block")
	}
	beforeContent := block.Content
	beforeVersion := m.viewport.RenderVersion()
	m.toolArgRenderState["call-todo-throttle-1"] = toolArgRenderState{
		lastBytes: len(startArgs),
		lastAt:    now,
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID:       "call-todo-throttle-1",
		Name:     "TodoWrite",
		AgentID:  "",
		ArgsJSON: updateArgs,
	}})

	if block.Content != beforeContent {
		t.Fatalf("block.Content changed before render cadence:\nbefore=%q\nafter=%q", beforeContent, block.Content)
	}
	if got := m.viewport.RenderVersion(); got != beforeVersion {
		t.Fatalf("viewport render version = %d, want unchanged %d", got, beforeVersion)
	}

	m.toolArgRenderState["call-todo-throttle-1"] = toolArgRenderState{
		lastBytes: len(startArgs),
		lastAt:    now.Add(-foregroundCadence.visualAnimDelay),
	}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID:       "call-todo-throttle-1",
		Name:     "TodoWrite",
		AgentID:  "",
		ArgsJSON: updateArgs,
	}})

	if block.Content == beforeContent {
		t.Fatal("block.Content should update once render cadence allows it")
	}
	if got := m.viewport.RenderVersion(); got == beforeVersion {
		t.Fatal("viewport render version should bump when throttled args are rendered")
	}
}

func TestToolCharCountProgressUsesExactDigits(t *testing.T) {
	largeJSON := `{"command":"` + strings.Repeat("x", 2190) + `"}`
	progress := inferToolArgProgress("Bash", largeJSON)
	if progress == nil {
		t.Fatal("expected inferred arg progress")
	}
	if progress.Text != "2204 chars received" {
		t.Fatalf("progress.Text = %q, want exact digits", progress.Text)
	}
}

func TestToolArgStreamingDoneClearsReceivedCharCountImmediately(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	argsJSON := `{"command":"echo hello world"}`

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-bash-progress-done-1",
		Name:     "Bash",
		AgentID:  "",
		ArgsJSON: argsJSON,
	}})

	block, ok := m.viewport.FindBlockByToolID("call-bash-progress-done-1")
	if !ok {
		t.Fatal("expected tool block")
	}
	if block.ToolProgress == nil {
		t.Fatal("expected transient char count before streaming completes")
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID:                "call-bash-progress-done-1",
		Name:              "Bash",
		AgentID:           "",
		ArgsJSON:          argsJSON,
		ArgsStreamingDone: true,
	}})

	if block.ToolProgress != nil {
		t.Fatalf("expected temp char count cleared on tool arg completion, got %+v", *block.ToolProgress)
	}
	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if strings.Contains(joined, "chars received") {
		t.Fatalf("expected tool to hide temp char count when arg streaming completes; got:\n%s", joined)
	}
}

func TestToolDoesNotShowCharCountWhenNoArgsReceived(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-zero-progress-1",
		Name:     "Bash",
		AgentID:  "",
		ArgsJSON: ``,
	}})

	block, ok := m.viewport.FindBlockByToolID("call-zero-progress-1")
	if !ok {
		t.Fatal("expected tool block")
	}
	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if strings.Contains(joined, "chars received") {
		t.Fatalf("did not expect char count when no args have been received; got:\n%s", joined)
	}
}

func TestWriteToolHidesReceivedCharCountWhenExecutionStarts(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-write-progress-hide-1",
		Name:     "Write",
		AgentID:  "",
		ArgsJSON: `{"path":"demo.txt","content":"hello world"}`,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallExecutionEvent{
		ID:       "call-write-progress-hide-1",
		Name:     "Write",
		AgentID:  "",
		ArgsJSON: `{"path":"demo.txt","content":"hello world"}`,
		State:    agent.ToolCallExecutionStateRunning,
	}})

	block, ok := m.viewport.FindBlockByToolID("call-write-progress-hide-1")
	if !ok {
		t.Fatal("expected write tool block")
	}
	if block.ToolProgress != nil {
		t.Fatalf("expected write temp char count to be cleared, got %+v", *block.ToolProgress)
	}
	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if strings.Contains(joined, "chars received") {
		t.Fatalf("expected write tool to hide temp char count after execution starts; got:\n%s", joined)
	}
}

func TestToolUpdateAfterExecutionStartsDoesNotRestoreReceivedCharCount(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	initialArgs := `{"command":"echo hello"}`
	finalArgs := `{"command":"echo hello world"}`

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-bash-progress-after-running-1",
		Name:     "Bash",
		AgentID:  "",
		ArgsJSON: initialArgs,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallExecutionEvent{
		ID:       "call-bash-progress-after-running-1",
		Name:     "Bash",
		AgentID:  "",
		ArgsJSON: initialArgs,
		State:    agent.ToolCallExecutionStateRunning,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID:       "call-bash-progress-after-running-1",
		Name:     "Bash",
		AgentID:  "",
		ArgsJSON: finalArgs,
	}})

	block, ok := m.viewport.FindBlockByToolID("call-bash-progress-after-running-1")
	if !ok {
		t.Fatal("expected bash tool block")
	}
	if block.ToolProgress != nil {
		t.Fatalf("expected post-running arg update not to restore temp char count, got %+v", *block.ToolProgress)
	}
	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if strings.Contains(joined, "chars received") {
		t.Fatalf("expected post-running arg update to keep temp char count hidden; got:\n%s", joined)
	}
	if !strings.Contains(block.Content, "echo hello world") {
		t.Fatalf("expected post-running arg update to refresh content, got %q", block.Content)
	}
}

func TestEditToolHidesReceivedCharCountWhenExecutionStarts(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-edit-progress-hide-1",
		Name:     "Edit",
		AgentID:  "",
		ArgsJSON: `{"path":"demo.txt","old_string":"old","new_string":"abcdef"}`,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallExecutionEvent{
		ID:       "call-edit-progress-hide-1",
		Name:     "Edit",
		AgentID:  "",
		ArgsJSON: `{"path":"demo.txt","old_string":"old","new_string":"abcdef"}`,
		State:    agent.ToolCallExecutionStateRunning,
	}})

	block, ok := m.viewport.FindBlockByToolID("call-edit-progress-hide-1")
	if !ok {
		t.Fatal("expected edit tool block")
	}
	if block.ToolProgress != nil {
		t.Fatalf("expected edit temp char count to be cleared, got %+v", *block.ToolProgress)
	}
	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if strings.Contains(joined, "chars received") {
		t.Fatalf("expected edit tool to hide temp char count after execution starts; got:\n%s", joined)
	}
}

func TestRenderActivitySummaryFallsBackToExistingLabels(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	got := m.renderActivityPrimaryText(agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityWaitingHeaders})
	if got != "" {
		t.Fatalf("renderActivityPrimaryText = %q, want empty string for non-connecting non-progress state", got)
	}
}

func TestRenderExecutingSummaryShowsGearAndElapsed(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	m.activityStartTime["main"] = time.Now().Add(-12 * time.Second)
	got := m.renderExecutingSummary("main")
	if !strings.HasPrefix(got, "⚙ ") {
		t.Fatalf("renderExecutingSummary = %q, want gear prefix", got)
	}
	if !strings.Contains(got, "12s") {
		t.Fatalf("renderExecutingSummary = %q, want elapsed seconds", got)
	}
}

func TestRenderActivityExecutingUsesGearElapsedStyle(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	m.activityStartTime["main"] = time.Now().Add(-12 * time.Second)
	out := stripANSI(m.renderActivity(agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityExecuting}, 200))
	if !strings.Contains(out, "⚙ 12s") {
		t.Fatalf("renderActivity(executing) = %q, want gear elapsed", out)
	}
	if strings.Contains(out, "Loop:") {
		t.Fatalf("renderActivity(executing) should not include loop phase label; got %q", out)
	}
	if strings.Contains(out, "Running tools") {
		t.Fatalf("renderActivity(executing) should not include legacy label; got %q", out)
	}
	if strings.Contains(out, "(12s)") {
		t.Fatalf("renderActivity(executing) should not append legacy paren elapsed; got %q", out)
	}
}

func TestFlushVisibleRequestProgressPromotesRawValues(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	m.requestProgress["main"] = requestProgressState{RawBytes: 2048, RawEvents: 3}
	m.flushVisibleRequestProgress(time.Now())
	prog := m.requestProgress["main"]
	if prog.VisibleBytes != 2048 || prog.VisibleEvents != 3 {
		t.Fatalf("visible progress = (%d,%d), want (2048,3)", prog.VisibleBytes, prog.VisibleEvents)
	}
}

func TestStatusBarDynamicCacheKeyIncludesRequestProgress(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	m.activities["main"] = agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityCompacting}
	m.requestProgress["main"] = requestProgressState{VisibleBytes: 1024, VisibleEvents: 2}
	got := m.statusBarDynamicCacheKeyAt(time.UnixMilli(1000))
	if !strings.Contains(got, "frame:") || !strings.Contains(got, "↓ 1.0 KB · 2 events") {
		t.Fatalf("statusBarDynamicCacheKeyAt = %q, want frame-based progress summary", got)
	}
}

func TestToolProgressEventPreservesProgressInNarrowWidthByTruncatingHeader(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-progress-tight-1",
		Name:     "Delete",
		AgentID:  "",
		ArgsJSON: `{"paths":["a-very-long-file-name.txt","b-very-long-file-name.txt","c-very-long-file-name.txt","d-very-long-file-name.txt"],"reason":"cleanup stale generated artifacts from previous long-running benchmark pass"}`,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolProgressEvent{
		CallID:  "call-progress-tight-1",
		Name:    "Delete",
		AgentID: "",
		Progress: agent.ToolProgressSnapshot{
			Label:   "paths",
			Current: 3,
			Total:   8,
		},
	}})

	block, ok := m.viewport.FindBlockByToolID("call-progress-tight-1")
	if !ok {
		t.Fatal("expected running tool block")
	}
	joined := stripANSI(strings.Join(block.Render(44, "●"), "\n"))
	if !strings.Contains(joined, "3 / 8 paths") {
		t.Fatalf("expected narrow render to preserve progress; got:\n%s", joined)
	}
}

func TestToolProgressEventDoesNotUpdateQueuedToolCard(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-progress-q1",
		Name:     "Delete",
		AgentID:  "",
		ArgsJSON: `{"paths":["a.txt","b.txt"],"reason":"cleanup"}`,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallExecutionEvent{
		ID:       "call-progress-q1",
		Name:     "Delete",
		ArgsJSON: `{"paths":["a.txt","b.txt"],"reason":"cleanup"}`,
		State:    agent.ToolCallExecutionStateQueued,
		AgentID:  "",
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolProgressEvent{
		CallID:  "call-progress-q1",
		Name:    "Delete",
		AgentID: "",
		Progress: agent.ToolProgressSnapshot{
			Label:   "paths",
			Current: 1,
			Total:   2,
		},
	}})

	block, ok := m.viewport.FindBlockByToolID("call-progress-q1")
	if !ok {
		t.Fatal("expected queued tool block")
	}
	if block.ToolProgress != nil {
		t.Fatal("did not expect queued tool to keep progress")
	}
	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if strings.Contains(joined, "1 / 2 paths") {
		t.Fatalf("queued tool should not render progress; got:\n%s", joined)
	}
}

func TestToolResultEventClearsToolProgress(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-progress-clear-1",
		Name:     "Delete",
		AgentID:  "",
		ArgsJSON: `{"paths":["a.txt","b.txt","c.txt"],"reason":"cleanup"}`,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolProgressEvent{
		CallID:  "call-progress-clear-1",
		Name:    "Delete",
		AgentID: "",
		Progress: agent.ToolProgressSnapshot{
			Label:   "paths",
			Current: 2,
			Total:   3,
		},
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "call-progress-clear-1",
		Name:     "Delete",
		ArgsJSON: `{"paths":["a.txt","b.txt","c.txt"],"reason":"cleanup"}`,
		Result:   "Deleted (3):\n- a.txt\n- b.txt\n- c.txt",
		Status:   agent.ToolResultStatusSuccess,
		AgentID:  "",
	}})

	block, ok := m.viewport.FindBlockByToolID("call-progress-clear-1")
	if !ok {
		t.Fatal("expected tool block")
	}
	if block.ToolProgress != nil {
		t.Fatalf("expected ToolProgress to be cleared, got %+v", *block.ToolProgress)
	}
	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if strings.Contains(joined, "2 / 3 paths") {
		t.Fatalf("completed tool should not render stale progress; got:\n%s", joined)
	}
}

func TestSingleHiddenLineCompactToolCannotBeCollapsedByToggleAtWidth(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		content  string
		result   string
	}{
		{
			name:     "delete",
			toolName: "Delete",
			content:  `{"paths":["examples/compression-config.yaml"],"reason":"remove obsolete example"}`,
			result:   "Delete completed.\n\nDeleted (1):\n- examples/compression-config.yaml",
		},
		{
			name:     "grep",
			toolName: "Grep",
			content:  `{"pattern":"TODO"}`,
			result:   strings.Join([]string{"a.go:1:TODO", "b.go:2:TODO", "c.go:3:TODO", "d.go:4:TODO", "e.go:5:TODO", "f.go:6:TODO", "g.go:7:TODO", "h.go:8:TODO", "i.go:9:TODO", "j.go:10:TODO", "k.go:11:TODO"}, "\n"),
		},
		{
			name:     "glob",
			toolName: "Glob",
			content:  `{"pattern":"**/*.go"}`,
			result:   strings.Join([]string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go", "g.go", "h.go", "i.go", "j.go", "k.go"}, "\n"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := &Block{
				ID:                     1,
				Type:                   BlockToolCall,
				ToolName:               tt.toolName,
				Content:                tt.content,
				ResultContent:          tt.result,
				ResultDone:             true,
				ToolCallDetailExpanded: true,
			}

			block.ToggleAtWidth(120)

			if !block.ToolCallDetailExpanded {
				t.Fatal("single hidden line compact tool should remain expanded after toggle")
			}
		})
	}
}

func TestConfirmRequestForcesPriorityBoundaryFlush(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateForeground
	m.width, m.height = 80, 24
	m.cachedFullView = tea.View{Content: "cached"}
	m.cachedFullViewValid = true
	m.streamRenderDeferred = true
	m.streamRenderDeferNext = true

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.ConfirmRequestEvent{
		ToolName:  "Edit",
		RequestID: "req-1",
	}})
	if cmd == nil {
		t.Fatal("confirm request should return followup command batch")
	}
	msg := cmd()
	msgs := []tea.Msg{}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, sub := range batch {
			if sub == nil {
				continue
			}
			if subMsg := sub(); subMsg != nil {
				msgs = append(msgs, subMsg)
			}
		}
	} else if msg != nil {
		msgs = append(msgs, msg)
	}
	for _, subMsg := range msgs {
		updated, next := m.Update(subMsg)
		model, ok := updated.(*Model)
		if !ok {
			t.Fatalf("Update returned %T, want *Model", updated)
		}
		m = *model
		if next != nil {
			_ = next()
		}
	}
	if !m.streamRenderForceView {
		t.Fatal("confirm request should force next live render")
	}
	if m.streamRenderDeferred {
		t.Fatal("confirm request should clear deferred render state")
	}
}

func TestQuestionRequestForcesPriorityBoundaryFlush(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateForeground
	m.width, m.height = 80, 24
	m.cachedFullView = tea.View{Content: "cached"}
	m.cachedFullViewValid = true
	m.streamRenderDeferred = true
	m.streamRenderDeferNext = true

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.QuestionRequestEvent{
		RequestID: "q-1",
		Question:  "continue?",
	}})
	if cmd == nil {
		t.Fatal("question request should return followup command batch")
	}
	msg := cmd()
	msgs := []tea.Msg{}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, sub := range batch {
			if sub == nil {
				continue
			}
			if subMsg := sub(); subMsg != nil {
				msgs = append(msgs, subMsg)
			}
		}
	} else if msg != nil {
		msgs = append(msgs, msg)
	}
	for _, subMsg := range msgs {
		updated, next := m.Update(subMsg)
		model, ok := updated.(*Model)
		if !ok {
			t.Fatalf("Update returned %T, want *Model", updated)
		}
		m = *model
		if next != nil {
			_ = next()
		}
	}
	if !m.streamRenderForceView {
		t.Fatal("question request should force next live render")
	}
	if m.streamRenderDeferred {
		t.Fatal("question request should clear deferred render state")
	}
}

func TestWarnToastForcesPriorityBoundaryFlush(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateForeground
	m.width, m.height = 80, 24
	m.cachedFullView = tea.View{Content: "cached"}
	m.cachedFullViewValid = true
	m.streamRenderDeferred = true
	m.streamRenderDeferNext = true

	cmd := m.enqueueToast("careful", "warn")
	if cmd == nil {
		t.Fatal("warn toast should return command")
	}
	_ = cmd()
	if !m.streamRenderForceView {
		t.Fatal("warn toast should force next live render")
	}
	if m.streamRenderDeferred {
		t.Fatal("warn toast should clear deferred render state")
	}
}

func TestToolResultForcesPriorityBoundaryFlush(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateForeground
	m.width, m.height = 80, 24
	m.cachedFullView = tea.View{Content: "cached"}
	m.cachedFullViewValid = true
	m.streamRenderDeferred = true
	m.streamRenderDeferNext = true

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:   "call-boundary-1",
		Name: "Read",
	}})

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID: "call-boundary-1",
		Name:   "Read",
		Status: agent.ToolResultStatusSuccess,
		Result: "done",
	}})
	if cmd == nil {
		t.Fatal("tool result should return followup command batch")
	}
	_ = cmd()
	if !m.streamRenderForceView {
		t.Fatal("tool result should force next live render")
	}
	if m.streamRenderDeferred {
		t.Fatal("tool result should clear deferred render state")
	}
}

func TestToolResultEventShowsEditedBeforeApprovalSummary(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "call-edited-1",
		Name:     "Delete",
		AgentID:  "",
		ArgsJSON: `{"paths":["a.txt"],"reason":"cleanup"}`,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "call-edited-1",
		Name:     "Delete",
		ArgsJSON: `{"paths":["a.txt"],"reason":"cleanup"}`,
		Result:   "Deleted (1):\n- a.txt",
		Status:   agent.ToolResultStatusSuccess,
		AgentID:  "",
		Audit: &message.ToolArgsAudit{
			OriginalArgsJSON:  `{"paths":["old.txt"],"reason":"cleanup"}`,
			EffectiveArgsJSON: `{"paths":["a.txt"],"reason":"cleanup"}`,
			UserModified:      true,
		},
	}})

	block, ok := m.viewport.FindBlockByToolID("call-edited-1")
	if !ok {
		t.Fatal("expected tool block")
	}
	if block.Audit == nil || !block.Audit.UserModified {
		t.Fatalf("block.Audit = %#v, want user-modified audit", block.Audit)
	}
	joined := stripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if !strings.Contains(joined, "edited before approval") {
		t.Fatalf("expected edited-before-approval marker, got:\n%s", joined)
	}
}

func TestStreamRollbackPreservesCancelledSpeculativeToolCard(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:      "call-1",
		Name:    "Read",
		AgentID: "",
	}})
	if _, ok := m.viewport.FindBlockByToolID("call-1"); !ok {
		t.Fatal("expected speculative tool block after ToolCallStartEvent")
	}
	if got := len(m.viewport.visibleBlocks()); got != 1 {
		t.Fatalf("len(visibleBlocks()) = %d, want 1", got)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "call-1",
		Name:     "Read",
		Status:   agent.ToolResultStatusCancelled,
		Result:   "Cancelled",
		AgentID:  "",
		ArgsJSON: `{"path":"internal/llm/provider.go"}`,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamRollbackEvent{AgentID: ""}})

	block, ok := m.viewport.FindBlockByToolID("call-1")
	if !ok {
		t.Fatal("expected cancelled tool block to remain after rollback")
	}
	if !block.ResultDone {
		t.Fatal("expected tool block ResultDone after cancelled result")
	}
	if block.ResultStatus != agent.ToolResultStatusCancelled {
		t.Fatalf("ResultStatus = %q, want %q", block.ResultStatus, agent.ToolResultStatusCancelled)
	}
	if m.currentThinkingBlock != nil || m.currentAssistantBlock != nil {
		t.Fatal("expected rollback to clear only in-flight thinking/assistant blocks")
	}
}

func TestStreamRollbackPreservesCancelledSpeculativeWriteToolCard(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "write-call-1",
		Name:     "Write",
		AgentID:  "",
		ArgsJSON: `{"path":".chord/plans/plan-002.md","content":"partial"}`,
	}})
	block, ok := m.viewport.FindBlockByToolID("write-call-1")
	if !ok {
		t.Fatal("expected speculative Write tool block after ToolCallStartEvent")
	}
	if block.ResultDone {
		t.Fatal("expected speculative Write block to start pending")
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "write-call-1",
		Name:     "Write",
		Status:   agent.ToolResultStatusCancelled,
		Result:   "Cancelled",
		AgentID:  "",
		ArgsJSON: `{"path":".chord/plans/plan-002.md","content":"partial"}`,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamRollbackEvent{AgentID: ""}})

	block, ok = m.viewport.FindBlockByToolID("write-call-1")
	if !ok {
		t.Fatal("expected cancelled Write tool block to remain after rollback")
	}
	if !block.ResultDone {
		t.Fatal("expected Write block ResultDone after cancelled result")
	}
	if block.ResultStatus != agent.ToolResultStatusCancelled {
		t.Fatalf("ResultStatus = %q, want %q", block.ResultStatus, agent.ToolResultStatusCancelled)
	}
	if block.ResultContent != "Cancelled" {
		t.Fatalf("ResultContent = %q, want Cancelled", block.ResultContent)
	}
	if block.ToolName != "Write" {
		t.Fatalf("ToolName = %q, want Write", block.ToolName)
	}
	if m.currentThinkingBlock != nil || m.currentAssistantBlock != nil {
		t.Fatal("expected rollback to clear only in-flight thinking/assistant blocks")
	}
}

func TestTaskToolResultErrorClearsPendingPlaceholder(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:      "task-call-1",
		Name:    "Delegate",
		AgentID: "",
	}})
	if got := m.sidebar.PendingTasks(); got != 1 {
		t.Fatalf("PendingTasks after Delegate start = %d, want 1", got)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "task-call-1",
		Name:     "Delegate",
		Status:   agent.ToolResultStatusError,
		Result:   "max concurrent agents reached",
		AgentID:  "",
		ArgsJSON: `{"description":"child work","agent_type":"worker"}`,
	}})
	if got := m.sidebar.PendingTasks(); got != 0 {
		t.Fatalf("PendingTasks after Delegate error = %d, want 0", got)
	}
}

func TestTaskToolResultWithoutAgentIDClearsPendingPlaceholder(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:      "task-call-2",
		Name:    "Delegate",
		AgentID: "",
	}})
	if got := m.sidebar.PendingTasks(); got != 1 {
		t.Fatalf("PendingTasks after Delegate start = %d, want 1", got)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "task-call-2",
		Name:     "Delegate",
		Status:   agent.ToolResultStatusSuccess,
		Result:   `{"status":"child_limit_reached","message":"direct active child limit reached (max_children=10)"}`,
		AgentID:  "",
		ArgsJSON: `{"description":"child work","agent_type":"worker"}`,
	}})
	if got := m.sidebar.PendingTasks(); got != 0 {
		t.Fatalf("PendingTasks after Delegate child_limit_reached = %d, want 0", got)
	}
}

func TestStreamRollbackRemovesCurrentAssistantAndThinkingBlocks(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: "think", AgentID: ""}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: "answer", AgentID: ""}})
	if m.currentThinkingBlock == nil || m.currentAssistantBlock == nil {
		t.Fatal("expected in-flight thinking and assistant blocks before rollback")
	}
	if got := len(m.viewport.visibleBlocks()); got != 2 {
		t.Fatalf("len(visibleBlocks()) = %d, want 2 before rollback", got)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamRollbackEvent{AgentID: ""}})
	if m.currentThinkingBlock != nil {
		t.Fatal("expected currentThinkingBlock cleared after rollback")
	}
	if m.currentAssistantBlock != nil {
		t.Fatal("expected currentAssistantBlock cleared after rollback")
	}
	if got := len(m.viewport.visibleBlocks()); got != 0 {
		t.Fatalf("len(visibleBlocks()) = %d, want 0 after rollback", got)
	}
}

func TestStreamTextEventDoesNotTriggerInlineImageRefresh(t *testing.T) {
	ApplyTheme(DefaultTheme())
	caps := TerminalImageCapabilities{Backend: ImageBackendITerm2, SupportsInline: true, SupportsFullscreen: true}
	setCurrentTerminalImageCapabilities(caps)
	t.Cleanup(func() {
		setCurrentTerminalImageCapabilities(TerminalImageCapabilities{Backend: ImageBackendNone})
	})

	m := NewModelWithSize(nil, 80, 12)
	m.imageCaps = caps
	m.mode = ModeNormal

	m.viewport.AppendBlock(&Block{
		ID:         1,
		Type:       BlockUser,
		ImageCount: 1,
		ImageParts: []BlockImagePart{{
			FileName: "sample.png",
			MimeType: "image/png",
			Data:     makeTestPNG(t),
		}},
	})

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: "hello"}})
	if cmd == nil {
		t.Fatal("stream text should schedule a coalesced UI flush")
	}
	if m.currentAssistantBlock == nil || m.currentAssistantBlock.Content != "hello" {
		t.Fatalf("assistant block content = %#v, want hello", m.currentAssistantBlock)
	}
}

func TestWindowSizeMsgShrinkIsDebounced(t *testing.T) {
	m := NewModelWithSize(nil, 121, 40)

	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 119, Height: 39})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}

	if model.width != 121 || model.height != 40 {
		t.Fatalf("size changed eagerly to %dx%d, want initial 121x40", model.width, model.height)
	}
	if model.pendingResizeW != 119 || model.pendingResizeH != 39 {
		t.Fatalf("pending resize = %dx%d, want 119x39", model.pendingResizeW, model.pendingResizeH)
	}
	if model.resizeVersion != 1 {
		t.Fatalf("resizeVersion = %d, want 1", model.resizeVersion)
	}
	if cmd == nil {
		t.Fatal("small shrink WindowSizeMsg should schedule a debounced applyResizeMsg")
	}
}

func TestWindowSizeMsgLargeWidthShrinkAppliesImmediately(t *testing.T) {
	m := NewModelWithSize(nil, 121, 40)

	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 115, Height: 40})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}

	if model.width != 115 || model.height != 40 {
		t.Fatalf("size = %dx%d, want immediate 115x40", model.width, model.height)
	}
	if model.pendingResizeW != 115 || model.pendingResizeH != 40 {
		t.Fatalf("pending resize = %dx%d, want 115x40", model.pendingResizeW, model.pendingResizeH)
	}
	if model.resizeVersion != 1 {
		t.Fatalf("resizeVersion = %d, want 1", model.resizeVersion)
	}
	if model.rightPanelVisible {
		t.Fatal("rightPanelVisible should be false after immediate shrink to width 115")
	}
	if cmd != nil {
		t.Fatalf("large width shrink should not debounce, got %#v", cmd)
	}
}

func TestWindowSizeMsgLargeHeightShrinkAppliesImmediately(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)

	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}

	if model.width != 120 || model.height != 36 {
		t.Fatalf("size = %dx%d, want immediate 120x36", model.width, model.height)
	}
	if model.pendingResizeW != 120 || model.pendingResizeH != 36 {
		t.Fatalf("pending resize = %dx%d, want 120x36", model.pendingResizeW, model.pendingResizeH)
	}
	if model.resizeVersion != 1 {
		t.Fatalf("resizeVersion = %d, want 1", model.resizeVersion)
	}
	if cmd != nil {
		t.Fatalf("large height shrink should not debounce, got %#v", cmd)
	}
}

func TestWindowSizeMsgLargeWidthShrinkHeightSmallShrinkAppliesWidthImmediately(t *testing.T) {
	m := NewModelWithSize(nil, 150, 40)

	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 144, Height: 38})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}

	if model.width != 144 {
		t.Fatalf("width = %d, want immediate 144", model.width)
	}
	if model.height != 40 {
		t.Fatalf("height = %d, want old 40 until debounced small shrink applies", model.height)
	}
	if model.pendingResizeW != 144 || model.pendingResizeH != 38 {
		t.Fatalf("pending resize = %dx%d, want 144x38", model.pendingResizeW, model.pendingResizeH)
	}
	if model.resizeVersion != 1 {
		t.Fatalf("resizeVersion = %d, want 1", model.resizeVersion)
	}
	if cmd == nil {
		t.Fatal("remaining small shrink should still schedule debounced apply")
	}
}

func TestWindowSizeMsgGrowthAppliesImmediately(t *testing.T) {
	m := NewModelWithSize(nil, 120, 35)

	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 150, Height: 40})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}

	if model.width != 150 || model.height != 40 {
		t.Fatalf("size = %dx%d, want immediate 150x40", model.width, model.height)
	}
	if model.pendingResizeW != 150 || model.pendingResizeH != 40 {
		t.Fatalf("pending resize = %dx%d, want 150x40", model.pendingResizeW, model.pendingResizeH)
	}
	if model.resizeVersion != 1 {
		t.Fatalf("resizeVersion = %d, want 1", model.resizeVersion)
	}
	if cmd != nil {
		t.Fatalf("growth WindowSizeMsg should not debounce, got %#v", cmd)
	}
}

func TestWindowSizeMsgWidthGrowthHeightShrinkAppliesWidthImmediately(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)

	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 150, Height: 37})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}

	if model.width != 150 {
		t.Fatalf("width = %d, want immediate 150", model.width)
	}
	if model.height != 40 {
		t.Fatalf("height = %d, want old 40 until debounced shrink applies", model.height)
	}
	if model.pendingResizeW != 150 || model.pendingResizeH != 37 {
		t.Fatalf("pending resize = %dx%d, want 150x37", model.pendingResizeW, model.pendingResizeH)
	}
	if model.resizeVersion != 1 {
		t.Fatalf("resizeVersion = %d, want 1", model.resizeVersion)
	}
	if cmd == nil {
		t.Fatal("mixed grow/shrink resize should still schedule debounced shrink apply")
	}
}

func TestWindowSizeMsgHeightGrowthWidthShrinkAppliesHeightImmediately(t *testing.T) {
	m := NewModelWithSize(nil, 150, 35)

	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 145, Height: 40})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}

	if model.width != 150 {
		t.Fatalf("width = %d, want old 150 until debounced shrink applies", model.width)
	}
	if model.height != 40 {
		t.Fatalf("height = %d, want immediate 40", model.height)
	}
	if model.pendingResizeW != 145 || model.pendingResizeH != 40 {
		t.Fatalf("pending resize = %dx%d, want 145x40", model.pendingResizeW, model.pendingResizeH)
	}
	if model.resizeVersion != 1 {
		t.Fatalf("resizeVersion = %d, want 1", model.resizeVersion)
	}
	if cmd == nil {
		t.Fatal("mixed grow/shrink resize should schedule debounced shrink apply")
	}
}

func TestBlurMsgRestoresStableTerminalSizeAndFreezesResize(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(true)
	m.width = 90
	m.height = 30
	m.pendingResizeW = 90
	m.pendingResizeH = 30

	updated, cmd := m.Update(tea.BlurMsg{})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if !model.focusResizeFrozen {
		t.Fatal("BlurMsg should start resize freeze")
	}
	if model.width != 120 || model.height != 40 {
		t.Fatalf("BlurMsg should restore stable size 120x40, got %dx%d", model.width, model.height)
	}
	if cmd == nil {
		t.Fatal("BlurMsg should schedule idle sweep command")
	}

	updated, cmd = model.Update(tea.WindowSizeMsg{Width: 80, Height: 25})
	model, ok = updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if model.width != 120 || model.height != 40 {
		t.Fatalf("frozen blur resize should keep stable size 120x40, got %dx%d", model.width, model.height)
	}
	if model.pendingResizeW != 80 || model.pendingResizeH != 25 {
		t.Fatalf("pending resize = %dx%d, want 80x25 during freeze", model.pendingResizeW, model.pendingResizeH)
	}
	if cmd != nil {
		t.Fatalf("WindowSizeMsg during blur freeze should not schedule command, got %#v", cmd)
	}
}

func TestFocusMsgFreezesResizeUntilSettle(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(true)
	m.width = 95
	m.height = 32

	updated, cmd := m.Update(tea.FocusMsg{})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if !model.focusResizeFrozen {
		t.Fatal("FocusMsg should start resize freeze")
	}
	if model.focusResizeGeneration != 1 {
		t.Fatalf("focusResizeGeneration = %d, want 1", model.focusResizeGeneration)
	}
	if model.width != 120 || model.height != 40 {
		t.Fatalf("FocusMsg should restore stable size 120x40, got %dx%d", model.width, model.height)
	}
	if cmd == nil {
		t.Fatal("FocusMsg should schedule settle tick")
	}

	updated, cmd = model.Update(tea.WindowSizeMsg{Width: 150, Height: 35})
	model, ok = updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if model.width != 120 || model.height != 40 {
		t.Fatalf("frozen resize should keep stable size 120x40, got %dx%d", model.width, model.height)
	}
	if model.pendingResizeW != 150 || model.pendingResizeH != 35 {
		t.Fatalf("pending resize = %dx%d, want 150x35 during freeze", model.pendingResizeW, model.pendingResizeH)
	}
	if cmd != nil {
		t.Fatalf("WindowSizeMsg during freeze should not schedule immediate command, got %#v", cmd)
	}
}

func TestFocusMsgWhenImageViewerOpenMarksViewerForRetransmitAndClearsViewerCache(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(false)
	m.mode = ModeImageViewer
	m.imageViewer = imageViewerState{Open: true, ImageID: 123, NeedsRetransmit: false}
	m.imageCaps = TerminalImageCapabilities{Backend: ImageBackendKitty, SupportsFullscreen: true}
	m.kittyImageCache[123] = struct{}{}
	m.kittyPlacementCache[123] = struct{}{}
	m.kittyImageCache[999] = struct{}{}
	m.kittyPlacementCache[999] = struct{}{}

	updated, cmd := m.Update(tea.FocusMsg{})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if !model.imageViewer.NeedsRetransmit {
		t.Fatal("FocusMsg should mark open image viewer for retransmit")
	}
	if _, ok := model.kittyImageCache[123]; ok {
		t.Fatal("FocusMsg should clear current viewer image cache entry")
	}
	if _, ok := model.kittyPlacementCache[123]; ok {
		t.Fatal("FocusMsg should clear current viewer placement cache entry")
	}
	if _, ok := model.kittyImageCache[999]; !ok {
		t.Fatal("FocusMsg should not clear unrelated kitty image cache entries")
	}
	if _, ok := model.kittyPlacementCache[999]; !ok {
		t.Fatal("FocusMsg should not clear unrelated kitty placement cache entries")
	}
	if cmd == nil {
		t.Fatal("FocusMsg should schedule deferred replay command when viewer is open")
	}
}

func TestFocusMsgWhenKittyImageViewerSchedulesDeferredReplay(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.SetFocusResizeFreezeEnabled(false)
	pngData := makeTestPNG(t)
	block := &Block{
		ID:         42,
		Type:       BlockUser,
		ImageCount: 1,
		ImageParts: []BlockImagePart{{FileName: "image.png", MimeType: "image/png", Data: pngData}},
	}
	m.viewport.AppendBlock(block)
	m.focusedBlockID = block.ID
	m.imageCaps = TerminalImageCapabilities{Backend: ImageBackendKitty, SupportsFullscreen: true}
	m.kittyMetrics = kittyTerminalMetrics{CellWidthPx: 8, CellHeightPx: 16, WindowWidthPx: 640, WindowHeightPx: 384, Valid: true}
	m.layout = m.generateLayout(m.width, m.height)
	m.openImageViewer(block.ID, 0)
	m.imageViewer.ImageID = 123
	m.imageViewer.PlacementID = 456
	m.imageViewer.NeedsRetransmit = false
	m.kittyImageCache[123] = struct{}{}
	m.kittyPlacementCache[123] = struct{}{}

	updated, cmd := m.Update(tea.FocusMsg{})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd == nil {
		t.Fatal("FocusMsg should schedule kitty image viewer replay when viewer is renderable")
	}
	if !model.imageViewer.NeedsRetransmit {
		t.Fatal("FocusMsg should keep viewer retransmit pending until deferred replay runs")
	}
	if model.lastImageProtocolReason != "" {
		t.Fatalf("lastImageProtocolReason = %q, want empty before deferred replay", model.lastImageProtocolReason)
	}
	if model.focusResizeGeneration != 1 {
		t.Fatalf("focusResizeGeneration = %d, want 1", model.focusResizeGeneration)
	}
	if got := cmd(); got == nil {
		t.Fatal("FocusMsg deferred replay command should emit a message")
	}
}

func TestFocusMsgWhenVisibleInlineImagesReplayEvenWithoutFreeze(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(false)
	m.imageCaps = TerminalImageCapabilities{Backend: ImageBackendITerm2, SupportsInline: true}
	m.viewport.height = 8
	m.viewport.AppendBlock(&Block{
		ID:         1,
		Type:       BlockUser,
		ImageCount: 1,
		ImageParts: []BlockImagePart{{
			FileName:        "img.png",
			MimeType:        "image/png",
			Data:            makeTestPNG(t),
			RenderRows:      2,
			RenderCols:      8,
			RenderStartLine: 0,
			RenderEndLine:   1,
		}},
	})
	m.recalcViewportSize()
	m.viewport.offset = 0
	m.layout = m.generateLayout(m.width, m.height)

	updated, cmd := m.Update(tea.FocusMsg{})
	_ = updated.(*Model)
	if cmd == nil {
		t.Fatal("FocusMsg should schedule inline image replay when visible inline images exist")
	}
}

func TestImageProtocolTickReplaysKittyViewerAfterFocusRestore(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.SetFocusResizeFreezeEnabled(false)
	pngData := makeTestPNG(t)
	block := &Block{
		ID:         52,
		Type:       BlockUser,
		ImageCount: 1,
		ImageParts: []BlockImagePart{{FileName: "image.png", MimeType: "image/png", Data: pngData}},
	}
	m.viewport.AppendBlock(block)
	m.focusedBlockID = block.ID
	m.imageCaps = TerminalImageCapabilities{Backend: ImageBackendKitty, SupportsFullscreen: true}
	m.kittyMetrics = kittyTerminalMetrics{CellWidthPx: 8, CellHeightPx: 16, WindowWidthPx: 640, WindowHeightPx: 384, Valid: true}
	m.layout = m.generateLayout(m.width, m.height)
	m.openImageViewer(block.ID, 0)
	m.imageViewer.NeedsRetransmit = true
	m.focusResizeGeneration = 1

	updated, cmd := m.Update(imageProtocolTickMsg{generation: 1, reason: "focus-restore:image-viewer"})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd == nil {
		t.Fatal("imageProtocolTickMsg should replay kitty image viewer")
	}
	if model.imageViewer.NeedsRetransmit {
		t.Fatal("successful deferred replay should consume kitty viewer retransmit flag")
	}
	if model.lastImageProtocolReason != "focus-restore:image-viewer" {
		t.Fatalf("lastImageProtocolReason = %q, want focus-restore:image-viewer", model.lastImageProtocolReason)
	}
}

func TestFocusResizeSettleRequestsWindowSize(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(true)
	m.focusResizeFrozen = true
	m.focusResizeGeneration = 2

	updated, cmd := m.Update(focusResizeSettleMsg{generation: 2})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if model.focusResizeFrozen {
		t.Fatal("settle tick should end resize freeze")
	}
	if cmd == nil {
		t.Fatal("settle tick should request current window size")
	}
	if got := cmd(); got == nil {
		t.Fatal("settle command should emit a sequence message")
	}
}

func TestHostRedrawSettleTriggersImageProtocolReplay(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.imageCaps = TerminalImageCapabilities{Backend: ImageBackendITerm2, SupportsInline: true}
	m.viewport.height = 8
	m.viewport.AppendBlock(&Block{
		ID:         1,
		Type:       BlockUser,
		ImageCount: 1,
		ImageParts: []BlockImagePart{{FileName: "sample.png", MimeType: "image/png", Data: makeTestPNG(t), RenderCols: 4, RenderRows: 2, RenderStartLine: 0, RenderEndLine: 1}},
	})
	m.recalcViewportSize()
	m.viewport.offset = 0
	m.layout = m.generateLayout(m.width, m.height)

	updated, cmd := m.Update(hostRedrawSettleMsg{reason: "stream-flush"})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd == nil {
		t.Fatal("host redraw settle should replay image protocol")
	}
	if !strings.Contains(model.lastImageProtocolReason, "host-redraw:stream-flush") {
		t.Fatalf("lastImageProtocolReason = %q, want host-redraw:stream-flush", model.lastImageProtocolReason)
	}
}

func TestHostRedrawSettleSkipsPeriodicViewerReplay(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.mode = ModeImageViewer
	m.imageViewer = imageViewerState{Open: true}
	m.lastImageProtocolReason = "existing"

	updated, cmd := m.Update(hostRedrawSettleMsg{reason: "stream-flush"})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd != nil {
		t.Fatalf("periodic viewer settle should not schedule command, got %#v", cmd)
	}
	if model.lastImageProtocolReason != "existing" {
		t.Fatalf("lastImageProtocolReason = %q, want existing", model.lastImageProtocolReason)
	}
}

func TestHostRedrawSettleRecordsReasonEvenWhenReplaySkipped(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.imageCaps = TerminalImageCapabilities{Backend: ImageBackendITerm2, SupportsInline: true}

	updated, cmd := m.Update(hostRedrawSettleMsg{reason: "stream-flush"})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd != nil {
		t.Fatalf("host redraw settle without visible inline image should not schedule command, got %#v", cmd)
	}
	if model.lastImageProtocolReason != "host-redraw:stream-flush" {
		t.Fatalf("lastImageProtocolReason = %q, want host-redraw:stream-flush", model.lastImageProtocolReason)
	}
	if !strings.Contains(model.lastImageProtocolSummary, "visible_inline=false") {
		t.Fatalf("lastImageProtocolSummary = %q, want visible_inline=false", model.lastImageProtocolSummary)
	}
}

func TestHostRedrawSettleSkipsWhileResizeFrozen(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.focusResizeFrozen = true
	updated, cmd := m.Update(hostRedrawSettleMsg{reason: "scroll-flush"})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if !model.focusResizeFrozen {
		t.Fatal("host redraw settle should not clear resize freeze")
	}
	if cmd != nil {
		t.Fatalf("host redraw settle during freeze should not schedule command, got %#v", cmd)
	}
	if model.lastImageProtocolReason != "" {
		t.Fatalf("host redraw settle during freeze should not replay image protocol, got %q", model.lastImageProtocolReason)
	}
}

func TestFocusResizeSettleSchedulesInlineReplayOutsideViewer(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(true)
	m.focusResizeFrozen = true
	m.focusResizeGeneration = 2
	m.mode = ModeInsert

	updated, cmd := m.Update(focusResizeSettleMsg{generation: 2})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if model.focusResizeFrozen {
		t.Fatal("settle tick should end resize freeze")
	}
	if cmd == nil {
		t.Fatal("settle tick outside viewer should schedule replay sequence")
	}
	if got := cmd(); got == nil {
		t.Fatal("inline replay settle sequence should emit a message")
	}
	if model.lastImageProtocolReason != "" {
		t.Fatalf("focus settle should not execute replay synchronously, got reason %q", model.lastImageProtocolReason)
	}
}

func TestFocusResizeSettleWhenImageViewerOpenSchedulesViewerRedraw(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(true)
	m.mode = ModeImageViewer
	m.imageViewer = imageViewerState{Open: true}
	m.focusResizeFrozen = true
	m.focusResizeGeneration = 2

	updated, cmd := m.Update(focusResizeSettleMsg{generation: 2})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if model.focusResizeFrozen {
		t.Fatal("settle tick should end resize freeze")
	}
	if cmd == nil {
		t.Fatal("settle tick with open image viewer should schedule redraw sequence")
	}
	if got := cmd(); got == nil {
		t.Fatal("viewer settle sequence should emit a message")
	}
}

func TestFocusResizeSettleIgnoresStaleGeneration(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(true)
	m.focusResizeFrozen = true
	m.focusResizeGeneration = 3

	updated, cmd := m.Update(focusResizeSettleMsg{generation: 2})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if !model.focusResizeFrozen {
		t.Fatal("stale settle tick should not end resize freeze")
	}
	if cmd != nil {
		t.Fatalf("stale settle tick should not schedule command, got %#v", cmd)
	}
}

func TestPostFocusSettleRedrawTriggersStrongHostRedraw(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.SetFocusResizeFreezeEnabled(true)
	m.focusResizeGeneration = 5
	m.lastHostRedrawAt = time.Now().Add(-time.Second)

	cmd := m.handlePostFocusSettleRedraw(postFocusSettleRedrawMsg{generation: 5})
	if cmd == nil {
		t.Fatal("matching generation should schedule host redraw")
	}
	if m.lastHostRedrawReason != "post-focus-settle-redraw" {
		t.Fatalf("lastHostRedrawReason = %q, want post-focus-settle-redraw", m.lastHostRedrawReason)
	}

	cmds := m.hostRedrawSequence("post-focus-settle-redraw")
	if len(cmds) != 3 {
		t.Fatalf("post-focus-settle-redraw cmd count = %d, want 3", len(cmds))
	}
	if !containsCmd(cmds, tea.ClearScreen) {
		t.Fatal("post-focus-settle-redraw should include ClearScreen")
	}
	if !containsCmd(cmds, tea.RequestWindowSize) {
		t.Fatal("post-focus-settle-redraw should include RequestWindowSize")
	}
}

func TestPostFocusSettleRedrawSkipsStaleGeneration(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.focusResizeGeneration = 5

	_, cmd := m.Update(postFocusSettleRedrawMsg{generation: 3})
	if cmd != nil {
		t.Fatalf("stale generation should not issue command, got %#v", cmd)
	}
}

func TestDetectFocusResizeFreezeFromMap(t *testing.T) {
	if !detectFocusResizeFreezeFromMap(map[string]string{"CMUX_SOCKET_PATH": "/tmp/cmux-debug.sock"}) {
		t.Fatal("CMUX_SOCKET_PATH should enable focus resize freeze workaround")
	}
	if !detectFocusResizeFreezeFromMap(map[string]string{"CMUX_SOCKET": "/tmp/cmux-debug.sock"}) {
		t.Fatal("CMUX_SOCKET should enable focus resize freeze workaround")
	}
	if !detectFocusResizeFreezeFromMap(map[string]string{"TERM_PROGRAM": "ghostty"}) {
		t.Fatal("ghostty should enable focus resize freeze workaround")
	}
	if !detectFocusResizeFreezeFromMap(map[string]string{"TERM": "xterm-ghostty"}) {
		t.Fatal("xterm-ghostty should enable focus resize freeze workaround")
	}
	if detectFocusResizeFreezeFromMap(map[string]string{"TERM_PROGRAM": "iTerm.app"}) {
		t.Fatal("plain iTerm2 should not enable focus resize freeze workaround")
	}
}

func TestApplyResizeMsgUnchangedPendingSizeDoesNothing(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.pendingResizeW = 120
	m.pendingResizeH = 40
	m.resizeVersion = 2

	updated, cmd := m.Update(applyResizeMsg{version: 2})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if model.width != 120 || model.height != 40 {
		t.Fatalf("size changed unexpectedly to %dx%d", model.width, model.height)
	}
	if cmd != nil {
		t.Fatalf("no-op applyResizeMsg should not schedule command, got %#v", cmd)
	}
}

func TestApplyResizeMsgUsesLatestPendingSize(t *testing.T) {
	m := NewModelWithSize(nil, 150, 40)
	m.pendingResizeW = 121
	m.pendingResizeH = 36
	m.resizeVersion = 2

	updated, cmd := m.Update(applyResizeMsg{version: 2})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}

	if model.width != 121 || model.height != 36 {
		t.Fatalf("applied size = %dx%d, want 121x36", model.width, model.height)
	}
	if !model.rightPanelVisible {
		t.Fatal("rightPanelVisible should remain true at width 121")
	}
	if cmd != nil {
		t.Fatalf("applyResizeMsg should not schedule extra command, got %#v", cmd)
	}
}

func TestApplyResizeMsgIgnoresSupersededVersion(t *testing.T) {
	m := NewModelWithSize(nil, 150, 40)
	m.pendingResizeW = 121
	m.pendingResizeH = 36
	m.resizeVersion = 3

	updated, _ := m.Update(applyResizeMsg{version: 2})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}

	if model.width != 150 || model.height != 40 {
		t.Fatalf("superseded resize changed size to %dx%d, want 150x40", model.width, model.height)
	}
}

func TestViewShowsRestoringSessionPlaceholderDuringStartupRestore(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.startupRestorePending = true
	m.beginSessionSwitch("resume", "123")
	m.layout = m.generateLayout(m.width, m.height)

	got := stripANSI(m.View().Content)
	if !strings.Contains(got, "Restoring session...") {
		t.Fatalf("View() should show restoring placeholder, got %q", got)
	}
	if strings.Contains(got, "No messages yet. Start a conversation!") {
		t.Fatalf("View() should suppress empty welcome text during startup restore, got %q", got)
	}
	if !strings.Contains(got, "Resuming 123...") {
		t.Fatalf("View() should show status-bar resume progress during startup restore, got %q", got)
	}
}

func TestViewRefreshesComposerAfterInputChanges(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	m.mode = ModeInsert

	initial := stripANSI(m.View().Content)
	if strings.Contains(initial, "hello cache") {
		t.Fatalf("initial View() unexpectedly contains future composer text: %q", initial)
	}

	m.input.SetValue("hello cache")
	m.input.syncHeight()

	updated := stripANSI(m.View().Content)
	if !strings.Contains(updated, "hello cache") {
		t.Fatalf("View() should refresh composer text after input changes, got:\n%s", updated)
	}
}

func TestViewReadCardWithCRLFResultDoesNotLeakCarriageReturnIntoCanvas(t *testing.T) {
	m := NewModelWithSize(nil, 100, 12)
	m.mode = ModeNormal
	m.rightPanelVisible = false
	m.layout = m.generateLayout(m.width, m.height)
	m.viewport.AppendBlock(&Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   "Read",
		Content:    `{"path":"sample.csv","limit":20}`,
		ResultDone: true,
		ResultContent: strings.Join([]string{
			"     1\tissue,label\r",
			"     2\t\"a\",\"b\"\r",
		}, "\n"),
	})
	m.recalcViewportSize()

	view := m.View()
	if containsRawCarriageReturnForTest(view.Content) {
		t.Fatalf("View().Content should not contain raw carriage returns: %q", view.Content)
	}
	plain := stripANSI(view.Content)
	if !strings.Contains(plain, "issue,label") {
		t.Fatalf("expected View() content to contain first CSV row, got:\n%s", plain)
	}
	if !strings.Contains(plain, `"a","b"`) {
		t.Fatalf("expected View() content to contain second CSV row, got:\n%s", plain)
	}
}

func TestMessagesToBlocksSkillToolRestoresDisplaySummary(t *testing.T) {
	msgs := []message.Message{
		{
			Role: "assistant",
			ToolCalls: []message.ToolCall{{
				ID:   "skill-1",
				Name: "Skill",
				Args: json.RawMessage(`{"name":"skill-creator"}`),
			}},
		},
		{
			Role:       "tool",
			ToolCallID: "skill-1",
			Content:    "<skill>\n<name>skill-creator</name>\n<path>/tmp/skills/skill-creator/SKILL.md</path>\n<root>/tmp/skills/skill-creator</root>\n\n# Skill Creator\n\n- Step one\n</skill>",
		},
	}

	nextID := 1
	blocks := messagesToBlocks(msgs, &nextID)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	block := blocks[0]
	if block.ToolName != "Skill" {
		t.Fatalf("ToolName = %q, want Skill", block.ToolName)
	}
	if got, want := block.Content, `{"name":"skill-creator","result":"\u003cpath\u003e/tmp/skills/skill-creator/SKILL.md\u003c/path\u003e"}`; got != want {
		t.Fatalf("Content = %q, want %q", got, want)
	}
	if !strings.Contains(block.ResultContent, "# Skill Creator") {
		t.Fatalf("ResultContent should preserve full skill body, got %q", block.ResultContent)
	}
}

func TestHandleAgentEventSkillToolCollapsedSummaryButExpandedBody(t *testing.T) {
	m := NewModelWithSize(nil, 100, 20)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:       "skill-1",
		Name:     "Skill",
		ArgsJSON: `{"name":"skill-creator","args":"Create 4 new skills"}`,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID:   "skill-1",
		Name:     "Skill",
		ArgsJSON: `{"name":"skill-creator","args":"Create 4 new skills"}`,
		Status:   agent.ToolResultStatusSuccess,
		Result:   "<skill>\n<name>skill-creator</name>\n<path>/tmp/skills/skill-creator/SKILL.md</path>\n<root>/tmp/skills/skill-creator</root>\n<notes>ignored</notes>\n\n# Skill Creator\n\n- Step one\n- Step two\n</skill>",
	}})

	block, ok := m.viewport.FindBlockByToolID("skill-1")
	if !ok {
		t.Fatal("expected Skill tool block")
	}
	if got, want := block.Content, `{"name":"skill-creator","result":"\u003cpath\u003e/tmp/skills/skill-creator/SKILL.md\u003c/path\u003e"}`; got != want {
		t.Fatalf("Content = %q, want %q", got, want)
	}
	if !strings.Contains(block.ResultContent, "# Skill Creator") {
		t.Fatalf("ResultContent should preserve full skill body, got %q", block.ResultContent)
	}
	collapsed := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(collapsed, "Skill skill-creator") {
		t.Fatalf("expected collapsed skill card summary to keep full skill name, got:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "name:") {
		t.Fatalf("collapsed skill card should not repeat a name param line, got:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "Create 4 new skills") || strings.Contains(collapsed, "Step one") || strings.Contains(collapsed, "<skill>") {
		t.Fatalf("collapsed skill card should hide args/body/tags, got:\n%s", collapsed)
	}

	block.Toggle()
	expanded := stripANSI(strings.Join(block.Render(120, ""), "\n"))
	for _, want := range []string{"Skill skill-creator", "Skill Creator", "Step one", "Step two"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("expanded skill card missing %q; got:\n%s", want, expanded)
		}
	}
	if strings.Contains(expanded, "<skill>") || strings.Contains(expanded, "<root>") || strings.Contains(expanded, "Create 4 new skills") {
		t.Fatalf("expanded skill card should show body but not wrapper tags/args, got:\n%s", expanded)
	}
}

func TestMessagesToBlocksUserPartsUseRawTextNotDisplayText(t *testing.T) {
	msgs := []message.Message{{
		Role: "user",
		Parts: []message.ContentPart{
			{Type: "text", Text: "before\n"},
			{Type: "text", Text: "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10\nline11", DisplayText: "[Pasted text #1 +11 lines]"},
			{Type: "text", Text: "\nafter"},
			{Type: "text", Text: `<file path="docs/ARCHITECTURE.md">\nignored\n</file>`},
		},
	}}
	nextID := 1
	blocks := messagesToBlocks(msgs, &nextID)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	b := blocks[0]
	if b.Type != BlockUser {
		t.Fatalf("block type = %v, want BlockUser", b.Type)
	}
	want := "before\nline1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10\nline11\nafter"
	if b.Content != want {
		t.Fatalf("user block content = %q, want full raw pasted text", b.Content)
	}
	if strings.Contains(b.Content, "[Pasted text #1 +11 lines]") {
		t.Fatalf("user block content should not contain placeholder: %q", b.Content)
	}
	if len(b.FileRefs) != 1 || b.FileRefs[0] != "docs/ARCHITECTURE.md" {
		t.Fatalf("FileRefs = %#v, want docs/ARCHITECTURE.md", b.FileRefs)
	}
}

func TestMessagesToBlocksCompactionSummaryCollapsedByDefaultAndExpandable(t *testing.T) {
	msgs := []message.Message{{
		Role:                "user",
		IsCompactionSummary: true,
		Content:             "[Context Summary]\n## Goal\n- Continue improving extraction quality.\n- Keep markdown headings visible.\n\n## Progress\n- Added preview state.\n\n## Key Decisions\n- Use compacting activity state.\n\n## Next Step\n- Ship.\n\n[Context compressed]\nEarlier conversation was compacted into the summary above.\nArchived history files:\n- history-1.md",
	}}
	nextID := 1
	blocks := messagesToBlocks(msgs, &nextID)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	b := blocks[0]
	if b.Type != BlockCompactionSummary {
		t.Fatalf("block type = %v, want BlockCompactionSummary", b.Type)
	}
	if !b.Collapsed {
		t.Fatal("compaction summary block should be collapsed by default")
	}
	if strings.Contains(b.Content, "history-1.md") {
		t.Fatalf("collapsed content should hide full preserved context, got %q", b.Content)
	}
	// Preview should be truncated (we used more than 10 lines of summary above).
	if !strings.Contains(b.Content, "…") {
		t.Fatalf("collapsed content should show truncated preview marker, got %q", b.Content)
	}
	b.Toggle()
	if b.Collapsed {
		t.Fatal("compaction summary block should expand after toggle")
	}
	if !strings.Contains(b.Content, "history-1.md") {
		t.Fatalf("expanded content should show archived history path, got %q", b.Content)
	}
	b.Toggle()
	if !b.Collapsed {
		t.Fatal("compaction summary block should collapse after second toggle")
	}
	if strings.Contains(b.Content, "history-1.md") {
		t.Fatalf("collapsed content should again hide full preserved context, got %q", b.Content)
	}
}

func TestSessionRestoredRebuildClearsStaleFocusedBlock(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{{Role: "user", IsCompactionSummary: true, Content: "[Context Summary]\nsummary\n\n[Context compressed]\nArchived history files:\n- history-1.md"}}}
	m := NewModel(backend)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 42, Type: BlockUser, Content: "stale"})
	m.focusedBlockID = 42
	m.refreshBlockFocus()

	m.rebuildViewportFromMessagesWithReason("session_restored")

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 || blocks[0].Type != BlockCompactionSummary {
		t.Fatalf("visible blocks after rebuild = %#v, want single compaction summary", blocks)
	}
	if m.focusedBlockID != blocks[0].ID {
		t.Fatalf("focusedBlockID after rebuild = %d, want compaction summary block %d", m.focusedBlockID, blocks[0].ID)
	}
	if !blocks[0].Focused {
		t.Fatal("rebuild should auto-focus visible compaction summary")
	}
}

func TestToggleCollapseFallsBackToBlockAtOffsetWhenFocusedBlockIsStale(t *testing.T) {
	m := NewModelWithSize(nil, 100, 24)
	m.mode = ModeNormal
	block := &Block{
		ID:                     1,
		Type:                   BlockCompactionSummary,
		CompactionSummaryRaw:   "[Context Summary]\nsummary\n\n[Context compressed]\nArchived history files:\n- history-1.md",
		CompactionPreviewLines: maxCompactionSummaryPreviewLines,
		Content:                formatCompactionSummaryDisplay("[Context Summary]\nsummary\n\n[Context compressed]\nArchived history files:\n- history-1.md", true, maxCompactionSummaryPreviewLines),
		Collapsed:              true,
	}
	m.viewport.AppendBlock(block)
	m.recalcViewportSize()
	m.focusedBlockID = 99

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace}))

	if m.focusedBlockID != -1 {
		t.Fatalf("focusedBlockID after stale toggle fallback = %d, want -1", m.focusedBlockID)
	}
	got := m.viewport.GetFocusedBlock(block.ID)
	if got == nil {
		t.Fatal("expected compaction summary block to remain present")
	}
	if got.Collapsed {
		t.Fatal("compaction summary should expand when stale focus falls back to block at offset")
	}
	if !strings.Contains(got.Content, "history-1.md") {
		t.Fatalf("expanded compaction summary = %q, want archived history path", got.Content)
	}
}

func TestHandleNormalKeySpaceTogglesLinkedTaskCardWithoutSwitchingFocus(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 100, 24)
	m.mode = ModeNormal
	task := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "Delegate",
		Collapsed:     true,
		LinkedAgentID: "agent-1",
		Content:       `{"description":"review tests\ncheck coverage\nupdate docs","agent_type":"reviewer"}`,
		ResultContent: `{"status":"started","task_id":"adhoc-7","agent_id":"reviewer-2"}`,
		ResultDone:    true,
		Focused:       true,
	}
	m.viewport.AppendBlock(task)
	m.recalcViewportSize()
	m.focusedBlockID = task.ID

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace}))

	if m.focusedAgentID != "" || backend.focused != "" {
		t.Fatalf("space should not switch focus, got model=%q backend=%q", m.focusedAgentID, backend.focused)
	}
	if task.Collapsed {
		t.Fatal("space should expand linked Delegate card")
	}
}

func TestHandleNormalKeyEnterOnLinkedTaskSwitchesToWorkerView(t *testing.T) {
	backend := &sessionControlAgent{
		messagesByFocus: map[string][]message.Message{
			"": {
				{Role: "assistant", Content: "main history"},
			},
			"agent-1": {
				{Role: "assistant", Content: "worker history"},
			},
		},
	}
	m := NewModelWithSize(backend, 100, 24)
	m.mode = ModeNormal
	task := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "Delegate",
		Collapsed:     true,
		LinkedAgentID: "agent-1",
		Content:       `{"description":"review tests","agent_type":"reviewer"}`,
		ResultContent: `{"status":"started","task_id":"adhoc-7","agent_id":"agent-1"}`,
		ResultDone:    true,
		Focused:       true,
	}
	m.viewport.AppendBlock(task)
	m.recalcViewportSize()
	m.focusedBlockID = task.ID

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if m.focusedAgentID != "agent-1" {
		t.Fatalf("focusedAgentID = %q, want agent-1", m.focusedAgentID)
	}
	if backend.focused != "agent-1" {
		t.Fatalf("backend focused = %q, want agent-1", backend.focused)
	}
}

func TestSessionRestoredRebuildPrefersVisibleCompactionSummaryOverValidOldFocus(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{
		{Role: "user", IsCompactionSummary: true, Content: "[Context Summary]\nsummary\n\n[Context compressed]\nArchived history files:\n- history-1.md"},
		{Role: "user", Content: "recent tail"},
	}}
	m := NewModelWithSize(backend, 100, 24)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 11, Type: BlockUser, Content: "old first"})
	m.viewport.AppendBlock(&Block{ID: 12, Type: BlockUser, Content: "old second"})
	m.focusedBlockID = 12
	m.refreshBlockFocus()

	m.rebuildViewportFromMessagesWithReason("session_restored")

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want 2", len(blocks))
	}
	if blocks[0].Type != BlockCompactionSummary {
		t.Fatalf("blocks[0].Type = %v, want BlockCompactionSummary", blocks[0].Type)
	}
	if m.focusedBlockID != blocks[0].ID {
		t.Fatalf("focusedBlockID = %d, want compaction summary block %d", m.focusedBlockID, blocks[0].ID)
	}
	if !blocks[0].Focused {
		t.Fatal("expected visible compaction summary to take focus after session restore rebuild")
	}
}

func TestSessionRestoredRebuildDoesNotReuseOldCompactionRawForNewSummary(t *testing.T) {
	backend := &sessionControlAgent{messages: []message.Message{{
		Role:                "user",
		IsCompactionSummary: true,
		Content:             "[Context Summary]\nsummary 2\n\n[Context compressed]\nArchived history files:\n- history-2.md",
	}}}
	m := NewModelWithSize(backend, 100, 24)
	m.mode = ModeNormal
	old := &Block{
		ID:                     7,
		Type:                   BlockCompactionSummary,
		CompactionSummaryRaw:   "[Context Summary]\nsummary 1\n\n[Context compressed]\nArchived history files:\n- history-1.md",
		CompactionPreviewLines: maxCompactionSummaryPreviewLines,
		Content:                formatCompactionSummaryDisplay("[Context Summary]\nsummary 1\n\n[Context compressed]\nArchived history files:\n- history-1.md", false, maxCompactionSummaryPreviewLines),
		Collapsed:              false,
	}
	m.viewport.AppendBlock(old)
	m.focusedBlockID = old.ID
	m.refreshBlockFocus()

	m.rebuildViewportFromMessagesWithReason("session_restored")

	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 || blocks[0].Type != BlockCompactionSummary {
		t.Fatalf("visible blocks after rebuild = %#v, want single compaction summary", blocks)
	}
	got := blocks[0]
	if !got.Collapsed {
		t.Fatal("new compaction summary should not inherit expanded state from previous summary with different raw content")
	}
	if !strings.Contains(got.CompactionSummaryRaw, "history-2.md") {
		t.Fatalf("CompactionSummaryRaw = %q, want history-2.md", got.CompactionSummaryRaw)
	}
	if strings.Contains(got.CompactionSummaryRaw, "history-1.md") {
		t.Fatalf("CompactionSummaryRaw = %q, should not retain old history-1.md", got.CompactionSummaryRaw)
	}
	got.Toggle()
	if !strings.Contains(got.Content, "history-2.md") {
		t.Fatalf("expanded content = %q, want history-2.md", got.Content)
	}
}

func TestSessionRestoredEventClearsStartupRestorePlaceholder(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.startupRestorePending = true
	m.pendingScrollDelta = 9
	m.scrollFlushScheduled = true

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	if cmd == nil {
		t.Fatal("SessionRestoredEvent should schedule a rebuild message")
	}
	updated, next := m.Update(cmd())
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	m = *model
	if next != nil {
		_ = next
	}
	if m.startupRestorePending {
		t.Fatal("SessionRestoredEvent should clear startupRestorePending")
	}
	if m.pendingScrollDelta != 0 || m.scrollFlushScheduled {
		t.Fatal("SessionRestoredEvent should clear pending scroll flush state")
	}
}

func TestNarrowStatusBarShowsRunningModelRefVerbatim(t *testing.T) {
	events := make(chan agent.AgentEvent, 1)
	a := &sessionControlAgent{
		events:           events,
		providerModelRef: "sample/gpt-5.5@xhigh",
	}
	m := NewModelWithSize(a, 80, 24)
	m.mode = ModeNormal
	m.updateRightPanelVisible()

	plain := stripANSI(m.renderStatusBar())
	want := "sample/gpt-5.5@xhigh"
	if !strings.Contains(plain, want) {
		t.Fatalf("status bar should show RunningModelRef verbatim %q; got %q", want, plain)
	}
}

func TestNarrowStatusBarDoesNotLeakVariantToFallbackModel(t *testing.T) {
	events := make(chan agent.AgentEvent, 1)
	a := &sessionControlAgent{
		events:           events,
		providerModelRef: "sample/gpt-5.5@xhigh",
		runningModelRef:  "sample/glm-5.1",
		runningVariant:   "xhigh",
	}
	m := NewModelWithSize(a, 80, 24)
	m.mode = ModeNormal
	m.updateRightPanelVisible()

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "sample/glm-5.1") {
		t.Fatalf("status bar should show fallback running model; got %q", plain)
	}
	if strings.Contains(plain, "sample/glm-5.1@xhigh") {
		t.Fatalf("status bar should not leak selected variant onto fallback model; got %q", plain)
	}
}

func TestNarrowStatusBarShowsFallbackModelWithoutVariantWhenPrimaryHasNoVariant(t *testing.T) {
	events := make(chan agent.AgentEvent, 1)
	a := &sessionControlAgent{
		events:           events,
		providerModelRef: "sample/gpt-5.5",
		runningModelRef:  "sample/glm-5.1",
	}
	m := NewModelWithSize(a, 80, 24)
	m.mode = ModeNormal
	m.updateRightPanelVisible()

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "sample/glm-5.1") {
		t.Fatalf("status bar should show fallback model name; got %q", plain)
	}
	if strings.Contains(plain, "@") {
		t.Fatalf("status bar should not show variant for fallback when primary has none; got %q", plain)
	}
}

func TestNarrowStatusBarShowsFallbackVariantWhenRunningModelRefIncludesVariant(t *testing.T) {
	events := make(chan agent.AgentEvent, 1)
	a := &sessionControlAgent{
		events:           events,
		providerModelRef: "sample/gpt-5.5@xhigh",
		runningModelRef:  "sample/glm-5.1@high",
		runningVariant:   "xhigh",
	}
	m := NewModelWithSize(a, 80, 24)
	m.mode = ModeNormal
	m.updateRightPanelVisible()

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "sample/glm-5.1@high") {
		t.Fatalf("status bar should show fallback running model variant; got %q", plain)
	}
	if strings.Contains(plain, "sample/glm-5.1@xhigh") {
		t.Fatalf("status bar should not leak selected variant onto fallback model; got %q", plain)
	}
}

func TestSidebarSubAgentModelRefs(t *testing.T) {
	s := NewSidebar(DefaultTheme())
	s.Update([]agent.SubAgentInfo{
		{InstanceID: "a1", SelectedRef: "p/m@high", RunningRef: "p/m2"},
	}, "main", "builder")
	sel, run, ok := s.SubAgentModelRefs("a1")
	if !ok || sel != "p/m@high" || run != "p/m2" {
		t.Fatalf("SubAgentModelRefs = (%q,%q,%v), want (p/m@high, p/m2, true)", sel, run, ok)
	}
	_, _, ok = s.SubAgentModelRefs("missing")
	if ok {
		t.Fatal("expected ok false for unknown agent id")
	}
}

// Narrow status bar model pill matches InfoPanel MODEL: AgentForTUI.RunningModelRef() only
// (not sidebar-cached sub refs), so it follows backend focus the same way as the right panel.
func TestNarrowStatusBarModelUsesRunningModelRefNotSidebarCache(t *testing.T) {
	events := make(chan agent.AgentEvent, 1)
	a := &sessionControlAgent{
		events:           events,
		providerModelRef: "main/huge",
	}
	m := NewModelWithSize(a, 80, 24)
	m.mode = ModeNormal
	m.updateRightPanelVisible()
	m.focusedAgentID = "worker-1"
	m.sidebar.Update([]agent.SubAgentInfo{
		{InstanceID: "worker-1", SelectedRef: "sample/tiny", RunningRef: "sample/tiny"},
	}, "worker-1", "builder")

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "main/huge") {
		t.Fatalf("status bar should show RunningModelRef %q; got %q", "main/huge", plain)
	}
	if strings.Contains(plain, "sample/tiny") {
		t.Fatalf("status bar should not use sidebar-cached sub ref when it differs from RunningModelRef; got %q", plain)
	}
}

func TestStatusBarCurrentAgentLabelMainWithoutAgent(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	if got := m.statusBarCurrentAgentLabel(); got != "main" {
		t.Fatalf("label = %q, want main", got)
	}
}

func TestStatusBarCurrentAgentLabelSubUsesAgentDefName(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.focusedAgentID = "s-1"
	m.sidebar.Update([]agent.SubAgentInfo{
		{InstanceID: "s-1", AgentDefName: "reviewer", TaskDesc: "check code", SelectedRef: "p/m", RunningRef: "p/m"},
	}, "s-1", "builder")
	if got := m.statusBarCurrentAgentLabel(); got != "s-1" {
		t.Fatalf("label = %q, want s-1", got)
	}
}

func TestStatusBarCurrentAgentLabelSubOmitsTaskDesc(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.focusedAgentID = "s-2"
	m.sidebar.Update([]agent.SubAgentInfo{
		{InstanceID: "s-2", TaskDesc: "do the thing", SelectedRef: "p/m", RunningRef: "p/m"},
	}, "s-2", "builder")
	got := m.statusBarCurrentAgentLabel()
	if got != "s-2" {
		t.Fatalf("without AgentDefName, label should be instance id only; got %q", got)
	}
}

func TestHandleSwitchRoleInvalidatesCachesImmediately(t *testing.T) {
	backend := &sessionControlAgent{
		events:         make(chan agent.AgentEvent, 1),
		currentRole:    "builder",
		availableRoles: []string{"builder", "planner"},
	}
	m := NewModelWithSize(backend, 100, 24)
	m.cachedStatusKey = "cached-status"
	m.cachedInfoPanelFP = "cached-info"

	m.handleSwitchRole()

	if got := backend.currentRole; got != "planner" {
		t.Fatalf("currentRole = %q, want planner", got)
	}
	if m.cachedStatusKey != "" {
		t.Fatalf("cachedStatusKey = %q, want cleared", m.cachedStatusKey)
	}
	if m.cachedInfoPanelFP != "" {
		t.Fatalf("cachedInfoPanelFP = %q, want cleared", m.cachedInfoPanelFP)
	}
}

func TestHandleSwitchAgentRefreshesInfoPanelModelWithoutEvent(t *testing.T) {
	backend := &sessionControlAgent{
		events:           make(chan agent.AgentEvent, 1),
		currentRole:      "builder",
		providerModelRef: "main/huge",
		subAgents: []agent.SubAgentInfo{{
			InstanceID:   "agent-1",
			AgentDefName: "reviewer",
			TaskDesc:     "check code",
		}},
		providerModelRefByFocus: map[string]string{"": "main/huge", "agent-1": "worker/review"},
		runningModelRefByFocus:  map[string]string{"": "main/huge", "agent-1": "worker/review"},
		runningVariantByFocus:   map[string]string{"agent-1": "high"},
	}
	m := NewModelWithSize(backend, 100, 24)
	m.refreshSidebar()

	before := stripANSI(m.renderInfoPanel(40, 20))
	if !strings.Contains(before, "main/huge") {
		t.Fatalf("initial info panel = %q, want main model", before)
	}

	m.handleSwitchAgent()

	if got := m.focusedAgentID; got != "agent-1" {
		t.Fatalf("focusedAgentID = %q, want agent-1", got)
	}
	if got := backend.focused; got != "agent-1" {
		t.Fatalf("backend focused = %q, want agent-1", got)
	}
	after := stripANSI(m.renderInfoPanel(40, 20))
	if !strings.Contains(after, "worker/review@high") {
		t.Fatalf("info panel after agent switch = %q, want worker/review@high", after)
	}
	if strings.Contains(after, "main/huge") {
		t.Fatalf("info panel after agent switch should not keep stale main model: %q", after)
	}
}

func TestRoleChangedEventRefreshesCachedStatusBarViewingLabel(t *testing.T) {
	backend := &sessionControlAgent{
		events:         make(chan agent.AgentEvent, 1),
		currentRole:    "builder",
		availableRoles: []string{"builder", "planner"},
	}
	m := NewModelWithSize(backend, 100, 24)
	scr := newCountingScreen(100, 24)
	m.Draw(scr, image.Rect(0, 0, 100, 24))
	if plain := stripANSI(m.cachedStatusRender.text); !strings.Contains(plain, "builder") {
		t.Fatalf("initial status bar = %q, want builder", plain)
	}

	backend.currentRole = "planner"
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RoleChangedEvent{Role: "planner"}})
	scr = newCountingScreen(100, 24)
	m.Draw(scr, image.Rect(0, 0, 100, 24))

	plain := stripANSI(m.cachedStatusRender.text)
	if !strings.Contains(plain, "planner") {
		t.Fatalf("status bar after role change = %q, want planner", plain)
	}
	if strings.Contains(plain, "builder") {
		t.Fatalf("status bar after role change should not keep stale builder label: %q", plain)
	}
}

func TestHandleAgentEventRefreshSidebarAlsoInvalidatesUsageCaches(t *testing.T) {
	backend := &sessionControlAgent{
		events:      make(chan agent.AgentEvent, 1),
		currentRole: "builder",
		subAgents: []agent.SubAgentInfo{{
			InstanceID: "agent-1",
			TaskDesc:   "ship tests",
		}},
	}
	m := NewModelWithSize(backend, 100, 24)
	m.statusBarAgentSnapshotDirty = false
	m.usageStats.renderVersion = 7
	m.usageStats.linesCacheWidth = 80
	m.usageStats.linesCacheVer = 9
	m.usageStats.linesCacheLines = []string{"cached"}
	m.usageStats.dialogCacheW = 40
	m.usageStats.dialogCacheH = 10
	m.usageStats.dialogCacheScroll = 3
	m.usageStats.dialogCacheVer = 11
	m.usageStats.dialogCacheTheme = "dark"
	m.usageStats.dialogCacheText = "cached"

	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentStatusEvent{AgentID: "agent-1", Status: "idle"}})

	if !m.statusBarAgentSnapshotDirty {
		t.Fatal("AgentStatusEvent should dirty status bar snapshot when it refreshes sidebar")
	}
	if got := m.usageStats.renderVersion; got != 8 {
		t.Fatalf("usageStats.renderVersion = %d, want 8", got)
	}
	if m.usageStats.linesCacheWidth != 0 || m.usageStats.linesCacheVer != 0 || m.usageStats.linesCacheLines != nil {
		t.Fatal("AgentStatusEvent should clear usage stats lines cache when it refreshes sidebar")
	}
	if m.usageStats.dialogCacheW != 0 || m.usageStats.dialogCacheH != 0 || m.usageStats.dialogCacheScroll != 0 || m.usageStats.dialogCacheVer != 0 || m.usageStats.dialogCacheTheme != "" || m.usageStats.dialogCacheText != "" {
		t.Fatal("AgentStatusEvent should clear usage stats dialog cache when it refreshes sidebar")
	}
}

func TestSetFocusedAgentRefreshesCachedStatusBarViewingLabel(t *testing.T) {
	backend := &sessionControlAgent{
		events:      make(chan agent.AgentEvent, 1),
		currentRole: "builder",
		subAgents: []agent.SubAgentInfo{{
			InstanceID:   "agent-1",
			AgentDefName: "reviewer",
			TaskDesc:     "check code",
		}},
	}
	m := NewModelWithSize(backend, 100, 24)
	m.refreshSidebar()
	scr := newCountingScreen(100, 24)
	m.Draw(scr, image.Rect(0, 0, 100, 24))
	if plain := stripANSI(m.cachedStatusRender.text); !strings.Contains(plain, "builder") {
		t.Fatalf("initial status bar = %q, want builder", plain)
	}

	m.setFocusedAgent("agent-1")
	scr = newCountingScreen(100, 24)
	m.Draw(scr, image.Rect(0, 0, 100, 24))

	plain := stripANSI(m.cachedStatusRender.text)
	if !strings.Contains(plain, "agent-1") {
		t.Fatalf("status bar after focus switch = %q, want agent-1", plain)
	}
	if strings.Contains(plain, "reviewer") {
		t.Fatalf("status bar after focus switch should not include sub-agent type: %q", plain)
	}
	if strings.Contains(plain, "◉ builder") {
		t.Fatalf("status bar after focus switch should not keep stale builder viewing pill: %q", plain)
	}
}

func TestSetFocusedAgentRestoresComposerStatePerAgent(t *testing.T) {
	backend := &sessionControlAgent{
		events: make(chan agent.AgentEvent, 1),
		messagesByFocus: map[string][]message.Message{
			"":        {{Role: "assistant", Content: "main"}},
			"agent-1": {{Role: "assistant", Content: "worker"}},
		},
	}
	m := NewModelWithSize(backend, 100, 24)
	m.input.SetValue("main draft")
	m.input.syncHeight()
	m.attachments = []Attachment{{FileName: "main.png", MimeType: "image/png", Data: []byte{1}}}
	m.editingQueuedDraftID = "draft-1"

	m.setFocusedAgent("agent-1")

	if got := m.input.Value(); got != "" {
		t.Fatalf("subagent input value = %q, want empty fresh draft", got)
	}
	if got := len(m.attachments); got != 0 {
		t.Fatalf("len(subagent attachments) = %d, want 0", got)
	}
	if got := m.editingQueuedDraftID; got != "" {
		t.Fatalf("subagent editingQueuedDraftID = %q, want empty", got)
	}

	m.input.SetValue("worker draft")
	m.input.syncHeight()

	m.setFocusedAgent("")

	if got := m.input.Value(); got != "main draft" {
		t.Fatalf("main input value after restore = %q, want main draft", got)
	}
	if got := len(m.attachments); got != 1 {
		t.Fatalf("len(main attachments) = %d, want 1", got)
	}
	if got := m.editingQueuedDraftID; got != "draft-1" {
		t.Fatalf("main editingQueuedDraftID = %q, want draft-1", got)
	}

	m.setFocusedAgent("agent-1")

	if got := m.input.Value(); got != "worker draft" {
		t.Fatalf("worker input value after restore = %q, want worker draft", got)
	}
	if got := len(m.attachments); got != 0 {
		t.Fatalf("len(worker attachments) = %d, want 0", got)
	}
	if got := m.editingQueuedDraftID; got != "" {
		t.Fatalf("worker editingQueuedDraftID after restore = %q, want empty", got)
	}
}

func TestFocusedSubAgentViewHidesMainQueuedDrafts(t *testing.T) {
	backend := &sessionControlAgent{
		events: make(chan agent.AgentEvent, 1),
		messagesByFocus: map[string][]message.Message{
			"":        {{Role: "assistant", Content: "main"}},
			"agent-1": {{Role: "assistant", Content: "worker"}},
		},
	}
	m := NewModelWithSize(backend, 100, 24)
	m.queuedDrafts = []queuedDraft{{
		ID:             "draft-1",
		Content:        "queued main",
		DisplayContent: "queued main",
		QueuedAt:       time.Now(),
	}}

	if got := stripANSI(m.renderQueuedDrafts(40, 3)); !strings.Contains(got, "queued main") {
		t.Fatalf("main renderQueuedDrafts = %q, want queued draft text", got)
	}

	m.setFocusedAgent("agent-1")

	if got := len(m.visibleQueuedDrafts()); got != 0 {
		t.Fatalf("len(visibleQueuedDrafts) in subagent view = %d, want 0", got)
	}
	if got := stripANSI(m.renderQueuedDrafts(40, 3)); got != "" {
		t.Fatalf("subagent renderQueuedDrafts = %q, want empty", got)
	}
	if got := m.generateLayout(m.width, m.height).queue.Dy(); got != 0 {
		t.Fatalf("subagent queue height = %d, want 0", got)
	}

	m.setFocusedAgent("")

	if got := stripANSI(m.renderQueuedDrafts(40, 3)); !strings.Contains(got, "queued main") {
		t.Fatalf("main renderQueuedDrafts after restore = %q, want queued draft text", got)
	}
}

func TestStatusBarViewingPillUsesFocusedAgentColor(t *testing.T) {
	backend := &sessionControlAgent{
		events:      make(chan agent.AgentEvent, 1),
		currentRole: "builder",
		subAgents: []agent.SubAgentInfo{{
			InstanceID:   "agent-1",
			AgentDefName: "reviewer",
			Color:        "196",
		}},
	}
	m := NewModelWithSize(backend, 100, 24)
	m.refreshSidebar()
	m.setFocusedAgent("agent-1")

	got := m.renderStatusBar()
	want := renderStatusBarViewingPill("agent-1", "196")
	if !strings.Contains(got, want) {
		t.Fatalf("status bar = %q, want colored viewing pill %q", got, want)
	}
}

func TestStatusBarViewingPillColorRefreshesWhenFocusedAgentChanges(t *testing.T) {
	backend := &sessionControlAgent{
		events:      make(chan agent.AgentEvent, 1),
		currentRole: "builder",
		subAgents: []agent.SubAgentInfo{
			{InstanceID: "agent-1", AgentDefName: "reviewer", Color: "196"},
			{InstanceID: "agent-2", AgentDefName: "coder", Color: "81"},
		},
	}
	m := NewModelWithSize(backend, 100, 24)
	m.refreshSidebar()

	m.setFocusedAgent("agent-1")
	first := m.renderStatusBar()
	wantFirst := renderStatusBarViewingPill("agent-1", "196")
	if !strings.Contains(first, wantFirst) {
		t.Fatalf("first status bar = %q, want %q", first, wantFirst)
	}

	m.setFocusedAgent("agent-2")
	second := m.renderStatusBar()
	wantSecond := renderStatusBarViewingPill("agent-2", "81")
	if !strings.Contains(second, wantSecond) {
		t.Fatalf("second status bar = %q, want %q", second, wantSecond)
	}
	if strings.Contains(second, wantFirst) {
		t.Fatalf("second status bar should not keep stale colored pill %q: %q", wantFirst, second)
	}
}

func TestFormatBusyTotalWall(t *testing.T) {
	if got := formatBusyTotalWall(59 * time.Second); got != "" {
		t.Fatalf("under 1m should be empty; got %q", got)
	}
	if got := formatBusyTotalWall(90 * time.Second); got != "1m30s" {
		t.Fatalf("90s = %q, want 1m30s", got)
	}
	if got := formatBusyTotalWall(3720 * time.Second); got != "1h2m0s" {
		t.Fatalf("3720s = %q, want 1h2m0s", got)
	}
}

func TestFormatStatusBarElapsedParen(t *testing.T) {
	if got := formatStatusBarElapsed(45 * time.Second); got != " 45s" {
		t.Fatalf("45s = %q, want \" 45s\"", got)
	}
	if got := formatStatusBarElapsed(59 * time.Second); got != " 59s" {
		t.Fatalf("59s = %q, want \" 59s\"", got)
	}
	if got := formatStatusBarElapsed(60 * time.Second); got != " 1m0s" {
		t.Fatalf("60s = %q, want \" 1m0s\"", got)
	}
	if got := formatStatusBarElapsed(135 * time.Second); got != " 2m15s" {
		t.Fatalf("135s = %q, want \" 2m15s\"", got)
	}
	if got := formatStatusBarElapsed(3720 * time.Second); got != " 1h2m0s" {
		t.Fatalf("3720s = %q, want \" 1h2m0s\"", got)
	}
}

func TestTurnBusyKey(t *testing.T) {
	if got := turnBusyKey(""); got != "main" {
		t.Fatalf("empty -> %q, want main", got)
	}
	if got := turnBusyKey("main"); got != "main" {
		t.Fatalf("main -> %q, want main", got)
	}
	if got := turnBusyKey("worker-1"); got != "worker-1" {
		t.Fatalf("worker-1 -> %q", got)
	}
}

func TestRenderStatusBarShowsCompactingContextWhenActivityIsCompacting(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityCompacting, AgentID: "main", Detail: "context"}
	// Compaction is now in the background lane
	m.compactionBgStatus = compactionBackgroundStatus{Active: true, StartedAt: time.Now().Add(-10 * time.Second)}
	plain := stripANSI(m.renderStatusBar())
	if strings.Contains(plain, "Compacting context") {
		t.Fatalf("status bar should not show legacy compacting text, got %q", plain)
	}
	if !strings.Contains(plain, "■") && !strings.Contains(plain, "▪") {
		t.Fatalf("status bar should still show compacting icon in background lane, got %q", plain)
	}
}

func TestRenderStatusBarShowsCompactionProgressInBackgroundLane(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.mode = ModeNormal
	m.compactionBgStatus = compactionBackgroundStatus{
		Active:    true,
		StartedAt: time.Now().Add(-12 * time.Second),
		Bytes:     8 * 1024,
		Events:    3,
	}
	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "■") && !strings.Contains(plain, "▪") {
		t.Fatalf("status bar should show compaction icon, got %q", plain)
	}
	if !strings.Contains(plain, "↓ 8 KB") {
		t.Fatalf("status bar should show compaction progress bytes, got %q", plain)
	}
}

func TestRenderStatusBarShowsLastWhenIdleAndSettled(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.workingDir = "/tmp"
	m.updateRightPanelVisible()
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}
	when := time.Date(2024, 6, 15, 15, 4, 12, 0, time.Local)
	b := &Block{ID: 1, Type: BlockAssistant, Content: "hi", AgentID: "", StartedAt: when}
	m.viewport.AppendBlock(b)

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "Since ") || !strings.Contains(plain, "15:04") {
		t.Fatalf("status bar should show last time; got %q", plain)
	}
	if strings.Contains(plain, "15:04:12") {
		t.Fatalf("status bar should use minute precision for idle timestamp; got %q", plain)
	}
}

func TestRenderStatusBarUsesCompactIdleLabelInNarrowLayout(t *testing.T) {
	m := NewModelWithSize(nil, 90, 24)
	m.workingDir = "/tmp"
	m.updateRightPanelVisible()
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}
	when := time.Date(2024, 6, 15, 15, 4, 12, 0, time.Local)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "hi", AgentID: "", StartedAt: when})

	plain := stripANSI(m.renderStatusBar())
	if strings.Contains(plain, "Since update ") {
		t.Fatalf("narrow status bar should not use malformed idle label; got %q", plain)
	}
}

func TestRenderStatusBarShowsLocalShellWhenPending(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.workingDir = "/tmp"
	m.updateRightPanelVisible()
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}
	m.localShellStartedAt = time.Now().Add(-5 * time.Second)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, AgentID: "", UserLocalShellCmd: "echo hi", UserLocalShellPending: true})

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "Shell") {
		t.Fatalf("status bar should show shell activity; got %q", plain)
	}
	if strings.Contains(plain, "Since") && !strings.Contains(plain, "Shell") {
		t.Fatalf("status bar should not show idle label while shell is pending; got %q", plain)
	}
}

func TestRenderStatusBarSwitchesLastByFocusedAgentView(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.workingDir = "/tmp"
	m.updateRightPanelVisible()
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}

	mainWhen := time.Date(2024, 6, 15, 15, 4, 0, 0, time.Local)
	subWhen := time.Date(2024, 6, 15, 15, 6, 0, 0, time.Local)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "main", AgentID: "", StartedAt: mainWhen})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: "sub", AgentID: "agent-1", StartedAt: subWhen})

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "15:04") {
		t.Fatalf("main view should show main settle time; got %q", plain)
	}
	if strings.Contains(plain, "15:06") {
		t.Fatalf("main view should not show sub-agent settle time; got %q", plain)
	}

	m.setFocusedAgent("agent-1")
	plain = stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "15:06") {
		t.Fatalf("sub-agent view should show sub-agent settle time; got %q", plain)
	}
	if strings.Contains(plain, "15:04") {
		t.Fatalf("sub-agent view should not show main settle time; got %q", plain)
	}
}

func TestRenderActivityStreamingUsesElapsedWhenNoProgress(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	started := time.Now().Add(-90 * time.Second)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "hi", StartedAt: started})
	a := agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	out := stripANSI(m.renderActivity(a, 200))
	if !strings.Contains(out, "⣿") && !strings.Contains(out, "⣶") {
		t.Fatalf("expected streaming icon in %q", out)
	}
	if !strings.Contains(out, "1m30s") {
		t.Fatalf("streaming render should show canonical elapsed while in progress, got %q", out)
	}
}

func TestRenderActivityCompactingUsesUnifiedProgressStyle(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	m.activityStartTime["main"] = time.Now().Add(-8 * time.Second)
	a := agent.AgentActivityEvent{Type: agent.ActivityCompacting, AgentID: "main", Detail: "context"}
	out := stripANSI(m.renderActivity(a, 200))
	if strings.Contains(out, "Compacting context") {
		t.Fatalf("compacting render should not include legacy label, got %q", out)
	}
	if !strings.Contains(out, "■") && !strings.Contains(out, "▪") {
		t.Fatalf("compacting render should still show icon, got %q", out)
	}
	if !strings.Contains(out, " 8s") {
		t.Fatalf("compacting render should show current phase timer in parens, got %q", out)
	}
}

func TestRenderActivityRetryingUsesExplicitRoundAndElapsedLabels(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	m.activityStartTime["main"] = time.Now().Add(-17 * time.Second)
	a := agent.AgentActivityEvent{Type: agent.ActivityRetrying, AgentID: "main", Detail: "round 6"}
	out := stripANSI(m.renderActivity(a, 200))
	if strings.Contains(out, "Retrying") {
		t.Fatalf("retrying render should not include legacy label, got %q", out)
	}
	if !strings.Contains(out, "↺") {
		t.Fatalf("retrying render should still show icon, got %q", out)
	}
	if !strings.Contains(out, " 17s") {
		t.Fatalf("retrying render should show current phase timer in parens, got %q", out)
	}
}

func TestRenderActivityCoolingUsesExplicitRemainingLabel(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	m.activityStartTime["main"] = time.Now().Add(-3 * time.Second)
	a := agent.AgentActivityEvent{Type: agent.ActivityCooling, AgentID: "main", Detail: "45s"}
	out := stripANSI(m.renderActivity(a, 200))
	if strings.Contains(out, "Cooling down") {
		t.Fatalf("cooling render should not include legacy label, got %q", out)
	}
}

func TestRenderActivityWaitingUsesExplicitElapsedLabel(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	m.activityStartTime["main"] = time.Now().Add(-7 * time.Second)
	a := agent.AgentActivityEvent{Type: agent.ActivityWaitingHeaders, AgentID: "main"}
	out := stripANSI(m.renderActivity(a, 200))
	if strings.Contains(out, "Waiting for headers") {
		t.Fatalf("waiting render should not include legacy label, got %q", out)
	}
	if !strings.Contains(out, " 7s") {
		t.Fatalf("waiting render should keep phase timer in parens, got %q", out)
	}
}

func TestRenderActivityWaitingTokenUsesDistinctLabel(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	m.activityStartTime["main"] = time.Now().Add(-7 * time.Second)
	a := agent.AgentActivityEvent{Type: agent.ActivityWaitingToken, AgentID: "main"}
	out := stripANSI(m.renderActivity(a, 200))
	if strings.Contains(out, "Waiting for first token") {
		t.Fatalf("waiting_token render should not include legacy label, got %q", out)
	}
	if !strings.Contains(out, " 7s") {
		t.Fatalf("waiting_token render should keep phase timer in parens, got %q", out)
	}
}

func TestRenderActivityStreamingDoesNotShowLegacyLabel(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	m.activityStartTime["main"] = time.Now().Add(-7 * time.Second)
	out := stripANSI(m.renderActivity(agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}, 200))
	if strings.Contains(out, "Streaming") {
		t.Fatalf("renderActivity(streaming) should not include legacy label; got %q", out)
	}
}

func TestRenderActivityUsesCompactParenStyleWhenWidthIsTight(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	m.activityStartTime["main"] = time.Now().Add(-7 * time.Second)

	waiting := stripANSI(m.renderActivity(agent.AgentActivityEvent{Type: agent.ActivityWaitingHeaders, AgentID: "main"}, 32))
	if strings.Contains(waiting, "Waiting for headers") {
		t.Fatalf("narrow waiting render should not use legacy label, got %q", waiting)
	}
	if !strings.Contains(waiting, "↺ 7s") {
		t.Fatalf("narrow waiting render should use icon+elapsed style, got %q", waiting)
	}

	waitingToken := stripANSI(m.renderActivity(agent.AgentActivityEvent{Type: agent.ActivityWaitingToken, AgentID: "main"}, 64))
	if strings.Contains(waitingToken, "Waiting for first token") {
		t.Fatalf("waiting_token render should not use legacy label, got %q", waitingToken)
	}
	if !strings.Contains(waitingToken, "↺ 7s") {
		t.Fatalf("waiting_token render should use icon+elapsed style, got %q", waitingToken)
	}

	cooling := stripANSI(m.renderActivity(agent.AgentActivityEvent{Type: agent.ActivityCooling, AgentID: "main", Detail: "45s"}, 32))
	if strings.Contains(cooling, "Cooling down") {
		t.Fatalf("narrow cooling render should not use legacy label, got %q", cooling)
	}
}

func TestRenderActivityShowsTimeFromZeroSeconds(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	m.activityStartTime["main"] = time.Now().Add(-4 * time.Second)
	a := agent.AgentActivityEvent{Type: agent.ActivityWaitingHeaders, AgentID: "main"}
	out := stripANSI(m.renderActivity(a, 200))
	if !strings.Contains(out, " 4s") {
		t.Fatalf("phase timer should start from 0s, got %q", out)
	}

	m2 := NewModelWithSize(nil, 200, 24)
	started := time.Now().Add(-4 * time.Second)
	m2.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "hi", StartedAt: started})
	out = stripANSI(m2.renderActivity(agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}, 200))
	if strings.Contains(out, statusBarTotalLabel()) || strings.Contains(out, statusBarIdleLabel(false)) {
		t.Fatalf("busy render should still avoid legacy extras, got %q", out)
	}
	if !strings.Contains(out, " 4s") && !strings.Contains(out, " 0s") {
		t.Fatalf("streaming elapsed should be shown immediately, got %q", out)
	}
}

func TestRenderActivityTruncatesToCoreWhenWidthIsTight(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	started := time.Now().Add(-90 * time.Second)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "hi", StartedAt: started})
	m.activityStartTime["main"] = time.Now().Add(-2 * time.Second)
	a := agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	wide := stripANSI(m.renderActivity(a, 200))
	if !strings.Contains(wide, "⣿") && !strings.Contains(wide, "⣶") {
		t.Fatalf("wide render should include streaming icon; got %q", wide)
	}
	if !strings.Contains(wide, " 2s") {
		t.Fatalf("wide render should show phase timer from 0s; got %q", wide)
	}
	if strings.Contains(wide, statusBarTotalLabel()) || strings.Contains(wide, statusBarIdleLabel(false)) {
		t.Fatalf("wide render should not include legacy busy extras; got %q", wide)
	}

	narrow := stripANSI(m.renderActivity(a, 24))
	if strings.Contains(narrow, statusBarTotalLabel()) {
		t.Fatalf("narrow render should drop total label first; got %q", narrow)
	}
	if !strings.Contains(narrow, " 2s") {
		t.Fatalf("narrow render should keep phase timer from 0s; got %q", narrow)
	}
	if strings.Contains(narrow, "Streaming") {
		t.Fatalf("narrow render should not preserve legacy streaming label; got %q", narrow)
	}
}

func TestRenderActivityUsesCompactLastLabelInCompactExtras(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	started := time.Now().Add(-90 * time.Second)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "hi", StartedAt: started})
	m.activityStartTime["main"] = time.Now().Add(-2 * time.Second)
	a := agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	compact := stripANSI(m.renderActivity(a, 46))
	if strings.Contains(compact, statusBarTotalLabel()) || strings.Contains(compact, statusBarIdleLabel(false)) {
		t.Fatalf("compact render should not include legacy busy extras; got %q", compact)
	}
	if !strings.Contains(compact, "⣿") && !strings.Contains(compact, "⣶") {
		t.Fatalf("compact render should keep streaming icon; got %q", compact)
	}
}

func TestRenderActivityOverflowDropsElapsedThenSinceThenPhaseTimer(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	started := time.Now().Add(-3 * time.Minute)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "hi", StartedAt: started})
	m.activityStartTime["main"] = time.Now().Add(-20 * time.Second)
	a := agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	full := stripANSI(m.renderActivity(a, 200))
	if !strings.Contains(full, "⣿") && !strings.Contains(full, "⣶") {
		t.Fatalf("full render should keep streaming icon, got %q", full)
	}
	if !strings.Contains(full, " 20s") {
		t.Fatalf("full render should keep phase elapsed, got %q", full)
	}
	if strings.Contains(full, statusBarTotalLabel()) || strings.Contains(full, statusBarIdleLabel(false)) {
		t.Fatalf("full render should not include legacy busy extras, got %q", full)
	}

	noElapsed := stripANSI(m.renderActivity(a, 40))
	if strings.Contains(noElapsed, statusBarTotalLabel()) {
		t.Fatalf("medium render should hide anchor elapsed first, got %q", noElapsed)
	}
	if !strings.Contains(noElapsed, " 20s") {
		t.Fatalf("medium render should retain phase timer, got %q", noElapsed)
	}
	if strings.Contains(noElapsed, statusBarIdleLabel(false)) {
		t.Fatalf("medium render should not retain legacy since label, got %q", noElapsed)
	}

	noSince := stripANSI(m.renderActivity(a, 28))
	if strings.Contains(noSince, statusBarTotalLabel()) {
		t.Fatalf("narrower render should not restore total label, got %q", noSince)
	}
	if !strings.Contains(noSince, " 20s") {
		t.Fatalf("narrower render should still retain phase timer, got %q", noSince)
	}

	noPhase := stripANSI(m.renderActivity(a, 14))
	if strings.Contains(noPhase, statusBarTotalLabel()) || strings.Contains(noPhase, statusBarIdleLabel(false)) {
		t.Fatalf("tight render should keep only the minimal icon/timer form, got %q", noPhase)
	}
	if strings.Contains(noPhase, "Streaming") {
		t.Fatalf("tight render should not restore legacy streaming label, got %q", noPhase)
	}
}

func TestRenderStatusBarUsesQueuedDraftStartWhenIdle(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.workingDir = "/tmp"
	m.updateRightPanelVisible()
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}
	queuedAt := time.Date(2024, 6, 15, 15, 7, 0, 0, time.Local)
	m.queuedDrafts = []queuedDraft{{ID: "draft-1", Content: "queued", DisplayContent: "queued", QueuedAt: queuedAt}}

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "15:07") {
		t.Fatalf("status bar should show queued draft start time; got %q", plain)
	}
}

func TestStatusBarDynamicCacheKeyBusyUsesAnimationFrameBucket(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityCooling, AgentID: "main", Detail: "45s"}
	t0 := time.Unix(100, 0)
	t1 := t0.Add(199 * time.Millisecond)
	t2 := t0.Add(200 * time.Millisecond)
	if got := m.statusBarDynamicCacheKeyAt(t0); got != "frame:500" {
		t.Fatalf("busy cache key at t0 = %q, want frame:500", got)
	}
	if got := m.statusBarDynamicCacheKeyAt(t1); got != "frame:500" {
		t.Fatalf("busy cache key at t1 = %q, want frame:500", got)
	}
	if got := m.statusBarDynamicCacheKeyAt(t2); got != "frame:501" {
		t.Fatalf("busy cache key at t2 = %q, want frame:501", got)
	}
}

func TestStatusBarDynamicCacheKeyIdleUsesMinuteBucket(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	when := time.Date(2024, 6, 15, 15, 4, 12, 0, time.Local)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "hi", StartedAt: when})
	t0 := time.Unix(120, 0)
	t1 := t0.Add(30 * time.Second)
	t2 := t0.Add(time.Minute)
	if got := m.statusBarDynamicCacheKeyAt(t0); got != "min:2" {
		t.Fatalf("idle cache key at t0 = %q, want min:2", got)
	}
	if got := m.statusBarDynamicCacheKeyAt(t1); got != "min:2" {
		t.Fatalf("idle cache key at t1 = %q, want min:2", got)
	}
	if got := m.statusBarDynamicCacheKeyAt(t2); got != "min:3" {
		t.Fatalf("idle cache key at t2 = %q, want min:3", got)
	}
}

func TestStatusBarDynamicCacheKeyCompactingUsesAnimationFrames(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityCompacting, AgentID: "main", Detail: "context"}
	t1 := time.UnixMilli(1000)
	t2 := t1.Add(200 * time.Millisecond)
	if got1, got2 := m.statusBarDynamicCacheKeyAt(t1), m.statusBarDynamicCacheKeyAt(t2); got1 == got2 {
		t.Fatalf("compacting cache key should follow animation frame cadence, got identical keys %q", got1)
	}
}

func TestStatusBarDynamicCacheKeyCompactingUsesSecondBucket(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityCompacting, AgentID: "main"}
	t0 := time.UnixMilli(120000)
	t1 := t0.Add(100 * time.Millisecond)
	t2 := t0.Add(200 * time.Millisecond)
	if got0, got1, got2 := m.statusBarDynamicCacheKeyAt(t0), m.statusBarDynamicCacheKeyAt(t1), m.statusBarDynamicCacheKeyAt(t2); got0 == got1 && got1 == got2 {
		t.Fatalf("compacting cache keys should change with animation frames, got %q %q %q", got0, got1, got2)
	}
}

func TestAgentEventBatchSchedulesStatusBarTickForCompacting(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	updated, cmd := m.Update(agentEventBatchMsg{{
		event: agent.AgentActivityEvent{Type: agent.ActivityCompacting, AgentID: "main", Detail: "context"},
	}})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd == nil {
		t.Fatal("compacting activity batch should schedule follow-up commands")
	}
	if !model.statusBarTickScheduled {
		t.Fatal("compacting activity should schedule a status-bar timing tick")
	}
	if model.animRunning {
		t.Fatal("compacting activity should not restart the visual animation loop")
	}
}

func TestRenderActivityUsesQueuedDraftStartForTotal(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	queuedAt := time.Now().Add(-90 * time.Second)
	m.queuedDrafts = []queuedDraft{{ID: "draft-1", Content: "queued", DisplayContent: "queued", QueuedAt: queuedAt}}
	a := agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	out := stripANSI(m.renderActivity(a, 200))
	if !strings.Contains(out, "⣿") && !strings.Contains(out, "⣶") {
		t.Fatalf("expected streaming icon in %q", out)
	}
	if strings.Contains(out, statusBarTotalLabel()) || strings.Contains(out, statusBarIdleLabel(false)) {
		t.Fatalf("queued draft should not restore legacy total/since labels in %q", out)
	}
}

func TestRenderActivityPrefersNewerToolStartOverEarlierSettledBlock(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	older := time.Now().Add(-3 * time.Minute)
	newer := time.Now().Add(-90 * time.Second)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "done", SettledAt: older})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockToolCall, ToolName: "Bash", StartedAt: newer})
	a := agent.AgentActivityEvent{Type: agent.ActivityExecuting, AgentID: "main"}
	out := stripANSI(m.renderActivity(a, 200))
	if !strings.Contains(out, "⚙ 1m30s") {
		t.Fatalf("expected newer tool start to anchor executing elapsed; got %q", out)
	}
}

func TestRenderActivityShowsUnifiedBusyElapsedStyle(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	started := time.Now().Add(-90 * time.Second)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "hi", StartedAt: started})
	m.activityStartTime["main"] = time.Now().Add(-20 * time.Second)
	a := agent.AgentActivityEvent{Type: agent.ActivityConnecting, AgentID: "main"}
	out := stripANSI(m.renderActivity(a, 200))
	if !strings.Contains(out, "⇋ 20s") {
		t.Fatalf("expected unified busy elapsed style in %q", out)
	}
	if strings.Contains(out, statusBarIdleLabel(false)) || strings.Contains(out, statusBarTotalLabel()) {
		t.Fatalf("busy render should not include legacy since/total labels, got %q", out)
	}
}

func TestRenderStatusBarLocalShellDropsStartedWhenWidthIsTight(t *testing.T) {
	m := NewModel(nil)
	m.localShellStartedAt = time.Now().Add(-5 * time.Second)

	wide := stripANSI(m.renderStatusBarLocalShell(200))
	if !strings.Contains(wide, statusBarStartedLabel()) {
		t.Fatalf("wide local shell render should include %q; got %q", statusBarStartedLabel(), wide)
	}

	narrow := stripANSI(m.renderStatusBarLocalShell(22))
	if strings.Contains(narrow, statusBarStartedLabel()) {
		t.Fatalf("narrow local shell render should drop started labels; got %q", narrow)
	}
	if !strings.Contains(narrow, "Shell") {
		t.Fatalf("narrow shell render should keep core text; got %q", narrow)
	}
}

func TestRenderStatusBarLocalShellUsesCompactStartedLabelInCompactLayout(t *testing.T) {
	m := NewModel(nil)
	m.localShellStartedAt = time.Now().Add(-5 * time.Second)

	compact := stripANSI(m.renderStatusBarLocalShell(36))
	if !strings.Contains(compact, statusBarStartedLabel()) {
		t.Fatalf("compact local shell render should use started label; got %q", compact)
	}
}

// ---------------------------------------------------------------------------
// IME switch / restore tests
// ---------------------------------------------------------------------------

func preventIMEApplyInTests(m *Model) {
	m.imeMu.Lock()
	m.imeApplying = true
	m.imeMu.Unlock()
}

// TestIMESwitchIfTransitionOnlyTriggersForNormalModes verifies that
// runIMESwitchIfTransition returns a Cmd only when transitioning INTO a
// Normal-like mode, and nil for same-mode or Insert transitions.
func TestQueryIMECurrentReadsStdoutWithoutConflict(t *testing.T) {
	helper := os.Getenv("CHORD_IME_QUERY_HELPER")
	if helper == "1" {
		_, _ = os.Stdout.WriteString("com.apple.inputmethod.SCIM.ITABC\n")
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestQueryIMECurrentReadsStdoutWithoutConflict")
	cmd.Env = append(os.Environ(), "CHORD_IME_QUERY_HELPER=1")
	out, err := queryIMECurrent(cmd)
	if err != nil {
		t.Fatalf("queryIMECurrent() error = %v", err)
	}
	if out != "com.apple.inputmethod.SCIM.ITABC" {
		t.Fatalf("queryIMECurrent() = %q, want trimmed output", out)
	}
}

func TestGetIMECurrentCmdUsesQueryOutput(t *testing.T) {
	m := NewModel(nil)
	orig := imeQueryCurrent
	defer func() { imeQueryCurrent = orig }()
	var queried bool
	imeQueryCurrent = func() (string, error) {
		queried = true
		return "zh-orig", nil
	}
	cmd := m.getIMECurrentCmd()
	if cmd == nil {
		t.Fatal("getIMECurrentCmd() returned nil")
	}
	msg := cmd()
	imsg, ok := msg.(imeCurrentMsg)
	if !ok {
		t.Fatalf("cmd() msg = %T, want imeCurrentMsg", msg)
	}
	if !queried {
		t.Fatal("imeQueryCurrent was not called")
	}
	if imsg.current != "zh-orig" {
		t.Fatalf("imeCurrentMsg.current = %q, want zh-orig", imsg.current)
	}
}

func TestGetIMECurrentCmdReturnsEmptyOnQueryError(t *testing.T) {
	m := NewModel(nil)
	orig := imeQueryCurrent
	defer func() { imeQueryCurrent = orig }()
	imeQueryCurrent = func() (string, error) {
		return "", errors.New("boom")
	}
	msg := m.getIMECurrentCmd()()
	imsg, ok := msg.(imeCurrentMsg)
	if !ok {
		t.Fatalf("cmd() msg = %T, want imeCurrentMsg", msg)
	}
	if imsg.current != "" {
		t.Fatalf("imeCurrentMsg.current = %q, want empty", imsg.current)
	}
}

func TestIMESwitchIfTransitionOnlyTriggersForNormalModes(t *testing.T) {
	m := NewModel(nil)
	m.imeSwitchTarget = "com.apple.keylayout.ABC"

	cases := []struct {
		from, to Mode
		wantCmd  bool
	}{
		{ModeInsert, ModeNormal, true},
		{ModeInsert, ModeDirectory, true},
		{ModeInsert, ModeSessionSelect, true},
		{ModeInsert, ModeConfirm, true},
		{ModeInsert, ModeQuestion, true},
		{ModeInsert, ModeRules, true},
		{ModeNormal, ModeInsert, false}, // entering Insert must NOT trigger switch
		{ModeNormal, ModeNormal, false}, // same mode: no transition
		{ModeInsert, ModeInsert, false}, // same mode: no transition
	}
	for _, tc := range cases {
		m.mode = tc.from
		cmd := m.runIMESwitchIfTransition(tc.from, tc.to)
		got := cmd != nil
		if got != tc.wantCmd {
			t.Errorf("runIMESwitchIfTransition(%v→%v): cmd!=nil = %v, want %v", tc.from, tc.to, got, tc.wantCmd)
		}
	}
}

// TestIMECurrentMsgIgnoredInInsertMode verifies Bug 1 fix:
// if imeCurrentMsg arrives after the user has already switched back to Insert,
// imeBeforeNormal must NOT be updated and IME must NOT be switched.
func TestIMECurrentMsgIgnoredInInsertMode(t *testing.T) {
	m := NewModel(nil)
	m.imeSwitchTarget = "com.apple.keylayout.ABC"
	m.mode = ModeInsert // user already back in Insert before msg arrived
	m.imeBeforeNormal = "zh-orig"

	updated, _ := m.Update(imeCurrentMsg{seq: 0, current: "zh-new"})
	model := updated.(*Model)

	if model.imeBeforeNormal != "zh-orig" {
		t.Fatalf("imeBeforeNormal changed to %q in Insert mode, want zh-orig", model.imeBeforeNormal)
	}
}

// TestIMECurrentMsgSavesAndSwitchesInNormalMode verifies that imeCurrentMsg
// in an English-IME mode saves imeBeforeNormal (the actual switching is a goroutine
// side-effect we don't assert on, but state must be updated).
func TestIMECurrentMsgSavesAndSwitchesInNormalMode(t *testing.T) {
	m := NewModel(nil)
	m.imeSwitchTarget = "com.apple.keylayout.ABC"
	m.mode = ModeNormal
	m.imeBeforeNormal = ""

	updated, _ := m.Update(imeCurrentMsg{seq: 0, current: "zh"})
	model := updated.(*Model)

	if model.imeBeforeNormal != "zh" {
		t.Fatalf("imeBeforeNormal = %q, want \"zh\"", model.imeBeforeNormal)
	}
}

func TestSwitchModeWithIMERestoresWhenEnteringInsert(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeNormal
	m.imeBeforeNormal = "zh-orig"
	preventIMEApplyInTests(&m)

	m.switchModeWithIME(ModeInsert)

	if m.imeBeforeNormal != "" {
		t.Fatalf("imeBeforeNormal = %q after switchModeWithIME(Insert), want empty", m.imeBeforeNormal)
	}
	if !m.imePending || m.imePendingTarget != "zh-orig" {
		t.Fatalf("pending IME restore = (%v, %q), want (true, zh-orig)", m.imePending, m.imePendingTarget)
	}
}

func TestHandleNormalKeyEnterInsertClearsActiveSearchSession(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeNormal
	m.search.State.Active = true
	m.search.State.Query = "grep"
	m.search.State.Matches = []MatchPosition{{BlockIndex: 0}}
	m.search.State.Current = 0
	m.imeBeforeNormal = "zh-orig"
	preventIMEApplyInTests(&m)

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "i"}))

	if m.mode != ModeInsert {
		t.Fatalf("mode = %v, want ModeInsert", m.mode)
	}
	if m.search.State.Active || m.search.State.Query != "" {
		t.Fatalf("search state after entering insert = %+v, want cleared", m.search.State)
	}
}

func TestHandleNormalKeyEnterInsertQueuesIMERestore(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeNormal
	m.imeBeforeNormal = "zh-orig"
	preventIMEApplyInTests(&m)

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "i"}))

	if m.mode != ModeInsert {
		t.Fatalf("mode = %v, want ModeInsert", m.mode)
	}
	if !m.imePending || m.imePendingTarget != "zh-orig" {
		t.Fatalf("pending IME restore = (%v, %q), want (true, zh-orig)", m.imePending, m.imePendingTarget)
	}
}

func TestIsAgentBusyInflightDraftWithoutActivity(t *testing.T) {
	m := NewModel(nil)
	m.activities = map[string]agent.AgentActivityEvent{
		"main": {Type: agent.ActivityIdle, AgentID: "main"},
	}
	d := queuedDraft{Content: "hi"}
	m.inflightDraft = &d
	if !m.isAgentBusy() {
		t.Fatal("isAgentBusy: want true while inflightDraft set (activity can lag or events may drop)")
	}
	if !m.isFocusedAgentBusy() {
		t.Fatal("isFocusedAgentBusy: want true while inflightDraft set")
	}
}

func TestIsFocusedAgentBusyTreatsCompactingAsBusy(t *testing.T) {
	m := NewModel(nil)
	m.activities = map[string]agent.AgentActivityEvent{
		"main": {Type: agent.ActivityCompacting, AgentID: "main"},
	}
	if !m.isFocusedAgentBusy() {
		t.Fatal("isFocusedAgentBusy: want true during compacting so input separator uses busy styling")
	}
}

func TestIsFocusedAgentBusyIgnoresInflightDraftOwnedByOtherAgent(t *testing.T) {
	m := NewModel(nil)
	m.focusedAgentID = "agent-1"
	m.activities = map[string]agent.AgentActivityEvent{
		"agent-1": {Type: agent.ActivityIdle, AgentID: "agent-1"},
		"main":    {Type: agent.ActivityIdle, AgentID: "main"},
	}
	m.inflightDraft = &queuedDraft{ID: "draft-1", Content: "queued"}

	if m.isFocusedAgentBusy() {
		t.Fatal("isFocusedAgentBusy: want false when inflight draft belongs to main but focus is agent-1")
	}
}

func TestRenderStatusBarDoesNotUseSyntheticConnectingForOtherAgentDraft(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.focusedAgentID = "agent-1"
	m.activities = map[string]agent.AgentActivityEvent{
		"agent-1": {Type: agent.ActivityIdle, AgentID: "agent-1"},
		"main":    {Type: agent.ActivityIdle, AgentID: "main"},
	}
	m.inflightDraft = &queuedDraft{ID: "draft-1", Content: "queued", QueuedAt: time.Now()}

	got := stripANSI(m.renderStatusBar())
	if strings.Contains(got, "Connecting") {
		t.Fatalf("status bar should not show synthetic connecting for another agent's inflight draft, got %q", got)
	}
}

func TestLoadQueuedDraftIntoComposerQueuesIMERestore(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeNormal
	m.imeBeforeNormal = "zh-orig"
	preventIMEApplyInTests(&m)
	draft := queuedDraft{Content: "hello"}

	_ = m.loadQueuedDraftIntoComposer(draft)

	if m.mode != ModeInsert {
		t.Fatalf("mode = %v, want ModeInsert", m.mode)
	}
	if !m.imePending || m.imePendingTarget != "zh-orig" {
		t.Fatalf("pending IME restore = (%v, %q), want (true, zh-orig)", m.imePending, m.imePendingTarget)
	}
}

func TestHandleCtrlCCancelSessionSelectRestoresInsertIME(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeSessionSelect
	m.sessionSelect.prevMode = ModeInsert
	m.imeBeforeNormal = "zh-orig"
	preventIMEApplyInTests(&m)

	_ = m.handleCtrlC()
	if m.mode != ModeInsert {
		t.Fatalf("mode = %v, want ModeInsert", m.mode)
	}
	if !m.imePending || m.imePendingTarget != "zh-orig" {
		t.Fatalf("pending IME restore = (%v, %q), want (true, zh-orig)", m.imePending, m.imePendingTarget)
	}
}

func TestCtrlCHintClearsOnNonCtrlCKeyAcrossModes(t *testing.T) {
	m := NewModel(nil)
	m.pendingQuitAt = time.Now()
	m.pendingQuitBy = "ctrl+c"

	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Text: "x", Code: 'x'}))

	if !m.pendingQuitAt.IsZero() || m.pendingQuitBy != "" {
		t.Fatalf("pending quit = (%v, %q), want cleared", m.pendingQuitAt, m.pendingQuitBy)
	}
}

func TestPendingQuitTimerDoesNotClearNewerState(t *testing.T) {
	// Test that a stale clearPendingQuitMsg from a previous quit attempt
	// does not clear a newer pending quit state.
	m := NewModel(nil)
	m.mode = ModeNormal

	// First ctrl+c sets pending quit with gen=1.
	_ = m.handleCtrlC()
	if m.pendingQuitGen != 1 {
		t.Fatalf("pendingQuitGen = %d, want 1", m.pendingQuitGen)
	}
	if m.pendingQuitBy != "ctrl+c" {
		t.Fatalf("pendingQuitBy = %q, want ctrl+c", m.pendingQuitBy)
	}

	// Clear the state, but keep generation monotonic.
	m.clearPendingQuit()
	if m.pendingQuitGen != 1 {
		t.Fatalf("pendingQuitGen = %d after clear, want 1", m.pendingQuitGen)
	}

	// Second ctrl+c must allocate a newer generation.
	_ = m.handleCtrlC()
	if m.pendingQuitGen != 2 {
		t.Fatalf("pendingQuitGen = %d, want 2", m.pendingQuitGen)
	}

	// Now simulate the old timer from the first attempt (gen=1) firing.
	// This should NOT clear the current state (gen=2).
	_, cmd := m.Update(clearPendingQuitMsg{generation: 1})
	if cmd != nil {
		t.Fatalf("Update(clearPendingQuitMsg{generation: 1}) returned non-nil cmd")
	}
	if m.pendingQuitBy != "ctrl+c" {
		t.Fatalf("stale timer cleared pending quit; pendingQuitBy = %q, want ctrl+c", m.pendingQuitBy)
	}

	// But a matching generation should clear it.
	_, cmd = m.Update(clearPendingQuitMsg{generation: 2})
	if cmd != nil {
		t.Fatalf("Update(clearPendingQuitMsg{generation: 2}) returned non-nil cmd")
	}
	if m.pendingQuitBy != "" {
		t.Fatalf("matching timer did not clear pending quit; pendingQuitBy = %q", m.pendingQuitBy)
	}
}

func TestPendingQuitTimerGenerationForQ(t *testing.T) {
	// Test that 'q' also uses generation-based timer protection.
	m := NewModel(nil)
	m.mode = ModeNormal

	// First q sets pending quit with gen=1.
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Text: "q", Code: 'q'}))
	if m.pendingQuitGen != 1 {
		t.Fatalf("pendingQuitGen = %d, want 1", m.pendingQuitGen)
	}
	if m.pendingQuitBy != "q" {
		t.Fatalf("pendingQuitBy = %q, want q", m.pendingQuitBy)
	}

	// Clear the state, but keep generation monotonic.
	m.clearPendingQuit()
	if m.pendingQuitGen != 1 {
		t.Fatalf("pendingQuitGen = %d after clear, want 1", m.pendingQuitGen)
	}

	// Second q sets pending quit with gen=2.
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Text: "q", Code: 'q'}))
	if m.pendingQuitGen != 2 {
		t.Fatalf("pendingQuitGen = %d, want 2", m.pendingQuitGen)
	}

	// Stale timer from the first attempt (gen=1) should not clear.
	_, _ = m.Update(clearPendingQuitMsg{generation: 1})
	if m.pendingQuitBy != "q" {
		t.Fatalf("stale timer cleared pending quit; pendingQuitBy = %q, want q", m.pendingQuitBy)
	}

	// Matching timer should clear.
	_, _ = m.Update(clearPendingQuitMsg{generation: 2})
	if m.pendingQuitBy != "" {
		t.Fatalf("matching timer did not clear pending quit; pendingQuitBy = %q", m.pendingQuitBy)
	}
}

func TestViewRefreshesStatusBarForCtrlCHintWhileToastActive(t *testing.T) {
	m := NewModelWithSize(nil, 100, 24)
	m.activeToast = &toastItem{Message: "toast", Level: "info"}
	m.recalcViewportSize()

	initial := stripANSI(m.View().Content)
	if strings.Contains(initial, "Press Ctrl+C again to quit") {
		t.Fatalf("initial View() unexpectedly contains quit hint: %q", initial)
	}

	m.pendingQuitAt = time.Now()
	m.pendingQuitBy = "ctrl+c"

	updated := stripANSI(m.View().Content)
	if !strings.Contains(updated, "Press Ctrl+C again to quit") {
		t.Fatalf("updated View() missing quit hint while toast active: %q", updated)
	}
}

func TestViewRefreshesStatusBarForTransientStateChanges(t *testing.T) {
	type testCase struct {
		name   string
		mutate func(m *Model, backend *sessionControlAgent)
		want   string
	}

	tests := []testCase{
		{
			name: "pending quit q hint",
			mutate: func(m *Model, backend *sessionControlAgent) {
				m.pendingQuitAt = time.Now()
				m.pendingQuitBy = "q"
			},
			want: "Press q again to quit",
		},
		{
			name: "search pill",
			mutate: func(m *Model, backend *sessionControlAgent) {
				m.search.State.Active = true
				m.search.State.Query = "grep"
				m.search.State.Matches = []MatchPosition{{BlockIndex: 0}, {BlockIndex: 1}, {BlockIndex: 2}}
				m.search.State.Current = 1
			},
			want: "/grep [2/3]",
		},
		{
			name: "chord pill",
			mutate: func(m *Model, backend *sessionControlAgent) {
				m.chord = chordState{count: 12, op: chordE, startAt: time.Now()}
			},
			want: "12e",
		},
		{
			name: "insert mode text",
			mutate: func(m *Model, backend *sessionControlAgent) {
				m.mode = ModeInsert
				m.input.SetValue("first\nsecond")
				m.input.syncHeight()
			},
			want: "INSERT 2/2",
		},
		{
			name: "hidden model pill",
			mutate: func(m *Model, backend *sessionControlAgent) {
				backend.runningModelRef = "anthropic/claude-opus-4.6"
				m.invalidateStatusBarAgentSnapshot()
			},
			want: "claude-opus-4.6",
		},
		{
			name: "hidden usage/context pills",
			mutate: func(m *Model, backend *sessionControlAgent) {
				backend.tokenUsage = message.TokenUsage{InputTokens: 123, OutputTokens: 456}
				backend.sidebarUsage = analytics.SessionStats{EstimatedCost: 1.25}
				backend.contextCurrent = 2048
				backend.contextLimit = 8192
				m.invalidateStatusBarAgentSnapshot()
			},
			want: "↑ 123",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			backend := &sessionControlAgent{providerModelRef: "anthropic/claude-opus-4.7"}
			m := NewModelWithSize(backend, 100, 24)
			m.rightPanelVisible = false
			m.layout = m.generateLayout(m.width, m.height)

			initial := stripANSI(m.View().Content)
			tc.mutate(&m, backend)
			updated := stripANSI(m.View().Content)

			if initial == updated {
				t.Fatalf("View() did not change after transient state mutation; content=%q", updated)
			}
			if !strings.Contains(updated, tc.want) {
				t.Fatalf("updated View() missing %q, got %q", tc.want, updated)
			}
		})
	}
}

func TestResolveConfirmRestoresInsertModeWithIMERestore(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeConfirm
	m.confirm = confirmState{
		request:  &ConfirmRequest{ToolName: "Edit"},
		prevMode: ModeInsert,
	}
	m.imeBeforeNormal = "zh-orig"
	preventIMEApplyInTests(&m)

	cmd := m.resolveConfirm(ConfirmResult{Action: ConfirmDeny})
	if cmd == nil {
		t.Fatal("resolveConfirm() returned nil cmd")
	}
	if m.mode != ModeInsert {
		t.Fatalf("mode = %v, want ModeInsert", m.mode)
	}
	if !m.imePending || m.imePendingTarget != "zh-orig" {
		t.Fatalf("pending IME restore = (%v, %q), want (true, zh-orig)", m.imePending, m.imePendingTarget)
	}
}

func TestResolveQuestionRestoresInsertModeWithIMERestore(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeQuestion
	m.question = questionState{
		request:  &QuestionRequest{Questions: []tools.QuestionItem{{Header: "name", Question: "who?"}}},
		prevMode: ModeInsert,
	}
	m.imeBeforeNormal = "zh-orig"
	preventIMEApplyInTests(&m)

	cmd := m.resolveQuestion(QuestionResult{Err: errors.New("cancelled")})
	if cmd == nil {
		t.Fatal("resolveQuestion() returned nil cmd")
	}
	if m.mode != ModeInsert {
		t.Fatalf("mode = %v, want ModeInsert", m.mode)
	}
	if !m.imePending || m.imePendingTarget != "zh-orig" {
		t.Fatalf("pending IME restore = (%v, %q), want (true, zh-orig)", m.imePending, m.imePendingTarget)
	}
}

func TestHandleNormalKeyNonSearchKeyClearsActiveSearchSession(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.mode = ModeNormal
	m.search.State.Active = true
	m.search.State.Query = "grep"
	m.search.State.Matches = []MatchPosition{{BlockIndex: 0}}
	m.search.State.Current = 0
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "hello"})

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "j", Code: 'j'}))

	if m.search.State.Active || m.search.State.Query != "" {
		t.Fatalf("search state after normal key = %+v, want cleared", m.search.State)
	}
}

func TestHandleNormalKeySearchNavigationPreservesActiveSearchSession(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.mode = ModeNormal
	m.search.State.Active = true
	m.search.State.Query = "grep"
	m.search.State.Matches = []MatchPosition{{BlockIndex: 0}, {BlockIndex: 1}}
	m.search.State.Current = 0
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "grep one"})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: "grep two"})

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "n", Code: 'n'}))

	if !m.search.State.Active || m.search.State.Query != "grep" {
		t.Fatalf("search state after n = %+v, want preserved", m.search.State)
	}
	if m.search.State.Current != 1 {
		t.Fatalf("search current after n = %d, want 1", m.search.State.Current)
	}
}

func TestExecuteSearchStartsFromFocusedBlock(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "grep first"})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: "grep second"})
	m.viewport.AppendBlock(&Block{ID: 3, Type: BlockAssistant, Content: "grep third"})
	m.focusedBlockID = 2

	m.search = NewSearchModel(ModeNormal)
	m.executeSearchAgainstCurrentTranscript("grep")

	if m.search.State.Current != 1 {
		t.Fatalf("search current = %d, want 1 for focused block anchor", m.search.State.Current)
	}
	match, ok := m.search.State.CurrentMatch()
	if !ok {
		t.Fatal("expected current search match")
	}
	if match.BlockID != 2 {
		t.Fatalf("current match block = %d, want 2", match.BlockID)
	}
}

func TestExecuteSearchStartsFromViewportTopWhenNoFocusedBlock(t *testing.T) {
	m := NewModelWithSize(nil, 120, 3)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "grep first"})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: "grep second"})
	m.viewport.AppendBlock(&Block{ID: 3, Type: BlockAssistant, Content: "grep third"})
	if lineOffset, ok := m.viewport.LineOffsetForBlockID(2); ok {
		m.viewport.offset = lineOffset
	}

	m.search = NewSearchModel(ModeNormal)
	m.executeSearchAgainstCurrentTranscript("grep")

	if m.search.State.Current != 1 {
		t.Fatalf("search current = %d, want 1 for viewport-top anchor", m.search.State.Current)
	}
	match, ok := m.search.State.CurrentMatch()
	if !ok {
		t.Fatal("expected current search match")
	}
	if match.BlockID != 2 {
		t.Fatalf("current match block = %d, want 2", match.BlockID)
	}
}

func TestSwitchModeWithIMEEnteringInsertClearsActiveSearchSession(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.mode = ModeNormal
	m.search.State.Active = true
	m.search.State.Query = "grep"
	m.search.State.Matches = []MatchPosition{{BlockIndex: 0}}
	m.search.State.Current = 0

	_ = m.switchModeWithIME(ModeInsert)

	if m.mode != ModeInsert {
		t.Fatalf("mode = %v, want ModeInsert", m.mode)
	}
	if m.search.State.Active || m.search.State.Query != "" {
		t.Fatalf("search state after switchModeWithIME(Insert) = %+v, want cleared", m.search.State)
	}
}

func TestLoadQueuedDraftIntoComposerClearsActiveSearchSession(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.mode = ModeNormal
	m.search.State.Active = true
	m.search.State.Query = "grep"
	m.search.State.Matches = []MatchPosition{{BlockIndex: 0}}
	m.search.State.Current = 0
	draft := queuedDraft{ID: "draft-1", Content: "hello"}

	_ = m.loadQueuedDraftIntoComposer(draft)

	if m.mode != ModeInsert {
		t.Fatalf("mode = %v, want ModeInsert", m.mode)
	}
	if m.search.State.Active || m.search.State.Query != "" {
		t.Fatalf("search state after loadQueuedDraftIntoComposer = %+v, want cleared", m.search.State)
	}
}

func TestMouseClickInputZoneClearsActiveSearchSession(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.mode = ModeNormal
	m.search.State.Active = true
	m.search.State.Query = "grep"
	m.search.State.Matches = []MatchPosition{{BlockIndex: 0}}
	m.search.State.Current = 0

	m.width = 140
	m.height = 24
	m.layout = m.generateLayout(m.width, m.height)
	_ = m.renderStatusBar()
	clickX := m.layout.input.Min.X + inputPromptWidth
	clickY := m.layout.input.Min.Y + 1
	updated, _ := m.Update(tea.MouseClickMsg{X: clickX, Y: clickY, Button: tea.MouseLeft})
	model := updated.(*Model)
	if model.mode != ModeInsert {
		t.Fatalf("mode = %v, want ModeInsert", model.mode)
	}
	if model.search.State.Active || model.search.State.Query != "" {
		t.Fatalf("search state after input-zone click = %+v, want cleared", model.search.State)
	}
}

func TestOpenHandoffSelectClearsActiveSearchSession(t *testing.T) {
	backend := &sessionControlAgent{availableAgents: []string{"builder"}}
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeNormal
	m.search.State.Active = true
	m.search.State.Query = "grep"
	m.search.State.Matches = []MatchPosition{{BlockIndex: 0}}
	m.search.State.Current = 0

	m.openHandoffSelect("plan.md")

	if m.mode != ModeHandoffSelect {
		t.Fatalf("mode = %v, want ModeHandoffSelect", m.mode)
	}
	if m.search.State.Active || m.search.State.Query != "" {
		t.Fatalf("search state after openHandoffSelect = %+v, want cleared", m.search.State)
	}
}

func TestOpenImageViewerClearsActiveSearchSession(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.mode = ModeNormal
	m.imageCaps.Backend = ImageBackendKitty
	m.imageCaps.SupportsFullscreen = true
	m.search.State.Active = true
	m.search.State.Query = "grep"
	m.search.State.Matches = []MatchPosition{{BlockIndex: 0}}
	m.search.State.Current = 0
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, ImageParts: []BlockImagePart{{FileName: "shot.png", ImagePath: "testdata/shot.png"}}})
	m.focusedBlockID = 1

	m.openImageViewer(-1, 0)

	if m.mode != ModeImageViewer {
		t.Fatalf("mode = %v, want ModeImageViewer", m.mode)
	}
	if m.search.State.Active || m.search.State.Query != "" {
		t.Fatalf("search state after openImageViewer = %+v, want cleared", m.search.State)
	}
}

func TestOpenModelSelectClearsActiveSearchSession(t *testing.T) {
	backend := &sessionControlAgent{availableModels: []agent.ModelOption{{ProviderModel: "main/test"}}}
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeNormal
	m.search.State.Active = true
	m.search.State.Query = "grep"
	m.search.State.Matches = []MatchPosition{{BlockIndex: 0}}
	m.search.State.Current = 0

	m.openModelSelect()

	if m.mode != ModeModelSelect {
		t.Fatalf("mode = %v, want ModeModelSelect", m.mode)
	}
	if m.search.State.Active || m.search.State.Query != "" {
		t.Fatalf("search state after openModelSelect = %+v, want cleared", m.search.State)
	}
}

func TestMouseSelectionRangeConvertsInclusiveDragEndpointToExclusive(t *testing.T) {
	m := NewModel(nil)
	m.selStartBlockID = 1
	m.selStartLine = 0
	m.selStartCol = 4
	m.selEndBlockID = 1
	m.selEndLine = 0
	m.selEndCol = 13
	m.selEndInclusiveForCopy = true

	sel := m.mouseSelectionRange()
	if sel.StartCol != 4 || sel.EndCol != 14 {
		t.Fatalf("mouseSelectionRange() = (%d, %d), want (4, 14)", sel.StartCol, sel.EndCol)
	}
}

func TestMouseSelectionRangePreservesExclusiveWordSelection(t *testing.T) {
	m := NewModel(nil)
	m.selStartBlockID = 1
	m.selStartLine = 0
	m.selStartCol = 4
	m.selEndBlockID = 1
	m.selEndLine = 0
	m.selEndCol = 14
	m.selEndInclusiveForCopy = false

	sel := m.mouseSelectionRange()
	if sel.StartCol != 4 || sel.EndCol != 14 {
		t.Fatalf("mouseSelectionRange() = (%d, %d), want unchanged (4, 14)", sel.StartCol, sel.EndCol)
	}
}

func TestOpenUsageStatsClearsActiveSearchSession(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.mode = ModeNormal
	m.search.State.Active = true
	m.search.State.Query = "grep"
	m.search.State.Matches = []MatchPosition{{BlockIndex: 0}}
	m.search.State.Current = 0

	m.openUsageStats()

	if m.mode != ModeUsageStats {
		t.Fatalf("mode = %v, want ModeUsageStats", m.mode)
	}
	if m.search.State.Active || m.search.State.Query != "" {
		t.Fatalf("search state after openUsageStats = %+v, want cleared", m.search.State)
	}
}

func TestOpenHelpClearsActiveSearchSession(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.mode = ModeNormal
	m.search.State.Active = true
	m.search.State.Query = "grep"
	m.search.State.Matches = []MatchPosition{{BlockIndex: 0}}
	m.search.State.Current = 0

	_ = m.openHelp()

	if m.mode != ModeHelp {
		t.Fatalf("mode = %v, want ModeHelp", m.mode)
	}
	if m.search.State.Active || m.search.State.Query != "" {
		t.Fatalf("search state after openHelp = %+v, want cleared", m.search.State)
	}
}

func TestConfirmRequestMsgSwitchesIMEWhenEnteringConfirm(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeInsert
	m.imeSwitchTarget = "com.apple.keylayout.ABC"

	updated, cmd := m.Update(confirmRequestMsg{request: ConfirmRequest{ToolName: "Edit"}})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if model.mode != ModeConfirm {
		t.Fatalf("mode = %v, want ModeConfirm", model.mode)
	}
	if cmd == nil {
		t.Fatal("confirmRequestMsg should trigger IME query command when entering Confirm from Insert")
	}
}

func TestQuestionRequestMsgSwitchesIMEWhenEnteringQuestion(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeInsert
	m.imeSwitchTarget = "com.apple.keylayout.ABC"

	updated, cmd := m.Update(questionRequestMsg{request: QuestionRequest{Questions: []tools.QuestionItem{{Header: "name", Question: "who?", Options: []tools.QuestionOption{{Label: "alice"}}}}}})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if model.mode != ModeQuestion {
		t.Fatalf("mode = %v, want ModeQuestion", model.mode)
	}
	if cmd == nil {
		t.Fatal("questionRequestMsg should trigger IME query command when entering Question from Insert")
	}
}

func TestFocusMsgReappliesEnglishIMEForConfirmAndQuestionModes(t *testing.T) {
	cases := []struct {
		name string
		mode Mode
	}{
		{name: "normal", mode: ModeNormal},
		{name: "confirm", mode: ModeConfirm},
		{name: "question", mode: ModeQuestion},
		{name: "rules", mode: ModeRules},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel(nil)
			m.SetFocusResizeFreezeEnabled(false)
			m.mode = tc.mode
			m.imeSwitchTarget = "com.apple.keylayout.ABC"
			preventIMEApplyInTests(&m)

			updated, cmd := m.Update(tea.FocusMsg{})
			model, ok := updated.(*Model)
			if !ok {
				t.Fatalf("Update returned %T, want *Model", updated)
			}
			if !model.imePending || model.imePendingTarget != "com.apple.keylayout.ABC" {
				t.Fatalf("pending IME apply = (%v, %q), want (true, com.apple.keylayout.ABC)", model.imePending, model.imePendingTarget)
			}
			if cmd != nil {
				t.Fatalf("FocusMsg with freeze disabled should not schedule command, got %#v", cmd)
			}
		})
	}
}

// TestIMERestoreClearsBeforeNormal verifies Bug 2 fix:
// runIMERestoreIfNeeded must clear imeBeforeNormal after scheduling restore,
// so a stale imeCurrentMsg that arrives later cannot re-clobber Insert-mode IME.
func TestIMERestoreClearsBeforeNormal(t *testing.T) {
	m := NewModel(nil)
	m.imeSwitchTarget = "com.apple.keylayout.ABC"
	m.imeBeforeNormal = "zh"

	m.runIMERestoreIfNeeded()

	if m.imeBeforeNormal != "" {
		t.Fatalf("imeBeforeNormal = %q after restore, want empty", m.imeBeforeNormal)
	}
}

// TestIMERestoreNoopWhenEmpty verifies that runIMERestoreIfNeeded is a no-op
// when imeBeforeNormal is empty (first Insert entry, nothing to restore).
func TestIMERestoreNoopWhenEmpty(t *testing.T) {
	m := NewModel(nil)
	m.imeBeforeNormal = ""

	// Should not panic and imeBeforeNormal stays empty.
	m.runIMERestoreIfNeeded()

	if m.imeBeforeNormal != "" {
		t.Fatalf("imeBeforeNormal = %q, want empty", m.imeBeforeNormal)
	}
}

func TestNormalModeCountPrefixStartsWithOneToNine(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeNormal

	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "0", Code: '0'})); cmd != nil {
		t.Fatalf("0 should not start a chord count, got %#v", cmd)
	}
	if m.chord.active() {
		t.Fatal("0 should not start a pending chord")
	}

	cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "5", Code: '5'}))
	if cmd == nil {
		t.Fatal("5 should start a chord count")
	}
	if m.chord.count != 5 || m.chord.op != chordNone {
		t.Fatalf("chord = %+v, want count=5 op=none", m.chord)
	}
}

func TestNormalModeEscClearsChordBeforeCancellingBusyTurn(t *testing.T) {
	backend := &sessionControlAgent{cancelResult: true}
	m := NewModel(backend)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming}
	m.inflightDraft = &queuedDraft{ID: "draft-1", Content: "queued"}
	m.chord = chordState{op: chordG, startAt: time.Now()}

	cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if cmd != nil {
		t.Fatalf("esc with pending chord should not cancel turn, got %#v", cmd)
	}
	if m.chord.active() {
		t.Fatal("esc should clear pending chord")
	}
	if backend.cancelCalls != 0 {
		t.Fatalf("CancelCurrentTurn calls = %d, want 0", backend.cancelCalls)
	}

	cmd = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if cmd == nil {
		t.Fatal("second esc should cancel busy turn")
	}
	if backend.cancelCalls != 1 {
		t.Fatalf("CancelCurrentTurn calls = %d, want 1", backend.cancelCalls)
	}
}

func TestNormalModeEscDisablesLoopBeforeCancellingBusyTurn(t *testing.T) {
	backend := &sessionControlAgent{cancelResult: true, loopState: agent.LoopStateExecuting}
	m := NewModel(backend)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming}
	m.inflightDraft = &queuedDraft{ID: "draft-1", Content: "queued"}

	cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if cmd == nil {
		t.Fatal("first esc should return loop-disable toast command")
	}
	if backend.loopDisableCalls != 1 {
		t.Fatalf("DisableLoopMode calls = %d, want 1", backend.loopDisableCalls)
	}
	if backend.cancelCalls != 0 {
		t.Fatalf("CancelCurrentTurn calls = %d, want 0 on first esc", backend.cancelCalls)
	}
	backend.loopState = ""
	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if backend.cancelCalls != 1 {
		t.Fatalf("CancelCurrentTurn calls = %d, want 1 on second esc", backend.cancelCalls)
	}
}

func TestCopyFocusedBlockHydratesSpilledContent(t *testing.T) {
	m := NewModelWithSize(nil, 80, 6)
	m.mode = ModeNormal
	m.viewport.maxHotBytes = 1024
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: strings.Repeat("alpha ", 600)})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: "tail"})

	if !m.viewport.blocks[0].spillCold {
		t.Fatalf("expected block 1 to spill, got spillCold=%v", m.viewport.blocks[0].spillCold)
	}
	m.focusedBlockID = 1
	m.refreshBlockFocus()

	cmd := m.copyFocusedBlock()
	if cmd == nil {
		t.Fatal("copyFocusedBlock should return clipboard command")
	}
	msg := cmd()
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Len() != 2 {
		t.Fatalf("clipboard command msg = %T, want 2-command sequence", msg)
	}
	second := v.Index(1).Call(nil)[0].Interface().(clipboardWriteResultMsg)
	if second.success != "Message card copied to clipboard" {
		t.Fatalf("clipboard success = %q, want %q", second.success, "Message card copied to clipboard")
	}
	block := m.viewport.GetFocusedBlock(1)
	if block == nil || block.spillCold {
		t.Fatalf("focused block after copy = %#v, want hydrated block", block)
	}
	if got := blockPlainContent(block); !strings.Contains(got, "alpha") {
		t.Fatalf("blockPlainContent after copy = %q, want alpha content", got)
	}
}

func TestCopyFocusedBlocksHydratesSpilledBlocks(t *testing.T) {
	m := NewModelWithSize(nil, 80, 6)
	m.mode = ModeNormal
	m.viewport.maxHotBytes = 1024
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: strings.Repeat("alpha ", 600)})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: strings.Repeat("beta ", 600)})
	m.viewport.AppendBlock(&Block{ID: 3, Type: BlockAssistant, Content: "tail"})

	m.focusedBlockID = 1
	m.refreshBlockFocus()
	cmd := m.copyFocusedBlocks(2)
	if cmd == nil {
		t.Fatal("copyFocusedBlocks should return clipboard command")
	}
	msg := cmd()
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Len() != 2 {
		t.Fatalf("clipboard command msg = %T, want 2-command sequence", msg)
	}
	second := v.Index(1).Call(nil)[0].Interface().(clipboardWriteResultMsg)
	if second.success != "2 message cards copied to clipboard" {
		t.Fatalf("clipboard success = %q, want %q", second.success, "2 message cards copied to clipboard")
	}
	for _, id := range []int{1, 2} {
		block := m.viewport.GetFocusedBlock(id)
		if block == nil || block.spillCold {
			t.Fatalf("block %d after copy = %#v, want hydrated block", id, block)
		}
	}
}

func TestCopyFocusedBlockRejectsErrorCard(t *testing.T) {
	m := NewModelWithSize(nil, 80, 8)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockError, Content: "failed"})
	m.focusedBlockID = 1
	m.refreshBlockFocus()

	cmd := m.copyFocusedBlock()
	if cmd == nil {
		t.Fatal("copyFocusedBlock should return toast command for BlockError")
	}
	_ = cmd()
	if m.activeToast == nil {
		t.Fatal("copyFocusedBlock should enqueue toast for non-copyable block")
	}
	if got, want := m.activeToast.Message, "This card type cannot be copied"; got != want {
		t.Fatalf("toast message = %q, want %q", got, want)
	}
}

func TestSuperCopyMouseSelectionKeepsLastCharacter(t *testing.T) {
	m := NewModelWithSize(nil, 100, 20)
	m.mode = ModeNormal
	block := &Block{ID: 1, Type: BlockAssistant, Content: "prefix `app_id/app_secret` suffix"}
	m.viewport.AppendBlock(block)

	lines := block.Render(m.viewport.width, "")
	target := -1
	startCol := -1
	for i, line := range lines {
		plain := stripANSI(line)
		if idx := strings.Index(plain, "app_id/app_secret"); idx >= 0 {
			target = i
			startCol = ansi.StringWidth(plain[:idx])
			break
		}
	}
	if target < 0 || startCol < 0 {
		t.Fatalf("failed to find rendered inline code in %#v", lines)
	}

	m.selStartBlockID = 1
	m.selStartLine = target
	m.selStartCol = startCol
	m.selEndBlockID = 1
	m.selEndLine = target
	m.selEndCol = startCol + len("app_id/app_secret") - 1
	m.selEndInclusiveForCopy = true

	cmd := m.handleSuperCopy()
	if cmd == nil {
		t.Fatal("handleSuperCopy should return clipboard command for mouse selection")
	}
	msg := cmd()
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Len() != 2 {
		t.Fatalf("clipboard command msg = %T, want 2-command sequence", msg)
	}
	second := v.Index(1).Call(nil)[0].Interface().(clipboardWriteResultMsg)
	if second.success != "Selection copied to clipboard" {
		t.Fatalf("clipboard success = %q, want %q", second.success, "Selection copied to clipboard")
	}
	if got := m.viewport.ExtractSelectionText(m.mouseSelectionRange()); got != "app_id/app_secret" {
		t.Fatalf("copied selection text = %q, want %q", got, "app_id/app_secret")
	}
}

func TestSuperCopyRejectsErrorCard(t *testing.T) {
	m := NewModelWithSize(nil, 80, 8)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockError, Content: "failed"})
	m.focusedBlockID = 1
	m.refreshBlockFocus()

	cmd := m.handleSuperCopy()
	if cmd == nil {
		t.Fatal("handleSuperCopy should return toast command for BlockError")
	}
	_ = cmd()
	if m.activeToast == nil {
		t.Fatal("handleSuperCopy should enqueue toast for non-copyable block")
	}
	if got, want := m.activeToast.Message, "This card type cannot be copied"; got != want {
		t.Fatalf("toast message = %q, want %q", got, want)
	}
}

func TestNormalModeYankSkipsErrorCardAndFocusesNextSelectable(t *testing.T) {
	m := NewModelWithSize(nil, 80, 8)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockError, Content: "failed"})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: "hello"})

	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'})); cmd == nil {
		t.Fatal("first y should start yank chord")
	}
	cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}))
	if cmd == nil {
		t.Fatal("yy should return clipboard command")
	}
	if m.focusedBlockID != 2 {
		t.Fatalf("focusedBlockID = %d, want 2 (skip BlockError)", m.focusedBlockID)
	}
	msg := cmd()
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Len() != 2 {
		t.Fatalf("clipboard command msg = %T, want 2-command sequence", msg)
	}
	second := v.Index(1).Call(nil)[0].Interface().(clipboardWriteResultMsg)
	if second.success != "Message card copied to clipboard" {
		t.Fatalf("clipboard success = %q, want %q", second.success, "Message card copied to clipboard")
	}
}

func TestNormalModeJKNavigatesSkipsErrorCards(t *testing.T) {
	m := NewModelWithSize(nil, 80, 8)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockError, Content: "e1"})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: strings.Repeat("a\n", 4)})
	m.viewport.AppendBlock(&Block{ID: 3, Type: BlockError, Content: "e2"})
	m.viewport.AppendBlock(&Block{ID: 4, Type: BlockAssistant, Content: strings.Repeat("b\n", 4)})

	m.viewport.ScrollToTop()
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "j", Code: 'j'})); cmd != nil {
		t.Fatalf("j should move synchronously without extra cmd, got %#v", cmd)
	}
	if m.focusedBlockID != 2 {
		t.Fatalf("focusedBlockID after j = %d, want 2", m.focusedBlockID)
	}

	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "j", Code: 'j'})); cmd != nil {
		t.Fatalf("j should move synchronously without extra cmd, got %#v", cmd)
	}
	if m.focusedBlockID != 4 {
		t.Fatalf("focusedBlockID after second j = %d, want 4", m.focusedBlockID)
	}

	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "k", Code: 'k'})); cmd != nil {
		t.Fatalf("k should move synchronously without extra cmd, got %#v", cmd)
	}
	if m.focusedBlockID != 2 {
		t.Fatalf("focusedBlockID after k = %d, want 2", m.focusedBlockID)
	}
}

func TestNormalModeCountedYankCopiesVisibleBlocks(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, Content: "one"})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: "two"})
	m.viewport.AppendBlock(&Block{ID: 3, Type: BlockAssistant, Content: "three"})

	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "2", Code: '2'})); cmd == nil {
		t.Fatal("2 should start count prefix")
	}
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'})); cmd == nil {
		t.Fatal("y should start yank chord")
	}
	cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}))
	if cmd == nil {
		t.Fatal("2yy should return clipboard command")
	}
	if m.chord.active() {
		t.Fatal("2yy should clear chord state")
	}
	if m.focusedBlockID != 1 {
		t.Fatalf("focusedBlockID = %d, want 1 from viewport top", m.focusedBlockID)
	}
	msg := cmd()
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Len() != 2 {
		t.Fatalf("clipboard command msg = %T, want 2-command sequence", msg)
	}
	second := v.Index(1).Call(nil)[0].Interface().(clipboardWriteResultMsg)
	if second.success != "2 message cards copied to clipboard" {
		t.Fatalf("clipboard success = %q, want %q", second.success, "2 message cards copied to clipboard")
	}
}

func TestNormalModeEscapeCancelsFocusedSubAgentTurn(t *testing.T) {
	backend := &sessionControlAgent{cancelResult: true}
	m := NewModel(backend)
	m.mode = ModeNormal
	m.focusedAgentID = "agent-1"
	m.activities["agent-1"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "agent-1"}

	cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if cmd == nil {
		t.Fatal("esc in focused subagent view should cancel busy turn")
	}
	if backend.cancelCalls != 1 {
		t.Fatalf("CancelCurrentTurn calls = %d, want 1", backend.cancelCalls)
	}
}

func TestNormalModeJKNavigatesBlocksByDefault(t *testing.T) {
	m := NewModelWithSize(nil, 80, 8)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: strings.Repeat("a\n", 4)})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: strings.Repeat("b\n", 4)})
	m.viewport.AppendBlock(&Block{ID: 3, Type: BlockAssistant, Content: strings.Repeat("c\n", 4)})
	entries := m.viewport.MessageDirectory()
	m.viewport.ScrollToTop()

	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "j", Code: 'j'})); cmd != nil {
		t.Fatalf("j should move synchronously without extra cmd, got %#v", cmd)
	}
	if m.focusedBlockID != 2 {
		t.Fatalf("focusedBlockID after j = %d, want 2", m.focusedBlockID)
	}
	if m.viewport.offset != entries[1].LineOffset {
		t.Fatalf("viewport offset after j = %d, want %d", m.viewport.offset, entries[1].LineOffset)
	}

	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "k", Code: 'k'})); cmd != nil {
		t.Fatalf("k should move synchronously without extra cmd, got %#v", cmd)
	}
	if m.focusedBlockID != 1 {
		t.Fatalf("focusedBlockID after k = %d, want 1", m.focusedBlockID)
	}
	if m.viewport.offset != entries[0].LineOffset {
		t.Fatalf("viewport offset after k = %d, want %d", m.viewport.offset, entries[0].LineOffset)
	}

	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "2", Code: '2'})); cmd == nil {
		t.Fatal("2 should start count prefix")
	}
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "j", Code: 'j'})); cmd != nil {
		t.Fatalf("2j should move synchronously without extra cmd, got %#v", cmd)
	}
	if m.focusedBlockID != 3 {
		t.Fatalf("focusedBlockID after 2j = %d, want 3", m.focusedBlockID)
	}
	if m.viewport.offset != entries[2].LineOffset {
		t.Fatalf("viewport offset after 2j = %d, want %d", m.viewport.offset, entries[2].LineOffset)
	}
}

func TestViewRefreshesForViewportAppendAndNavigation(t *testing.T) {
	m := NewModelWithSize(nil, 80, 8)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "alpha"})
	first := m.View().Content
	if !strings.Contains(first, "alpha") {
		t.Fatalf("initial view = %q, want alpha", first)
	}

	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: strings.Repeat("beta\n", 6)})
	second := m.View().Content
	if !strings.Contains(second, "beta") {
		t.Fatalf("view after append = %q, want beta", second)
	}

	entries := m.viewport.MessageDirectory()
	if len(entries) < 2 {
		t.Fatalf("visible entries = %d, want >= 2", len(entries))
	}
	m.viewport.ScrollToTop()
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "j", Code: 'j'})); cmd != nil {
		t.Fatalf("j should move synchronously without extra cmd, got %#v", cmd)
	}
	if m.viewport.offset != entries[1].LineOffset {
		t.Fatalf("viewport offset after j = %d, want %d", m.viewport.offset, entries[1].LineOffset)
	}

	third := m.View().Content
	if third == second {
		t.Fatalf("view should change after navigation; before=%q after=%q", second, third)
	}
	if !strings.Contains(third, "beta") {
		t.Fatalf("view after navigation = %q, want beta visible", third)
	}
}

func TestNormalModeCountedTopJumpsToVisibleBlock(t *testing.T) {
	m := NewModelWithSize(nil, 80, 8)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, Content: strings.Repeat("a\n", 4)})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: strings.Repeat("b\n", 4)})
	m.viewport.AppendBlock(&Block{ID: 3, Type: BlockAssistant, Content: strings.Repeat("c\n", 4)})
	entries := m.viewport.MessageDirectory()
	if len(entries) < 3 {
		t.Fatalf("visible entries = %d, want >=3", len(entries))
	}
	m.viewport.ScrollToBottom()
	m.focusedBlockID = 3
	m.refreshBlockFocus()

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "2", Code: '2'}))
	cmd1 := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "g", Code: 'g'}))
	if cmd1 == nil {
		t.Fatal("second key in 2gg should keep chord alive")
	}
	cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "g", Code: 'g'}))
	if cmd != nil {
		t.Fatalf("2gg should move synchronously without extra cmd, got %#v", cmd)
	}
	if m.viewport.offset != entries[1].LineOffset {
		t.Fatalf("viewport offset = %d, want %d", m.viewport.offset, entries[1].LineOffset)
	}
	if m.focusedBlockID != -1 {
		t.Fatalf("focusedBlockID = %d, want -1 after absolute jump", m.focusedBlockID)
	}
	if m.chord.active() {
		t.Fatal("2gg should clear chord state")
	}
}

func TestNormalModeCountedGMatchesCountedGG(t *testing.T) {
	m := NewModelWithSize(nil, 80, 8)
	m.mode = ModeNormal
	for i := 1; i <= 4; i++ {
		m.viewport.AppendBlock(&Block{ID: i, Type: BlockAssistant, Content: strings.Repeat(string(rune('a'+i-1))+"\n", 3)})
	}
	entries := m.viewport.MessageDirectory()
	want := entries[2].LineOffset

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "3", Code: '3'}))
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "G", Code: 'G'})); cmd != nil {
		t.Fatalf("3G should move synchronously without extra cmd, got %#v", cmd)
	}
	if m.viewport.offset != want {
		t.Fatalf("3G offset = %d, want %d", m.viewport.offset, want)
	}

	m.viewport.ScrollToBottom()
	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "3", Code: '3'}))
	cmd1 := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "g", Code: 'g'}))
	if cmd1 == nil {
		t.Fatal("3gg should keep chord alive after first g")
	}
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "g", Code: 'g'})); cmd != nil {
		t.Fatalf("3gg should move synchronously without extra cmd, got %#v", cmd)
	}
	if m.viewport.offset != want {
		t.Fatalf("3gg offset = %d, want %d", m.viewport.offset, want)
	}
}

func TestNormalModeCountedDDClearsInputAndAttachments(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeNormal
	m.input.SetValue("draft")
	m.attachments = []Attachment{{FileName: "image.png", MimeType: "image/png", Data: []byte{1}}}

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "3", Code: '3'}))
	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "d", Code: 'd'}))
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "d", Code: 'd'})); cmd != nil {
		t.Fatalf("3dd should clear input inline, got %#v", cmd)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input value = %q, want empty", got)
	}
	if len(m.attachments) != 0 {
		t.Fatalf("attachments len = %d, want 0", len(m.attachments))
	}
	if m.chord.active() {
		t.Fatal("3dd should clear chord state")
	}
}

func TestNormalModeInvalidChordClearsStateWithoutSideEffects(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "one"})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: "two"})
	m.viewport.ScrollToBottom()
	prevOffset := m.viewport.offset

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "5", Code: '5'}))
	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "g", Code: 'g'}))
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "x", Code: 'x'})); cmd != nil {
		t.Fatalf("gx should not execute a command, got %#v", cmd)
	}
	if m.chord.active() {
		t.Fatal("invalid chord should clear state")
	}
	if m.viewport.offset != prevOffset {
		t.Fatalf("viewport offset = %d, want unchanged %d", m.viewport.offset, prevOffset)
	}
}

func TestNormalModeChordClearsOnModeSwitch(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeNormal

	m.clearChordState()
	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "4", Code: '4'}))
	if !m.chord.active() {
		t.Fatal("count prefix should activate chord state")
	}
	m.switchModeWithIME(ModeInsert)
	if m.mode != ModeInsert {
		t.Fatalf("mode = %v, want ModeInsert", m.mode)
	}
	if m.chord.active() {
		t.Fatal("switching mode should clear chord state")
	}
}

func TestChordTimeoutClearsPendingState(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeNormal
	m.chord = chordState{op: chordY, startAt: time.Now()}
	m.chordTickGeneration = 7

	updated, cmd := m.Update(chordTimeoutMsg{generation: 7})
	if cmd != nil {
		_ = cmd
	}
	model := updated.(*Model)
	if model.chord.active() {
		t.Fatal("timeout message should clear pending chord state")
	}
}

func TestChordTimeoutStaleGenerationDoesNotClearState(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeNormal
	m.chord = chordState{op: chordY, startAt: time.Now()}
	m.chordTickGeneration = 7

	updated, _ := m.Update(chordTimeoutMsg{generation: 6})
	model := updated.(*Model)
	if !model.chord.active() {
		t.Fatal("stale timeout should not clear chord state")
	}
}

func TestModelSelectorSingleGJumpsToTop(t *testing.T) {
	backend := &sessionControlAgent{availableModels: []agent.ModelOption{
		{ProviderName: "one", ProviderModel: "one/a", ModelID: "a"},
		{ProviderName: "two", ProviderModel: "two/b", ModelID: "b"},
		{ProviderName: "three", ProviderModel: "three/c", ModelID: "c"},
	}}
	m := NewModel(backend)
	m.openModelSelect()
	if m.modelSelect.table == nil {
		t.Fatal("expected model selector table")
	}
	m.modelSelect.table.list.SetCursor(3)

	if cmd := m.handleModelSelectKey(tea.KeyPressMsg(tea.Key{Text: "g", Code: 'g'})); cmd != nil {
		t.Fatalf("single g in model selector should not return cmd, got %#v", cmd)
	}
	if got := m.modelSelect.table.list.CursorAt(); got != 1 {
		t.Fatalf("model selector cursor = %d, want 1 (first selectable row)", got)
	}
}

func TestAgentDoneEventRefreshesTaskBlockLastTime(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.workingDir = "/tmp"
	m.updateRightPanelVisible()
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}
	old := time.Date(2024, 6, 15, 15, 4, 0, 0, time.Local)
	task := &Block{ID: 1, Type: BlockToolCall, ToolName: "Delegate", LinkedAgentID: "agent-1", StartedAt: old, SettledAt: old}
	m.viewport.AppendBlock(task)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentDoneEvent{AgentID: "agent-1", Summary: "done"}})

	if task.DoneSummary != "done" {
		t.Fatalf("DoneSummary = %q, want done", task.DoneSummary)
	}
	if !task.SettledAt.After(old) {
		t.Fatalf("task SettledAt = %v, want > %v", task.SettledAt, old)
	}
	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "Since ") {
		t.Fatalf("status bar should show last time after task completion; got %q", plain)
	}
}

func TestThinkingDurationFrozenOnStreamThinkingEvent(t *testing.T) {
	// When StreamThinkingEvent (thinking_end) arrives, ThinkingDuration
	// should be frozen immediately rather than waiting for
	// finalizeAssistantBlock(). This prevents tool call execution time
	// from being incorrectly included in the thinking duration.
	m := NewModelWithSize(nil, 80, 12)

	// Start thinking.
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingStartedEvent{}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: "I need to analyze this carefully.", AgentID: ""}})

	// thinking_end — duration should be frozen now and the block detached.
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingEvent{AgentID: ""}})

	if m.currentThinkingBlock != nil {
		t.Fatal("expected currentThinkingBlock to be detached after StreamThinkingEvent so the next round starts a fresh card")
	}
	if !m.thinkingStartTime.IsZero() {
		t.Fatal("expected thinkingStartTime to be cleared after StreamThinkingEvent froze the duration")
	}

	thinkingBlock := findThinkingBlockInViewport(m.viewport)
	if thinkingBlock == nil {
		t.Fatal("expected a thinking block in viewport after thinking_end")
	}
	if thinkingBlock.ThinkingDuration == 0 {
		t.Fatal("expected ThinkingDuration to be frozen after StreamThinkingEvent, got 0")
	}
	if thinkingBlock.Streaming {
		t.Fatal("expected settled thinking block to have Streaming=false")
	}

	// Record the frozen duration.
	frozenDuration := thinkingBlock.ThinkingDuration

	// A subsequent tool call (which triggers finalizeAssistantBlock) must
	// not recompute the duration on the already-settled block.
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:   "call-1",
		Name: "Read",
	}})

	thinkingBlock = findThinkingBlockInViewport(m.viewport)
	if thinkingBlock == nil {
		t.Fatal("expected a thinking block in viewport")
	}
	if thinkingBlock.ThinkingDuration != frozenDuration {
		t.Fatalf("ThinkingDuration changed after finalize: was %v, now %v", frozenDuration, thinkingBlock.ThinkingDuration)
	}
}

func findThinkingBlockInViewport(v *Viewport) *Block {
	for _, b := range v.visibleBlocks() {
		if b.Type == BlockThinking {
			return b
		}
	}
	return nil
}

func TestThinkingDurationFallbackInFinalizeWhenNoThinkingEnd(t *testing.T) {
	// When thinking_end is never received (e.g. cancellation or provider
	// interleaving), finalizeAssistantBlock should still compute
	// ThinkingDuration as a fallback.
	m := NewModelWithSize(nil, 80, 12)

	// Start thinking but never send StreamThinkingEvent (thinking_end).
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingStartedEvent{}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: "Analyzing...", AgentID: ""}})

	if m.thinkingStartTime.IsZero() {
		t.Fatal("expected thinkingStartTime to be set after ThinkingStartedEvent")
	}

	// Finalize without thinking_end (e.g. via ToolCallStartEvent or IdleEvent).
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:   "call-1",
		Name: "Read",
	}})

	// The thinking block should have a duration computed by finalizeAssistantBlock.
	blocks := m.viewport.visibleBlocks()
	var thinkingBlock *Block
	for _, b := range blocks {
		if b.Type == BlockThinking {
			thinkingBlock = b
			break
		}
	}
	if thinkingBlock == nil {
		t.Fatal("expected a thinking block in viewport")
	}
	if thinkingBlock.ThinkingDuration == 0 {
		t.Fatal("expected ThinkingDuration > 0 when finalizeAssistantBlock computed fallback duration")
	}
}

func TestMultipleThinkingRoundsProduceIndependentCards(t *testing.T) {
	// After thinking_end, subsequent thinking deltas must start a fresh
	// card instead of appending to the already-settled block. Otherwise
	// the new round would render its streaming content alongside the
	// previous round's frozen duration footer.
	m := NewModelWithSize(nil, 80, 12)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingStartedEvent{}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: "first round", AgentID: ""}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingEvent{AgentID: ""}})

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingStartedEvent{}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: "second round", AgentID: ""}})

	var thinkingBlocks []*Block
	for _, b := range m.viewport.visibleBlocks() {
		if b.Type == BlockThinking {
			thinkingBlocks = append(thinkingBlocks, b)
		}
	}
	if len(thinkingBlocks) != 2 {
		t.Fatalf("expected 2 thinking blocks after a second round started, got %d", len(thinkingBlocks))
	}
	if thinkingBlocks[0].Streaming || thinkingBlocks[0].ThinkingDuration == 0 {
		t.Fatalf("first thinking block should be settled with frozen duration; streaming=%v duration=%v", thinkingBlocks[0].Streaming, thinkingBlocks[0].ThinkingDuration)
	}
	if !thinkingBlocks[1].Streaming {
		t.Fatal("second thinking block should still be streaming")
	}
	if thinkingBlocks[1].ThinkingDuration != 0 {
		t.Fatalf("second thinking block should not carry a duration while streaming; got %v", thinkingBlocks[1].ThinkingDuration)
	}
	if !strings.Contains(thinkingBlocks[1].Content, "second round") {
		t.Fatalf("second thinking block content = %q, want to include 'second round'", thinkingBlocks[1].Content)
	}
}

func TestStreamingStaleUsesLastDeltaNotActivityStart(t *testing.T) {
	// streamingStale should check when the last streaming delta arrived,
	// not when ActivityStreaming began. A model that has been thinking for
	// >5 minutes but still sending deltas should NOT be considered stale.
	m := NewModelWithSize(nil, 80, 24)

	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.activityStartTime["main"] = time.Now().Add(-6 * time.Minute)

	// Simulate a recent delta received 10 seconds ago.
	m.streamLastDeltaAt["main"] = time.Now().Add(-10 * time.Second)

	if m.streamingStale() {
		t.Fatal("streamingStale should return false when a recent delta was received, even if activity started >5 min ago")
	}
}

func TestStreamingStaleTriggersWhenNoDeltaForFiveMinutes(t *testing.T) {
	// When no streaming delta has been received for >5 minutes, the
	// connection is likely lost and streamingStale should return true.
	m := NewModelWithSize(nil, 80, 24)

	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.activityStartTime["main"] = time.Now().Add(-10 * time.Minute)
	m.streamLastDeltaAt["main"] = time.Now().Add(-6 * time.Minute)

	if !m.streamingStale() {
		t.Fatal("streamingStale should return true when last delta was received >5 min ago")
	}
}

func TestStreamingStaleWithoutDeltaFallsBackToActivityStart(t *testing.T) {
	// When no delta timestamp is recorded (e.g. streaming started but
	// no visible output yet), fall back to activityStartTime.
	m := NewModelWithSize(nil, 80, 24)

	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.activityStartTime["main"] = time.Now().Add(-6 * time.Minute)

	if !m.streamingStale() {
		t.Fatal("streamingStale should fall back to activityStartTime when no delta recorded")
	}

	// Recent activity start should not trigger stale.
	m.activityStartTime["main"] = time.Now().Add(-30 * time.Second)
	if m.streamingStale() {
		t.Fatal("streamingStale should return false when activity started recently and no delta recorded")
	}
}

func TestStreamingDeltaTouchPreventsThinkingSplit(t *testing.T) {
	// Simulate long-running thinking that continuously sends deltas.
	// The thinking block should NOT be split by streamingStale.
	m := NewModelWithSize(nil, 80, 24)

	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.activityStartTime["main"] = time.Now().Add(-6 * time.Minute)

	// Start thinking.
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ThinkingStartedEvent{}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamThinkingDeltaEvent{Text: "initial analysis", AgentID: ""}})

	// StreamThinkingDeltaEvent should have touched the delta timestamp.
	if _, ok := m.streamLastDeltaAt["main"]; !ok {
		t.Fatal("expected streamLastDeltaAt[main] to be set after StreamThinkingDeltaEvent")
	}

	// Even though activity started >5 min ago, last delta is recent.
	if m.streamingStale() {
		t.Fatal("streamingStale should not trigger when thinking deltas are still arriving")
	}

	// The thinking block should remain a single card.
	var thinkingBlocks []*Block
	for _, b := range m.viewport.visibleBlocks() {
		if b.Type == BlockThinking {
			thinkingBlocks = append(thinkingBlocks, b)
		}
	}
	if len(thinkingBlocks) != 1 {
		t.Fatalf("expected 1 thinking block, got %d", len(thinkingBlocks))
	}
	if !thinkingBlocks[0].Streaming {
		t.Fatal("thinking block should still be streaming")
	}
}

func TestTouchStreamDeltaFromTextEvent(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: "hello", AgentID: ""}})

	if _, ok := m.streamLastDeltaAt["main"]; !ok {
		t.Fatal("expected streamLastDeltaAt[main] to be set after StreamTextEvent")
	}
}

func TestTouchStreamDeltaFromToolCallStart(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:   "call-1",
		Name: "Read",
	}})

	if _, ok := m.streamLastDeltaAt["main"]; !ok {
		t.Fatal("expected streamLastDeltaAt[main] to be set after ToolCallStartEvent")
	}
}

func TestTouchStreamDeltaFromRequestProgress(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{
		AgentID: "main",
		Bytes:   1024,
		Events:  5,
	}})

	if _, ok := m.streamLastDeltaAt["main"]; !ok {
		t.Fatal("expected streamLastDeltaAt[main] to be set after RequestProgressEvent")
	}
}

func TestTouchStreamDeltaNotSetOnRequestProgressDone(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{
		AgentID: "main",
		Bytes:   2048,
		Events:  10,
		Done:    true,
	}})

	if _, ok := m.streamLastDeltaAt["main"]; ok {
		t.Fatal("streamLastDeltaAt should not be set when RequestProgressEvent.Done is true")
	}
}

func TestMarkAgentIdleClearsStreamLastDelta(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.streamLastDeltaAt["main"] = time.Now()

	m.markAgentIdle("main")

	if _, ok := m.streamLastDeltaAt["main"]; ok {
		t.Fatal("expected streamLastDeltaAt[main] to be cleared after markAgentIdle")
	}
}
