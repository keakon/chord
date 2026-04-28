package tui

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/keakon/chord/internal/config"
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
	if kind != "tool-call-update" {
		return detail
	}
	if idx := strings.Index(detail, " len="); idx >= 0 {
		return detail[:idx]
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
	content = strings.ReplaceAll(content, "\r\n", "\n")
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

func (m *Model) buildDiagnosticDump(now time.Time, trigger string) (string, string, error) {
	baseDir := strings.TrimSpace(m.workingDir)
	if baseDir == "" {
		if _, err := os.Getwd(); err != nil {
			return "", "", fmt.Errorf("resolve working dir: %w", err)
		}
	}
	locator, err := config.DefaultPathLocator()
	if err != nil {
		return "", "", fmt.Errorf("resolve storage paths: %w", err)
	}
	path := filepath.Join(locator.LogsDir, "tui-dumps",
		fmt.Sprintf("tui-dump-%s-%d.log", now.Format("20060102-150405.000"), os.Getpid()))

	m.recordTUIDiagnostic("diagnostic-dump", "%s", trigger)
	content, err := m.buildDiagnosticDumpContent(now, trigger, path, false)
	if err != nil {
		return "", "", err
	}
	return path, content, nil
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
	var cacheLenMatch bool
	var totalMatch bool
	var startsMonotonic bool
	var spansPositive bool
	var spanChainMatch bool
	if m.viewport != nil {
		viewportRender = m.viewport.Render(m.activityFrame(), m.viewportSelectionPtr(), m.searchCurrentBlockIndex())
		visible = m.viewport.visibleBlocks()
		cacheLenMatch = len(m.viewport.blockStartsCache) == len(visible) && len(m.viewport.blockSpansCache) == len(visible)
		startsMonotonic = true
		spansPositive = true
		spanChainMatch = true
		expectedStart := 0
		for i, block := range visible {
			span := debugBlockLineCount(m.viewport, block, max(1, m.viewport.width))
			recomputedTotal += span
			if span <= 0 {
				spansPositive = false
			}
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
	if scratch.RenderBuffer != nil {
		screenRender = strings.ReplaceAll(scratch.Render(), "\r\n", "\n")
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
	fmt.Fprintf(&sb, "focus_resize_frozen: %t\n", m.focusResizeFrozen)
	fmt.Fprintf(&sb, "last_foreground_at: %s\n", debugTimeString(m.lastForegroundAt))
	fmt.Fprintf(&sb, "last_background_at: %s\n", debugTimeString(m.lastBackgroundAt))
	fmt.Fprintf(&sb, "background_idle_since: %s\n", debugTimeString(m.backgroundIdleSince))
	fmt.Fprintf(&sb, "last_sweep_at: %s\n", debugTimeString(m.lastSweepAt))
	fmt.Fprintf(&sb, "idle_sweep_generation: %d scheduled=%t\n", m.idleSweepGeneration, m.idleSweepScheduled)
	fmt.Fprintf(&sb, "stable_size: %dx%d\n", m.stableWidth, m.stableHeight)
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
	fmt.Fprintf(&sb, "last_host_redraw: %s\n", safe(m.hostRedrawSummary()))
	fmt.Fprintf(&sb, "kitty_image_cache_len: %d\n", len(m.kittyImageCache))
	fmt.Fprintf(&sb, "kitty_placement_cache_len: %d\n", len(m.kittyPlacementCache))
	fmt.Fprintf(&sb, "cached_main_key: %q\n", safe(m.cachedMainKey))
	fmt.Fprintf(&sb, "cached_main_text_len: %d\n", len(m.cachedMainRender.text))
	fmt.Fprintf(&sb, "cached_input_key: %q\n", safe(m.cachedInputKey))
	fmt.Fprintf(&sb, "cached_status_key: %q\n", safe(m.cachedStatusKey))
	fmt.Fprintf(&sb, "cached_dir_key: %q\n", safe(m.cachedDirKey))

	fmt.Fprintf(&sb, "\n[layout]\n")
	fmt.Fprintf(&sb, "area: %s\n", debugRectString(layout.area))
	fmt.Fprintf(&sb, "main: %s\n", debugRectString(layout.main))
	fmt.Fprintf(&sb, "info_panel: %s\n", debugRectString(layout.infoPanel))
	fmt.Fprintf(&sb, "attachments: %s\n", debugRectString(layout.attachments))
	fmt.Fprintf(&sb, "queue: %s\n", debugRectString(layout.queue))
	fmt.Fprintf(&sb, "input: %s\n", debugRectString(layout.input))
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
				lineCount = debugBlockLineCount(m.viewport, block, max(1, m.viewport.width))
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
				"idx=%d id=%d type=%s agent=%q focused=%t collapsed=%t read_expanded=%t detail_expanded=%t streaming=%t spill_cold=%t result_done=%t start=%d span=%d line_count=%d summary=%q\n",
				i,
				block.ID,
				debugBlockTypeString(block.Type),
				block.AgentID,
				block.Focused,
				block.Collapsed,
				block.ReadContentExpanded,
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
		spinner := m.activityFrame()
		for _, block := range visible {
			fmt.Fprintf(&sb, "-- block id=%d type=%s width=%d --\n", block.ID, debugBlockTypeString(block.Type), max(1, m.viewport.width))
			rendered := strings.Join(block.Render(max(1, m.viewport.width), spinner), "\n")
			writeDiagnosticDumpSection(&sb, safe(rendered))
		}
	}

	fmt.Fprintf(&sb, "\n[viewport_render]\n")
	writeDiagnosticDumpSection(&sb, safe(viewportRender))

	fmt.Fprintf(&sb, "\n[screen_buffer]\n")
	writeDiagnosticDumpSection(&sb, safe(screenRender))

	return sb.String(), nil
}

func debugBlockLineCount(v *Viewport, block *Block, width int) int {
	if block == nil {
		return 0
	}
	if width <= 0 {
		width = 1
	}
	if block.spillCold {
		inspect, temporary := block.inspectionBlock()
		if inspect == nil {
			return 0
		}
		lc := inspect.MeasureLineCount(width)
		if temporary {
			inspect.InvalidateCache()
		}
		return lc
	}
	return block.MeasureLineCount(width)
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
	case ModeSessionSelect:
		return "session-select"
	case ModeSessionDeleteConfirm:
		return "session-delete-confirm"
	case ModeHandoffSelect:
		return "handoff-select"
	case ModeUsageStats:
		return "usage-stats"
	case ModeHelp:
		return "help"
	case ModeImageViewer:
		return "image-viewer"
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
