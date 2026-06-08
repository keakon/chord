package tui

import (
	"fmt"
	"image"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	tea "github.com/keakon/bubbletea/v2"
	uv "github.com/keakon/ultraviolet"
)

type cachedRenderable struct {
	text       string
	lines      [][]uv.Cell
	cellsValid bool
	scratch    uv.ScreenBuffer
	scratchW   int
	scratchOK  bool
}

func (m *Model) shouldFreezeRender() bool {
	if m == nil || !m.renderFreezeActive {
		return false
	}
	if !m.cachedFrozenViewValid {
		return false
	}
	return true
}

func (m *Model) exitRenderFreeze() {
	if m == nil {
		return
	}
	m.renderFreezeActive = false
	m.renderFreezeReason = ""
	m.renderFreezeEnteredAt = time.Time{}
	m.cachedFrozenView = tea.View{}
	m.cachedFrozenViewValid = false
	m.setStreamRenderInvalidation(streamRenderInvalidateForce)
}

func (m *Model) tryEnterRenderFreeze(reason string) bool {
	if m == nil || m.renderFreezeActive {
		return false
	}
	if m.displayState != stateBackground {
		return false
	}
	if m.focusedAgentBusyForIdleSweep() {
		return false
	}
	if m.confirm.request != nil || m.question.request != nil {
		return false
	}
	if m.activeToast != nil && shouldBreakFreezeForToastLevel(m.activeToast.Level) {
		return false
	}
	if m.backgroundIdleSince.IsZero() || time.Since(m.backgroundIdleSince) < 10*time.Second {
		return false
	}
	if !m.cachedFullViewValid {
		return false
	}
	m.renderFreezeActive = true
	m.renderFreezeReason = strings.TrimSpace(reason)
	if m.renderFreezeReason == "" {
		m.renderFreezeReason = "background-idle"
	}
	m.renderFreezeEnteredAt = time.Now()
	m.cachedFrozenView = m.cachedFullView
	m.cachedFrozenViewValid = true
	m.setStreamRenderInvalidation(streamRenderInvalidateClear)
	m.stopTerminalTitleTicker()
	return true
}

func shouldBreakFreezeForToastLevel(level string) bool {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "warn", "error":
		return true
	default:
		return false
	}
}

func (m *Model) shouldDeferStreamRender() bool {
	if m == nil || m.renderFreezeActive || !m.streamRenderDeferred || m.streamRenderForceView {
		return false
	}
	if m.width <= 0 || m.height <= 0 {
		return false
	}
	if m.quitting {
		return false
	}
	if m.displayState != stateForeground {
		return false
	}
	if m.mode != ModeInsert && m.mode != ModeNormal {
		return false
	}
	if m.activeToast != nil || m.confirm.request != nil || m.question.request != nil {
		return false
	}
	if m.search.State.Active || m.mode == ModeSearch || m.mode == ModeDirectory || m.mode == ModeHelp || m.mode == ModeContentViewer || m.mode == ModeModelSelect || m.mode == ModeMCPSelect || m.mode == ModeSessionSelect || m.mode == ModeSessionDeleteConfirm || m.mode == ModeHandoffSelect || m.mode == ModeUsageStats || m.mode == ModeImageViewer {
		return false
	}
	if !m.cachedFullViewValid {
		return false
	}
	return true
}

func (m *Model) markStreamRenderDirty() {
	if m == nil {
		return
	}
	m.markBackgroundDirty("stream-render")
	m.setStreamRenderInvalidation(streamRenderInvalidateDefer)
}

func (m *Model) requestStreamBoundaryFlush() tea.Cmd {
	if m == nil {
		return nil
	}
	m.exitRenderFreeze()
	m.setStreamRenderInvalidation(streamRenderInvalidateForce)
	return tea.Batch(
		m.scheduleStreamFlush(m.streamBoundaryFlushDelay()),
		m.hostRedrawForContentBoundaryCmd("content-boundary"),
	)
}

func (m *Model) streamBoundaryFlushDelay() time.Duration {
	if m == nil {
		return 0
	}
	if m.displayState == stateForeground {
		return foregroundBoundaryFlushCadence
	}
	if delay := m.currentCadence().contentFlushDelay; delay > 0 {
		return delay
	}
	return foregroundBoundaryFlushCadence
}

func (m *Model) scheduleStreamFlush(delay time.Duration) tea.Cmd {
	if m == nil {
		return nil
	}
	if delay <= 0 {
		delay = m.currentCadence().contentFlushDelay
	}
	if delay <= 0 {
		return nil
	}
	if m.streamFlushScheduled {
		if m.streamFlushDelay <= 0 || m.streamFlushDelay <= delay {
			return nil
		}
		m.streamFlushGeneration++
		m.streamFlushScheduled = false
	}
	m.streamFlushScheduled = true
	m.streamFlushDelay = delay
	m.streamFlushGeneration++
	return streamFlushTick(m.streamFlushGeneration, delay)
}

func (m *Model) consumeStreamFlush(msg streamFlushTickMsg) bool {
	if msg.generation != m.streamFlushGeneration {
		return false
	}
	m.streamFlushScheduled = false
	m.streamFlushDelay = 0
	return true
}

func (m *Model) drawCachedRenderableToClearedArea(scr uv.Screen, area image.Rectangle, cache *cachedRenderable) {
	if cache == nil || area.Dx() <= 0 || area.Dy() <= 0 {
		return
	}
	type lineAccessor interface {
		Line(y int) uv.Line
	}
	lines, ok := scr.(lineAccessor)
	if !ok {
		m.drawCachedRenderable(scr, area, cache)
		return
	}
	// Always clear the full destination region (not just the rows we will draw).
	// Some terminals can leave stale styled cells behind unless every cell is
	// overwritten, especially when the new render has fewer lines than the previous
	// one (e.g. collapsing sections in the info panel).
	for row := 0; row < area.Dy(); row++ {
		dst := lines.Line(area.Min.Y + row)
		if area.Min.X >= len(dst) {
			continue
		}
		rowEnd := min(area.Max.X, len(dst))
		for i := area.Min.X; i < rowEnd; i++ {
			dst[i] = uv.EmptyCell
		}

		if row >= len(cache.lines) {
			continue
		}
		src := cache.lines[row]
		if len(src) == 0 {
			continue
		}
		end := min(area.Min.X+len(src), area.Max.X)
		end = min(end, len(dst))
		copy(dst[area.Min.X:end], src[:end-area.Min.X])
	}
}

func (m *Model) drawCachedRenderable(scr uv.Screen, area image.Rectangle, cache *cachedRenderable) {
	if cache == nil || len(cache.lines) == 0 || area.Dx() <= 0 || area.Dy() <= 0 {
		return
	}
	maxRows := min(area.Dy(), len(cache.lines))
	for row := range maxRows {
		y := area.Min.Y + row
		line := cache.lines[row]
		for x := area.Min.X; x < area.Max.X; {
			idx := x - area.Min.X
			if idx < 0 || idx >= len(line) {
				scr.SetCell(x, y, nil)
				x++
				continue
			}
			c := &line[idx]
			if c.IsZero() {
				scr.SetCell(x, y, nil)
				x++
				continue
			}
			scr.SetCell(x, y, c)
			w := c.Width
			if w <= 0 {
				w = 1
			}
			x += w
		}
	}
}

func (m *Model) renderToCache(cache *cachedRenderable, text string) {
	if cache == nil {
		return
	}
	if cache.text == text && cache.cellsValid {
		return
	}
	cache.text = text
	parts := strings.Split(text, "\n")
	cache.lines = make([][]uv.Cell, len(parts))
	for i, part := range parts {
		if part == "" {
			cache.lines[i] = nil
			continue
		}
		w := ansi.StringWidth(part)
		if w <= 0 {
			w = 1
		}
		buf := cache.renderScratchBuffer(w)
		uv.NewStyledString(part).Draw(buf, buf.Bounds())
		line := buf.Line(0)
		copied := make([]uv.Cell, min(w, len(line)))
		copy(copied, line[:len(copied)])
		cache.lines[i] = copied
	}
	cache.cellsValid = true
}

func (cache *cachedRenderable) renderScratchBuffer(width int) uv.ScreenBuffer {
	if width <= 0 {
		width = 1
	}
	if !cache.scratchOK || cache.scratch.RenderBuffer == nil {
		cache.scratch = newScreenBuffer(width, 1)
		cache.scratchW = width
		cache.scratchOK = true
		return cache.scratch
	}
	if cache.scratchW < width {
		cache.scratch.Resize(width, 1)
		cache.scratchW = width
	} else {
		cache.scratch.Resize(cache.scratchW, 1)
	}
	cache.scratch.Method = ansi.GraphemeWidth
	line := cache.scratch.Line(0)
	for i := range line {
		line[i] = uv.EmptyCell
	}
	return cache.scratch
}

func (m *Model) SetFocusResizeFreezeEnabled(enabled bool) {
	m.useFocusResizeFreeze = enabled
	if !enabled {
		m.lastHostRedrawAt = time.Time{}
		m.lastHostRedrawReason = ""
	}
}

func (m *Model) queuedDraftsFingerprint(width, maxLines int) string {
	drafts := m.visibleQueuedDrafts()
	if len(drafts) == 0 || width <= 0 || maxLines <= 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d|%d|%d", width, maxLines, len(drafts))
	for _, draft := range drafts {
		b.WriteByte('|')
		b.WriteString(draft.ID)
		b.WriteByte(':')
		b.WriteString(draft.DisplayContent)
		b.WriteByte(':')
		b.WriteString(draft.Content)
		for _, part := range draft.Parts {
			b.WriteByte(':')
			b.WriteString(part.Type)
			b.WriteByte('=')
			b.WriteString(part.DisplayText)
		}
	}
	return b.String()
}

func (m *Model) attachmentsFingerprint() string {
	if len(m.attachments) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d", len(m.attachments))
	for _, att := range m.attachments {
		b.WriteByte('|')
		b.WriteString(att.FileName)
		b.WriteByte(':')
		b.WriteString(att.MimeType)
		b.WriteByte(':')
		fmt.Fprintf(&b, "%d", len(att.Data))
	}
	return b.String()
}

func appendIntKeyPart(b *strings.Builder, scratch *[20]byte, value int) {
	b.Write(strconv.AppendInt(scratch[:0], int64(value), 10))
}

func appendUint64KeyPart(b *strings.Builder, scratch *[20]byte, value uint64) {
	b.Write(strconv.AppendUint(scratch[:0], value, 10))
}

func appendBoolKeyPart(b *strings.Builder, value bool) {
	if value {
		b.WriteByte('1')
	} else {
		b.WriteByte('0')
	}
}

func (m *Model) toastFingerprint() string {
	if m.activeToast == nil {
		return ""
	}
	return m.activeToast.Level + "|" + m.activeToast.Message
}

func (m *Model) mainRenderKey(mode Mode, viewportWidth int) string {
	var b strings.Builder
	var scratch [20]byte
	b.Grow(128 + len(m.search.State.Query) + len(m.focusedAgentID))
	appendIntKeyPart(&b, &scratch, int(mode))
	b.WriteByte('|')
	appendIntKeyPart(&b, &scratch, viewportWidth)
	b.WriteByte('|')
	appendIntKeyPart(&b, &scratch, m.viewport.height)
	b.WriteByte('|')
	appendIntKeyPart(&b, &scratch, m.viewport.offset)
	b.WriteByte('|')
	appendBoolKeyPart(&b, m.animRunning)
	b.WriteByte('|')
	appendBoolKeyPart(&b, m.rightPanelVisible)
	b.WriteByte('|')
	appendIntKeyPart(&b, &scratch, len(m.viewport.blocks))
	b.WriteByte('|')
	appendIntKeyPart(&b, &scratch, m.focusedBlockID)
	b.WriteByte('|')
	appendIntKeyPart(&b, &scratch, m.search.State.Current)
	b.WriteByte('|')
	b.Write(strconv.AppendQuote(scratch[:0], m.search.State.Query))
	b.WriteByte('|')
	appendBoolKeyPart(&b, m.startupRestorePending)
	b.WriteByte('|')
	appendUint64KeyPart(&b, &scratch, m.viewport.RenderVersion())
	b.WriteByte('|')
	b.WriteString(m.focusedAgentID)
	return b.String()
}

func (m *Model) scheduleScrollFlush(delay time.Duration) tea.Cmd {
	if m.scrollFlushScheduled {
		return nil
	}
	m.scrollFlushScheduled = true
	m.scrollFlushGeneration++
	return scrollFlushTick(m.scrollFlushGeneration, delay)
}

func (m *Model) consumeScrollFlush(msg scrollFlushTickMsg) tea.Cmd {
	if msg.generation != m.scrollFlushGeneration {
		return nil
	}
	m.scrollFlushScheduled = false
	if m.pendingScrollDelta == 0 || m.viewport == nil {
		m.pendingScrollDelta = 0
		return nil
	}
	prevOffset := m.viewport.offset
	delta := m.pendingScrollDelta
	m.pendingScrollDelta = 0
	m.applyWheelScrollDelta(delta)
	var extra []tea.Cmd
	if m.viewport != nil && m.viewport.offset != prevOffset {
		extra = append(extra, m.hostRedrawCmd("scroll-flush"))
	}
	return m.refreshInlineImagesIfViewportMoved(prevOffset, extra...)
}

func (m *Model) applyWheelScrollDelta(delta int) {
	if m == nil || m.viewport == nil || delta == 0 {
		return
	}
	if !m.hasDeferredStartupTranscript() {
		if delta > 0 {
			m.viewport.ScrollDown(delta)
		} else {
			m.viewport.ScrollUp(-delta)
		}
		return
	}
	step := 1
	steps := delta
	if steps < 0 {
		step = -1
		steps = -steps
	}
	for range steps {
		trigger := "mouse_wheel_down"
		if step < 0 {
			trigger = "mouse_wheel_up"
		}
		m.deferredScrollOneLine(step, trigger)
	}
}

func (m *Model) resetPendingScrollFlush() {
	m.pendingScrollDelta = 0
	m.scrollFlushScheduled = false
	m.scrollFlushGeneration++
}

// invalidateDrawCaches clears every per-render cache the draw pipeline owns.
// viewCacheState only contains fields that are safe to bulk reset; runtime
// state such as animation/ticker bookkeeping and startup transcript windowing
// lives in renderRuntimeState and must survive cache invalidation.
func (m *Model) invalidateDrawCaches() {
	m.viewCacheState = viewCacheState{cachedMainSearchBlockIndex: -1}
	m.statusBarAgentSnapshotDirty = true
	m.infoPanelHitBoxes = nil
}
