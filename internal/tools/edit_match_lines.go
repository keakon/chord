package tools

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// countLineBreaks counts the line breaks inside content[from:to] the way read
// numbers lines: LF, CRLF and a bare CR each end one line. A CR at to-1 whose
// LF sits at to is left for that LF, so counts over adjacent ranges add up to
// the count over their union.
func countLineBreaks(content string, from, to int) int {
	s := content[from:to]
	n := strings.Count(s, "\n")
	if strings.IndexByte(s, '\r') < 0 {
		return n
	}
	n += strings.Count(s, "\r") - strings.Count(s, "\r\n")
	if s[len(s)-1] == '\r' && to < len(content) && content[to] == '\n' {
		n--
	}
	return n
}

// lineNumbersAt converts increasing byte offsets into 1-based line numbers. One
// counter advances over the gaps between offsets instead of recounting every
// prefix, so the cost stays linear in the scanned text.
func lineNumbersAt(content string, offsets []int) []int {
	lines := make([]int, len(offsets))
	line, prev := 1, 0
	for i, offset := range offsets {
		line += countLineBreaks(content, prev, offset)
		lines[i] = line
		prev = offset
	}
	return lines
}

// exactMatchOffsets returns the byte offsets of up to limit non-overlapping
// matches of needle, scanning left to right like strings.Count. A negative
// limit collects every match.
func exactMatchOffsets(content, needle string, limit int) []int {
	if needle == "" || limit == 0 {
		return nil
	}
	var offsets []int
	for offset := 0; offset < len(content) && (limit < 0 || len(offsets) < limit); {
		at := strings.Index(content[offset:], needle)
		if at < 0 {
			break
		}
		offsets = append(offsets, offset+at)
		offset += at + len(needle)
	}
	return offsets
}

// maxMatchLinesShown bounds the match locations an ambiguity error prints, and
// with them the scan: a one-byte old_string in a large file can match millions
// of times, and listing them all costs far more than the message is worth.
const maxMatchLinesShown = 12

// matchLineNumbers returns the one-based source line for at most limit
// non-overlapping exact matches. The edit error uses these locations to let the
// model choose a unique context block without another exploratory read; the
// total match count comes from the caller's own count.
func matchLineNumbers(content, needle string, limit int) []int {
	if limit <= 0 {
		return nil
	}
	return lineNumbersAt(content, exactMatchOffsets(content, needle, limit))
}

func formatMatchLines(lines []int, total int) string {
	if len(lines) == 0 {
		return "unknown"
	}
	parts := make([]string, len(lines))
	for i, line := range lines {
		parts[i] = strconv.Itoa(line)
	}
	result := strings.Join(parts, ", ")
	if omitted := total - len(lines); omitted > 0 {
		result += fmt.Sprintf(", … (+%d more)", omitted)
	}
	return result
}

// tolerantMatchLines converts normalized-space match start indices to 1-based
// file line numbers, so callers can report where a tolerant replacement
// landed. Spans index content's runes, not its bytes, so the rune offsets are
// walked forward to byte offsets once, in match order; matches never overlap,
// which keeps both the walk and the returned lines strictly increasing.
func tolerantMatchLines(content string, contentSpans []punctSpan, starts []int) []int {
	offsets := make([]int, len(starts))
	runeIndex, byteOffset := 0, 0
	for i, m := range starts {
		for target := contentSpans[m].start; runeIndex < target; runeIndex++ {
			_, size := utf8.DecodeRuneInString(content[byteOffset:])
			byteOffset += size
		}
		offsets[i] = byteOffset
	}
	return lineNumbersAt(content, offsets)
}

// formatTolerantMatchLines renders the replacement landing lines for success
// messages: "at line 12" for a single hit, "at lines 12, 45" for many.
func formatTolerantMatchLines(lines []int) string {
	if len(lines) == 0 {
		return ""
	}
	if len(lines) == 1 {
		return fmt.Sprintf(" at line %d", lines[0])
	}
	parts := make([]string, len(lines))
	for i, l := range lines {
		parts[i] = strconv.Itoa(l)
	}
	return " at lines " + strings.Join(parts, ", ")
}
