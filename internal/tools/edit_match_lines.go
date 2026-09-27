package tools

import (
	"fmt"
	"strconv"
	"strings"
)

// tolerantMatchLines converts normalized-space match start indices to
// 1-based file line numbers, so callers can report where a tolerant
// replacement landed. A match starts at the normalized span's original
// rune offset; counting newlines in content[:offset] yields the line number
// directly. Matches never overlap, so the returned lines are strictly
// increasing. The line counter advances over the disjoint segments between
// match starts instead of recounting the whole prefix for every match.

func tolerantMatchLines(content string, contentSpans []punctSpan, starts []int) []int {
	lines := make([]int, 0, len(starts))
	line := 1
	prev := 0
	for _, m := range starts {
		offset := contentSpans[m].start
		line += strings.Count(content[prev:offset], "\n")
		lines = append(lines, line)
		prev = offset
	}
	return lines
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
