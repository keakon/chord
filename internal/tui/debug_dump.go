package tui

import (
	"fmt"
	"image"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	uv "github.com/keakon/ultraviolet"
	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/buildinfo"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/llm/mediacache"
	"github.com/keakon/chord/internal/textclean"
)

const maxTUIDiagnosticEvents = 128

const tuiDiagnosticDumpSectionMaxLines = 240

type tuiDiagnosticEvent struct {
	At          time.Time
	Kind        string
	Detail      string
	RepeatCount int
	FirstAt     time.Time
}

func (m *Model) recordTUIDiagnostic(kind, format string, args ...any) {
	if m == nil {
		return
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = "event"
	}
	now := time.Now()
	detail := strings.TrimSpace(fmt.Sprintf(format, args...))
	coalesceKey := tuiDiagnosticCoalesceKey(kind, detail)

	if m.tuiDiagnosticCount > 0 {
		lastIdx := (m.tuiDiagnosticNext - 1 + maxTUIDiagnosticEvents) % maxTUIDiagnosticEvents
		last := &m.tuiDiagnosticEvents[lastIdx]
		if last.Kind == kind && tuiDiagnosticCoalesceKey(last.Kind, last.Detail) == coalesceKey {
			last.At = now
			last.Detail = detail
			last.RepeatCount++
			return
		}
	}

	idx := m.tuiDiagnosticNext % maxTUIDiagnosticEvents
	m.tuiDiagnosticEvents[idx] = tuiDiagnosticEvent{
		At:      now,
		Kind:    kind,
		Detail:  detail,
		FirstAt: now,
	}
	m.tuiDiagnosticNext = (idx + 1) % maxTUIDiagnosticEvents
	if m.tuiDiagnosticCount < maxTUIDiagnosticEvents {
		m.tuiDiagnosticCount++
	}
}

func tuiDiagnosticCoalesceKey(kind, detail string) string {
	if kind == "render-frame" {
		if before, _, ok := strings.Cut(detail, " frame="); ok {
			return before
		}
	}
	if kind != "tool-call-update" {
		return detail
	}
	if before, _, ok := strings.Cut(detail, " len="); ok {
		return before
	}
	return detail
}

func (m *Model) snapshotTUIDiagnosticEvents() []tuiDiagnosticEvent {
	if m == nil || m.tuiDiagnosticCount == 0 {
		return nil
	}
	out := make([]tuiDiagnosticEvent, 0, m.tuiDiagnosticCount)
	start := m.tuiDiagnosticNext - m.tuiDiagnosticCount
	if start < 0 {
		start += maxTUIDiagnosticEvents
	}
	for i := 0; i < m.tuiDiagnosticCount; i++ {
		idx := (start + i) % maxTUIDiagnosticEvents
		evt := m.tuiDiagnosticEvents[idx]
		if evt.At.IsZero() {
			continue
		}
		out = append(out, evt)
	}
	return out
}

func writeDiagnosticDumpSection(sb *strings.Builder, content string) {
	content = textclean.NormalizeNewlines(content)
	content = strings.TrimRight(content, "\n")
	if strings.TrimSpace(content) == "" {
		fmt.Fprintf(sb, "(empty)\n")
		return
	}
	lines := strings.Split(content, "\n")
	if len(lines) <= tuiDiagnosticDumpSectionMaxLines {
		sb.WriteString(strings.Join(lines, "\n"))
		sb.WriteByte('\n')
		return
	}
	head := tuiDiagnosticDumpSectionMaxLines / 2
	tail := tuiDiagnosticDumpSectionMaxLines - head
	sb.WriteString(strings.Join(lines[:head], "\n"))
	sb.WriteByte('\n')
	fmt.Fprintf(sb, "... truncated %d middle lines ...\n", len(lines)-tuiDiagnosticDumpSectionMaxLines)
	sb.WriteString(strings.Join(lines[len(lines)-tail:], "\n"))
	sb.WriteByte('\n')
}

func writeDiagnosticBottomLines(sb *strings.Builder, content string, startRow int) {
	content = textclean.NormalizeNewlines(content)
	content = strings.TrimRight(content, "\n")
	if strings.TrimSpace(content) == "" {
		fmt.Fprintf(sb, "(empty)\n")
		return
	}
	lines := strings.Split(content, "\n")
	if startRow < 0 {
		startRow = 0
	}
	if startRow > len(lines) {
		startRow = len(lines)
	}
	for i := startRow; i < len(lines); i++ {
		line := lines[i]
		fmt.Fprintf(sb, "y=%d width=%d raw=%q\n", i, ansi.StringWidth(line), line)
	}
}

func writeDiagnosticScreenBufferBottom(sb *strings.Builder, scr uv.ScreenBuffer, startRow, height int) {
	if scr.RenderBuffer == nil || height <= 0 {
		fmt.Fprintf(sb, "(empty)\n")
		return
	}
	if startRow < 0 {
		startRow = 0
	}
	if startRow > height {
		startRow = height
	}
	for y := startRow; y < height; y++ {
		line := scr.Line(y)
		plain, occupied, cells := debugScreenBufferLineSummary(line)
		fmt.Fprintf(sb, "y=%d plain_width=%d occupied=%d cells=%d plain=%q\n", y, ansi.StringWidth(plain), occupied, cells, plain)
	}
}

func writeDiagnosticHorizontalLineRows(sb *strings.Builder, scr uv.ScreenBuffer, height int) {
	if scr.RenderBuffer == nil || height <= 0 {
		fmt.Fprintf(sb, "(empty)\n")
		return
	}
	found := false
	for y := range height {
		line := scr.Line(y)
		plain, occupied, cells := debugScreenBufferLineSummary(line)
		plainWidth := ansi.StringWidth(plain)
		horizontal := debugHorizontalLineRuneCount(plain)
		if horizontal < 8 || horizontal*2 < max(1, plainWidth) {
			continue
		}
		found = true
		fmt.Fprintf(sb, "y=%d plain_width=%d horizontal=%d occupied=%d cells=%d plain=%q\n", y, plainWidth, horizontal, occupied, cells, plain)
	}
	if !found {
		fmt.Fprintf(sb, "(none)\n")
	}
}

func debugHorizontalLineRuneCount(s string) int {
	count := 0
	for _, r := range s {
		switch r {
		case '─', '━', '═', '╌', '╍', '┄', '┅', '┈', '┉', '╴', '╶', '╾', '╼':
			count++
		}
	}
	return count
}

func debugScreenBufferLineSummary(line uv.Line) (plain string, occupied, cells int) {
	cells = len(line)
	var b strings.Builder
	for _, cell := range line {
		if cell.IsZero() {
			b.WriteByte(' ')
			continue
		}
		occupied++
		content := cell.Content
		if content == "" {
			content = " "
		}
		b.WriteString(content)
	}
	return strings.TrimRight(b.String(), " "), occupied, cells
}

func debugOptionalRow(rect image.Rectangle, delta int) string {
	if rect.Empty() {
		return "none"
	}
	return fmt.Sprintf("%d", rect.Min.Y+delta)
}

func debugSeparatorRows(layout tuiLayout) string {
	parts := []string{
		fmt.Sprintf("input=%s", debugOptionalRow(layout.input, 0)),
		fmt.Sprintf("status=%s", debugOptionalRow(layout.status, 0)),
		fmt.Sprintf("attachments=%s", debugOptionalRow(layout.attachments, 0)),
		fmt.Sprintf("queue=%s", debugOptionalRow(layout.queue, 0)),
		fmt.Sprintf("toast=%s", debugOptionalRow(layout.toast, 0)),
	}
	if !layout.main.Empty() {
		parts = append(parts, fmt.Sprintf("main_last=%d", layout.main.Max.Y-1))
	}
	if !layout.infoPanel.Empty() {
		parts = append(parts, fmt.Sprintf("info_panel_last=%d", layout.infoPanel.Max.Y-1))
	}
	return strings.Join(parts, " ")
}

func (m *Model) buildDiagnosticDumpContent(now time.Time, trigger, outputPath string, sanitize bool) (string, error) {
	baseDir := strings.TrimSpace(m.workingDir)
	if baseDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve working dir: %w", err)
		}
		baseDir = cwd
	}

	width := max(1, m.width)
	height := max(1, m.height)
	scratch := newScreenBuffer(width, height)
	_ = m.Draw(scratch, scratch.Bounds())
	layout := m.layout

	var viewportRender string
	var visible []*Block
	var visibleWindowIDs []int
	var recomputedTotal int
	var recomputedSkipped int
	var cacheLenMatch bool
	var totalMatch bool
	var startsMonotonic bool
	var spansPositive bool
	var spanChainMatch bool
	if m.viewport != nil {
		viewportRender = m.viewport.Render(m.activityFrame(), m.viewportSelectionPtr(), m.searchCurrentBlockIndex(), m.searchCurrentInnerOffset(), m.search.State.Query)
		visible = m.viewport.visibleBlocks()
		cacheLenMatch = len(m.viewport.blockStartsCache) == len(visible) && len(m.viewport.blockSpansCache) == len(visible)
		startsMonotonic = true
		spansPositive = true
		spanChainMatch = true
		expectedStart := 0
		for i, block := range visible {
			span, known := debugBlockLineCount(block, max(1, m.viewport.width))
			if known {
				if span <= 0 {
					spansPositive = false
				}
			} else {
				// Cold blocks report the count recorded at spill time. Without
				// one, fall back to the viewport's own span so the start chain
				// stays checkable instead of hydrating the block here.
				recomputedSkipped++
				if i < len(m.viewport.blockSpansCache) {
					span = m.viewport.blockSpansCache[i]
				}
			}
			recomputedTotal += span
			if i < len(m.viewport.blockStartsCache) {
				if m.viewport.blockStartsCache[i] != expectedStart {
					startsMonotonic = false
					spanChainMatch = false
				}
			}
			if i < len(m.viewport.blockSpansCache) && m.viewport.blockSpansCache[i] != span {
				spanChainMatch = false
			}
			expectedStart += span
		}
		totalMatch = recomputedTotal == m.viewport.totalLines
		visibleWindowIDs = sortedBlockIDList(m.viewport.visibleWindowBlockIDs())
	}

	screenRender := ""
	frameRender := ""
	if scratch.RenderBuffer != nil {
		screenRender = textclean.NormalizeNewlines(scratch.Render())
		frameRender = screenRender
	}

	safe := func(s string) string {
		if !sanitize {
			return s
		}
		return sanitizeDiagnosticText(s, baseDir)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Chord TUI diagnostics dump\n")
	fmt.Fprintf(&sb, "generated_at: %s\n", now.Format(time.RFC3339Nano))
	writeBuildInfoDiagnostics(&sb, buildinfo.Current(), baseDir)
	fmt.Fprintf(&sb, "trigger: %s\n", strings.TrimSpace(trigger))
	fmt.Fprintf(&sb, "working_dir: %s\n", safe(baseDir))
	fmt.Fprintf(&sb, "dump_path: %s\n", safe(outputPath))
	fmt.Fprintf(&sb, "\n[model]\n")
	fmt.Fprintf(&sb, "mode: %s\n", debugModeString(m.mode))
	fmt.Fprintf(&sb, "size: %dx%d\n", m.width, m.height)
	fmt.Fprintf(&sb, "theme: %s\n", m.theme.Name)
	fmt.Fprintf(&sb, "focused_agent: %q\n", debugFocusedAgentLabel(m.focusedAgentID))
	fmt.Fprintf(&sb, "focused_block_id: %d\n", m.focusedBlockID)
	fmt.Fprintf(&sb, "display_state: %s\n", debugDisplayState(m.displayState))
	fmt.Fprintf(&sb, "right_panel_visible: %t\n", m.rightPanelVisible)
	fmt.Fprintf(&sb, "terminal_focused: %t\n", m.terminalAppFocused)
	fmt.Fprintf(&sb, "render_frame_generation: %d\n", m.renderFrameGeneration)
	fmt.Fprintf(&sb, "last_foreground_at: %s\n", debugTimeString(m.lastForegroundAt))
	fmt.Fprintf(&sb, "last_background_at: %s\n", debugTimeString(m.lastBackgroundAt))
	fmt.Fprintf(&sb, "background_idle_since: %s\n", debugTimeString(m.backgroundIdleSince))
	fmt.Fprintf(&sb, "last_cache_sweep_at: %s\n", debugTimeString(m.lastCacheSweepAt))
	fmt.Fprintf(&sb, "last_free_os_memory_at: %s\n", debugTimeString(m.lastFreeOSMemoryAt))
	fmt.Fprintf(&sb, "free_os_memory_pending: %t\n", m.freeOSMemoryPending)
	fmt.Fprintf(&sb, "free_os_memory_last_reclaimable_bytes: %d\n", m.freeOSMemoryLastReclaimable)
	fmt.Fprintf(&sb, "free_os_memory_last_ran: %t\n", m.freeOSMemoryLastRan)
	fmt.Fprintf(&sb, "idle_sweep_generation: %d scheduled=%t\n", m.idleSweepGeneration, m.idleSweepScheduled)
	fmt.Fprintf(&sb, "pending_resize: %dx%d version=%d\n", m.pendingResizeW, m.pendingResizeH, m.resizeVersion)
	fmt.Fprintf(&sb, "stream_flush_generation: %d scheduled=%t\n", m.streamFlushGeneration, m.streamFlushScheduled)
	fmt.Fprintf(&sb, "startup_restore_pending: %t\n", m.startupRestorePending)
	fmt.Fprintf(&sb, "attachments: %d\n", len(m.attachments))
	fmt.Fprintf(&sb, "queued_drafts: %d\n", len(m.queuedDrafts))
	fmt.Fprintf(&sb, "active_toast: %t\n", m.activeToast != nil)
	fmt.Fprintf(&sb, "input_value: %q\n", safe(m.input.Value()))
	fmt.Fprintf(&sb, "input_display_lines: %d\n", m.input.ClampedDisplayLineCount())
	fmt.Fprintf(&sb, "input_cursor: row=%d col=%d\n", m.input.Line(), m.input.Column())
	fmt.Fprintf(&sb, "selected_search_block: %d\n", m.searchCurrentBlockIndex())
	fmt.Fprintf(&sb, "image_backend: %s inline=%t fullscreen=%t reason=%q\n",
		m.imageCaps.Backend.String(), m.imageCaps.SupportsInline, m.imageCaps.SupportsFullscreen, safe(m.imageCaps.Reason))
	fmt.Fprintf(&sb, "last_image_protocol_at: %s\n", debugTimeString(m.lastImageProtocolAt))
	fmt.Fprintf(&sb, "last_image_protocol_reason: %q\n", safe(m.lastImageProtocolReason))
	fmt.Fprintf(&sb, "last_image_protocol_summary: %q\n", safe(m.lastImageProtocolSummary))
	fmt.Fprintf(&sb, "kitty_image_cache_len: %d\n", len(m.kittyImageCache))
	fmt.Fprintf(&sb, "kitty_placement_cache_len: %d\n", len(m.kittyPlacementCache))
	fmt.Fprintf(&sb, "cached_main_key: %q\n", safe(m.cachedMainKey))
	fmt.Fprintf(&sb, "cached_main_text_len: %d\n", len(m.cachedMainRender.text))
	fmt.Fprintf(&sb, "cached_input_key: %q\n", safe(m.cachedInputKey))
	fmt.Fprintf(&sb, "cached_status_key: %q\n", safe(m.cachedStatusKey))

	fmt.Fprintf(&sb, "\n[layout]\n")
	fmt.Fprintf(&sb, "area: %s\n", debugRectString(layout.area))
	fmt.Fprintf(&sb, "main: %s\n", debugRectString(layout.main))
	fmt.Fprintf(&sb, "info_panel: %s\n", debugRectString(layout.infoPanel))
	fmt.Fprintf(&sb, "attachments: %s\n", debugRectString(layout.attachments))
	fmt.Fprintf(&sb, "queue: %s\n", debugRectString(layout.queue))
	fmt.Fprintf(&sb, "input: %s\n", debugRectString(layout.input))
	fmt.Fprintf(&sb, "input_separator_row: %d input_height=%d input_area_height=%d\n", layout.input.Min.Y, layout.input.Dy(), m.inputAreaHeight())
	fmt.Fprintf(&sb, "expected_separator_rows: %s\n", debugSeparatorRows(layout))
	fmt.Fprintf(&sb, "toast: %s\n", debugRectString(layout.toast))
	fmt.Fprintf(&sb, "status: %s\n", debugRectString(layout.status))

	fmt.Fprintf(&sb, "\n[viewport]\n")
	if m.viewport == nil {
		fmt.Fprintf(&sb, "nil: true\n")
	} else {
		fmt.Fprintf(&sb, "width: %d\n", m.viewport.width)
		fmt.Fprintf(&sb, "height: %d\n", m.viewport.height)
		fmt.Fprintf(&sb, "offset: %d\n", m.viewport.offset)
		fmt.Fprintf(&sb, "sticky: %t\n", m.viewport.sticky)
		fmt.Fprintf(&sb, "filter_agent_id: %q\n", m.viewport.filterAgentID)
		fmt.Fprintf(&sb, "total_lines_cached: %d\n", m.viewport.totalLines)
		fmt.Fprintf(&sb, "hot_bytes: %d\n", m.viewport.hotBytes)
		fmt.Fprintf(&sb, "hot_budget_dirty: %t\n", m.viewport.hotBudgetDirty)
		fmt.Fprintf(&sb, "hot_bytes_dirty: %t\n", m.viewport.hotBytesDirty)
		fmt.Fprintf(&sb, "max_hot_bytes: %d\n", m.viewport.maxHotBytes)
		fmt.Fprintf(&sb, "base_hot_bytes: %d\n", m.viewport.baseHotBytes)
		fmt.Fprintf(&sb, "total_lines_recomputed: %d\n", recomputedTotal)
		fmt.Fprintf(&sb, "total_lines_recomputed_skipped: %d\n", recomputedSkipped)
		fmt.Fprintf(&sb, "last_block_span: %d dirty=%t\n", m.viewport.lastBlockSpan, m.viewport.lastBlockDirty)
		fmt.Fprintf(&sb, "blocks_total: %d\n", len(m.viewport.blocks))
		fmt.Fprintf(&sb, "blocks_visible: %d\n", len(visible))
		fmt.Fprintf(&sb, "starts_cache_len: %d\n", len(m.viewport.blockStartsCache))
		fmt.Fprintf(&sb, "spans_cache_len: %d\n", len(m.viewport.blockSpansCache))
		fmt.Fprintf(&sb, "visible_window_ids: %v\n", visibleWindowIDs)
		fmt.Fprintf(&sb, "consistency.total_match: %t\n", totalMatch)
		fmt.Fprintf(&sb, "consistency.cache_lengths_match: %t\n", cacheLenMatch)
		fmt.Fprintf(&sb, "consistency.starts_monotonic: %t\n", startsMonotonic)
		fmt.Fprintf(&sb, "consistency.spans_positive: %t\n", spansPositive)
		fmt.Fprintf(&sb, "consistency.span_chain_match: %t\n", spanChainMatch)
	}

	// Every number below answers "how many bytes are held in the heap". An
	// attachment stored in the session directory contributes nothing to
	// ctxmgr.image_bytes even though it still occupies context: the model
	// receives it, the process does not hold it.
	fmt.Fprintf(&sb, "\n[memory]\n")
	if reporter, ok := m.agent.(agent.ContextMemoryReporter); ok {
		breakdown := reporter.GetResidentMemoryBreakdown()
		fmt.Fprintf(&sb, "ctxmgr.text_bytes: %d\n", breakdown.Text)
		fmt.Fprintf(&sb, "ctxmgr.image_bytes: %d\n", breakdown.Image)
		fmt.Fprintf(&sb, "ctxmgr.pdf_bytes: %d\n", breakdown.PDF)
		fmt.Fprintf(&sb, "ctxmgr.tool_call_bytes: %d\n", breakdown.ToolCalls)
		fmt.Fprintf(&sb, "ctxmgr.thinking_bytes: %d\n", breakdown.Thinking)
		fmt.Fprintf(&sb, "ctxmgr.responses_output_bytes: %d\n", breakdown.Responses)
		fmt.Fprintf(&sb, "ctxmgr.gemini_part_bytes: %d\n", breakdown.Gemini)
		fmt.Fprintf(&sb, "ctxmgr.total_bytes: %d\n", breakdown.Total())
		fmt.Fprintf(&sb, "agent.attachment_cache_bytes: %d\n", reporter.AttachmentCacheBytes())
	} else {
		fmt.Fprintf(&sb, "agent: unavailable\n")
	}
	if m.viewport != nil {
		fmt.Fprintf(&sb, "tui.hot_bytes: %d\n", m.viewport.hotBytes)
		fmt.Fprintf(&sb, "tui.resident_bytes: %d\n", m.viewport.ResidentBytes())
	}
	if state := m.startupDeferredTranscript; state != nil {
		hot, cold := 0, 0
		var hotBytes int64
		for _, block := range state.allBlocks {
			if block == nil {
				continue
			}
			if block.spillCold {
				cold++
				continue
			}
			hot++
			hotBytes += block.estimatedHotBytes()
		}
		fmt.Fprintf(&sb, "tui.deferred_blocks: %d\n", len(state.allBlocks))
		fmt.Fprintf(&sb, "tui.deferred_window: [%d,%d)\n", state.windowStart, state.windowEnd)
		fmt.Fprintf(&sb, "tui.deferred_blocks_hot: %d\n", hot)
		fmt.Fprintf(&sb, "tui.deferred_blocks_cold: %d\n", cold)
		fmt.Fprintf(&sb, "tui.deferred_hot_bytes: %d\n", hotBytes)
	}
	encodedBytes, dataURLBytes := mediacache.ResidentBytes()
	fmt.Fprintf(&sb, "llm.image_wire_cache_bytes: %d\n", llm.ImageWireCacheBytes())
	fmt.Fprintf(&sb, "llm.image_encoding_cache_bytes: %d\n", encodedBytes)
	fmt.Fprintf(&sb, "llm.image_data_url_cache_bytes: %d\n", dataURLBytes)
	// Non-zero at rest means a retry walk failed to release its cached request
	// body; mid-turn it is expected to be the marshaled body plus its
	// compressed copy.
	fmt.Fprintf(&sb, "llm.body_cache_bytes: %d\n", llm.BodyCacheRetainedBytes())

	// Go runtime heap accounting separates live objects from reclaimable
	// spans. HeapIdle-HeapReleased is what debug.FreeOSMemory can return to
	// the OS; HeapAlloc is live heap. ReadMemStats briefly stops the world,
	// which is acceptable for an explicit diagnostics dump.
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	fmt.Fprintf(&sb, "go.heap_alloc_bytes: %d\n", mem.HeapAlloc)
	fmt.Fprintf(&sb, "go.heap_inuse_bytes: %d\n", mem.HeapInuse)
	fmt.Fprintf(&sb, "go.heap_idle_bytes: %d\n", mem.HeapIdle)
	fmt.Fprintf(&sb, "go.heap_released_bytes: %d\n", mem.HeapReleased)
	fmt.Fprintf(&sb, "go.heap_reclaimable_bytes: %d\n", heapReclaimableBytes(&mem))
	fmt.Fprintf(&sb, "go.heap_sys_bytes: %d\n", mem.HeapSys)
	fmt.Fprintf(&sb, "go.heap_objects: %d\n", mem.HeapObjects)
	fmt.Fprintf(&sb, "go.stack_inuse_bytes: %d\n", mem.StackInuse)
	fmt.Fprintf(&sb, "go.stack_sys_bytes: %d\n", mem.StackSys)
	fmt.Fprintf(&sb, "go.gc_sys_bytes: %d\n", mem.GCSys)
	fmt.Fprintf(&sb, "go.other_sys_bytes: %d\n", mem.OtherSys)
	fmt.Fprintf(&sb, "go.sys_bytes: %d\n", mem.Sys)
	fmt.Fprintf(&sb, "go.next_gc_bytes: %d\n", mem.NextGC)
	fmt.Fprintf(&sb, "go.total_alloc_bytes: %d\n", mem.TotalAlloc)
	fmt.Fprintf(&sb, "go.num_gc: %d\n", mem.NumGC)
	fmt.Fprintf(&sb, "go.num_forced_gc: %d\n", mem.NumForcedGC)
	fmt.Fprintf(&sb, "go.gc_pause_total_ns: %d\n", mem.PauseTotalNs)
	fmt.Fprintf(&sb, "go.gc_cpu_fraction: %.6f\n", mem.GCCPUFraction)

	fmt.Fprintf(&sb, "\n[recent_events]\n")
	events := m.snapshotTUIDiagnosticEvents()
	if len(events) == 0 {
		fmt.Fprintf(&sb, "(none)\n")
	} else {
		for _, evt := range events {
			detail := safe(evt.Detail)
			if evt.RepeatCount > 0 {
				fmt.Fprintf(&sb, "%s [%s] %s (×%d since %s)\n",
					evt.At.Format(time.RFC3339Nano), evt.Kind, detail,
					evt.RepeatCount+1, evt.FirstAt.Format(time.RFC3339Nano))
			} else {
				fmt.Fprintf(&sb, "%s [%s] %s\n", evt.At.Format(time.RFC3339Nano), evt.Kind, detail)
			}
		}
	}

	fmt.Fprintf(&sb, "\n[blocks.visible]\n")
	if len(visible) == 0 {
		fmt.Fprintf(&sb, "(none)\n")
	} else {
		for i, block := range visible {
			lineCount := 0
			if m.viewport != nil {
				lineCount, _ = debugBlockLineCount(block, max(1, m.viewport.width))
			}
			start := -1
			span := -1
			if m.viewport != nil {
				if i < len(m.viewport.blockStartsCache) {
					start = m.viewport.blockStartsCache[i]
				}
				if i < len(m.viewport.blockSpansCache) {
					span = m.viewport.blockSpansCache[i]
				}
			}
			fmt.Fprintf(&sb,
				"idx=%d id=%d type=%s agent=%q focused=%t collapsed=%t detail_expanded=%t streaming=%t spill_cold=%t result_done=%t start=%d span=%d line_count=%d summary=%q\n",
				i,
				block.ID,
				debugBlockTypeString(block.Type),
				block.AgentID,
				block.Focused,
				block.Collapsed,
				block.ToolCallDetailExpanded,
				block.Streaming,
				block.spillCold,
				block.ResultDone,
				start,
				span,
				lineCount,
				safe(block.Summary()),
			)
		}
	}

	fmt.Fprintf(&sb, "\n[blocks.rendered]\n")
	if len(visible) == 0 || m.viewport == nil {
		fmt.Fprintf(&sb, "(none)\n")
	} else {
		// Only the visible window is rendered. Rendering every block forced a
		// re-render (and spill hydration) of the whole transcript on the Update
		// path, which froze the UI for seconds on long sessions; the screen
		// buffer already captures the visible frame.
		window := make(map[int]struct{}, len(visibleWindowIDs))
		for _, id := range visibleWindowIDs {
			window[id] = struct{}{}
		}
		spinner := m.activityFrame()
		rendered := 0
		for _, block := range visible {
			if _, ok := window[block.ID]; !ok {
				continue
			}
			rendered++
			fmt.Fprintf(&sb, "-- block id=%d type=%s width=%d --\n", block.ID, debugBlockTypeString(block.Type), max(1, m.viewport.width))
			lines := block.Render(max(1, m.viewport.width), spinner)
			writeDiagnosticDumpSection(&sb, safe(strings.Join(lines, "\n")))
		}
		if rendered == 0 {
			fmt.Fprintf(&sb, "(none)\n")
		}
		fmt.Fprintf(&sb, "-- %d off-screen blocks omitted --\n", len(visible)-rendered)
	}

	fmt.Fprintf(&sb, "\n[viewport_render]\n")
	writeDiagnosticDumpSection(&sb, safe(viewportRender))

	fmt.Fprintf(&sb, "\n[frame_bottom]\n")
	writeDiagnosticBottomLines(&sb, safe(frameRender), max(0, height-8))

	fmt.Fprintf(&sb, "\n[screen_buffer_bottom]\n")
	writeDiagnosticScreenBufferBottom(&sb, scratch, max(0, height-8), height)

	fmt.Fprintf(&sb, "\n[screen_buffer_horizontal_lines]\n")
	writeDiagnosticHorizontalLineRows(&sb, scratch, height)

	fmt.Fprintf(&sb, "\n[screen_buffer]\n")
	writeDiagnosticDumpSection(&sb, safe(screenRender))

	return sb.String(), nil
}

// debugBlockLineCount reports a block's line count without hydrating spilled
// content. Diagnostics dumps run on the Update path, and a cold block's spill
// read plus full re-render would block the UI; the count recorded at spill time
// is enough for the dump. Warm blocks use (or build) their line-count cache.
func debugBlockLineCount(block *Block, width int) (int, bool) {
	if block == nil {
		return 0, false
	}
	if width <= 0 {
		width = 1
	}
	if block.spillCold {
		lc, ok := block.spillLineCounts[width]
		return lc, ok
	}
	return block.LineCount(width), true
}

func sortedBlockIDList(ids map[int]struct{}) []int {
	if len(ids) == 0 {
		return nil
	}
	out := make([]int, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Ints(out)
	return out
}

func debugRectString(r image.Rectangle) string {
	return fmt.Sprintf("[(%d,%d)-(%d,%d) w=%d h=%d]", r.Min.X, r.Min.Y, r.Max.X, r.Max.Y, r.Dx(), r.Dy())
}

func debugTimeString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}

func debugFocusedAgentLabel(agentID string) string {
	if strings.TrimSpace(agentID) == "" {
		return "main"
	}
	return agentID
}

func debugDisplayState(state displayState) string {
	switch state {
	case stateForeground:
		return "foreground"
	case stateBackground:
		return "background"
	default:
		return fmt.Sprintf("displayState(%d)", state)
	}
}

func debugModeString(mode Mode) string {
	switch mode {
	case ModeInsert:
		return "insert"
	case ModeNormal:
		return "normal"
	case ModeDirectory:
		return "directory"
	case ModeConfirm:
		return "confirm"
	case ModeQuestion:
		return "question"
	case ModeSearch:
		return "search"
	case ModeModelSelect:
		return "model-select"
	case ModeRoleSelect:
		return "role-select"
	case ModeSkillSelect:
		return "skill-select"
	case ModeInputHistorySearch:
		return "input_history_search"
	case ModeInputHistoryClearConfirm:
		return "input_history_clear"
	case ModeMemoryPanel:
		return "memory"
	case ModeSessionSelect:
		return "session-select"
	case ModeSessionDeleteConfirm:
		return "session-delete-confirm"
	case ModeHandoffSelect:
		return "handoff-select"
	case ModeStatus:
		return "status"
	case ModeUsageStats:
		return "usage-stats"
	case ModeErrorPanel:
		return "error-panel"
	case ModeHelp:
		return "help"
	case ModeContentViewer:
		return "content-viewer"
	case ModeImageViewer:
		return "image-viewer"
	case ModeStopJobConfirm:
		return "stop-job-confirm"
	case ModeContext:
		return "context"
	case ModeJobsOverlay:
		return "jobs-overlay"
	default:
		return fmt.Sprintf("mode(%d)", mode)
	}
}

func debugBlockTypeString(t BlockType) string {
	switch t {
	case BlockUser:
		return "user"
	case BlockAssistant:
		return "assistant"
	case BlockThinking:
		return "thinking"
	case BlockToolCall:
		return "tool-call"
	case BlockToolResult:
		return "tool-result"
	case BlockError:
		return "error"
	case BlockStatus:
		return "status"
	case BlockBoundaryMarker:
		return "boundary-marker"
	case BlockCompactionSummary:
		return "compaction-summary"
	default:
		return fmt.Sprintf("block(%d)", t)
	}
}
