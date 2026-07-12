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

// TruncateStringHeadTail keeps the first headRunes and last tailRunes runes of
// s, separated by sep, never splitting a multi-byte UTF-8 rune. It is used for
// middle-elision truncation where both ends must remain valid UTF-8 (e.g.
// compacted history snippets written to disk and later re-read). When the whole
// string fits within headRunes+tailRunes+len(sep) runes it is returned whole.
func TruncateStringHeadTail(s string, headRunes, tailRunes int, sep string) string {
	if headRunes < 0 {
		headRunes = 0
	}
	if tailRunes < 0 {
		tailRunes = 0
	}
	total := utf8.RuneCountInString(s)
	sepRunes := utf8.RuneCountInString(sep)
	if total <= headRunes+tailRunes+sepRunes {
		return s
	}
	if total == len(s) {
		return s[:headRunes] + sep + s[len(s)-tailRunes:]
	}
	head := TruncateStringFirstRunes(s, headRunes)
	tail := TruncateStringLastRunes(s, tailRunes)
	return head + sep + tail
}

// TruncateStringFirstRunes returns the prefix of s containing at most n runes,
// always ending on a rune boundary.
func TruncateStringFirstRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	for i := range s {
		if n == 0 {
			return s[:i]
		}
		n--
	}
	return s
}

// TruncateStringBytes returns the longest prefix of s that fits within maxBytes
// bytes without splitting a multi-byte UTF-8 rune. It is used where the budget
// is expressed in bytes (e.g. hook automation bodies) but the result must stay
// valid UTF-8.
func TruncateStringBytes(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	// Back off to the start of the rune that straddles the byte limit.
	end := maxBytes
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}

// TruncateStringLastRunes returns the suffix of s containing at most n runes,
// always starting on a rune boundary.
func TruncateStringLastRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	total := utf8.RuneCountInString(s)
	if total <= n {
		return s
	}
	skip := total - n
	for i := range s {
		if skip == 0 {
			return s[i:]
		}
		skip--
	}
	return s
}
