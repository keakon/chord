package textclean

import "strings"

// NormalizeNewlines converts CRLF and bare CR line endings to LF.
func NormalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}
