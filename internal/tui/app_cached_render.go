package tui

import (
	"fmt"
	"image"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

type cachedRenderable struct {
	text  string
	lines [][]uv.Cell
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
	m.streamRenderForceView = true
	m.streamRenderDeferred = false
	m.streamRenderDeferNext = false
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
	m.streamRenderForceView = false
	m.streamRenderDeferred = false
	m.streamRenderDeferNext = false
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
	if m.search.State.Active || m.mode == ModeSearch || m.mode == ModeDirectory || m.mode == ModeHelp || m.mode == ModeModelSelect || m.mode == ModeSessionSelect || m.mode == ModeSessionDeleteConfirm || m.mode == ModeHandoffSelect || m.mode == ModeUsageStats || m.mode == ModeImageViewer {
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
	m.streamRenderDeferNext = true
	m.streamRenderDeferred = true
	m.streamRenderForceView = false
}

func (m *Model) requestStreamBoundaryFlush() tea.Cmd {
	if m == nil {
		return nil
	}
	m.exitRenderFreeze()
	m.streamRenderDeferNext = false
	m.streamRenderForceView = true
	m.streamRenderDeferred = false
	if m.streamFlushScheduled {
		m.streamFlushGeneration++
		m.streamFlushScheduled = false
	}
	return tea.Batch(
		m.scheduleStreamFlush(1*time.Millisecond),
		m.hostRedrawForContentBoundaryCmd("content-boundary"),
	)
}

func (m *Model) drawCachedRenderableToClearedArea(scr uv.Screen, area image.Rectangle, cache *cachedRenderable) {
	if cache == nil || len(cache.lines) == 0 || area.Dx() <= 0 || area.Dy() <= 0 {
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
	maxRows := min(area.Dy(), len(cache.lines))
	for row := range maxRows {
		src := cache.lines[row]
		if len(src) == 0 {
			continue
		}
		dst := lines.Line(area.Min.Y + row)
		if dst == nil || area.Min.X >= len(dst) {
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
	if cache.text == text {
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
		buf := newScreenBuffer(w, 1)
		uv.NewStyledString(part).Draw(buf, buf.Bounds())
		line := buf.Line(0)
		copied := make([]uv.Cell, len(line))
		copy(copied, line)
		cache.lines[i] = copied
	}
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

func (m *Model) toastFingerprint() string {
	if m.activeToast == nil {
		return ""
	}
	return m.activeToast.Level + "|" + m.activeToast.Message
}

func (m *Model) mainRenderKey(mode Mode, viewportWidth int) string {
	return fmt.Sprintf("%d|%d|%d|%d|%t|%t|%d|%d|%d|%t|%d|%s", mode, viewportWidth, m.viewport.height, m.viewport.offset,
		m.animRunning, m.rightPanelVisible, len(m.viewport.blocks), m.focusedBlockID, m.search.State.Current, m.startupRestorePending, m.viewport.RenderVersion(), m.focusedAgentID)
}

func (m *Model) scheduleStreamFlush(delay time.Duration) tea.Cmd {
	if m.streamFlushScheduled {
		return nil
	}
	profileDelay := m.currentCadence().contentFlushDelay
	if delay <= 0 {
		delay = profileDelay
	} else if profileDelay > 0 && profileDelay > delay {
		delay = profileDelay
	}
	if delay <= 0 {
		return nil
	}
	m.streamFlushScheduled = true
	m.streamFlushGeneration++
	return streamFlushTick(m.streamFlushGeneration, delay)
}

func (m *Model) consumeStreamFlush(msg streamFlushTickMsg) bool {
	if msg.generation != m.streamFlushGeneration {
		return false
	}
	m.streamFlushScheduled = false
	return true
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
	return m.refreshInlineImagesIfViewportMoved(prevOffset, m.hostRedrawCmd("scroll-flush"))
}

func (m *Model) applyWheelScrollDelta(delta int) {
	if m == nil || m.viewport == nil || delta == 0 {
		return
	}
	step := 1
	steps := delta
	if steps < 0 {
		step = -1
		steps = -steps
	}
	for range steps {
		if step < 0 && m.hasDeferredStartupTranscript() &&
			m.viewport.offset <= startupDeferredPageUpSwitchThreshold(m.viewport.height) {
			if m.maybeStepStartupDeferredTranscriptWindow(-1, "mouse_wheel_up") {
				continue
			}
			m.maybeHydrateStartupDeferredTranscript("mouse_wheel_up")
		}
		if step > 0 && m.hasDeferredStartupTranscript() && m.viewport.atBottom() {
			if m.maybeStepStartupDeferredTranscriptWindow(1, "mouse_wheel_down") {
				continue
			}
		}
		if step > 0 {
			m.viewport.ScrollDown(1)
		} else {
			m.viewport.ScrollUp(1)
		}
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
