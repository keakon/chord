package tui

import (
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

func cloneBlockForDeferredSource(src *Block) *Block {
	if src == nil {
		return nil
	}
	clone := *src
	clone.codeHL = nil
	clone.richMarkdownHL = nil
	clone.thinkingStreamSettled = nil
	clone.toolArgsCacheKeys = append([]string(nil), src.toolArgsCacheKeys...)
	if len(src.toolArgsCacheVals) > 0 {
		clone.toolArgsCacheVals = make(map[string]string, len(src.toolArgsCacheVals))
		maps.Copy(clone.toolArgsCacheVals, src.toolArgsCacheVals)
	}
	clone.toolHeaderCacheParamLines = append([]string(nil), src.toolHeaderCacheParamLines...)
	clone.displayWorkingDir = src.displayWorkingDir
	if src.ToolProgress != nil {
		progress := *src.ToolProgress
		clone.ToolProgress = &progress
	}
	clone.FileRefs = append([]string(nil), src.FileRefs...)
	clone.ImageParts = append([]BlockImagePart(nil), src.ImageParts...)
	clone.PDFNames = append([]string(nil), src.PDFNames...)
	clone.ThinkingParts = append([]string(nil), src.ThinkingParts...)
	clone.mdCache = append([]string(nil), src.mdCache...)
	clone.mdCacheContent = src.mdCacheContent
	clone.mdCacheThemeVersion = src.mdCacheThemeVersion
	clone.mdCacheSyntheticPrefixWidths = append([]int(nil), src.mdCacheSyntheticPrefixWidths...)
	clone.mdCacheSoftWrapContinuations = append([]bool(nil), src.mdCacheSoftWrapContinuations...)
	clone.streamSettledLines = append([]string(nil), src.streamSettledLines...)
	clone.streamSettledSyntheticPrefixWidths = append([]int(nil), src.streamSettledSyntheticPrefixWidths...)
	clone.streamSettledSoftWrapContinuations = append([]bool(nil), src.streamSettledSoftWrapContinuations...)
	clone.streamSettledLineCount = src.streamSettledLineCount
	clone.streamTailRaw = src.streamTailRaw
	clone.streamTailWidth = src.streamTailWidth
	clone.streamTailLines = append([]string(nil), src.streamTailLines...)
	clone.streamTailSyntheticPrefixWidths = append([]int(nil), src.streamTailSyntheticPrefixWidths...)
	clone.streamTailSoftWrapContinuations = append([]bool(nil), src.streamTailSoftWrapContinuations...)
	clone.streamCardHeadLines = append([]string(nil), src.streamCardHeadLines...)
	clone.streamCardHeadKey = src.streamCardHeadKey
	clone.lineCache = append([]string(nil), src.lineCache...)
	clone.viewportCache = append([]string(nil), src.viewportCache...)
	clone.renderSyntheticPrefixWidths = append([]int(nil), src.renderSyntheticPrefixWidths...)
	clone.renderSoftWrapContinuations = append([]bool(nil), src.renderSoftWrapContinuations...)
	clone.spillRef = nil
	clone.spillStore = nil
	clone.spillSummary = ""
	clone.spillLineCounts = cloneLineCounts(src.spillLineCounts)
	clone.spillCold = false
	clone.lastAccess = 0
	clone.spillRecover = nil
	return &clone
}

func (b *Block) toolResultIsError() bool {
	return b.ResultStatus == agent.ToolResultStatusError
}

func (b *Block) toolResultIsCancelled() bool {
	return b.ResultStatus == agent.ToolResultStatusCancelled
}

func (b *Block) toolExecutionIsRunning() bool {
	if b == nil || b.ResultDone {
		return false
	}
	return b.ToolExecutionState == agent.ToolCallExecutionStateRunning
}

func (b *Block) toolExecutionIsQueued() bool {
	return b != nil && !b.ResultDone && b.ToolExecutionState == agent.ToolCallExecutionStateQueued
}

func (b *Block) toolElapsed() time.Duration {
	if b == nil {
		return 0
	}
	if !b.StartedAt.IsZero() {
		end := b.SettledAt
		if end.IsZero() {
			end = time.Now()
		}
		if end.Before(b.StartedAt) {
			return 0
		}
		return end.Sub(b.StartedAt)
	}
	if b.PersistedDuration > 0 {
		return b.PersistedDuration
	}
	return 0
}

func (b *Block) toolElapsedLabel() string {
	elapsed := b.toolElapsed()
	if elapsed < 5*time.Second {
		return ""
	}
	return elapsed.Round(time.Second).String()
}

// IsUserLocalShell reports a merged USER + local !shell block.
func (b *Block) IsUserLocalShell() bool {
	return b != nil && b.Type == BlockUser && b.UserLocalShellCmd != ""
}

func blockLabelWithID(label string, id int) string {
	if id < 0 {
		return label
	}
	return fmt.Sprintf("%s #%d", label, id+1)
}

// Render produces the styled lines for this block, word-wrapped to width.
func (b *Block) Render(width int, spinnerFrame string) (lines []string) {
	if b == nil {
		return wrapText("[render error: nil block]", width)
	}
	_ = b.ensureMaterialized()
	if width <= 0 {
		width = 80
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			lines = blockRenderPanicFallback(b, width, recovered)
		}
	}()
	switch b.Type {
	case BlockUser:
		return b.renderUser(width, spinnerFrame)
	case BlockAssistant:
		return b.renderAssistant(width)
	case BlockThinking:
		return b.renderThinking(width)
	case BlockToolCall:
		return b.renderToolCall(width, spinnerFrame)
	case BlockToolResult:
		return b.renderToolResult(width)
	case BlockError:
		return b.renderError(width)
	case BlockStatus:
		return b.renderStatus(width)
	case BlockBoundaryMarker:
		return b.renderBoundaryMarker(width)
	case BlockCompactionSummary:
		return b.renderCompactionSummary(width)
	default:
		return wrapText(b.Content, width)
	}
}

func blockRenderPanicFallback(b *Block, width int, recovered any) []string {
	if width <= 0 {
		width = 80
	}
	label := fmt.Sprintf("[render error: %v]", recovered)
	if b == nil {
		return wrapText(label, width)
	}
	content := b.Content
	if strings.TrimSpace(content) == "" && b.ResultContent != "" {
		content = b.ResultContent
	}
	content = sanitizeDisplayText(content)
	if strings.TrimSpace(content) == "" {
		return wrapText(label, width)
	}
	lines := wrapText(label, width)
	lines = append(lines, wrapText(content, width)...)
	return lines
}

// LineCount returns how many terminal lines this block occupies.
func (b *Block) LineCount(width int) int {
	_ = b.ensureMaterialized()
	if b.lineCache == nil || b.lineCacheWidth != width {
		b.lineCache = b.Render(width, "")
		b.lineCacheWidth = width
		b.lineCountCache = len(b.lineCache)
		b.hotBytesMemoValid = false
	}
	return b.lineCountCache
}

// RenderRange returns the rendered block lines in [start,end). It prefers
// cached full-render results and slices them when available.
func (b *Block) RenderRange(width int, spinnerFrame string, start, end int) []string {
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	if cached := b.GetViewportCache(width, spinnerFrame); cached != nil {
		if start >= len(cached) {
			return nil
		}
		if end > len(cached) {
			end = len(cached)
		}
		return cached[start:end]
	}
	if b.lineCache != nil && b.lineCacheWidth == width && spinnerFrame == "" {
		if start >= len(b.lineCache) {
			return nil
		}
		if end > len(b.lineCache) {
			end = len(b.lineCache)
		}
		return b.lineCache[start:end]
	}
	lines := b.Render(width, spinnerFrame)
	if spinnerFrame == "" {
		b.lineCache = lines
		b.lineCacheWidth = width
		b.lineCountCache = len(lines)
		b.hotBytesMemoValid = false
	}
	if start >= len(lines) {
		return nil
	}
	if end > len(lines) {
		end = len(lines)
	}
	return lines[start:end]
}

// MeasureLineCount returns the block's rendered line count without populating
// lineCache. Use this only when the rendered lines will not be needed afterward
// (e.g. spill inspection or debug dumps); hot paths that render the block right
// after measuring should prefer LineCount so the render is reused.
func (b *Block) MeasureLineCount(width int) int {
	_ = b.ensureMaterialized()
	return len(b.Render(width, ""))
}

// Toggle flips the collapsed state (only meaningful for tool blocks and
// assistant blocks with thinking parts).
func (b *Block) Toggle() {
	b.ToggleAtWidth(0)
}

func (b *Block) ToggleAtWidth(width int) {
	switch b.Type {
	case BlockUser:
		if b.UserLocalShellCmd != "" && !b.UserLocalShellPending && strings.TrimSpace(b.UserLocalShellResult) != "" {
			b.Collapsed = !b.Collapsed
			b.InvalidateCache()
		}
	case BlockToolCall, BlockToolResult:
		if b.Type == BlockToolCall && (b.ToolName == tools.NameWrite || b.ToolName == tools.NameRead) {
			if b.Collapsed {
				b.Collapsed = false
				b.InvalidateCache()
				return
			}
			rowCount := len(strings.Split(b.ResultContent, "\n"))
			if b.ToolName == tools.NameWrite {
				_, vals := b.toolArgsParsed()
				if vals != nil {
					rows, _ := parsePlainContentPreviewLines(vals["content"])
					rowCount = len(rows)
				}
			}
			if rowCount <= maxReadDefaultLines {
				return
			}
			b.ReadContentExpanded = !b.ReadContentExpanded
			b.InvalidateCache()
			return
		}
		if b.Type == BlockToolCall && toolUsesCompactDetailToggle(b.ToolName) {
			if b.ToolCallDetailExpanded && width > 0 && b.compactToolResultForceExpandedForRenderWidth(width) {
				return
			}
			b.ToolCallDetailExpanded = !b.ToolCallDetailExpanded
			b.InvalidateCache()
			return
		}
		if b.Type == BlockToolResult {
			b.Collapsed = !b.Collapsed
			b.InvalidateCache()
			return
		}
		b.Collapsed = !b.Collapsed
		b.InvalidateCache()
	case BlockAssistant:
		if len(b.ThinkingParts) > 0 {
			b.ThinkingCollapsed = !b.ThinkingCollapsed
			b.InvalidateCache()
		}
	case BlockCompactionSummary:
		b.Collapsed = !b.Collapsed
		if raw := strings.TrimSpace(b.CompactionSummaryRaw); raw != "" {
			b.Content = formatCompactionSummaryDisplay(raw, b.Collapsed, b.CompactionPreviewLines)
		}
		b.InvalidateCache()
	}
}

// InvalidateCache clears render caches that must be recomputed after content
// changes. It intentionally preserves streamSettled* and thinkingStreamSettled
// so append-only streaming updates can reuse already-rendered stable prefixes
// across deltas.
func (b *Block) InvalidateCache() {
	b.lineCache = nil
	b.lineCacheWidth = 0
	b.lineCountCache = 0
	b.hotBytesMemoValid = false
	if b.Streaming {
		b.mdCache = nil
		b.mdCacheWidth = 0
		b.mdCacheContent = ""
		b.mdCacheThemeVersion = 0
		b.mdCacheSyntheticPrefixWidths = nil
		b.mdCacheSoftWrapContinuations = nil
	}
	b.streamSettledLineCount = 0
	b.viewportCache = nil
	b.viewportCacheWidth = 0
	b.renderSyntheticPrefixWidths = nil
	b.renderSoftWrapContinuations = nil
	b.renderSyntheticPrefixWidthsW = 0
	b.searchTextLower = ""
	b.searchTextReady = false
	b.searchMatchQueryLower = ""
	b.searchMatchWidth = 0
	b.searchMatchOffset = 0
	b.searchMatchFound = false
	b.searchMatchReady = false
}

// InvalidateStreamingSettledCache clears the cached rendered markdown for the
// stable prefix of a streaming assistant block. Call this when the content is
// replaced non-monotonically, streaming mode ends, or other state changes make
// prefix reuse invalid.
func (b *Block) InvalidateStreamingSettledCache() {
	b.streamSettledRaw = ""
	b.streamSettledFrontier = 0
	b.streamSettledWidth = 0
	b.streamSettledLines = nil
	b.streamSettledSyntheticPrefixWidths = nil
	b.streamSettledSoftWrapContinuations = nil
	b.streamTailRaw = ""
	b.streamTailWidth = 0
	b.streamTailLines = nil
	b.streamTailSyntheticPrefixWidths = nil
	b.streamTailSoftWrapContinuations = nil
	b.streamSettledLineCount = 0
	b.streamCardHeadLines = nil
	b.streamCardHeadKey = streamCardHeadKey{}
	b.streamTableCheckedLen = 0
	b.streamTableFound = false
	b.streamFrontierScanner = nil
}

// InvalidateThinkingStreamingSettledCache clears cached rendered markdown for
// in-flight thinking parts. Call this when thinking content is replaced
// non-monotonically or part ordering changes.
func (b *Block) InvalidateThinkingStreamingSettledCache() {
	b.thinkingStreamSettled = nil
	b.streamCardHeadLines = nil
	b.streamCardHeadKey = streamCardHeadKey{}
}

// GetViewportCache returns the styled and truncated lines cached for Viewport.Render,
// or nil if the cache is invalid. If the block has a varying animation (spinner),
// it returns nil to force re-render.
func (b *Block) GetViewportCache(width int, spinnerFrame string) []string {
	if b.viewportCache == nil || b.viewportCacheWidth != width {
		return nil
	}
	if spinnerFrame != "" && b.Type == BlockToolCall && b.toolExecutionIsRunning() {
		return nil
	}
	if spinnerFrame != "" && b.Type == BlockUser && b.UserLocalShellCmd != "" && b.UserLocalShellPending {
		return nil
	}
	return b.viewportCache
}

// SetViewportCache saves the final styled and truncated lines for this block.
func (b *Block) SetViewportCache(width int, lines []string) {
	b.viewportCache = append([]string(nil), lines...)
	b.viewportCacheWidth = width
	b.hotBytesMemoValid = false
}

// Summary returns a one-line description for the message directory.
func (b *Block) Summary() string {
	if b.spillCold && b.spillSummary != "" {
		return b.spillSummary
	}
	switch b.Type {
	case BlockUser:
		if b.UserLocalShellCmd != "" {
			return "[user] " + truncateOneLine(b.Content, 36) + " (!shell)"
		}
		summary := strings.TrimSpace(b.Content)
		return "[user] " + truncateOneLine(summary, 60)
	case BlockAssistant:
		return "[assistant] " + truncateOneLine(b.Content, 55)
	case BlockThinking:
		return "▸ thinking " + truncateOneLine(b.Content, 55)
	case BlockToolCall:
		return "Tool: " + b.ToolName
	case BlockToolResult:
		prefix := "Result: "
		if b.IsError {
			prefix = "Error: "
		}
		return prefix + truncateOneLine(b.Content, 60)
	case BlockError:
		return "✗ " + truncateOneLine(b.Content, 65)
	case BlockBoundaryMarker:
		return "··· " + truncateOneLine(strings.TrimSpace(b.Content), 58)
	case BlockCompactionSummary:
		return "[context summary] " + truncateOneLine(b.Content, 50)
	default:
		return truncateOneLine(b.Content, 70)
	}
}

func (b *Block) searchableTextLower() string {
	if b == nil {
		return ""
	}
	if b.searchTextReady {
		return b.searchTextLower
	}
	var sb strings.Builder
	appendText := func(text string) {
		if strings.TrimSpace(text) == "" {
			return
		}
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(text)
	}
	switch b.Type {
	case BlockToolCall:
		appendText(b.ToolName)
		appendText(b.Content)
		appendText(b.ResultContent)
		appendText(b.Diff)
		appendText(b.DoneSummary)
		appendText(b.DoneReport)
		appendText(formatToolProgress(b.ToolProgress))
	case BlockCompactionSummary:
		raw := strings.TrimSpace(b.CompactionSummaryRaw)
		if raw != "" {
			appendText(raw)
		} else {
			appendText(b.Content)
		}
	default:
		appendText(b.Content)
		if b.Type == BlockUser && b.UserLocalShellCmd != "" {
			appendText(b.UserLocalShellCmd)
			appendText(b.UserLocalShellResult)
		}
		for _, image := range b.ImageParts {
			appendText(image.FileName)
		}
		for _, name := range b.PDFNames {
			appendText(name)
		}
		for _, ref := range b.FileRefs {
			appendText(ref)
		}
		for _, tp := range b.ThinkingParts {
			appendText(tp)
		}
		for _, translation := range b.ThinkingTranslations {
			appendText(translation.Content)
		}
	}
	b.searchTextLower = strings.ToLower(sb.String())
	b.searchTextReady = true
	return b.searchTextLower
}
