package tui

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/keakon/lipgloss/v2"

	"github.com/keakon/chord/internal/textclean"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

const (
	assistantVirtualChunkSegments = 32
	assistantVirtualMinBytes      = 16 << 10
	assistantVirtualMinSegments   = 64
)

// assistantVirtualSegment is one top-level Markdown block in a settled
// assistant body. Only single-line paragraphs and ATX headings are accepted:
// their rendered height is predicted with glamour's wrapping function, so the
// viewport's line count never has to wait for a full glamour render.
type assistantVirtualSegment struct {
	start   int
	end     int
	line    int
	span    int
	heading bool
}

type assistantVirtualChunk struct {
	startLine int
	endLine   int
	startSeg  int
	endSeg    int
	lines     []string
	rendered  bool
}

// assistantVirtualLayout lazily renders aligned groups of top-level blocks.
// The rendered groups are byte-identical to the same rows of a one-shot
// glamour render because each group includes its neighbouring block: glamour
// carries paragraph/heading margin styling across that boundary.
type assistantVirtualLayout struct {
	contentKey   string
	width        int
	themeVersion uint64
	labelID      int

	source     string
	contentW   int
	innerWidth int
	bodyOffset int
	totalBody  int
	totalCard  int
	segments   []assistantVirtualSegment
	chunks     []assistantVirtualChunk
	valid      bool
}

func (b *Block) virtualAssistantLineCount(width int) (int, bool) {
	layout, ok := b.assistantVirtualLayout(width)
	if !ok {
		return 0, false
	}
	return layout.totalCard, true
}

func (b *Block) assistantVirtualLayout(width int) (*assistantVirtualLayout, bool) {
	if b == nil || b.Type != BlockAssistant || b.Streaming || width <= 0 {
		return nil, false
	}
	labelID := b.displayLabelID()
	if layout := b.virtualAssistant; layout != nil &&
		layout.contentKey == b.Content && layout.width == width &&
		layout.themeVersion == appliedThemeVersion && layout.labelID == labelID {
		if !layout.valid {
			return nil, false
		}
		return layout, true
	}
	if len(b.Content) < assistantVirtualMinBytes {
		return nil, false
	}
	// Remember rejected candidates too: LineCount runs before the ordinary
	// rendered-line cache check on every viewport render.
	layout := &assistantVirtualLayout{
		contentKey:   b.Content,
		width:        width,
		themeVersion: appliedThemeVersion,
		labelID:      labelID,
	}
	b.virtualAssistant = layout
	for _, part := range b.ThinkingParts {
		if strings.TrimSpace(removeTrailingCursorGlyph(part)) != "" {
			return nil, false
		}
	}

	rawContent := removeTrailingCursorGlyph(b.Content)
	summary := parseAssistantSummary(rawContent)
	if summary.HasMeta {
		return nil, false
	}
	bodyContent := stripAssistantSummary(rawContent, summary)
	if len(bodyContent) < assistantVirtualMinBytes {
		return nil, false
	}
	source := textclean.NormalizeNewlines(terminaltext.Sanitize(bodyContent))

	style := currentStyles.AssistantCardStyle
	boxWidth := max((width-railWidthToReserve(style))-style.GetHorizontalMargins(), 10)
	innerWidth := max(boxWidth-style.GetHorizontalPadding()-style.GetHorizontalBorderSize(), 10)
	contentW := markdownRenderWidthWithTable(false, innerWidth)

	segments, totalBody, ok := buildAssistantVirtualSegments(source, contentW)
	if !ok || len(segments) < assistantVirtualMinSegments {
		return nil, false
	}

	frame := newPrewrappedCardFrame(style, innerWidth, currentStyles.Palette.AssistantCardBg, "")
	bodyOffset := frame.marginTop + frame.padTop + 2 // label + gap
	footer := frame.padBottom + frame.marginBottom
	*layout = assistantVirtualLayout{
		contentKey:   b.Content,
		width:        width,
		themeVersion: appliedThemeVersion,
		labelID:      labelID,
		source:       source,
		contentW:     contentW,
		innerWidth:   innerWidth,
		bodyOffset:   bodyOffset,
		totalBody:    totalBody,
		totalCard:    bodyOffset + totalBody + footer,
		segments:     segments,
		valid:        true,
	}
	layout.chunks = make([]assistantVirtualChunk, 0, (len(segments)+assistantVirtualChunkSegments-1)/assistantVirtualChunkSegments)
	for start := 0; start < len(segments); start += assistantVirtualChunkSegments {
		end := min(start+assistantVirtualChunkSegments, len(segments))
		endLine := totalBody
		if end < len(segments) {
			endLine = segments[end].line
		}
		layout.chunks = append(layout.chunks, assistantVirtualChunk{
			startLine: segments[start].line,
			endLine:   endLine,
			startSeg:  start,
			endSeg:    end,
		})
	}
	b.virtualAssistant = layout
	return layout, true
}

func buildAssistantVirtualSegments(source string, width int) ([]assistantVirtualSegment, int, bool) {
	if source == "" || width <= 0 {
		return nil, 0, false
	}
	segments := make([]assistantVirtualSegment, 0, strings.Count(source, "\n")+1)
	for offset := 0; offset < len(source); {
		lineEnd := strings.IndexByte(source[offset:], '\n')
		if lineEnd < 0 {
			lineEnd = len(source)
		} else {
			lineEnd += offset
		}
		line := source[offset:lineEnd]
		// Markdown blank lines contain only ASCII spaces or tabs; Unicode
		// whitespace can be paragraph content and must reach the syntax gate.
		if strings.Trim(line, " \t") == "" {
			if lineEnd >= len(source) {
				break
			}
			offset = lineEnd + 1
			continue
		}

		start := offset
		end := lineEnd
		nonEmpty := 1
		next := lineEnd
		if next < len(source) {
			next++
		}
		for next < len(source) {
			nextEnd := strings.IndexByte(source[next:], '\n')
			if nextEnd < 0 {
				nextEnd = len(source)
			} else {
				nextEnd += next
			}
			if strings.Trim(source[next:nextEnd], " \t") == "" {
				break
			}
			nonEmpty++
			if nonEmpty > 1 {
				return nil, 0, false
			}
			end = nextEnd
			next = nextEnd
			if next < len(source) {
				next++
			}
		}

		raw := source[start:end]
		heading, text, ok := assistantVirtualSegmentText(raw)
		if !ok {
			return nil, 0, false
		}
		span := strings.Count(lipgloss.Wrap(text, width, ""), "\n") + 1
		if heading {
			// Glamour emits a trailing heading margin line even when the heading
			// is the final block.
			span++
		}
		segments = append(segments, assistantVirtualSegment{
			start:   start,
			end:     end,
			span:    span,
			heading: heading,
		})
		if next >= len(source) {
			break
		}
		offset = next
	}

	total := 0
	for i := range segments {
		segments[i].line = total
		total += segments[i].span
		if !segments[i].heading && i < len(segments)-1 {
			// A paragraph contributes one blank separator before the next
			// block. Headings already include their trailing margin in span.
			total++
		}
	}
	return segments, total, true
}

// assistantVirtualSegmentText returns the display text used for wrapping and
// rejects any syntax whose rendered height cannot be derived from that text.
func assistantVirtualSegmentText(raw string) (heading bool, text string, ok bool) {
	if raw == "" || raw != strings.TrimSpace(raw) || !utf8.ValidString(raw) {
		return false, "", false
	}
	if strings.ContainsAny(raw, "\t\r\n\u2028\u2029") || strings.ContainsAny(raw, "*_`[]<>\\|~&") {
		return false, "", false
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < 0x20 || raw[i] == 0x7f {
			return false, "", false
		}
	}
	if strings.HasPrefix(raw, "#") {
		level := 0
		for level < len(raw) && raw[level] == '#' {
			level++
		}
		if level < 1 || level > 6 || level >= len(raw) || raw[level] != ' ' {
			return false, "", false
		}
		heading = true
		text = strings.TrimSpace(raw[level:])
	} else {
		if strings.HasPrefix(raw, "-") || strings.HasPrefix(raw, "+") || strings.HasPrefix(raw, ">") || strings.HasPrefix(raw, ":") {
			return false, "", false
		}
		text = raw
	}
	if text == "" || strings.Contains(text, "www.") || (heading && strings.Contains(text, "#")) ||
		assistantVirtualOrderedListPrefix(text) || assistantVirtualContainsEmojiShortcode(text) {
		return false, "", false
	}
	return heading, text, true
}

func assistantVirtualOrderedListPrefix(text string) bool {
	i := 0
	for i < len(text) && text[i] >= '0' && text[i] <= '9' {
		i++
	}
	return i > 0 && i+1 < len(text) && (text[i] == '.' || text[i] == ')') && text[i+1] == ' '
}

func assistantVirtualContainsEmojiShortcode(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] != ':' {
			continue
		}
		for j := i + 1; j < len(text); j++ {
			if text[j] == ':' {
				if j > i+1 {
					return true
				}
				break
			}
			if text[j] == ' ' || text[j] == '\t' {
				break
			}
		}
	}
	return false
}

func (l *assistantVirtualLayout) chunkForLine(line int) int {
	index := sort.Search(len(l.chunks), func(i int) bool {
		return line < l.chunks[i].endLine
	})
	if index >= len(l.chunks) {
		index = len(l.chunks) - 1
	}
	return max(index, 0)
}

func (b *Block) ensureVirtualChunk(layout *assistantVirtualLayout, index int) bool {
	if index < 0 || index >= len(layout.chunks) {
		return false
	}
	chunk := &layout.chunks[index]
	if chunk.rendered {
		return true
	}
	groupStart := max(chunk.startSeg-1, 0)
	groupEnd := min(chunk.endSeg+1, len(layout.segments))
	if groupStart >= groupEnd {
		return false
	}
	source := layout.source[layout.segments[groupStart].start:layout.segments[groupEnd-1].end]
	rendered := contentMarkdown.Render(source, layout.contentW)
	groupLine := layout.segments[groupStart].line
	last := layout.segments[groupEnd-1]
	groupHeight := last.line + last.span - groupLine
	start := chunk.startLine - groupLine
	end := chunk.endLine - groupLine
	if len(rendered) != groupHeight || start < 0 || end < start || end > len(rendered) {
		layout.valid = false
		return false
	}
	chunk.lines = append([]string(nil), rendered[start:end]...)
	chunk.rendered = true
	b.hotBytesMemoValid = false
	return true
}

func (b *Block) virtualAssistantBodyLines(layout *assistantVirtualLayout, start, end int) ([]string, bool) {
	start = max(start, 0)
	end = min(end, layout.totalBody)
	if end <= start {
		return nil, true
	}
	out := make([]string, 0, end-start)
	for line := start; line < end; {
		index := layout.chunkForLine(line)
		chunk := &layout.chunks[index]
		if !b.ensureVirtualChunk(layout, index) {
			return nil, false
		}
		offset := line - chunk.startLine
		take := min(end-line, chunk.endLine-line)
		if offset < 0 || offset+take > len(chunk.lines) {
			layout.valid = false
			return nil, false
		}
		out = append(out, chunk.lines[offset:offset+take]...)
		line += take
	}
	return out, true
}

// A rejected prediction must retire every cache derived from its row count
// before the viewport measures again, including when Render was called by
// search or copying rather than by the viewport itself.
func (b *Block) renderAssistantVirtualFallback(width int) []string {
	layout := b.virtualAssistant
	layout.valid = false
	layout.chunks = nil
	b.viewportCache = nil
	b.viewportCacheWidth = 0
	b.searchMatchReady = false
	delete(b.spillLineCounts, width)
	lines := b.renderAssistant(width)
	b.cacheRenderedLines(width, lines)
	if len(lines) != layout.totalCard && b.renderLineCountChanged != nil {
		b.renderLineCountChanged(width)
	}
	return lines
}

func (b *Block) renderVirtualAssistantRange(layout *assistantVirtualLayout, width, start, end int) []string {
	start = max(start, 0)
	end = min(end, layout.totalCard)
	if end <= start {
		return nil
	}
	style := currentStyles.AssistantCardStyle
	frame := newPrewrappedCardFrame(style, layout.innerWidth, currentStyles.Palette.AssistantCardBg, cardRailPrefix("assistant", b.Focused))
	labelLine := currentStyles.AssistantLabelStyle.Render(blockLabelWithID("ASSISTANT", b.displayLabelID()))
	top := frame.appendTop(nil)
	bottom := frame.appendBottom(nil)
	bodyEnd := layout.bodyOffset + layout.totalBody

	out := make([]string, 0, end-start)
	synthetic := make([]int, 0, end-start)
	softWraps := make([]bool, 0, end-start)
	cardInset := style.GetMarginLeft() + style.GetPaddingLeft()
	labelInset := currentStyles.AssistantLabelStyle.GetMarginLeft() + currentStyles.AssistantLabelStyle.GetPaddingLeft()

	appendLine := func(line string, syntheticWidth int, softWrap bool) {
		out = append(out, line)
		synthetic = append(synthetic, syntheticWidth)
		softWraps = append(softWraps, softWrap)
	}
	for pos := start; pos < end; {
		switch {
		case pos < len(top):
			take := min(end-pos, len(top)-pos)
			for _, line := range top[pos : pos+take] {
				appendLine(line, 0, false)
			}
			pos += take
		case pos < layout.bodyOffset:
			headerIndex := pos - len(top)
			if headerIndex == 0 {
				appendLine(frame.renderBodyLine(labelLine), cardInset+labelInset, false)
			} else {
				appendLine(frame.renderBodyLine(""), cardInset, false)
			}
			pos++
		case pos < bodyEnd:
			bodyStart := pos - layout.bodyOffset
			bodyCount := min(end, bodyEnd) - pos
			lines, ok := b.virtualAssistantBodyLines(layout, bodyStart, bodyStart+bodyCount)
			if !ok {
				return nil
			}
			for _, line := range lines {
				appendLine(frame.renderBodyLine("  "+line), cardInset, false)
			}
			pos += bodyCount
		default:
			bottomIndex := pos - bodyEnd
			take := min(end-pos, len(bottom)-bottomIndex)
			for _, line := range bottom[bottomIndex : bottomIndex+take] {
				appendLine(line, 0, false)
			}
			pos += take
		}
	}

	b.renderSyntheticPrefixWidths = synthetic
	b.renderSoftWrapContinuations = softWraps
	b.renderSyntheticPrefixWidthsW = width
	b.renderSyntheticPrefixWidthsStart = start
	return out
}

func (l *assistantVirtualLayout) hotBytes() int64 {
	if l == nil {
		return 0
	}
	total := int64(len(l.source)) + int64(len(l.segments))*48
	for i := range l.chunks {
		for _, line := range l.chunks[i].lines {
			total += int64(len(line))
		}
	}
	return total
}
