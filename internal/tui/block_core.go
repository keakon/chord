package tui

import (
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
	clone.diffHL = nil
	clone.richMarkdownHL = nil
	clone.thinkingStreamSettled = nil
	clone.toolArgsCacheKeys = append([]string(nil), src.toolArgsCacheKeys...)
	if len(src.toolArgsCacheVals) > 0 {
		clone.toolArgsCacheVals = make(map[string]string, len(src.toolArgsCacheVals))
		for k, v := range src.toolArgsCacheVals {
			clone.toolArgsCacheVals[k] = v
		}
	}
	clone.toolHeaderCacheParamLines = append([]string(nil), src.toolHeaderCacheParamLines...)
	clone.displayWorkingDir = src.displayWorkingDir
	if src.ToolProgress != nil {
		progress := *src.ToolProgress
		clone.ToolProgress = &progress
	}
	clone.FileRefs = append([]string(nil), src.FileRefs...)
	clone.ImageParts = append([]BlockImagePart(nil), src.ImageParts...)
	clone.ThinkingParts = append([]string(nil), src.ThinkingParts...)
	clone.mdCache = append([]string(nil), src.mdCache...)
	clone.mdCacheSyntheticPrefixWidths = append([]int(nil), src.mdCacheSyntheticPrefixWidths...)
	clone.mdCacheSoftWrapContinuations = append([]bool(nil), src.mdCacheSoftWrapContinuations...)
	clone.streamSettledLines = append([]string(nil), src.streamSettledLines...)
	clone.streamSettledSyntheticPrefixWidths = append([]int(nil), src.streamSettledSyntheticPrefixWidths...)
	clone.streamSettledSoftWrapContinuations = append([]bool(nil), src.streamSettledSoftWrapContinuations...)
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
	return b.ToolExecutionState == "" || b.ToolExecutionState == agent.ToolCallExecutionStateRunning
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

// Render produces the styled lines for this block, word-wrapped to width.
func (b *Block) Render(width int, spinnerFrame string) []string {
	_ = b.ensureMaterialized()
	if width <= 0 {
		width = 80
	}
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

// LineCount returns how many terminal lines this block occupies.
func (b *Block) LineCount(width int) int {
	_ = b.ensureMaterialized()
	if b.lineCache == nil || b.lineCacheWidth != width {
		b.lineCache = b.Render(width, "")
		b.lineCacheWidth = width
		b.lineCountCache = len(b.lineCache)
	}
	return b.lineCountCache
}

// MeasureLineCount returns the block's rendered line count without populating
// lineCache. This is useful on hot paths where the caller only needs the span
// (e.g. deferred tail-block height recompute during streaming) and the block
// will be rendered again immediately afterward for viewport output.
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
		if b.Type == BlockToolCall && (b.ToolName == tools.NameWrite || b.ToolName == tools.NameEdit) {
			if b.Collapsed {
				b.Collapsed = false
				b.InvalidateCache()
			}
			return
		}
		if b.Type == BlockToolCall && b.ToolName == tools.NameRead {
			if b.Collapsed {
				b.Collapsed = false
				b.InvalidateCache()
				return
			}
			lines := strings.Split(b.ResultContent, "\n")
			if len(lines) <= maxReadDefaultLines {
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
	b.mdCache = nil
	b.mdCacheWidth = 0
	b.mdCacheSyntheticPrefixWidths = nil
	b.mdCacheSoftWrapContinuations = nil
	b.streamSettledLineCount = 0
	b.viewportCache = nil
	b.viewportCacheWidth = 0
	b.renderSyntheticPrefixWidths = nil
	b.renderSoftWrapContinuations = nil
	b.renderSyntheticPrefixWidthsW = 0
	b.searchTextLower = ""
	b.searchTextReady = false
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
	b.streamSettledLineCount = 0
}

// InvalidateThinkingStreamingSettledCache clears cached rendered markdown for
// in-flight thinking parts. Call this when thinking content is replaced
// non-monotonically or part ordering changes.
func (b *Block) InvalidateThinkingStreamingSettledCache() {
	b.thinkingStreamSettled = nil
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
	switch b.Type {
	case BlockToolCall:
		sb.WriteString(b.ToolName)
		if b.Content != "" {
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(b.Content)
		}
		if b.ResultContent != "" {
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(b.ResultContent)
		}
	case BlockCompactionSummary:
		raw := strings.TrimSpace(b.CompactionSummaryRaw)
		if raw != "" {
			sb.WriteString(raw)
		} else {
			sb.WriteString(b.Content)
		}
	default:
		sb.WriteString(b.Content)
		for _, img := range b.ImageParts {
			if strings.TrimSpace(img.FileName) == "" {
				continue
			}
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(img.FileName)
		}
		for _, tp := range b.ThinkingParts {
			if strings.TrimSpace(tp) == "" {
				continue
			}
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(tp)
		}
	}
	b.searchTextLower = strings.ToLower(sb.String())
	b.searchTextReady = true
	return b.searchTextLower
}
