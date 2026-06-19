package llm

import "unicode/utf8"

// TruncateStringRunes truncates s at a UTF-8 rune boundary and appends suffix
// only when truncation occurs.
func TruncateStringRunes(s string, maxRunes int, suffix string) string {
	if maxRunes <= 0 {
		if s == "" {
			return ""
		}
		return suffix
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	for i := range s {
		if maxRunes == 0 {
			return s[:i] + suffix
		}
		maxRunes--
	}
	return s
}
