package markdownrender

import (
	"strings"

	"github.com/keakon/chord/internal/tui/terminaltext"
)

func normalizeRenderedMarkdownIndent(lines []string) []string {
	minIndent := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		n := terminaltext.CountIndent(line)
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
