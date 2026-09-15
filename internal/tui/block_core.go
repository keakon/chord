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
	clone.Audit = src.Audit.Clone()
	clone.codeHL = nil
	clone.previewHL = nil
	clone.patchPreviewLen = 0
	clone.patchPreviewText = ""
	clone.clearApplyPatchPreviewMemo()
	clone.richMarkdownHL = nil
	clone.compactionSectionHL = nil
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
	clone.mdCacheSyntheticPrefixWidths = nil
	clone.mdCacheSoftWrapContinuations = nil
	// The deferred clone is the archive source of record, not a render
	// source: hydrated blocks rebuild their render caches on first paint, so
	// deep-copying the streaming/render line caches here would multiply the
	// per-event clone cost and keep every synced block's rendered lines hot
	// outside the viewport's spill budget.
	clone.streamSettledLines = nil
	clone.streamSettledSyntheticPrefixWidths = nil
	clone.streamSettledSoftWrapContinuations = nil
	clone.streamSettledLineCount = 0
	clone.streamTailRaw = src.streamTailRaw
	clone.streamTailWidth = 0
	clone.streamTailLines = nil
	clone.streamTailSyntheticPrefixWidths = nil
	clone.streamTailSoftWrapContinuations = nil
	clone.streamCardHeadLines = nil
	clone.streamCardHeadBody = nil
	clone.streamCardHeadKey = streamCardHeadKey{}
	clone.lineCache = nil
	clone.lineCacheWidth = 0
	clone.lineCountCache = 0
	clone.viewportCache = nil
	clone.renderSyntheticPrefixWidths = nil
	clone.renderSoftWrapContinuations = nil
	clone.spillRef = nil
	clone.spillStore = nil
	clone.spillSummary = ""
	clone.spillLineCounts = cloneLineCounts(src.spillLineCounts)
	clone.spillCold = false
	clone.spillIdentityKey = ""
	clone.lastAccess = 0
	clone.spillRecover = nil
	return &clone
}

// copyMutableBlockViewState copies the per-card view state a rebuilt or
// spill-restored block must keep from the live block it replaces: the fold and
// toggle state plus the timing the status rows render. New per-card view state
// belongs in this list, or a rebuilt card silently loses it.
func copyMutableBlockViewState(dst, src *Block) {
	dst.Collapsed = src.Collapsed
	dst.ToolCallDetailExpanded = src.ToolCallDetailExpanded
	dst.ThinkingCollapsed = src.ThinkingCollapsed
	dst.Streaming = src.Streaming
	dst.UserLocalShellPending = src.UserLocalShellPending
	dst.UserLocalShellFailed = src.UserLocalShellFailed
	dst.StartedAt = src.StartedAt
	dst.SettledAt = src.SettledAt
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

func (b *Block) toolArgumentsAreReceiving() bool {
	return b != nil && !b.ResultDone && b.ToolExecutionState == agent.ToolCallExecutionStateReceiving
}

func (b *Block) toolExecutionIsQueued() bool {
	return b != nil && !b.ResultDone && b.ToolExecutionState == agent.ToolCallExecutionStateQueued
}

func (b *Block) toolElapsed() time.Duration {
	if b == nil {
		return 0
	}
	if b.ResultDone && b.PersistedDuration > 0 {
		return b.PersistedDuration
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
	return 0
}

// toolElapsedLabelMin is the shortest total a finished card bothers to print.
// Sub-second calls are the common case and a header slot that reads "0s" or
// "1s" says nothing about the work, so the card stays quiet and the status bar
// carries the live time while the call runs.
const toolElapsedLabelMin = time.Second

func (b *Block) toolElapsedLabel() string {
	elapsed := b.toolElapsed()
	if elapsed < toolElapsedLabelMin {
		return ""
	}
	return tools.FormatElapsed(elapsed)
}

// IsUserLocalShell reports a merged USER + local !shell block.
func (b *Block) IsUserLocalShell() bool {
	return b != nil && b.Type == BlockUser && (b.UserLocalShell || b.UserLocalShellCmd != "")
}

func blockLabelWithID(label string, id int) string {
	if id < 0 {
		return label
	}
	return fmt.Sprintf("%s #%d", label, id+1)
}

func (b *Block) displayLabelID() int {
	if b == nil {
		return -1
	}
	if b.DisplaySequence > 0 {
		return b.DisplaySequence - 1
	}
	return b.ID
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
	if b.streamingApplyPatchRangeEligible() {
		if width <= 0 {
			width = 80
		}
		count := b.streamingApplyPatchLineCount(width)
		b.lineCache = nil
		b.lineCacheWidth = width
		b.lineCountCache = count
		b.hotBytesMemoValid = false
		return count
	}
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
	if b.streamingApplyPatchRangeEligible() {
		if width <= 0 {
			width = 80
		}
		return b.renderStreamingApplyPatchRange(width, spinnerFrame, start, end)
	}
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

// userLocalShellToggleable reports whether a `!shell` user card has something
// to fold: the disclosure marker is only rendered when the card can actually
// toggle, so the marker and the toggle gate must agree.
func userLocalShellToggleable(b *Block) bool {
	return b.UserLocalShellCmd != "" && !b.UserLocalShellPending && strings.TrimSpace(b.UserLocalShellResult) != ""
}

func (b *Block) ToggleAtWidth(width int) bool {
	switch b.Type {
	case BlockUser:
		if userLocalShellToggleable(b) {
			b.Collapsed = !b.Collapsed
			b.InvalidateCache()
			return true
		}
	case BlockToolCall, BlockToolResult:
		// The always-expanded cards keep their full content visible; a
		// disclosure hint would only offer a state they cannot have.
		if toolCardAlwaysExpanded(b.ToolName) {
			return false
		}
		// The read call card's disclosure depends on the body it will render;
		// refuse the toggle when there is nothing to expand so the card never
		// flips state with no visible change and no marker. BlockToolResult
		// keeps the permissive toggle: its header shape changes between the
		// compact and expanded forms, and only the short restore-path cards
		// can flip without a disclosure marker. width <= 0 is a programmatic
		// toggle (no layout width); keep it permissive.
		if b.Type == BlockToolCall && b.ToolName == tools.NameRead && width > 0 && !b.readCardHasDisclosure(newWideHeaderToolCardMetrics(width).contentWidth) {
			return false
		}
		if b.Type == BlockToolCall && toolUsesCompactDetailToggle(b.ToolName) {
			if (b.ToolName == tools.NameGrep || b.ToolName == tools.NameGlob) && !b.searchResultCanExpand() {
				return false
			}
			// A force-expanded card renders the same body either way, so the
			// toggle would flip state with no visible change and no marker —
			// refuse it in both directions, not only while it is expanded.
			if width > 0 && b.compactToolResultForceExpandedForRenderWidth(width) {
				return false
			}
			b.ToolCallDetailExpanded = !b.ToolCallDetailExpanded
			b.InvalidateCache()
			return true
		}
		b.Collapsed = !b.Collapsed
		b.InvalidateCache()
		return true
	case BlockAssistant:
		if len(b.ThinkingParts) > 0 {
			b.ThinkingCollapsed = !b.ThinkingCollapsed
			b.InvalidateCache()
			return true
		}
	case BlockStatus:
		// JOB RESULT cards fold to each job's headline and always accept the
		// toggle: the disclosure marker sits on the headline that survives the
		// toggle, so the marker itself changes even for a job whose collapsed
		// body already showed everything, and the toggle is never a silent
		// no-op. Every other status card folds to its badge alone only when the
		// collapsed form hides something: a mailbox card carries the worker
		// model's own message and stays fully visible, and a body that renders
		// to a single line is already its own summary.
		if b.isBackgroundResultCard() {
			b.Collapsed = !b.Collapsed
			b.InvalidateCache()
			return true
		}
		if !b.statusCardBodyFoldable(b.statusCardBodyLines(width)) {
			return false
		}
		b.Collapsed = !b.Collapsed
		b.InvalidateCache()
		return true
	case BlockCompactionSummary:
		// Compaction summary cards are always fully expanded: the archived
		// context (and any storage facts) must stay visible, matching the
		// non-collapsible treatment of Delete cards. Toggle is a no-op.
	}
	return false
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
	b.streamCardHeadBody = nil
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
	b.streamCardHeadBody = nil
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
		name := sanitizeToolDisplayText(b.ToolName)
		if primary := b.toolCallSummaryMainPart(); primary != "" {
			return "Tool: " + name + " " + truncateOneLine(primary, 46)
		}
		return "Tool: " + name
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
