package tui

import (
	"fmt"
	"image/color"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

const maxCompactionSummaryPreviewLines = 10

const (
	// maxTextWidth is the maximum width for text content to prevent unreadable
	// wide text on large terminals.
	maxTextWidth = 120
	// maxMarkdownTableWidth lets tables use more of a wide viewport without making
	// ordinary prose harder to read.
	maxMarkdownTableWidth = 220
	// preformattedTabWidth is the visual tab stop used when rendering pasted
	// user content.
	preformattedTabWidth = 4
)

// clampCardInnerWidth caps a card's inner width so the card surface ends near
// the wrapped-text cap instead of stretching a mostly-empty background across
// very wide viewports. textCap is the renderer's content-width cap (usually
// maxTextWidth; assistant passes its own so markdown tables can stretch). The
// cap is expressed as a shared box width (horizontal padding + inner width)
// so cards with different padding keep their right edges aligned: prose cards
// use 2 padding columns + 2 content-indent columns around textCap, hence
// textCap + 4.
func clampCardInnerWidth(innerWidth int, style lipgloss.Style, textCap int) int {
	if boxCap := textCap + 4 - style.GetHorizontalPadding(); innerWidth > boxCap {
		return boxCap
	}
	return innerWidth
}

// ansiStrip removes ANSI CSI sequences for display-width calculation.
var ansiStrip = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// tuiWidthMethod is the display-width algorithm used by the viewport cell
// renderer. All card wrapping, truncation, and padding code must use the same
// method so emoji/variation-selector graphemes do not shift background fills.
const tuiWidthMethod = ansi.GraphemeWidth

func tuiStringWidth(s string) int {
	return tuiWidthMethod.StringWidth(s)
}

func plainASCIIWidth(s string) (int, bool) {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c >= 0x7f {
			return 0, false
		}
	}
	return len(s), true
}

func tuiCut(s string, left, right int) string {
	return tuiWidthMethod.Cut(s, left, right)
}

func tuiWrapHeadTail(s string, width int) (string, string) {
	if width <= 0 || s == "" {
		return s, ""
	}
	totalWidth := tuiStringWidth(s)
	head := tuiCut(s, 0, width)
	headWidth := tuiStringWidth(head)
	for headWidth == 0 && width < totalWidth {
		width++
		head = tuiCut(s, 0, width)
		headWidth = tuiStringWidth(head)
	}
	if head != "" {
		return head, tuiCut(s, headWidth, totalWidth)
	}
	cluster, _ := ansi.FirstGraphemeCluster(s, tuiWidthMethod)
	if cluster == "" {
		return s, ""
	}
	return cluster, s[len(cluster):]
}

func tuiHardwrap(s string, width int) []string {
	wrapped := tuiWidthMethod.Hardwrap(s, width, true)
	parts := strings.Split(wrapped, "\n")
	if len(parts) > 1 && parts[0] == "" {
		parts = parts[1:]
	}
	return parts
}

// padLineToDisplayWidth pads line with spaces to exactly width display columns
// so block backgrounds extend to full width and selection aligns. Uses the same
// grapheme-aware width method as the viewport so emoji/wide chars match highlight column math.
func padLineToDisplayWidth(line string, width int) string {
	w := tuiStringWidth(line)
	if w >= width {
		return line
	}
	padding := strings.Repeat(" ", width-w)
	if strings.HasSuffix(line, "\x1b[0m") {
		return line[:len(line)-4] + padding + "\x1b[0m"
	}
	if strings.HasSuffix(line, "\x1b[m") {
		return line[:len(line)-3] + padding + "\x1b[m"
	}
	return line + padding
}

// ensureStyledLineReset appends a final SGR reset when a styled line still ends
// with an active style sequence. This is especially important for assistant
// fenced code lines, where we deliberately re-apply the code-block background
// after inner resets inside the line; without a final close, outer card padding
// can inherit the code-block background and visually "leak" to the right.
func ensureStyledLineReset(line string) string {
	if line == "" || !strings.Contains(line, "\x1b[") {
		return line
	}
	if strings.HasSuffix(line, "\x1b[0m") || strings.HasSuffix(line, ansi.ResetStyle) {
		return line
	}
	return line + ansi.ResetStyle
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
	w := tuiStringWidth(line)
	if w >= width {
		return line
	}
	return line + style.Render(strings.Repeat(" ", width-w))
}

// colorToANSIBgSeq converts a color string to an ANSI SGR background sequence.
// It handles:
//   - ANSI 256-color numbers (e.g. "235") → "\x1b[48;5;235m"
//   - Hex colors (e.g. "#1e3d2e") → "\x1b[48;2;R;G;Bm" (truecolor)
//   - Empty string → ""
//
// This ensures preserveBackground works correctly on terminals that support
// truecolor profiles (most modern terminals) when theme values use hex.
func colorToANSIBgSeq(colorStr string) string {
	if colorStr == "" {
		return ""
	}
	if colorStr[0] == '#' {
		// Hex color → truecolor (24-bit) ANSI sequence
		r, g, b, ok := parseHexColor(colorStr)
		if !ok {
			return ""
		}
		return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r, g, b)
	}
	// Assume ANSI 256-color palette index
	return "\x1b[48;5;" + colorStr + "m"
}

// parseHexColor parses a hex color string like "#rgb", "#rrggbb", "rgb", or "rrggbb".
// Returns r, g, b components and true on success.
func parseHexColor(hex string) (r, g, b uint8, ok bool) {
	if len(hex) == 0 {
		return 0, 0, 0, false
	}
	if hex[0] == '#' {
		hex = hex[1:]
	}
	switch len(hex) {
	case 3:
		// #RGB shorthand
		rv, e1 := parseHexByte(hex[0:1] + hex[0:1])
		gv, e2 := parseHexByte(hex[1:2] + hex[1:2])
		bv, e3 := parseHexByte(hex[2:3] + hex[2:3])
		if e1 != nil || e2 != nil || e3 != nil {
			return 0, 0, 0, false
		}
		return rv, gv, bv, true
	case 6:
		rv, e1 := parseHexByte(hex[0:2])
		gv, e2 := parseHexByte(hex[2:4])
		bv, e3 := parseHexByte(hex[4:6])
		if e1 != nil || e2 != nil || e3 != nil {
			return 0, 0, 0, false
		}
		return rv, gv, bv, true
	default:
		return 0, 0, 0, false
	}
}

func parseHexByte(s string) (uint8, error) {
	var v byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			v = v*16 + (c - '0')
		case c >= 'a' && c <= 'f':
			v = v*16 + (c - 'a' + 10)
		case c >= 'A' && c <= 'F':
			v = v*16 + (c - 'A' + 10)
		default:
			return 0, fmt.Errorf("invalid hex digit: %c", c)
		}
	}
	return v, nil
}

// preserveBackground re-inserts bgColor after ANSI resets inside a rendered
// string so an outer container background survives inner Render() calls.
// Supports both ANSI 256-color palette indices and hex colors via colorToANSIBgSeq.
func preserveBackground(line, bgColor string) string {
	if bgColor == "" || line == "" {
		return line
	}
	if !strings.Contains(line, "\x1b[") {
		return line
	}
	bgSeq := colorToANSIBgSeq(bgColor)
	if bgSeq == "" {
		return line
	}
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+bgSeq)
	line = strings.ReplaceAll(line, "\x1b[m", "\x1b[m"+bgSeq)
	line = strings.ReplaceAll(line, "\x1b[49m", "\x1b[49m"+bgSeq)
	line = strings.ReplaceAll(line, "\x1b[39m", "\x1b[39m"+bgSeq)
	return line
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
		lines[i] = preserveBackground(line, bgColorNum)
	}
	return lines
}

func sanitizeDisplayText(s string) string {
	if s == "" {
		return ""
	}
	needsSanitization := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\r' || ((c < 0x20 && c != '\t' && c != '\n') || c == 0x7f) {
			needsSanitization = true
			break
		}
	}
	if !needsSanitization {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\r':
			if i+1 < len(s) && s[i+1] == '\n' {
				continue
			}
			b.WriteString(`\r`)
		case c == '\t' || c == '\n':
			b.WriteByte(c)
		case c < 0x20 || c == 0x7f:
			b.WriteString(displayControlLiteral(c))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func displayControlLiteral(c byte) string {
	switch c {
	case '\a':
		return `\a`
	case '\b':
		return `\b`
	case '\f':
		return `\f`
	case '\v':
		return `\v`
	default:
		return fmt.Sprintf(`\x%02x`, c)
	}
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
		chunk := n
		if chunk > len(spacePad) {
			chunk = len(spacePad)
		}
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

func wrapLineWithBackgroundAndRail(marginPrefix, innerPrefix, line, innerSuffix, bgSeq, marginSuffix, railSeq string, pad int) string {
	if railSeq == "" {
		return wrapLineWithBackground(marginPrefix, innerPrefix, line, innerSuffix, bgSeq, marginSuffix, pad)
	}
	if bgSeq == "" && pad == 0 {
		return railSeq + "│" + ansi.ResetStyle + marginPrefix + innerPrefix + line + innerSuffix + marginSuffix
	}
	var b strings.Builder
	b.Grow(len(railSeq) + len("│") + len(ansi.ResetStyle) + len(bgSeq) + len(marginPrefix) + len(innerPrefix) + len(line) + pad + len(innerSuffix) + len(ansi.ResetStyle) + len(marginSuffix))
	b.WriteString(railSeq)
	b.WriteString("│")
	b.WriteString(ansi.ResetStyle)
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
	railSeq      string
	padTop       int
	padBottom    int
	marginTop    int
	marginBottom int
	blankWrapped string
	blankMargin  string
}

func newPrewrappedCardFrame(style lipgloss.Style, innerWidth int, bgColorNum, railSeq string) prewrappedCardFrame {
	if innerWidth < 0 {
		innerWidth = 0
	}
	padTop, padRight, padBottom, padLeft := style.GetPadding()
	marginTop, marginRight, marginBottom, marginLeft := style.GetMargin()
	bgSeq := colorToANSIBgSeq(bgColorNum)
	marginPrefix := strings.Repeat(" ", marginLeft)
	marginSuffix := strings.Repeat(" ", marginRight)
	lineWidth := padLeft + innerWidth + padRight
	return prewrappedCardFrame{
		innerWidth:   innerWidth,
		bgColorNum:   bgColorNum,
		bgSeq:        bgSeq,
		marginPrefix: marginPrefix,
		marginSuffix: marginSuffix,
		innerPrefix:  strings.Repeat(" ", padLeft),
		innerSuffix:  strings.Repeat(" ", padRight),
		railSeq:      railSeq,
		padTop:       padTop,
		padBottom:    padBottom,
		marginTop:    marginTop,
		marginBottom: marginBottom,
		blankWrapped: wrapLineWithBackgroundAndRail(marginPrefix, "", strings.Repeat(" ", lineWidth), "", bgSeq, marginSuffix, railSeq, 0),
		blankMargin:  strings.Repeat(" ", marginLeft+lineWidth+marginRight),
	}
}

func (f *prewrappedCardFrame) renderBodyLine(line string) string {
	line = preserveBackground(line, f.bgColorNum)
	lineDisplayWidth, plainASCII := plainASCIIWidth(line)
	if !plainASCII {
		lineDisplayWidth = tuiStringWidth(line)
	}
	if lineDisplayWidth > f.innerWidth {
		line = truncateLineToDisplayWidth(line, f.innerWidth)
		line = preserveBackground(line, f.bgColorNum)
		return wrapLineWithBackgroundAndRail(f.marginPrefix, f.innerPrefix, line, f.innerSuffix, f.bgSeq, f.marginSuffix, f.railSeq, 0)
	}
	pad := 0
	if lineDisplayWidth < f.innerWidth {
		pad = f.innerWidth - lineDisplayWidth
	}
	return wrapLineWithBackgroundAndRail(f.marginPrefix, f.innerPrefix, line, f.innerSuffix, f.bgSeq, f.marginSuffix, f.railSeq, pad)
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
func renderPrewrappedCard(style lipgloss.Style, innerWidth int, lines []string, bgColorNum string, railSeq string) []string {
	frame := newPrewrappedCardFrame(style, innerWidth, bgColorNum, railSeq)
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
func renderPrewrappedToolCard(style lipgloss.Style, cardWidth int, title string, body []string, bgColorNum string, railSeq string) []string {
	final := make([]string, 0, len(body)+2)
	final = append(final, title, "")
	final = append(final, body...)
	final = preserveCardBg(final, bgColorNum)
	return renderPrewrappedCard(style, cardWidth, final, bgColorNum, railSeq)
}

// railANSISeq returns the ANSI foreground sequence for the conversation rail
// matching the given card kind, or "" if the kind has no rail.
func railANSISeq(kind string, focused bool) string {
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
		return "\x1b[38;5;" + color + "m"
	}
	switch kind {
	case "user":
		return pick(currentTheme.RailUserFg, currentTheme.RailUserFocusedFg)
	case "assistant":
		return pick(currentTheme.RailAssistantFg, currentTheme.RailAssistantFocusedFg)
	case "tool":
		return pick(currentTheme.RailToolFg, currentTheme.RailToolFocusedFg)
	case "thinking":
		return pick(currentTheme.RailThinkingFg, currentTheme.RailThinkingFocusedFg)
	case "error":
		return pick(currentTheme.RailErrorFg, currentTheme.RailErrorFocusedFg)
	default:
		return ""
	}
}

func renderUserText(text string, width int) []string {
	// Preserve original newlines and indentation; expand tabs; soft-wrap by display width.
	text = sanitizeDisplayText(text)
	return wrapPreformattedText(text, width)
}

func expandTabsForDisplay(s string, tabWidth int) string {
	if tabWidth <= 0 || !strings.ContainsRune(s, '\t') {
		return s
	}
	var b strings.Builder
	col := 0
	for len(s) > 0 {
		cluster, w := ansi.FirstGraphemeCluster(s, tuiWidthMethod)
		if cluster == "" {
			break
		}
		if cluster == "\t" {
			spaces := tabWidth - (col % tabWidth)
			if spaces <= 0 {
				spaces = tabWidth
			}
			b.WriteString(strings.Repeat(" ", spaces))
			col += spaces
			s = s[len(cluster):]
			continue
		}
		b.WriteString(cluster)
		col += w
		s = s[len(cluster):]
	}
	return b.String()
}

// expandTabsForDisplayANSI expands literal tab characters in a string that may
// contain ANSI escape sequences. ANSI sequences are treated as zero-width when
// calculating tab stops.
func expandTabsForDisplayANSI(s string, tabWidth int) string {
	if tabWidth <= 0 || !strings.ContainsRune(s, '\t') {
		return s
	}
	var b strings.Builder
	col := 0
	for i := 0; i < len(s); {
		if s[i] == '\x1b' {
			end := skipANSISequence(s, i)
			if end <= i {
				end = i + 1
			}
			b.WriteString(s[i:end])
			i = end
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 0 {
			break
		}
		if r == '\t' {
			spaces := tabWidth - (col % tabWidth)
			if spaces <= 0 {
				spaces = tabWidth
			}
			b.WriteString(strings.Repeat(" ", spaces))
			col += spaces
			i += size
			continue
		}
		b.WriteRune(r)
		col += tuiStringWidth(string(r))
		i += size
	}
	return b.String()
}

func wrapPreformattedText(text string, width int) []string {
	if width <= 0 {
		width = 80
	}
	if text == "" {
		return []string{""}
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	var result []string
	for line := range strings.SplitSeq(text, "\n") {
		expanded := expandTabsForDisplay(line, preformattedTabWidth)
		if expanded == "" {
			result = append(result, "")
			continue
		}
		wrapped := tuiHardwrap(expanded, width)
		result = append(result, wrapped...)
	}
	if len(result) == 0 {
		return []string{""}
	}
	return result
}

// wrapText word-wraps text to the given column width (in display columns).
// It splits on existing newlines first, then wraps each paragraph by words.
// Uses the viewport width method so wrap break points match final cell rendering.
func wrapText(text string, width int) []string {
	if width <= 0 {
		width = 80
	}
	if text == "" {
		return []string{""}
	}
	if isPlainASCIIWrapText(text) {
		return wrapPlainASCIIText(text, width)
	}

	var result []string
	for para := range strings.SplitSeq(text, "\n") {
		if para == "" {
			result = append(result, "")
			continue
		}

		trimmed := strings.TrimLeft(para, " \t")
		indent := para[:len(para)-len(trimmed)]
		indentWidth := tuiStringWidth(indent)
		if trimmed == "" {
			result = append(result, "")
			continue
		}

		var cur strings.Builder
		cur.WriteString(indent)
		curWidth := indentWidth
		for i := 0; i < len(trimmed); {
			for i < len(trimmed) {
				r, size := utf8.DecodeRuneInString(trimmed[i:])
				if !unicode.IsSpace(r) {
					break
				}
				i += size
			}
			if i >= len(trimmed) {
				break
			}

			wordStart := i
			for i < len(trimmed) {
				r, size := utf8.DecodeRuneInString(trimmed[i:])
				if unicode.IsSpace(r) {
					break
				}
				i += size
			}
			word := trimmed[wordStart:i]
			wordWidth := tuiStringWidth(word)
			if curWidth == indentWidth {
				appendWord(&result, &cur, &curWidth, word, wordWidth, width)
			} else if curWidth+1+wordWidth > width {
				result = append(result, cur.String())
				cur.Reset()
				cur.WriteString(indent)
				curWidth = indentWidth
				appendWord(&result, &cur, &curWidth, word, wordWidth, width)
			} else {
				cur.WriteByte(' ')
				cur.WriteString(word)
				curWidth += 1 + wordWidth
			}
		}
		if cur.Len() > 0 {
			result = append(result, cur.String())
		}
	}

	if len(result) == 0 {
		result = append(result, "")
	}
	return result
}

func isPlainASCIIWrapText(text string) bool {
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c == '\x1b' || c == '\t' || c >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func wrapPlainASCIIText(text string, width int) []string {
	var result []string
	for para := range strings.SplitSeq(text, "\n") {
		if para == "" {
			result = append(result, "")
			continue
		}
		trimmed := strings.TrimLeft(para, " ")
		indent := para[:len(para)-len(trimmed)]
		indentWidth := len(indent)
		if trimmed == "" {
			result = append(result, "")
			continue
		}
		var cur strings.Builder
		cur.WriteString(indent)
		curWidth := indentWidth
		for i := 0; i < len(trimmed); {
			for i < len(trimmed) && trimmed[i] == ' ' {
				i++
			}
			if i >= len(trimmed) {
				break
			}
			wordStart := i
			for i < len(trimmed) && trimmed[i] != ' ' {
				i++
			}
			word := trimmed[wordStart:i]
			wordWidth := len(word)
			if curWidth == indentWidth {
				appendASCIIWord(&result, &cur, &curWidth, word, width)
			} else if curWidth+1+wordWidth > width {
				result = append(result, cur.String())
				cur.Reset()
				cur.WriteString(indent)
				curWidth = indentWidth
				appendASCIIWord(&result, &cur, &curWidth, word, width)
			} else {
				cur.WriteByte(' ')
				cur.WriteString(word)
				curWidth += 1 + wordWidth
			}
		}
		if cur.Len() > 0 {
			result = append(result, cur.String())
		}
	}
	if len(result) == 0 {
		return []string{""}
	}
	return result
}

func appendASCIIWord(result *[]string, cur *strings.Builder, curWidth *int, word string, width int) {
	if len(word) <= width {
		cur.WriteString(word)
		*curWidth += len(word)
		return
	}
	for len(word) > 0 {
		space := width - *curWidth
		if space <= 0 {
			*result = append(*result, cur.String())
			cur.Reset()
			*curWidth = 0
			space = width
		}
		n := min(space, len(word))
		cur.WriteString(word[:n])
		*curWidth += n
		word = word[n:]
		if len(word) > 0 {
			*result = append(*result, cur.String())
			cur.Reset()
			*curWidth = 0
		}
	}
}

// appendWord adds a word to the builder, breaking it by runes if its display
// width exceeds the available line width. Uses the viewport width method so
// columns match final rendering.
func appendWord(result *[]string, cur *strings.Builder, curWidth *int, word string, wordWidth, width int) {
	if wordWidth <= width {
		cur.WriteString(word)
		*curWidth += wordWidth
		return
	}
	// Word is wider than the line — break it by grapheme clusters.
	parts := tuiHardwrap(word, width)
	if len(parts) == 0 {
		return
	}
	for i, part := range parts {
		if i > 0 {
			*result = append(*result, cur.String())
			cur.Reset()
			*curWidth = 0
		}
		cur.WriteString(part)
		*curWidth += tuiStringWidth(part)
	}
}

// truncateOneLine returns the first line of s, trimmed and truncated to maxLen
// display columns (with "..." appended when truncated). CJK characters are
// correctly counted as 2 columns.
func truncateOneLine(s string, maxLen int) string {
	if maxLen <= 0 {
		maxLen = 20
	}
	// Take first line.
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	s = strings.TrimSpace(s)
	if runewidth.StringWidth(s) <= maxLen {
		return s
	}
	if maxLen > 3 {
		return runewidth.Truncate(s, maxLen, "...")
	}
	return runewidth.Truncate(s, maxLen, "")
}
