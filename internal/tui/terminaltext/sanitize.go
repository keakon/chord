package terminaltext

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

func Sanitize(s string) string {
	if s == "" {
		return ""
	}
	// One pass detects control characters, the 0xEF lead byte of
	// U+FE0E/U+FE0F and the 0xF0 lead byte of emoji modifiers, so clean
	// text — the overwhelming majority — pays a single scan instead of
	// separate glyph probes. A control character ends the scan early, so a
	// glyph hiding after it is only covered by also normalizing on
	// needsSanitization; the normalizers' own probes keep that rare path
	// honest. Glyph normalization still runs before the control rebuild so
	// every downstream path (markdown, streaming tail, Wrap fallbacks)
	// measures width consistently.
	needsSanitization := false
	maybeGlyph := false
	for i := 0; i < len(s); {
		c := s[i]
		if c == '\r' || ((c < 0x20 && c != '\t' && c != '\n') || c == 0x7f) {
			needsSanitization = true
			break
		}
		if c == 0xEF || c == 0xF0 {
			maybeGlyph = true
		}
		if c < utf8.RuneSelf {
			i++
			continue
		}
		if _, _, ok := c1ControlLiteral(s[i:]); ok {
			needsSanitization = true
			break
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		if size <= 0 {
			size = 1
		}
		i += size
	}
	if maybeGlyph || needsSanitization {
		s = NormalizeGlyphs(s)
	}
	if !needsSanitization {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\r':
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
				continue
			}
			b.WriteString(`\r`)
			i++
		case c == '\t' || c == '\n':
			b.WriteByte(c)
			i++
		case c < 0x20 || c == 0x7f:
			b.WriteString(displayControlLiteral(c))
			i++
		default:
			if c == '\x1b' {
				b.WriteString(displayControlLiteral(c))
				i++
				continue
			}
			if c < utf8.RuneSelf {
				b.WriteByte(c)
				i++
				continue
			}
			if literal, size, ok := c1ControlLiteral(s[i:]); ok {
				b.WriteString(literal)
				i += size
				continue
			}
			_, size := utf8.DecodeRuneInString(s[i:])
			if size <= 0 {
				size = 1
			}
			b.WriteString(s[i : i+size])
			i += size
		}
	}
	return b.String()
}

// c1ControlLiteral recognizes both UTF-8 encoded C1 controls and raw C1
// bytes in otherwise invalid input. Valid UTF-8 continuation bytes must not be
// treated as controls individually, so callers invoke this at rune boundaries.
func c1ControlLiteral(s string) (literal string, size int, ok bool) {
	if s == "" {
		return "", 0, false
	}
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError && size == 1 && s[0] >= 0x80 && s[0] <= 0x9f {
		return fmt.Sprintf(`\x%02x`, s[0]), 1, true
	}
	if r < 0x80 || r > 0x9f {
		return "", 0, false
	}
	return fmt.Sprintf(`\x%02x`, r), size, true
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

// SanitizeSGR behaves like Sanitize but keeps
// well-formed SGR sequences (ESC [ ... m) intact, so styled header fragments
// such as struck-through ignored arguments survive header sanitization. Any
// other escape sequence is still neutralized to its control literal.
func SanitizeSGR(s string) string {
	if s == "" {
		return ""
	}
	if !displayNeedsSGRAwareSanitize(s) {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == '\x1b' {
			if j := SkipANSI(s, i); j > i+1 && s[j-1] == 'm' && s[i+1] == '[' {
				b.WriteString(s[i:j])
				i = j
				continue
			}
		}
		c := s[i]
		switch {
		case c == '\r':
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
				continue
			}
			b.WriteString(`\r`)
			i++
		case c == '\t' || c == '\n':
			b.WriteByte(c)
			i++
		case c < 0x20 || c == 0x7f:
			b.WriteString(displayControlLiteral(c))
			i++
		default:
			if c == '\x1b' {
				b.WriteString(displayControlLiteral(c))
				i++
				continue
			}
			if c < utf8.RuneSelf {
				b.WriteByte(c)
				i++
				continue
			}
			if literal, size, ok := c1ControlLiteral(s[i:]); ok {
				b.WriteString(literal)
				i += size
				continue
			}
			_, size := utf8.DecodeRuneInString(s[i:])
			if size <= 0 {
				size = 1
			}
			b.WriteString(s[i : i+size])
			i += size
		}
	}
	return b.String()
}

func displayNeedsSGRAwareSanitize(s string) bool {
	for i := 0; i < len(s); {
		c := s[i]
		if c != '\x1b' {
			if c == '\r' || ((c < 0x20 && c != '\t' && c != '\n') || c == 0x7f) {
				return true
			}
			if c < utf8.RuneSelf {
				i++
				continue
			}
			if _, _, ok := c1ControlLiteral(s[i:]); ok {
				return true
			}
			_, size := utf8.DecodeRuneInString(s[i:])
			if size <= 0 {
				size = 1
			}
			i += size
			continue
		}
		j := SkipANSI(s, i)
		if j <= i+1 || s[j-1] != 'm' || (i+1 < len(s) && s[i+1] != '[') {
			return true
		}
		i = j
	}
	return false
}
