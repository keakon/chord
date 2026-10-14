package texttruncate

import "unicode/utf8"

// Runes truncates s at a UTF-8 rune boundary and appends suffix
// only when truncation occurs.
func Runes(s string, maxRunes int, suffix string) string {
	prefix := FirstRunes(s, maxRunes)
	if len(prefix) < len(s) {
		return prefix + suffix
	}
	return s
}

// HeadTail keeps the first headRunes and last tailRunes runes of
// s, separated by sep, never splitting a multi-byte UTF-8 rune. It is used for
// middle-elision truncation where both ends must remain valid UTF-8 (e.g.
// compacted history snippets written to disk and later re-read). When the whole
// string fits within headRunes+tailRunes+len(sep) runes it is returned whole.
func HeadTail(s string, headRunes, tailRunes int, sep string) string {
	head := FirstRunes(s, headRunes)
	if len(head) == len(s) {
		return s
	}
	tail := LastRunes(s, tailRunes)
	tailStart := len(s) - len(tail)
	if len(head) >= tailStart {
		return s
	}
	// Only inspect enough of the gap to decide whether the separator saves
	// space. This bounds scanning by the retained runes, even for large logs,
	// and avoids adding budgets that could overflow an int.
	gap := s[len(head):tailStart]
	if len(FirstRunes(gap, utf8.RuneCountInString(sep))) == len(gap) {
		return s
	}
	return head + sep + tail
}

// FirstRunes returns the prefix of s containing at most n runes,
// always ending on a rune boundary.
func FirstRunes(s string, n int) string {
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

// Bytes returns the longest prefix of s that fits within maxBytes
// bytes without splitting a multi-byte UTF-8 rune. It is used where the budget
// is expressed in bytes (e.g. hook automation bodies) but the result must stay
// valid UTF-8.
func Bytes(s string, maxBytes int) string {
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

// LastRunes returns the suffix of s containing at most n runes,
// always starting on a rune boundary.
func LastRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	start := len(s)
	for n > 0 && start > 0 {
		if s[start-1] < utf8.RuneSelf {
			start--
		} else {
			_, size := utf8.DecodeLastRuneInString(s[:start])
			start -= size
		}
		n--
	}
	return s[start:]
}

// PrefixBytes returns s unchanged when it fits within n bytes. Otherwise it
// returns the valid UTF-8 prefix within that budget, stopping at an invalid
// sequence or an incomplete rune. Unlike Bytes, it validates a truncated prefix.
func PrefixBytes(s string, n int) string {
	if n >= len(s) {
		return s
	}
	if n <= 0 {
		return ""
	}
	s = s[:n]
	// Single forward pass: the original loop re-validated the whole prefix per
	// trimmed byte, which is quadratic for binary output. The longest valid
	// UTF-8 prefix ends either at the first invalid sequence or at the start
	// of a rune the boundary splits.
	i := 0
	for i < n {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			return s[:i]
		}
		if i+size > n {
			return s[:i]
		}
		i += size
	}
	return s[:n]
}
