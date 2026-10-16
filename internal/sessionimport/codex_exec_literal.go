package sessionimport

import (
	"strconv"
	"strings"
)

// parseCodexJSValue evaluates the literal subset of JavaScript that Codex emits
// in `exec` argument sources. It exists because those sources are JavaScript,
// not JSON: Codex writes `{cmd:"git status",workdir:"/repo"}` with unquoted
// keys, single-quoted strings, and trailing commas, none of which
// encoding/json accepts.
//
// The parser is deliberately literal-only. An identifier reference, a function
// call, a template substitution, or any other expression can only be resolved by
// running the script, so it reports failure instead of guessing — the caller
// then falls back to a note. Returning ok=false never means "no argument".
func parseCodexJSValue(src string) (any, bool) {
	p := codexJSParser{src: src}
	p.skipSpace()
	value, ok := p.value()
	if !ok {
		return nil, false
	}
	p.skipSpace()
	if p.i != len(p.src) {
		// Trailing operators or a second expression: not a single literal.
		return nil, false
	}
	return value, true
}

type codexJSParser struct {
	src string
	i   int
}

func (p *codexJSParser) value() (any, bool) {
	if p.i >= len(p.src) {
		return nil, false
	}
	switch c := p.src[p.i]; {
	case c == '{':
		return p.object()
	case c == '[':
		return p.array()
	case c == '\'' || c == '"' || c == '`':
		return p.string()
	case c == '-' || c == '.' || (c >= '0' && c <= '9'):
		return p.number()
	case isJSIdentPart(c):
		return p.ident()
	}
	return nil, false
}

func (p *codexJSParser) object() (any, bool) {
	p.i++ // consume '{'
	out := make(map[string]any)
	p.skipSpace()
	if p.i < len(p.src) && p.src[p.i] == '}' {
		p.i++
		return out, true
	}
	for {
		p.skipSpace()
		key, ok := p.key()
		if !ok {
			return nil, false
		}
		p.skipSpace()
		if p.i >= len(p.src) || p.src[p.i] != ':' {
			return nil, false
		}
		p.i++
		p.skipSpace()
		value, ok := p.value()
		if !ok {
			return nil, false
		}
		out[key] = value
		p.skipSpace()
		if p.i >= len(p.src) {
			return nil, false
		}
		switch p.src[p.i] {
		case ',':
			p.i++
			p.skipSpace()
			if p.i < len(p.src) && p.src[p.i] == '}' {
				// Trailing comma.
				p.i++
				return out, true
			}
		case '}':
			p.i++
			return out, true
		default:
			return nil, false
		}
	}
}

func (p *codexJSParser) key() (string, bool) {
	if p.i >= len(p.src) {
		return "", false
	}
	switch c := p.src[p.i]; {
	case c == '\'' || c == '"' || c == '`':
		value, ok := p.string()
		if !ok {
			return "", false
		}
		s, ok := value.(string)
		return s, ok
	case isJSIdentPart(c):
		start := p.i
		for p.i < len(p.src) && isJSIdentPart(p.src[p.i]) {
			p.i++
		}
		// Unquoted keys are the normal shape, and numeric keys are legal.
		return p.src[start:p.i], true
	}
	return "", false
}

func (p *codexJSParser) array() (any, bool) {
	p.i++ // consume '['
	out := []any{}
	p.skipSpace()
	if p.i < len(p.src) && p.src[p.i] == ']' {
		p.i++
		return out, true
	}
	for {
		p.skipSpace()
		value, ok := p.value()
		if !ok {
			return nil, false
		}
		out = append(out, value)
		p.skipSpace()
		if p.i >= len(p.src) {
			return nil, false
		}
		switch p.src[p.i] {
		case ',':
			p.i++
			p.skipSpace()
			if p.i < len(p.src) && p.src[p.i] == ']' {
				// Trailing comma.
				p.i++
				return out, true
			}
		case ']':
			p.i++
			return out, true
		default:
			return nil, false
		}
	}
}

// string parses a single-, double-, or back-quoted literal. A template with a
// `${...}` substitution is rejected: its value depends on runtime state.
func (p *codexJSParser) string() (any, bool) {
	quote := p.src[p.i]
	var b strings.Builder
	for i := p.i + 1; i < len(p.src); {
		switch c := p.src[i]; {
		case c == quote:
			p.i = i + 1
			return b.String(), true
		case c == '\\':
			next, ok := codexJSUnescape(p.src, i, &b)
			if !ok {
				return nil, false
			}
			i = next
		case c == '\n':
			if quote != '`' {
				return nil, false
			}
			b.WriteByte(c)
			i++
		case c == '$' && quote == '`' && codexHasPrefixAt(p.src, i+1, "{"):
			return nil, false
		default:
			b.WriteByte(c)
			i++
		}
	}
	return nil, false
}

// codexJSUnescape decodes the escape sequence starting at i (which points at the
// backslash) and returns the index just past it.
func codexJSUnescape(s string, i int, b *strings.Builder) (int, bool) {
	if i+1 >= len(s) {
		return 0, false
	}
	switch esc := s[i+1]; esc {
	case 'n':
		b.WriteByte('\n')
		return i + 2, true
	case 'r':
		b.WriteByte('\r')
		return i + 2, true
	case 't':
		b.WriteByte('\t')
		return i + 2, true
	case 'b':
		b.WriteByte('\b')
		return i + 2, true
	case 'f':
		b.WriteByte('\f')
		return i + 2, true
	case 'v':
		b.WriteByte('\v')
		return i + 2, true
	case '0':
		b.WriteByte(0)
		return i + 2, true
	case '\n':
		// Line continuation.
		return i + 2, true
	case '\r':
		if codexHasPrefixAt(s, i+2, "\n") {
			return i + 3, true
		}
		return i + 2, true
	case 'u':
		return codexJSUnescapeUnicode(s, i, b)
	case 'x':
		if i+4 > len(s) {
			return 0, false
		}
		v, err := strconv.ParseUint(s[i+2:i+4], 16, 8)
		if err != nil {
			return 0, false
		}
		b.WriteByte(byte(v))
		return i + 4, true
	default:
		// \' \" \` \\ \/ and any other escaped byte stand for themselves.
		b.WriteByte(esc)
		return i + 2, true
	}
}

// codexJSUnescapeUnicode decodes `\uXXXX` and `\u{XXXXX}`, joining a surrogate
// pair written as two escapes.
func codexJSUnescapeUnicode(s string, i int, b *strings.Builder) (int, bool) {
	j := i + 2
	if j < len(s) && s[j] == '{' {
		end := strings.IndexByte(s[j:], '}')
		if end < 0 {
			return 0, false
		}
		v, err := strconv.ParseUint(s[j+1:j+end], 16, 32)
		if err != nil || v > 0x10FFFF {
			return 0, false
		}
		b.WriteRune(rune(v))
		return j + end + 1, true
	}
	if j+4 > len(s) {
		return 0, false
	}
	v, err := strconv.ParseUint(s[j:j+4], 16, 32)
	if err != nil {
		return 0, false
	}
	next := j + 4
	if v >= 0xD800 && v <= 0xDBFF && next+6 <= len(s) && codexHasPrefixAt(s, next, `\u`) {
		if low, err := strconv.ParseUint(s[next+2:next+6], 16, 32); err == nil && low >= 0xDC00 && low <= 0xDFFF {
			b.WriteRune(rune(0x10000 + (v-0xD800)<<10 + (low - 0xDC00)))
			return next + 6, true
		}
	}
	b.WriteRune(rune(v))
	return next, true
}

func (p *codexJSParser) number() (any, bool) {
	start := p.i
	if p.src[p.i] == '-' {
		p.i++
	}
	for p.i < len(p.src) {
		c := p.src[p.i]
		if c >= '0' && c <= '9' || c == '.' || c == 'e' || c == 'E' {
			p.i++
			continue
		}
		// An exponent sign only continues the number when it follows `e`/`E`.
		if (c == '-' || c == '+') && p.i > start {
			if prev := p.src[p.i-1]; prev == 'e' || prev == 'E' {
				p.i++
				continue
			}
		}
		break
	}
	if p.i == start {
		return nil, false
	}
	// JavaScript has a single number type, so parse as float64. That also keeps
	// the value identical to what encoding/json produces for the same literal,
	// which is what every downstream argument normalizer expects.
	value, err := strconv.ParseFloat(p.src[start:p.i], 64)
	if err != nil {
		return nil, false
	}
	return value, true
}

func (p *codexJSParser) ident() (any, bool) {
	start := p.i
	for p.i < len(p.src) && isJSIdentPart(p.src[p.i]) {
		p.i++
	}
	switch p.src[start:p.i] {
	case "true":
		return true, true
	case "false":
		return false, true
	case "null", "undefined":
		return nil, true
	}
	// A reference or call: only the running script can resolve it.
	return nil, false
}

func (p *codexJSParser) skipSpace() {
	for p.i < len(p.src) {
		switch {
		case p.src[p.i] == ' ' || p.src[p.i] == '\t' || p.src[p.i] == '\n' || p.src[p.i] == '\r':
			p.i++
		case codexHasPrefixAt(p.src, p.i, "//"):
			end := strings.IndexByte(p.src[p.i:], '\n')
			if end < 0 {
				p.i = len(p.src)
				return
			}
			p.i += end
		case codexHasPrefixAt(p.src, p.i, "/*"):
			end := strings.Index(p.src[p.i+2:], "*/")
			if end < 0 {
				p.i = len(p.src)
				return
			}
			p.i += end + 4
		default:
			return
		}
	}
}
