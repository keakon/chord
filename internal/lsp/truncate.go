package lsp

import "unicode/utf8"

// truncateBytesAtRune caps s at maxBytes bytes and appends suffix when it had
// to cut, backing the cut up to a UTF-8 rune boundary first.
//
// The budget is in bytes, not runes, because both callers feed bounded sinks:
// a log line and an LSP sidebar row. Both carry non-ASCII text often enough to
// matter - localized server messages and non-ASCII paths - and a byte slice
// through a multi-byte character would leave invalid UTF-8 behind.
//
// maxBytes counts the retained bytes; the suffix is added on top of it.
func truncateBytesAtRune(s string, maxBytes int, suffix string) string {
	if maxBytes < 0 {
		maxBytes = 0
	}
	if len(s) <= maxBytes {
		return s
	}
	// maxBytes < len(s) here, so s[maxBytes] is in range, and s[0] always
	// starts a rune for valid UTF-8, so the walk terminates at a boundary.
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + suffix
}
