package message

import "unicode"

// IsVisibleTextRune reports whether r contributes visible text on its own.
func IsVisibleTextRune(r rune) bool {
	return !unicode.IsSpace(r) &&
		!unicode.IsControl(r) &&
		!unicode.Is(unicode.Cf, r) &&
		!unicode.Is(unicode.Mn, r) &&
		!unicode.Is(unicode.Me, r)
}

// HasVisibleText reports whether text contains any visible Unicode content.
func HasVisibleText(text string) bool {
	for _, r := range text {
		if IsVisibleTextRune(r) {
			return true
		}
	}
	return false
}

// NormalizeInvisibleText converts wholly invisible text to the canonical empty
// string without changing format or combining characters embedded in visible
// text, preserving emoji ZWJ sequences and other meaningful Unicode content.
func NormalizeInvisibleText(text string) string {
	if HasVisibleText(text) {
		return text
	}
	return ""
}
