package tui

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/keakon/chord/internal/thinkingtranslate"
	"github.com/keakon/chord/internal/tui/markdownutil"
)

// thinkingGluedBoldBreakRE matches a sentence or word glued directly to a new
// markdown bold section (e.g. "too.**Planning**") so we can insert a paragraph
// break before "**". The rune after "**" must look like a section header start
// (uppercase, digit, or CJK) to avoid splitting inline bold like "word**bold**".
var thinkingGluedBoldBreakRE = regexp.MustCompile(
	`([a-zA-Z0-9.!?)}\]'"。．！？）』」])(\*\*)(\p{Lu}|[0-9]|[\x{4e00}-\x{9fff}])`,
)

// preprocessThinkingMarkdown inserts blank lines before "**" headers that were
// concatenated to the previous token without whitespace. Glamour then renders
// them as separate blocks instead of one inline-wrapped paragraph.
func preprocessThinkingMarkdown(s string) string {
	if s == "" {
		return s
	}
	return thinkingGluedBoldBreakRE.ReplaceAllString(s, "$1\n\n$2$3")
}

// styleRenderedThinkingLines applies title styling to the first line of each
// markdown paragraph (segments separated by blank lines) so multiple
// "**Section**" blocks in one thinking round each read as a heading.
func styleRenderedThinkingLines(mdLines []string) []string {
	var raw []string
	paraStart := true
	for _, line := range mdLines {
		if strings.TrimSpace(line) == "" {
			raw = append(raw, "")
			paraStart = true
			continue
		}
		style := ThinkingContentStyle
		if paraStart {
			style = ThinkingTitleStyle
			paraStart = false
		}
		line = preserveStyleAfterResets(line, style)
		raw = append(raw, "  "+style.Render(line))
	}
	return raw
}

func normalizeRenderedMarkdownIndent(lines []string) []string {
	minIndent := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		n := countLeadingWhitespace(line)
		if minIndent < 0 || n < minIndent {
			minIndent = n
		}
	}
	// Only normalize small, renderer-introduced padding. Avoid stripping
	// meaningful indentation like code blocks (commonly 4 spaces).
	if minIndent <= 0 || minIndent > 2 {
		return lines
	}
	for i := range lines {
		lines[i] = trimLeadingWhitespace(lines[i], minIndent)
	}
	return lines
}

func countLeadingWhitespace(s string) int {
	// Skip rail prefix characters if present (conversation rail uses │)
	for {
		if strings.HasPrefix(s, "|") {
			s = s[1:]
		} else if strings.HasPrefix(s, "│") {
			s = s[len("│"):]
		} else if strings.HasPrefix(s, "▏") {
			s = s[len("▏"):]
		} else {
			break
		}
	}
	// Skip ANSI reset after rail
	if strings.HasPrefix(s, "\x1b[0m") {
		s = s[4:]
	} else if strings.HasPrefix(s, "\x1b[m") {
		s = s[3:]
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' {
			n++
			continue
		}
		break
	}
	return n
}

func trimLeadingWhitespace(s string, n int) string {
	if n <= 0 {
		return s
	}
	cut := 0
	for cut < len(s) && n > 0 {
		if s[cut] == ' ' || s[cut] == '\t' {
			cut++
			n--
			continue
		}
		break
	}
	return s[cut:]
}

func removeTrailingCursorGlyph(s string) string {
	if before, ok := strings.CutSuffix(s, " ▌"); ok {
		return before
	}
	if before, ok := strings.CutSuffix(s, "▌"); ok {
		return before
	}
	return s
}

type assistantMarkdownSegment struct {
	raw         string
	code        bool
	fenceLang   string
	fenceMarker byte
	fenceLen    int
}

type thinkingStreamSettledCache struct {
	raw       string
	frontier  int
	width     int
	lines     []string
	tailRaw   string
	tailWidth int
	tailLines []string
}

func splitAssistantMarkdownSegments(content string) []assistantMarkdownSegment {
	content = markdownutil.NormalizeNewlines(content)
	if content == "" {
		return nil
	}

	var segments []assistantMarkdownSegment
	var buf strings.Builder
	inCode := false
	currentFence := markdownutil.Fence{}
	flush := func(code bool) {
		if buf.Len() == 0 {
			return
		}
		seg := assistantMarkdownSegment{raw: buf.String(), code: code}
		if code {
			seg.fenceLang = normalizeCodeFenceLanguage(markdownutil.FirstFenceInfoField(currentFence.Info))
			seg.fenceMarker = currentFence.Delimiter
			seg.fenceLen = currentFence.Length
		}
		segments = append(segments, seg)
		buf.Reset()
	}

	for _, line := range strings.SplitAfter(content, "\n") {
		if !inCode {
			if fence, ok := markdownutil.ParseFenceLine(line); ok {
				flush(false)
				currentFence = fence
				buf.WriteString(line)
				inCode = true
				continue
			}
			buf.WriteString(line)
			continue
		}
		buf.WriteString(line)
		if markdownutil.IsFenceClose(line, currentFence) {
			flush(true)
			inCode = false
			currentFence = markdownutil.Fence{}
		}
	}
	flush(inCode)
	return segments
}

func wrapStyledLiteralLineWithContinuation(line string, width, continuationExtra int) ([]string, []int) {
	if width <= 0 {
		width = 80
	}
	if line == "" {
		return []string{""}, []int{0}
	}
	if continuationExtra <= 0 {
		return []string{line}, []int{0}
	}
	continuationWidth := width - continuationExtra
	if continuationWidth <= 0 {
		continuationWidth = width
		continuationExtra = 0
	}

	var out []string
	var synthetic []int
	remaining := line
	first := true
	for {
		limit := width
		prefix := ""
		prefixWidth := 0
		if !first {
			limit = continuationWidth
			prefix = strings.Repeat(" ", continuationExtra)
			prefixWidth = continuationExtra
		}
		if tuiStringWidth(remaining) <= limit {
			out = append(out, prefix+remaining)
			synthetic = append(synthetic, prefixWidth)
			break
		}
		head, tail := tuiWrapHeadTail(remaining, limit)
		out = append(out, prefix+head)
		synthetic = append(synthetic, prefixWidth)
		remaining = tail
		first = false
	}
	return out, synthetic
}

func addAssistantCodeWrapIndent(lines []string, width, continuationExtra int) ([]string, []int, []bool) {
	if len(lines) == 0 {
		return nil, nil, nil
	}
	out := make([]string, 0, len(lines))
	synthetic := make([]int, 0, len(lines))
	softWraps := make([]bool, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(stripANSI(line)) == "" {
			out = append(out, line)
			synthetic = append(synthetic, 0)
			softWraps = append(softWraps, false)
			continue
		}
		wrapped, wrappedSynthetic := wrapStyledLiteralLineWithContinuation(line, width, continuationExtra)
		for i, seg := range wrapped {
			out = append(out, seg)
			synthetic = append(synthetic, wrappedSynthetic[i])
			softWraps = append(softWraps, i > 0)
		}
	}
	return out, synthetic, softWraps
}

type codeFenceTheme struct {
	bg      string
	fg      string
	labelFg string
}

func assistantCodeFenceTheme() codeFenceTheme {
	return codeFenceTheme{
		bg:      currentTheme.CodeBlockBg,
		fg:      currentTheme.CodeBlockFg,
		labelFg: currentTheme.CodeBlockLabelFg,
	}
}

func dialogCodeFenceTheme() codeFenceTheme {
	bg := currentTheme.DialogCodeBlockBg
	if bg == "" {
		bg = currentTheme.CodeBlockBg
	}
	return codeFenceTheme{
		bg:      bg,
		fg:      currentTheme.CodeBlockFg,
		labelFg: currentTheme.CodeBlockLabelFg,
	}
}

func styleCodeBlockLines(lines []string, width int, theme codeFenceTheme) []string {
	if len(lines) == 0 {
		return nil
	}
	bg := theme.bg
	fg := theme.fg
	if bg == "" {
		return lines
	}
	bgStyle := lipgloss.NewStyle().Background(lipgloss.Color(bg)).Foreground(lipgloss.Color(fg))
	bgSeq := ansiSeqForColor(lipgloss.Color(bg), false)
	fgSeq := ansiSeqForColor(lipgloss.Color(fg), true)
	if bgSeq == "" {
		return lines
	}
	linePrefix := bgSeq + fgSeq
	lineSuffix := ansi.ResetStyle
	reapplyBg := func(line string) string {
		line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+bgSeq)
		line = strings.ReplaceAll(line, "\x1b[m", "\x1b[m"+bgSeq)
		return strings.ReplaceAll(line, "\x1b[49m", "\x1b[49m"+bgSeq)
	}
	out := make([]string, len(lines))
	for i, line := range lines {
		line = reapplyBg(linePrefix + line + lineSuffix)
		line = padLineToDisplayWidthWithStyle(bgStyle, line, width)
		out[i] = ensureStyledLineReset(line)
	}
	return out
}

func renderAssistantCodeFence(seg assistantMarkdownSegment, codeSample string, width, continuationExtra int, hl **codeHighlighter) ([]string, []int, []bool) {
	return renderCodeFence(seg, codeSample, width, continuationExtra, hl, assistantCodeFenceTheme())
}

func renderCodeFence(seg assistantMarkdownSegment, codeSample string, width, continuationExtra int, hl **codeHighlighter, theme codeFenceTheme) ([]string, []int, []bool) {
	const (
		codeBlockInnerPadX = 1
		codeBlockInnerPadY = 1
	)
	innerWidth := width - codeBlockInnerPadX*2
	if innerWidth < 1 {
		innerWidth = 1
	}

	code := sanitizeDisplayText(seg.raw)
	fenceDelim := seg.fenceMarker
	if fenceDelim == 0 {
		fenceDelim = '`'
	}
	fenceLen := seg.fenceLen
	if fenceLen < 3 {
		fenceLen = 3
	}
	code = strings.TrimSpace(code)
	if nl := strings.IndexByte(code, '\n'); nl >= 0 {
		code = code[nl+1:]
	} else {
		code = ""
	}
	closing := strings.Repeat(string(fenceDelim), max(3, fenceLen))
	if before, ok := strings.CutSuffix(code, closing); ok {
		code = before
	} else if fence := strings.LastIndex(code, closing); fence >= 0 {
		code = code[:fence]
	}
	code = strings.TrimSuffix(code, "\n")

	lang := normalizeCodeFenceLanguage(seg.fenceLang)
	label := strings.ToUpper(lang)
	if label == "" {
		label = "TEXT"
	}
	labelLine := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.labelFg)).Bold(true).Render(label)
	labelLine = strings.Repeat(" ", codeBlockInnerPadX) + labelLine + strings.Repeat(" ", codeBlockInnerPadX)

	var bodyLines []string
	if lang == "" || lang == "text" {
		bodyLines = strings.Split(code, "\n")
		if len(bodyLines) == 0 {
			bodyLines = []string{""}
		}
		for i := range bodyLines {
			bodyLines[i] = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.fg)).Render(bodyLines[i])
		}
	} else {
		h := ensureCodeHighlighterWithLanguage(hl, "", codeSample, lang)
		bodyLines = highlightCodeLines(h, strings.Split(code, "\n"), theme.bg)
		if len(bodyLines) == 0 {
			bodyLines = []string{""}
		}
	}
	for i := range bodyLines {
		bodyLines[i] = strings.TrimRight(bodyLines[i], " ")
	}
	bodyLines, synthetic, softWraps := addAssistantCodeWrapIndent(bodyLines, innerWidth, continuationExtra)
	for i := range bodyLines {
		bodyLines[i] = strings.Repeat(" ", codeBlockInnerPadX) + bodyLines[i] + strings.Repeat(" ", codeBlockInnerPadX)
		synthetic[i] += codeBlockInnerPadX
	}
	blankLine := strings.Repeat(" ", codeBlockInnerPadX*2)
	lines := make([]string, 0, codeBlockInnerPadY+2+len(bodyLines)+codeBlockInnerPadY)
	syntheticOut := make([]int, 0, codeBlockInnerPadY+2+len(synthetic)+codeBlockInnerPadY)
	softWrapsOut := make([]bool, 0, codeBlockInnerPadY+2+len(softWraps)+codeBlockInnerPadY)
	for range codeBlockInnerPadY {
		lines = append(lines, blankLine)
		syntheticOut = append(syntheticOut, codeBlockInnerPadX)
		softWrapsOut = append(softWrapsOut, false)
	}
	lines = append(lines, labelLine, blankLine)
	syntheticOut = append(syntheticOut, codeBlockInnerPadX, codeBlockInnerPadX)
	softWrapsOut = append(softWrapsOut, false, false)
	lines = append(lines, bodyLines...)
	syntheticOut = append(syntheticOut, synthetic...)
	softWrapsOut = append(softWrapsOut, softWraps...)
	for range codeBlockInnerPadY {
		lines = append(lines, blankLine)
		syntheticOut = append(syntheticOut, codeBlockInnerPadX)
		softWrapsOut = append(softWrapsOut, false)
	}
	lines = styleCodeBlockLines(lines, width, theme)
	return lines, syntheticOut, softWrapsOut
}

func renderRichMarkdownContent(content string, width int, hl **codeHighlighter) []string {
	return renderRichMarkdownContentWithCodeFenceTheme(content, width, hl, assistantCodeFenceTheme())
}

func renderDialogMarkdownContent(content string, width int) []string {
	return renderRichMarkdownContentWithCodeFenceTheme(content, width, nil, dialogCodeFenceTheme())
}

func renderRichMarkdownContentWithCodeFenceTheme(content string, width int, hl **codeHighlighter, theme codeFenceTheme) []string {
	var localHL *codeHighlighter
	if hl == nil {
		hl = &localHL
	}
	segments := splitAssistantMarkdownSegments(content)
	if len(segments) == 0 {
		return renderMarkdownContent(content, width)
	}

	var out []string
	appendSegment := func(segLines []string) {
		if len(segLines) == 0 {
			return
		}
		for len(out) > 0 && len(segLines) > 0 && out[len(out)-1] == "" && segLines[0] == "" {
			segLines = segLines[1:]
		}
		if len(segLines) == 0 {
			return
		}
		if len(out) > 0 && out[len(out)-1] != "" && segLines[0] != "" {
			out = append(out, "")
		}
		out = append(out, segLines...)
	}

	for _, seg := range segments {
		if seg.code {
			segLines, _, _ := renderCodeFence(seg, content, width, 0, hl, theme)
			appendSegment(segLines)
			continue
		}
		appendSegment(renderMarkdownContent(seg.raw, width))
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

func renderAssistantMarkdownContent(content, codeSample string, width, continuationExtra int, hl **codeHighlighter) ([]string, []int, []bool) {
	content = sanitizeDisplayText(content)
	codeSample = sanitizeDisplayText(codeSample)
	segments := splitAssistantMarkdownSegments(content)
	if len(segments) == 0 {
		lines := renderMarkdownContent(content, width)
		return lines, make([]int, len(lines)), make([]bool, len(lines))
	}

	var out []string
	var synthetic []int
	var softWraps []bool
	appendSegment := func(segLines []string, segSynthetic []int, segSoftWraps []bool) {
		if len(segLines) == 0 {
			return
		}
		for len(out) > 0 && len(segLines) > 0 && out[len(out)-1] == "" && segLines[0] == "" {
			segLines = segLines[1:]
			segSynthetic = segSynthetic[1:]
			segSoftWraps = segSoftWraps[1:]
		}
		if len(segLines) == 0 {
			return
		}
		if len(out) > 0 && out[len(out)-1] != "" && segLines[0] != "" {
			out = append(out, "")
			synthetic = append(synthetic, 0)
			softWraps = append(softWraps, false)
		}
		out = append(out, segLines...)
		synthetic = append(synthetic, segSynthetic...)
		softWraps = append(softWraps, segSoftWraps...)
	}

	const assistantCodeRenderWidth = 4096
	for _, seg := range segments {
		renderWidth := width
		if seg.code && assistantCodeRenderWidth > renderWidth {
			renderWidth = assistantCodeRenderWidth
		}
		var segLines []string
		var segSynthetic []int
		var segSoftWraps []bool
		if seg.code {
			segLines, segSynthetic, segSoftWraps = renderAssistantCodeFence(seg, codeSample, width, continuationExtra, hl)
		} else {
			segLines = renderMarkdownContent(seg.raw, renderWidth)
			segSynthetic = make([]int, len(segLines))
			segSoftWraps = make([]bool, len(segLines))
		}
		appendSegment(segLines, segSynthetic, segSoftWraps)
	}
	if len(out) == 0 {
		return []string{""}, []int{0}, []bool{false}
	}
	return out, synthetic, softWraps
}

func assistantMarkdownRenderWidth(content string, innerWidth int) int {
	contentWidth := innerWidth - 2
	if contentWidth < 10 {
		contentWidth = 10
	}
	limit := maxTextWidth
	if containsMarkdownTable(content) {
		limit = maxMarkdownTableWidth
	}
	if contentWidth > limit {
		contentWidth = limit
	}
	return contentWidth
}

func containsMarkdownTable(content string) bool {
	for _, seg := range splitAssistantMarkdownSegments(content) {
		if seg.code {
			continue
		}
		if containsMarkdownTableInText(seg.raw) {
			return true
		}
	}
	return false
}

func containsMarkdownTableInText(content string) bool {
	var previous string
	for _, line := range strings.Split(markdownutil.NormalizeNewlines(content), "\n") {
		if isMarkdownTableDelimiterLine(line) && isMarkdownTableHeaderLine(previous) {
			return true
		}
		previous = line
	}
	return false
}

func isMarkdownTableHeaderLine(line string) bool {
	return len(markdownTableCells(line)) >= 2
}

func isMarkdownTableDelimiterLine(line string) bool {
	cells := markdownTableCells(line)
	if len(cells) < 2 {
		return false
	}
	for _, cell := range cells {
		if cell == "" {
			return false
		}
		hyphens := 0
		for _, r := range cell {
			switch r {
			case '-':
				hyphens++
			case ':':
			default:
				return false
			}
		}
		if hyphens < 3 {
			return false
		}
	}
	return true
}

func markdownTableCells(line string) []string {
	line = strings.TrimSpace(line)
	if !strings.Contains(line, "|") {
		return nil
	}
	line = strings.Trim(line, "|")
	parts := strings.Split(line, "|")
	cells := make([]string, 0, len(parts))
	for _, part := range parts {
		cells = append(cells, strings.TrimSpace(part))
	}
	return cells
}

func (b *Block) renderAssistant(width int) []string {
	// (Note: we removed the "[assistant]" string header for a cleaner conversational look)
	style := AssistantCardStyle
	// v2: Width() sets border-box (excl margin).
	boxWidth := width - style.GetHorizontalMargins()
	if boxWidth < 10 {
		boxWidth = 10
	}
	// innerWidth is the content area for text wrapping
	innerWidth := boxWidth - style.GetHorizontalPadding() - style.GetHorizontalBorderSize()
	if innerWidth < 10 {
		innerWidth = 10
	}

	// Parse summary metadata from content (if present at the start)
	rawContent := removeTrailingCursorGlyph(b.Content)
	summary := parseAssistantSummary(rawContent)
	bodyContent := stripAssistantSummary(rawContent, summary)
	contentWidth := assistantMarkdownRenderWidth(bodyContent, innerWidth)

	var out []string

	// Thinking block (if any).
	hasThinking := false
	for _, p := range b.ThinkingParts {
		if strings.TrimSpace(removeTrailingCursorGlyph(p)) != "" {
			hasThinking = true
			break
		}
	}

	if hasThinking {
		tStyle := ThinkingCardStyle
		// Focus is indicated by the rail; the card surface stays the same.
		// renderThinkingParts with the same innerWidth
		tLines := b.renderThinkingParts(innerWidth)
		if len(tLines) > 0 {
			// Re-insert card background after inner ANSI resets.
			thinkBg := currentTheme.ThinkingCardBg
			tLines = preserveCardBg(tLines, thinkBg)
			out = append(out, renderPrewrappedCard(tStyle, innerWidth, tLines, thinkBg, railANSISeq("thinking", b.Focused))...)
			out = append(out, "") // margin between blocks
		}
	}

	// Assistant block body.
	hasContent := strings.TrimSpace(bodyContent) != "" || (summary.HasMeta && !b.Streaming)
	if !hasContent && !b.Streaming && !hasThinking {
		return nil
	}

	if hasContent || b.Streaming || !hasThinking {
		assistantContentPrefix := "  "
		continuationExtra := 2
		var contentLines []string
		var contentSynthetic []int
		var contentSoftWraps []bool
		if b.mdCache == nil || b.mdCacheWidth != width || (!b.Streaming && (b.mdCacheContent != bodyContent || b.mdCacheThemeVersion != appliedThemeVersion)) {
			var settledLines []string
			var settledSynthetic []int
			var settledSoftWraps []bool
			if b.Streaming {
				// Incremental markdown rendering for streaming content:
				// split into a stable prefix (settled, rendered via glamour once per
				// frontier advance) and an unstable tail (cheap wrapText path).
				rawContent := removeTrailingCursorGlyph(b.Content)
				frontier := markdownutil.FindStreamingSettledFrontier(rawContent)

				if frontier > 0 {
					settledRaw := rawContent[:frontier]
					// Rebuild settled cache only when frontier advances, width changes,
					// or the stable prefix text itself changed.
					if frontier != b.streamSettledFrontier || b.streamSettledWidth != contentWidth || settledRaw != b.streamSettledRaw {
						sL, sS, sW := renderAssistantMarkdownContent(settledRaw, settledRaw, contentWidth, continuationExtra, &b.codeHL)
						b.streamSettledRaw = settledRaw
						b.streamSettledLines = sL
						b.streamSettledSyntheticPrefixWidths = sS
						b.streamSettledSoftWrapContinuations = sW
						b.streamSettledFrontier = frontier
						b.streamSettledWidth = contentWidth
					}
					settledLines = b.streamSettledLines
					settledSynthetic = b.streamSettledSyntheticPrefixWidths
					settledSoftWraps = b.streamSettledSoftWrapContinuations
				} else {
					b.streamSettledRaw = ""
					b.streamSettledFrontier = 0
					b.streamSettledWidth = 0
					b.streamSettledLines = nil
					b.streamSettledSyntheticPrefixWidths = nil
					b.streamSettledSoftWrapContinuations = nil
				}

				// tail: content after the settled frontier, always cheap path.
				tailRaw := rawContent[frontier:]
				if tailRaw != "" {
					if b.streamTailRaw != tailRaw || b.streamTailWidth != contentWidth {
						b.streamTailLines = wrapText(tailRaw, contentWidth)
						b.streamTailSyntheticPrefixWidths = make([]int, len(b.streamTailLines))
						b.streamTailSoftWrapContinuations = make([]bool, len(b.streamTailLines))
						b.streamTailRaw = tailRaw
						b.streamTailWidth = contentWidth
					}
				} else {
					b.streamTailRaw = ""
					b.streamTailWidth = 0
					b.streamTailLines = nil
					b.streamTailSyntheticPrefixWidths = nil
					b.streamTailSoftWrapContinuations = nil
				}

				b.mdCache = settledLines
				b.mdCacheSyntheticPrefixWidths = settledSynthetic
				b.mdCacheSoftWrapContinuations = settledSoftWraps
				b.streamSettledLineCount = len(settledLines)
			} else {
				b.InvalidateStreamingSettledCache()
				b.mdCache, b.mdCacheSyntheticPrefixWidths, b.mdCacheSoftWrapContinuations = renderAssistantMarkdownContent(bodyContent, bodyContent, contentWidth, continuationExtra, &b.codeHL)
				b.mdCacheContent = bodyContent
				b.mdCacheThemeVersion = appliedThemeVersion
			}
			b.mdCacheWidth = width
		}
		contentLines = b.mdCache
		contentSynthetic = b.mdCacheSyntheticPrefixWidths
		contentSoftWraps = b.mdCacheSoftWrapContinuations

		var assistantLines []string
		var assistantSynthetic []int
		var assistantSoftWraps []bool
		assistantLines = append(assistantLines, AssistantLabelStyle.Render(blockLabelWithID("ASSISTANT", b.ID)))
		assistantSynthetic = append(assistantSynthetic, 0)
		assistantSoftWraps = append(assistantSoftWraps, false)

		// Render weakened meta section under the label (if summary exists and not streaming)
		if summary.HasMeta && !b.Streaming {
			metaLines := renderAssistantSummaryLines(summary, innerWidth-4)
			for _, ml := range metaLines {
				assistantLines = append(assistantLines, "  "+ml)
				assistantSynthetic = append(assistantSynthetic, 2)
				assistantSoftWraps = append(assistantSoftWraps, false)
			}
			if len(metaLines) > 0 {
				assistantLines = append(assistantLines, "") // gap after meta
				assistantSynthetic = append(assistantSynthetic, 0)
				assistantSoftWraps = append(assistantSoftWraps, false)
			}
		} else {
			assistantLines = append(assistantLines, "") // gap
			assistantSynthetic = append(assistantSynthetic, 0)
			assistantSoftWraps = append(assistantSoftWraps, false)
		}

		styleStreamingTail := styleHasTextAttributes(MessageContentStyle)
		appendAssistantSegment := func(lines []string, synthetic []int, softWraps []bool, styleTail bool) {
			for i, cl := range lines {
				line := cl
				if styleTail && styleStreamingTail {
					line = MessageContentStyle.Render(cl)
				}
				assistantLines = append(assistantLines, assistantContentPrefix+line)
				syntheticWidth := 0
				if i < len(synthetic) {
					syntheticWidth = synthetic[i]
				}
				assistantSynthetic = append(assistantSynthetic, syntheticWidth)
				softWrap := false
				if i < len(softWraps) {
					softWrap = softWraps[i]
				}
				assistantSoftWraps = append(assistantSoftWraps, softWrap)
			}
		}
		if b.Streaming {
			appendAssistantSegment(b.streamSettledLines, b.streamSettledSyntheticPrefixWidths, b.streamSettledSoftWrapContinuations, false)
			appendAssistantSegment(b.streamTailLines, b.streamTailSyntheticPrefixWidths, b.streamTailSoftWrapContinuations, true)
		} else {
			for i, cl := range contentLines {
				line := cl
				assistantLines = append(assistantLines, assistantContentPrefix+line)
				synthetic := 0
				if i < len(contentSynthetic) {
					synthetic = contentSynthetic[i]
				}
				assistantSynthetic = append(assistantSynthetic, synthetic)
				softWrap := false
				if i < len(contentSoftWraps) {
					softWrap = contentSoftWraps[i]
				}
				assistantSoftWraps = append(assistantSoftWraps, softWrap)
			}
		}

		// Re-insert card background after inner ANSI resets.
		assBg := currentTheme.AssistantCardBg
		assistantLines = preserveCardBg(assistantLines, assBg)
		cardLines := renderPrewrappedCard(style, innerWidth, assistantLines, assBg, railANSISeq("assistant", b.Focused))
		out = append(out, cardLines...)

		leftInset := style.GetMarginLeft() + style.GetPaddingLeft() + ansi.StringWidth(assistantContentPrefix)
		b.renderSyntheticPrefixWidths = make([]int, 0, len(out))
		b.renderSoftWrapContinuations = make([]bool, 0, len(out))
		for range len(out) - len(assistantLines) {
			b.renderSyntheticPrefixWidths = append(b.renderSyntheticPrefixWidths, 0)
			b.renderSoftWrapContinuations = append(b.renderSoftWrapContinuations, false)
		}
		for i := range assistantLines {
			prefixWidth := leftInset
			if i < len(assistantSynthetic) {
				prefixWidth += assistantSynthetic[i]
			}
			b.renderSyntheticPrefixWidths = append(b.renderSyntheticPrefixWidths, prefixWidth)
			softWrap := false
			if i < len(assistantSoftWraps) {
				softWrap = assistantSoftWraps[i]
			}
			b.renderSoftWrapContinuations = append(b.renderSoftWrapContinuations, softWrap)
		}
		b.renderSyntheticPrefixWidthsW = width
	}

	return out
}

// renderThinkingParts renders thinking sections with indentation and ThinkingContentStyle (gray) only
// so they are visually distinct from the main reply. Full line (including indent) is styled so color is consistent.
// When ThinkingCollapsed is true, only the last maxCollapsedThinkingLines are shown.
func (b *Block) renderThinkingMarkdownPart(part string, partIndex, contentWidth int) ([]string, int) {
	part = removeTrailingCursorGlyph(part)
	part = preprocessThinkingMarkdown(part)
	if !b.Streaming {
		return renderMarkdownContent(part, contentWidth), 0
	}

	frontier := markdownutil.FindStreamingSettledFrontier(part)
	for len(b.thinkingStreamSettled) <= partIndex {
		b.thinkingStreamSettled = append(b.thinkingStreamSettled, thinkingStreamSettledCache{})
	}
	cache := &b.thinkingStreamSettled[partIndex]

	var out []string
	settledLineCount := 0
	if frontier > 0 {
		settledRaw := part[:frontier]
		if cache.frontier != frontier || cache.width != contentWidth || cache.raw != settledRaw {
			cache.raw = settledRaw
			cache.frontier = frontier
			cache.width = contentWidth
			cache.lines = renderMarkdownContent(settledRaw, contentWidth)
		}
		out = append(out, cache.lines...)
		settledLineCount = len(out)
	} else if partIndex < len(b.thinkingStreamSettled) {
		b.thinkingStreamSettled[partIndex] = thinkingStreamSettledCache{}
		cache = &b.thinkingStreamSettled[partIndex]
	}

	if tail := part[frontier:]; tail != "" {
		if cache.tailRaw == tail && cache.tailWidth == contentWidth {
			out = append(out, cache.tailLines...)
			settledLineCount = len(out) - len(cache.tailLines)
		} else {
			tailLines := wrapText(tail, contentWidth)
			out = append(out, tailLines...)
			cache.tailRaw = tail
			cache.tailWidth = contentWidth
			cache.tailLines = tailLines
			settledLineCount = len(out) - len(tailLines)
		}
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out, settledLineCount
}

func styleStreamingThinkingLines(mdLines []string, settledLineCount int) []string {
	var raw []string
	paraStart := true
	for i, line := range mdLines {
		if strings.TrimSpace(line) == "" {
			raw = append(raw, "")
			paraStart = true
			continue
		}
		if i >= settledLineCount {
			raw = append(raw, "  "+ThinkingContentStyle.Render(line))
			paraStart = false
			continue
		}
		style := ThinkingContentStyle
		if paraStart {
			style = ThinkingTitleStyle
			paraStart = false
		}
		line = preserveStyleAfterResets(line, style)
		raw = append(raw, "  "+style.Render(line))
	}
	return raw
}

func renderThinkingTranslationHeader(targetLang string, width int) string {
	targetLang = strings.TrimSpace(targetLang)
	label := "Translated"
	if targetLang != "" {
		label += " · " + targetLang
	}
	if width < 8 {
		return ThinkingTranslationStyle.Render(label)
	}
	rawLabelWidth := ansi.StringWidth(label)
	if rawLabelWidth+2 >= width {
		return ThinkingTranslationStyle.Render(label)
	}
	ruleWidth := width - rawLabelWidth - 2
	left := ruleWidth / 2
	right := ruleWidth - left
	if left == 0 || right == 0 {
		return ThinkingTranslationStyle.Render(label)
	}
	rule := ThinkingTranslationRuleStyle.Render(strings.Repeat(SectionSeparator, left))
	trail := ThinkingTranslationRuleStyle.Render(strings.Repeat(SectionSeparator, right))
	return rule + " " + ThinkingTranslationStyle.Render(label) + " " + trail
}

func renderThinkingTranslationLines(translation ThinkingTranslationView, contentWidth int) []string {
	translated := strings.TrimSpace(thinkingtranslate.ExtractTranslationEnvelope(translation.Content))
	if translated == "" {
		return nil
	}
	lines := []string{"", renderThinkingTranslationHeader(translation.TargetLang, contentWidth), ""}
	for _, line := range renderMarkdownContent(preprocessThinkingMarkdown(removeTrailingCursorGlyph(translated)), contentWidth) {
		lines = append(lines, "  "+preserveStyleAfterResets(line, ThinkingContentStyle))
	}
	return lines
}

func renderThinkingTranslationSections(translations []ThinkingTranslationView, contentWidth int) []string {
	if len(translations) == 0 {
		return nil
	}
	var out []string
	for _, translation := range translations {
		out = append(out, renderThinkingTranslationLines(translation, contentWidth)...)
	}
	return out
}

func (b *Block) renderThinkingParts(innerWidth int) []string {
	if len(b.ThinkingParts) == 0 {
		return nil
	}
	contentWidth := innerWidth - 2
	if contentWidth < 10 {
		contentWidth = 10
	}
	if contentWidth > maxTextWidth {
		contentWidth = maxTextWidth
	}
	// Build a single slice: header, gap, then for each part both title and content lines.
	// Previously rawLines (titles) and tempLines (content) were merged by rawLines = tempLines,
	// which dropped the title lines; now we append everything to one slice.
	var rawLines []string
	if len(b.thinkingStreamSettled) > len(b.ThinkingParts) {
		b.thinkingStreamSettled = b.thinkingStreamSettled[:len(b.ThinkingParts)]
	}
	for i, part := range b.ThinkingParts {
		if i == 0 {
			rawLines = append(rawLines, ThinkingLabelStyle.Render(blockLabelWithID("THINKING", b.ID)))
			rawLines = append(rawLines, "") // gap
		} else if i > 0 {
			rawLines = append(rawLines, "") // small gap between distinct thinking segments
		}
		mdLines, settledLineCount := b.renderThinkingMarkdownPart(part, i, contentWidth)
		if b.Streaming {
			rawLines = append(rawLines, styleStreamingThinkingLines(mdLines, settledLineCount)...)
		} else {
			rawLines = append(rawLines, styleRenderedThinkingLines(mdLines)...)
			if i < len(b.ThinkingTranslations) {
				rawLines = append(rawLines, renderThinkingTranslationLines(b.ThinkingTranslations[i], contentWidth)...)
			}
		}
	}

	// Thinking is always shown in full (no collapse) so the THINKING label and all content are visible.

	// Thinking duration footer: show on its own line so it's distinct from thinking content.
	if b.ThinkingDuration >= time.Second && !b.Streaming {
		rawLines = append(rawLines, "") // blank line separator
		footer := ThinkingContentStyle.Render(fmt.Sprintf("⏱ %s", b.ThinkingDuration.Round(time.Second)))
		rawLines = append(rawLines, "  "+footer)
	}

	// Padding to innerWidth is done in renderAssistant with card bg style for uniform alignment.
	return rawLines
}

func (b *Block) renderThinking(width int) []string {
	style := ThinkingCardStyle
	// v2: Width() sets border-box (excl margin).
	boxWidth := width - style.GetHorizontalMargins()
	if boxWidth < 10 {
		boxWidth = 10
	}
	innerWidth := boxWidth - style.GetHorizontalPadding() - style.GetHorizontalBorderSize()
	if innerWidth < 10 {
		innerWidth = 10
	}
	contentWidth := innerWidth - 2
	if contentWidth < 10 {
		contentWidth = 10
	}
	if contentWidth > maxTextWidth {
		contentWidth = maxTextWidth
	}
	content := removeTrailingCursorGlyph(b.Content)
	content = preprocessThinkingMarkdown(content)
	if strings.TrimSpace(content) == "" && !b.Streaming {
		return nil
	}
	// Use transparent renderer for thinking as well
	mdLines, settledLineCount := b.renderThinkingMarkdownPart(content, 0, contentWidth)

	var rawLines []string
	rawLines = append(rawLines, ThinkingLabelStyle.Render(blockLabelWithID("THINKING", b.ID)))
	rawLines = append(rawLines, "") // internal gap

	if b.Streaming {
		rawLines = append(rawLines, styleStreamingThinkingLines(mdLines, settledLineCount)...)
	} else {
		rawLines = append(rawLines, styleRenderedThinkingLines(mdLines)...)
		rawLines = append(rawLines, renderThinkingTranslationSections(b.ThinkingTranslations, contentWidth)...)
	}

	// Thinking duration footer
	if b.ThinkingDuration >= time.Second && !b.Streaming {
		rawLines = append(rawLines, "")
		footer := ThinkingContentStyle.Render(fmt.Sprintf("⏱ %s", b.ThinkingDuration.Round(time.Second)))
		rawLines = append(rawLines, "  "+footer)
	}

	// Re-insert card background after inner ANSI resets.
	thinkBg2 := currentTheme.ThinkingCardBg
	rawLines = preserveCardBg(rawLines, thinkBg2)
	return renderPrewrappedCard(style, innerWidth, rawLines, thinkBg2, railANSISeq("thinking", b.Focused))
}
