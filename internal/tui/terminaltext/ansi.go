package terminaltext

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/keakon/x/ansi"
)

// BackgroundSequence converts a color string to an ANSI SGR background sequence.
// It handles:
//   - ANSI 256-color numbers (e.g. "235") → "\x1b[48;5;235m"
//   - Hex colors (e.g. "#1e3d2e") → "\x1b[48;2;R;G;Bm" (truecolor)
//   - Empty string → ""
//
// This ensures PreserveBackground works correctly on terminals that support
// truecolor profiles (most modern terminals) when theme values use hex.
func BackgroundSequence(colorStr string) string {
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

// PreserveBackground re-inserts bgColor after ANSI resets inside a rendered
// string so an outer container background survives inner Render() calls.
// Supports both ANSI 256-color palette indices and hex colors via BackgroundSequence.
func PreserveBackground(line, bgColor string) string {
	if bgColor == "" || line == "" {
		return line
	}
	if !strings.Contains(line, "\x1b[") {
		return line
	}
	bgSeq := BackgroundSequence(bgColor)
	if bgSeq == "" {
		return line
	}
	// One pass replaces the four ReplaceAll rounds it used to take. Every target
	// is a reset sequence, so a single scan over the escape sequences can emit
	// the background sequence in place. Card rendering calls this for every body
	// line of every frame, and the four passes each rescanned and reallocated
	// the whole line — the dominant cost of a large streaming apply_patch card.
	//
	// A sequence that is not one of the four resets resumes the scan one byte in
	// rather than past the sequence: a reset can start inside what looks like an
	// unterminated escape (`ESC [ ESC [ 0 m`), and skipping the outer run whole
	// would swallow it. That keeps this byte-identical to the plain substring
	// replacement it replaced.
	var b strings.Builder
	b.Grow(len(line) + 4*len(bgSeq))
	last := 0
	for i := 0; i < len(line); {
		if line[i] != '\x1b' {
			i++
			continue
		}
		end := SkipANSI(line, i)
		if seq := line[i:end]; seq == "\x1b[0m" || seq == "\x1b[m" || seq == "\x1b[49m" || seq == "\x1b[39m" {
			b.WriteString(line[last:end])
			b.WriteString(bgSeq)
			last = end
			i = end
			continue
		}
		i++
	}
	if last == 0 {
		return line
	}
	b.WriteString(line[last:])
	return b.String()
}

func StripANSI(s string) string {
	return ansi.Strip(s)
}

func SkipANSI(s string, i int) int {
	if i >= len(s) || s[i] != '\x1b' {
		return i
	}
	i++
	if i >= len(s) {
		return i
	}
	ch := s[i]
	switch ch {
	case '[':
		i++
		for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
			i++
		}
		if i < len(s) {
			i++
		}
	case ']':
		i++
		for i < len(s) && s[i] != '\x07' {
			if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '\\' {
				i += 2
				break
			}
			i++
		}
		if i < len(s) && s[i] == '\x07' {
			i++
		}
	default:
		i++
	}
	return i
}

func Truncate(s string, width int) string {
	if width <= 0 {
		return s
	}
	if Width(s) <= width {
		return s
	}
	out := Cut(s, 0, width)

	if len(out) < len(s) && strings.Contains(s, "\x1b[") {
		baseBg := firstSGRBackgroundSeq(s)
		out += "\x1b[49m"
		if baseBg != "" {
			out += baseBg
		}
		out += ansi.ResetStyle
	}
	return out
}

func firstSGRBackgroundSeq(s string) string {
	for i := 0; i < len(s); {
		if s[i] != '\x1b' {
			i++
			continue
		}
		if i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) && s[j] == 'm' {
				if sgrSetsBackground(s[i+2 : j]) {
					return s[i : j+1]
				}
				i = j + 1
				continue
			}
		}
		i = SkipANSI(s, i)
	}
	return ""
}

func sgrSetsBackground(params string) bool {
	if params == "" {
		return false
	}
	parts := strings.Split(params, ";")
	for idx := range parts {
		if parts[idx] != "48" {
			continue
		}
		if idx+2 >= len(parts) {
			continue
		}
		mode := parts[idx+1]
		switch mode {
		case "5":
			return true
		case "2":
			if idx+4 < len(parts) {
				return true
			}
		}
	}
	return false
}

var csiPattern = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// StripCSI removes simple CSI sequences containing numeric parameters.
func StripCSI(s string) string { return csiPattern.ReplaceAllString(s, "") }
