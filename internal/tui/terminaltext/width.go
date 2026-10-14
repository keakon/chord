package terminaltext

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/keakon/x/ansi"
	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/textclean"
)

func Width(s string) int {
	return WidthMethod.StringWidth(s)
}

func ASCIIWidth(s string) (int, bool) {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c >= 0x7f {
			return 0, false
		}
	}
	return len(s), true
}

func Cut(s string, left, right int) string {
	return WidthMethod.Cut(s, left, right)
}

func WrapHeadTail(s string, width int) (string, string) {
	if width <= 0 || s == "" {
		return s, ""
	}
	totalWidth := Width(s)
	head := Cut(s, 0, width)
	headWidth := Width(head)
	for headWidth == 0 && width < totalWidth {
		width++
		head = Cut(s, 0, width)
		headWidth = Width(head)
	}
	if head != "" {
		return head, Cut(s, headWidth, totalWidth)
	}
	cluster, _ := ansi.FirstGraphemeCluster(s, WidthMethod)
	if cluster == "" {
		return s, ""
	}
	return cluster, s[len(cluster):]
}

func Hardwrap(s string, width int) []string {
	wrapped := WidthMethod.Hardwrap(s, width, true)
	parts := strings.Split(wrapped, "\n")
	if len(parts) > 1 && parts[0] == "" {
		parts = parts[1:]
	}
	return parts
}

// Pad pads line with spaces to exactly width display columns
// so block backgrounds extend to full width and selection aligns. Uses the same
// grapheme-aware width method as the viewport so emoji/wide chars match highlight column math.
func Pad(line string, width int) string {
	w := Width(line)
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

// PadOutsideStyle keeps viewport padding outside a completed
// ANSI style. Card renderers close their background before the outer margin;
// inserting the viewport's spare columns before that reset would extend a
// capped card's background to the viewport edge.
func PadOutsideStyle(line string, width int) string {
	w := Width(line)
	if w >= width {
		return line
	}
	padding := strings.Repeat(" ", width-w)
	if strings.HasSuffix(line, "\x1b[0m") || strings.HasSuffix(line, "\x1b[m") {
		return line + padding
	}
	return Pad(line, width)
}

// EnsureReset appends a final SGR reset when a styled line still ends
// with an active style sequence. This is especially important for assistant
// fenced code lines, where we deliberately re-apply the code-block background
// after inner resets inside the line; without a final close, outer card padding
// can inherit the code-block background and visually "leak" to the right.
func EnsureReset(line string) string {
	if line == "" || !strings.Contains(line, "\x1b[") {
		return line
	}
	if strings.HasSuffix(line, "\x1b[0m") || strings.HasSuffix(line, ansi.ResetStyle) {
		return line
	}
	return line + ansi.ResetStyle
}

func ExpandTabs(s string, tabWidth int) string {
	if tabWidth <= 0 || !strings.ContainsRune(s, '\t') {
		return s
	}
	var b strings.Builder
	col := 0
	for len(s) > 0 {
		cluster, w := ansi.FirstGraphemeCluster(s, WidthMethod)
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

// ExpandTabsANSI expands literal tab characters in a string that may
// contain ANSI escape sequences. ANSI sequences are treated as zero-width when
// calculating tab stops.
func ExpandTabsANSI(s string, tabWidth int) string {
	if tabWidth <= 0 || !strings.ContainsRune(s, '\t') {
		return s
	}
	var b strings.Builder
	col := 0
	for i := 0; i < len(s); {
		if s[i] == '\x1b' {
			end := SkipANSI(s, i)
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
		col += Width(string(r))
		i += size
	}
	return b.String()
}

func WrapPreformatted(text string, width int) []string {
	if width <= 0 {
		width = 80
	}
	if text == "" {
		return []string{""}
	}
	text = textclean.NormalizeNewlines(text)
	var result []string
	for line := range strings.SplitSeq(text, "\n") {
		expanded := ExpandTabs(line, TabWidth)
		if expanded == "" {
			result = append(result, "")
			continue
		}
		wrapped := Hardwrap(expanded, width)
		result = append(result, wrapped...)
	}
	if len(result) == 0 {
		return []string{""}
	}
	return result
}

// Wrap word-wraps text to the given column width (in display columns).
// It splits on existing newlines first, then wraps each paragraph by words.
// Uses the viewport width method so wrap break points match final cell rendering.
func Wrap(text string, width int) []string {
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
		indentWidth := Width(indent)
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
			wordWidth := Width(word)
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
	if len(word) <= width-*curWidth {
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
	if wordWidth <= width-*curWidth {
		cur.WriteString(word)
		*curWidth += wordWidth
		return
	}
	// Fill the current line first, then hard-wrap the remainder.
	// Edge case: when the remaining space is 1 column and the next grapheme is
	// 2 columns wide, WrapHeadTail widens to keep the grapheme intact, so
	// that line can exceed width by one column. Callers only invoke this with
	// curWidth at the indent width, so it needs width-indent < 2 to trigger.
	for word != "" {
		available := width - *curWidth
		if available <= 0 {
			*result = append(*result, cur.String())
			cur.Reset()
			*curWidth = 0
			available = width
		}
		part, rest := WrapHeadTail(word, available)
		if part == "" {
			return
		}
		cur.WriteString(part)
		*curWidth += Width(part)
		word = rest
		if word != "" {
			*result = append(*result, cur.String())
			cur.Reset()
			*curWidth = 0
		}
	}
}

// TruncateOneLine returns the first line of s, trimmed and truncated to maxLen
// display columns (with "..." appended when truncated). CJK characters are
// correctly counted as 2 columns.
func TruncateOneLine(s string, maxLen int) string {
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

// WidthMethod keeps wrapping, truncation and cell geometry on one algorithm.
const WidthMethod = ansi.GraphemeWidth

// TabWidth is the visual tab stop used for preformatted content.
const TabWidth = 4
