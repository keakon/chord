package tui

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

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
	if strings.HasSuffix(s, " ▌") {
		return strings.TrimSuffix(s, " ▌")
	}
	if strings.HasSuffix(s, "▌") {
		return strings.TrimSuffix(s, "▌")
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
		if ansi.StringWidth(remaining) <= limit {
			out = append(out, prefix+remaining)
			synthetic = append(synthetic, prefixWidth)
			break
		}
		out = append(out, prefix+ansi.Cut(remaining, 0, limit))
		synthetic = append(synthetic, prefixWidth)
		remaining = ansi.Cut(remaining, limit, ansi.StringWidth(remaining))
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

func styleAssistantCodeBlockLines(lines []string, width int) []string {
	if len(lines) == 0 {
		return nil
	}
	bg := currentTheme.CodeBlockBg
	fg := currentTheme.CodeBlockFg
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
	const (
		codeBlockInnerPadX = 1
		codeBlockInnerPadY = 1
	)
	innerWidth := width - codeBlockInnerPadX*2
	if innerWidth < 1 {
		innerWidth = 1
	}

	code := seg.raw
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
	if strings.HasSuffix(code, closing) {
		code = strings.TrimSuffix(code, closing)
	} else if fence := strings.LastIndex(code, closing); fence >= 0 {
		code = code[:fence]
	}
	code = strings.TrimSuffix(code, "\n")

	lang := normalizeCodeFenceLanguage(seg.fenceLang)
	label := strings.ToUpper(lang)
	if label == "" {
		label = "TEXT"
	}
	labelLine := lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.CodeBlockLabelFg)).Bold(true).Render(label)
	labelLine = strings.Repeat(" ", codeBlockInnerPadX) + labelLine + strings.Repeat(" ", codeBlockInnerPadX)

	var bodyLines []string
	if lang == "" || lang == "text" {
		bodyLines = strings.Split(code, "\n")
		if len(bodyLines) == 0 {
			bodyLines = []string{""}
		}
		for i := range bodyLines {
			bodyLines[i] = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.CodeBlockFg)).Render(bodyLines[i])
		}
	} else {
		h := ensureCodeHighlighterWithLanguage(hl, "", codeSample, lang)
		bodyLines = highlightCodeLines(h, strings.Split(code, "\n"), currentTheme.CodeBlockBg)
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
	lines = styleAssistantCodeBlockLines(lines, width)
	return lines, syntheticOut, softWrapsOut
}

func renderCompactionSummaryMarkdown(content string, width int, hl **codeHighlighter) []string {
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
			segLines, _, _ := renderAssistantCodeFence(seg, content, width, 0, hl)
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

	contentWidth := innerWidth - 2
	if contentWidth < 10 {
		contentWidth = 10
	}
	if contentWidth > maxTextWidth {
		contentWidth = maxTextWidth
	}

	// Parse summary metadata from content (if present at the start)
	rawContent := removeTrailingCursorGlyph(b.Content)
	summary := parseAssistantSummary(rawContent)
	bodyContent := stripAssistantSummary(rawContent, summary)

	var out []string

	// 1. Thinking block (if any)
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
			// Re-insert card bg after inner ANSI resets (§1.25).
			thinkBg := currentTheme.ThinkingCardBg
			tLines = preserveCardBg(tLines, thinkBg)
			out = append(out, renderPrewrappedCard(tStyle, innerWidth, tLines, thinkBg, railANSISeq("thinking", b.Focused))...)
			out = append(out, "") // margin between blocks
		}
	}

	// 2. Assistant block body
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
		if b.mdCache == nil || b.mdCacheWidth != width {
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
						sL, sS, sW := renderAssistantMarkdownContent(settledRaw, settledRaw, contentWidth, continuationExtra, &b.diffHL)
						b.streamSettledRaw = settledRaw
						b.streamSettledLines = sL
						b.streamSettledSyntheticPrefixWidths = sS
						b.streamSettledSoftWrapContinuations = sW
						b.streamSettledFrontier = frontier
						b.streamSettledWidth = contentWidth
					}
				} else {
					b.InvalidateStreamingSettledCache()
				}

				// tail: content after the settled frontier, always cheap path.
				tailRaw := rawContent[frontier:]
				var tailLines []string
				var tailSynthetic []int
				var tailSoftWraps []bool
				if tailRaw != "" {
					tailLines = wrapText(tailRaw, contentWidth)
					tailSynthetic = make([]int, len(tailLines))
					tailSoftWraps = make([]bool, len(tailLines))
				}

				if frontier > 0 {
					// Merge settled (markdown-rendered) + tail (cheap path) without
					// collapsing seam blank lines: they can represent real paragraph
					// boundaries from the original markdown content.
					mergedLines := make([]string, len(b.streamSettledLines))
					copy(mergedLines, b.streamSettledLines)
					mergedSynthetic := make([]int, len(b.streamSettledSyntheticPrefixWidths))
					copy(mergedSynthetic, b.streamSettledSyntheticPrefixWidths)
					mergedSoftWraps := make([]bool, len(b.streamSettledSoftWrapContinuations))
					copy(mergedSoftWraps, b.streamSettledSoftWrapContinuations)
					b.mdCache = append(mergedLines, tailLines...)
					b.mdCacheSyntheticPrefixWidths = append(mergedSynthetic, tailSynthetic...)
					b.mdCacheSoftWrapContinuations = append(mergedSoftWraps, tailSoftWraps...)
					b.streamSettledLineCount = len(mergedLines)
				} else {
					b.mdCache = tailLines
					b.mdCacheSyntheticPrefixWidths = tailSynthetic
					b.mdCacheSoftWrapContinuations = tailSoftWraps
					b.streamSettledLineCount = 0
				}
			} else {
				b.InvalidateStreamingSettledCache()
				b.mdCache, b.mdCacheSyntheticPrefixWidths, b.mdCacheSoftWrapContinuations = renderAssistantMarkdownContent(bodyContent, bodyContent, contentWidth, continuationExtra, &b.diffHL)
			}
			b.mdCacheWidth = width
		}
		contentLines = b.mdCache
		contentSynthetic = b.mdCacheSyntheticPrefixWidths
		contentSoftWraps = b.mdCacheSoftWrapContinuations

		var assistantLines []string
		var assistantSynthetic []int
		var assistantSoftWraps []bool
		assistantLines = append(assistantLines, AssistantLabelStyle.Render("ASSISTANT"))
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

		for i, cl := range contentLines {
			line := cl
			if b.Streaming && i >= b.streamSettledLineCount {
				// Only apply cheap-path style to tail lines; settled lines already
				// carry full markdown ANSI styling from renderAssistantMarkdownContent.
				line = MessageContentStyle.Render(cl)
			}
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

		// Re-insert card bg after inner ANSI resets (§1.25).
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
	for i, part := range b.ThinkingParts {
		if i == 0 {
			rawLines = append(rawLines, ThinkingLabelStyle.Render("THINKING"))
			rawLines = append(rawLines, "") // gap
		} else if i > 0 {
			rawLines = append(rawLines, "") // small gap between distinct thinking segments
		}
		part = removeTrailingCursorGlyph(part)
		part = preprocessThinkingMarkdown(part)
		var mdLines []string
		if b.Streaming {
			// Same cheap-path rule as assistant streaming: use plain wrapping while
			// the thinking text is still arriving, then render settled markdown once
			// the block completes.
			plain := wrapText(part, contentWidth)
			mdLines = make([]string, len(plain))
			for j, line := range plain {
				mdLines[j] = ThinkingContentStyle.Render(line)
			}
		} else {
			mdLines = renderMarkdownContent(part, contentWidth)
		}
		rawLines = append(rawLines, styleRenderedThinkingLines(mdLines)...)
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
	var mdLines []string
	if b.Streaming {
		// Streaming THINKING blocks reuse the same cheap wrap-only path to avoid
		// running glamour on every incremental delta.
		plain := wrapText(content, contentWidth)
		mdLines = make([]string, len(plain))
		for i, line := range plain {
			mdLines[i] = ThinkingContentStyle.Render(line)
		}
	} else {
		mdLines = renderMarkdownContent(content, contentWidth)
	}

	var rawLines []string
	rawLines = append(rawLines, ThinkingLabelStyle.Render("THINKING"))
	rawLines = append(rawLines, "") // internal gap

	rawLines = append(rawLines, styleRenderedThinkingLines(mdLines)...)

	// Thinking duration footer
	if b.ThinkingDuration >= time.Second && !b.Streaming {
		rawLines = append(rawLines, "")
		footer := ThinkingContentStyle.Render(fmt.Sprintf("⏱ %s", b.ThinkingDuration.Round(time.Second)))
		rawLines = append(rawLines, "  "+footer)
	}

	// Re-insert card bg after inner ANSI resets (§1.25).
	thinkBg2 := currentTheme.ThinkingCardBg
	rawLines = preserveCardBg(rawLines, thinkBg2)
	return renderPrewrappedCard(style, innerWidth, rawLines, thinkBg2, railANSISeq("thinking", b.Focused))
}
