package terminaltext

import "strings"

const (
	thinVerticalBorder  = "│"
	heavyVerticalBorder = "┃"
)

// CountIndent counts leading spaces and tabs after vertical border glyphs and
// an optional ANSI reset. Tabs count as one character, matching source indent.
func CountIndent(s string) int {
	// Skip rail prefix characters if present (conversation rail uses │)
	for {
		if strings.HasPrefix(s, "|") {
			s = s[1:]
		} else if strings.HasPrefix(s, thinVerticalBorder) || strings.HasPrefix(s, heavyVerticalBorder) {
			s = s[len(thinVerticalBorder):]
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
