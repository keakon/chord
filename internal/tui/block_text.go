package tui

import (
	"image/color"
	"strings"

	"github.com/keakon/lipgloss/v2"
	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/tui/terminaltext"
)

const (
	// maxTextWidth is the maximum width for text content to prevent unreadable
	// wide text on large terminals.
	maxTextWidth = 120
	// maxProseWidth is the wrapping cap for natural-language prose (user/assistant
	// messages, thinking, status/summary cards). It is wider than maxTextWidth so
	// wide terminals lose fewer columns to right-side whitespace, which shortens
	// paragraphs vertically and fits more cards on screen. Monospace/structural
	// content (tool cards, shell output, code, diffs) keeps maxTextWidth because
	// column-aligned text gains nothing from extra width and reads worse when
	// stretched. Narrower viewports still wrap to the available width; this is
	// only an upper bound, never a floor.
	maxProseWidth = 160
	// maxMarkdownTableWidth lets tables use more of a wide viewport without making
	// ordinary prose harder to read.
	maxMarkdownTableWidth = 220
)

// railWidthToReserve returns how many columns to subtract from the available
// width so the conversation rail (a foreground-only "│" rendered OUTSIDE the
// card surface by wrapLineWithBackgroundAndRail) never pushes a card past the
// terminal width. A card border already occupies one column on its left edge,
// so when a border is present it absorbs the rail's width and nothing extra
// needs reserving; only borderless cards (wide-header read/shell cards, diff
// tool cards) need the full rail column reserved.
func railWidthToReserve(style lipgloss.Style) int {
	return max(cardRailWidth-style.GetHorizontalBorderSize(), 0)
}

// padLineToDisplayWidthWithStyle pads line to width using style.Render(padding) so the
// padded area keeps the style's background (e.g. MessageContentStyle), avoiding
// patchy colours when content has ANSI resets and padding would otherwise inherit outer bg.
// The padding carries its own SGR open/close pair, so we always append it after the
// original line: stripping a trailing reset here would leak character-level SGR state
// (e.g. lipgloss v2 wraps each strikethrough/underline rune with its own \x1b[...m<c>\x1b[m,
// so the sole trailing reset is the last rune's closing sequence — removing it lets the
// strike/underline bleed across the pad spaces).
func padLineToDisplayWidthWithStyle(style lipgloss.Style, line string, width int) string {
	w := terminaltext.Width(line)
	if w >= width {
		return line
	}
	return line + style.Render(strings.Repeat(" ", width-w))
}

// preserveCardBg re-inserts the card's background ANSI sequence after full
// resets (\x1b[0m / \x1b[m), foreground resets (\x1b[39m), and background
// resets (\x1b[49m) in each line. This prevents inner Render() calls and
// glamour's ANSI output from breaking the outer card's background color when
// nested ANSI-rendered content resets the terminal style.
func preserveCardBg(lines []string, bgColorNum string) []string {
	for i, line := range lines {
		if line == "" {
			continue
		}
		lines[i] = terminaltext.PreserveBackground(line, bgColorNum)
	}
	return lines
}

// hasExplicitStyleColor reports whether a lipgloss getter returned a real
// configured color rather than the package's NoColor sentinel.
func hasExplicitStyleColor(c color.Color) bool {
	if c == nil {
		return false
	}
	_, isNoColor := c.(lipgloss.NoColor)
	return !isNoColor
}

func styleHasTextAttributes(style lipgloss.Style) bool {
	return style.GetBold() ||
		style.GetFaint() ||
		style.GetItalic() ||
		style.GetUnderline() ||
		style.GetBlink() ||
		style.GetReverse() ||
		style.GetStrikethrough() ||
		hasExplicitStyleColor(style.GetForeground()) ||
		hasExplicitStyleColor(style.GetBackground()) ||
		hasExplicitStyleColor(style.GetUnderlineColor())
}

// ansiSeqForStyle builds an ANSI SGR sequence for the explicitly configured
// text attributes on a lipgloss style so ANSI-rich inner content can restore the
// surrounding line style after inline resets.
func ansiSeqForStyle(style lipgloss.Style) string {
	var seq ansi.Style
	if style.GetBold() {
		seq = seq.Bold()
	}
	if style.GetFaint() {
		seq = seq.Faint()
	}
	if style.GetItalic() {
		seq = seq.Italic(true)
	}
	if style.GetUnderline() {
		seq = seq.UnderlineStyle(ansi.Underline(style.GetUnderlineStyle()))
	}
	if style.GetBlink() {
		seq = seq.Blink(true)
	}
	if style.GetReverse() {
		seq = seq.Reverse(true)
	}
	if style.GetStrikethrough() {
		seq = seq.Strikethrough(true)
	}
	if fg := style.GetForeground(); hasExplicitStyleColor(fg) {
		seq = seq.ForegroundColor(fg)
	}
	if bg := style.GetBackground(); hasExplicitStyleColor(bg) {
		seq = seq.BackgroundColor(bg)
	}
	if ulc := style.GetUnderlineColor(); hasExplicitStyleColor(ulc) {
		seq = seq.UnderlineColor(ulc)
	}
	if len(seq) == 0 {
		return ""
	}
	return seq.String()
}

// preserveStyleAfterResets re-applies the given surrounding text style after
// ANSI reset sequences emitted by inner markdown spans. This keeps paragraph-
// level thinking styles (for example italic body text or bold pseudo-headings)
// active for the remainder of the line instead of letting inline markdown resets
// permanently clear them.
func preserveStyleAfterResets(line string, style lipgloss.Style) string {
	seq := ansiSeqForStyle(style)
	if seq == "" || line == "" {
		return line
	}
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+seq)
	line = strings.ReplaceAll(line, "\x1b[m", "\x1b[m"+seq)
	if style.GetBold() || style.GetFaint() {
		line = strings.ReplaceAll(line, "\x1b[22m", "\x1b[22m"+seq)
	}
	if style.GetItalic() {
		line = strings.ReplaceAll(line, "\x1b[23m", "\x1b[23m"+seq)
	}
	if style.GetUnderline() {
		line = strings.ReplaceAll(line, "\x1b[24m", "\x1b[24m"+seq)
	}
	if style.GetBlink() {
		line = strings.ReplaceAll(line, "\x1b[25m", "\x1b[25m"+seq)
	}
	if style.GetReverse() {
		line = strings.ReplaceAll(line, "\x1b[27m", "\x1b[27m"+seq)
	}
	if style.GetStrikethrough() {
		line = strings.ReplaceAll(line, "\x1b[29m", "\x1b[29m"+seq)
	}
	if hasExplicitStyleColor(style.GetForeground()) {
		line = strings.ReplaceAll(line, "\x1b[39m", "\x1b[39m"+seq)
	}
	if hasExplicitStyleColor(style.GetBackground()) {
		line = strings.ReplaceAll(line, "\x1b[49m", "\x1b[49m"+seq)
	}
	if hasExplicitStyleColor(style.GetUnderlineColor()) {
		line = strings.ReplaceAll(line, "\x1b[59m", "\x1b[59m"+seq)
	}
	return line
}

// spacePad is a reusable run of spaces so writeSpaces can fill padding without
// allocating a fresh strings.Repeat result per line.
const spacePad = "                                                                "

// writeSpaces appends n spaces to b, reusing spacePad to avoid per-call allocs.
func writeSpaces(b *strings.Builder, n int) {
	for n > 0 {
		chunk := min(n, len(spacePad))
		b.WriteString(spacePad[:chunk])
		n -= chunk
	}
}

func wrapLineWithBackground(marginPrefix, innerPrefix, line, innerSuffix, bgSeq, marginSuffix string, pad int) string {
	if bgSeq == "" && pad == 0 {
		return marginPrefix + innerPrefix + line + innerSuffix + marginSuffix
	}
	var b strings.Builder
	b.Grow(len(marginPrefix) + len(bgSeq) + len(innerPrefix) + len(line) + pad + len(innerSuffix) + len(ansi.ResetStyle) + len(marginSuffix))
	b.WriteString(marginPrefix)
	b.WriteString(bgSeq)
	b.WriteString(innerPrefix)
	b.WriteString(line)
	writeSpaces(&b, pad)
	b.WriteString(innerSuffix)
	if bgSeq != "" {
		b.WriteString(ansi.ResetStyle)
	}
	b.WriteString(marginSuffix)
	return b.String()
}

func wrapLineWithBackgroundAndRail(marginPrefix, innerPrefix, line, innerSuffix, bgSeq, marginSuffix, railPrefix string, pad int) string {
	if railPrefix == "" {
		return wrapLineWithBackground(marginPrefix, innerPrefix, line, innerSuffix, bgSeq, marginSuffix, pad)
	}
	if bgSeq == "" && pad == 0 {
		return railPrefix + marginPrefix + innerPrefix + line + innerSuffix + marginSuffix
	}
	var b strings.Builder
	b.Grow(len(railPrefix) + len(bgSeq) + len(marginPrefix) + len(innerPrefix) + len(line) + pad + len(innerSuffix) + len(ansi.ResetStyle) + len(marginSuffix))
	b.WriteString(railPrefix)
	b.WriteString(bgSeq)
	b.WriteString(marginPrefix)
	b.WriteString(innerPrefix)
	b.WriteString(line)
	writeSpaces(&b, pad)
	b.WriteString(innerSuffix)
	if bgSeq != "" {
		b.WriteString(ansi.ResetStyle)
	}
	b.WriteString(marginSuffix)
	return b.String()
}

// prewrappedCardFrame precomputes the per-line wrapping parameters used by
// renderPrewrappedCard so streaming renders can re-wrap only changed body
// lines while reusing cached card output for the stable prefix.
type prewrappedCardFrame struct {
	innerWidth   int
	bgColorNum   string
	bgSeq        string
	marginPrefix string
	marginSuffix string
	innerPrefix  string
	innerSuffix  string
	railPrefix   string
	padTop       int
	padBottom    int
	marginTop    int
	marginBottom int
	blankWrapped string
	blankMargin  string
}

func newPrewrappedCardFrame(style lipgloss.Style, innerWidth int, bgColorNum, railPrefix string) prewrappedCardFrame {
	if innerWidth < 0 {
		innerWidth = 0
	}
	padTop, padRight, padBottom, padLeft := style.GetPadding()
	marginTop, marginRight, marginBottom, marginLeft := style.GetMargin()
	bgSeq := terminaltext.BackgroundSequence(bgColorNum)
	marginPrefix := strings.Repeat(" ", marginLeft)
	marginSuffix := strings.Repeat(" ", marginRight)
	lineWidth := padLeft + innerWidth + padRight
	// blankMargin (the outer top/bottom margin rows) must span the same horizontal
	// extent as a body line. A rail-bearing body line is the rail prefix wider
	// than its inner content, so reserve that column here too; otherwise the
	// margin rows render one column short of the card's right edge.
	marginExtent := marginLeft + lineWidth + marginRight
	if railPrefix != "" {
		marginExtent += cardRailWidth
	}
	return prewrappedCardFrame{
		innerWidth:   innerWidth,
		bgColorNum:   bgColorNum,
		bgSeq:        bgSeq,
		marginPrefix: marginPrefix,
		marginSuffix: marginSuffix,
		innerPrefix:  strings.Repeat(" ", padLeft),
		innerSuffix:  strings.Repeat(" ", padRight),
		railPrefix:   railPrefix,
		padTop:       padTop,
		padBottom:    padBottom,
		marginTop:    marginTop,
		marginBottom: marginBottom,
		blankWrapped: wrapLineWithBackgroundAndRail(marginPrefix, "", strings.Repeat(" ", lineWidth), "", bgSeq, marginSuffix, railPrefix, 0),
		blankMargin:  strings.Repeat(" ", marginExtent),
	}
}

func (f *prewrappedCardFrame) renderBodyLine(line string) string {
	line = terminaltext.PreserveBackground(line, f.bgColorNum)
	lineDisplayWidth, plainASCII := terminaltext.ASCIIWidth(line)
	if !plainASCII {
		lineDisplayWidth = terminaltext.Width(line)
	}
	if lineDisplayWidth > f.innerWidth {
		line = terminaltext.Truncate(line, f.innerWidth)
		line = terminaltext.PreserveBackground(line, f.bgColorNum)
		return wrapLineWithBackgroundAndRail(f.marginPrefix, f.innerPrefix, line, f.innerSuffix, f.bgSeq, f.marginSuffix, f.railPrefix, 0)
	}
	pad := 0
	if lineDisplayWidth < f.innerWidth {
		pad = f.innerWidth - lineDisplayWidth
	}
	return wrapLineWithBackgroundAndRail(f.marginPrefix, f.innerPrefix, line, f.innerSuffix, f.bgSeq, f.marginSuffix, f.railPrefix, pad)
}

// appendBodyLines renders already-wrapped body lines into dst, splitting any
// embedded newlines per line so output matches a pre-split input.
func (f *prewrappedCardFrame) appendBodyLines(dst []string, lines []string) []string {
	for _, line := range lines {
		if strings.ContainsRune(line, '\n') {
			for part := range strings.SplitSeq(line, "\n") {
				dst = append(dst, f.renderBodyLine(part))
			}
			continue
		}
		dst = append(dst, f.renderBodyLine(line))
	}
	return dst
}

func (f *prewrappedCardFrame) appendTop(dst []string) []string {
	for range f.marginTop {
		dst = append(dst, f.blankMargin)
	}
	for range f.padTop {
		dst = append(dst, f.blankWrapped)
	}
	return dst
}

func (f *prewrappedCardFrame) appendBottom(dst []string) []string {
	for range f.padBottom {
		dst = append(dst, f.blankWrapped)
	}
	for range f.marginBottom {
		dst = append(dst, f.blankMargin)
	}
	return dst
}

// renderPrewrappedCard applies card padding/margins/background to lines that
// are already wrapped to the target inner width. Callers may pass raw ANSI-rich
// lines from Markdown/Glamour/chroma renderers; this helper owns final card-bg
// preservation after inner ANSI resets and after width truncation.
// This avoids sending a large ANSI-rich multi-line string back through lipgloss
// Width(...).Render(...), which would otherwise re-wrap and re-measure every line.
func renderPrewrappedCard(style lipgloss.Style, innerWidth int, lines []string, bgColorNum string, railPrefix string) []string {
	frame := newPrewrappedCardFrame(style, innerWidth, bgColorNum, railPrefix)
	out := make([]string, 0, frame.marginTop+frame.padTop+len(lines)+frame.padBottom+frame.marginBottom)
	out = frame.appendTop(out)
	out = frame.appendBodyLines(out, lines)
	out = frame.appendBottom(out)
	return out
}

// renderPrewrappedToolCard builds the standard tool card wrapper for content
// lines that are already wrapped to the target content width. Callers should
// pass raw logical lines here; this helper owns the final card-bg preservation
// and delegates width completion to renderPrewrappedCard so selection/copy use
// the same visible column baseline as the rendered card.
func renderPrewrappedToolCard(style lipgloss.Style, cardWidth int, title string, body []string, bgColorNum string, railPrefix string) []string {
	final := make([]string, 0, len(body)+2)
	final = append(final, title, "")
	final = append(final, body...)
	final = preserveCardBg(final, bgColorNum)
	return renderPrewrappedCard(style, cardWidth, final, bgColorNum, railPrefix)
}

// cardRailPrefix returns the styled glyph for the conversation rail
// matching the given card kind, or "" if the kind has no rail.
const cardRailGlyph = "│"
const focusedCardRailGlyph = "┃"

func cardRailPrefix(kind string, focused bool) string {
	pick := func(base, focusedColor string) string {
		color := strings.TrimSpace(base)
		if focused {
			if fc := strings.TrimSpace(focusedColor); fc != "" {
				color = fc
			}
		}
		if color == "" {
			return ""
		}
		glyph := cardRailGlyph
		if focused {
			glyph = focusedCardRailGlyph
		}
		return "\x1b[38;5;" + color + "m" + glyph + ansi.ResetStyle
	}
	switch kind {
	case "user":
		return pick(currentStyles.Palette.RailUserFg, currentStyles.Palette.RailUserFocusedFg)
	case "assistant":
		return pick(currentStyles.Palette.RailAssistantFg, currentStyles.Palette.RailAssistantFocusedFg)
	case "tool":
		return pick(currentStyles.Palette.RailToolFg, currentStyles.Palette.RailToolFocusedFg)
	case "thinking":
		return pick(currentStyles.Palette.RailThinkingFg, currentStyles.Palette.RailThinkingFocusedFg)
	case "error":
		return pick(currentStyles.Palette.RailErrorFg, currentStyles.Palette.RailErrorFocusedFg)
	default:
		return ""
	}
}

func renderUserText(text string, width int) []string {
	// Preserve original newlines and indentation; expand tabs; soft-wrap by display width.
	text = terminaltext.Sanitize(text)
	return terminaltext.WrapPreformatted(text, width)
}
