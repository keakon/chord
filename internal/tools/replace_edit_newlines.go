package tools

import "strings"

// fileLineEnding returns a uniform non-LF convention without copying the file.
// Mixed line endings remain byte-exact during replacement. It jumps between
// '\r' bytes and checks each gap for a bare '\n', so a uniform file costs two
// vectorized byte searches instead of a per-byte loop.
func fileLineEnding(content string) string {
	newline := ""
	for {
		i := strings.IndexByte(content, '\r')
		gap := content
		if i >= 0 {
			gap = content[:i]
		}
		if strings.IndexByte(gap, '\n') >= 0 {
			return ""
		}
		if i < 0 {
			return newline
		}
		current := "\r"
		if i+1 < len(content) && content[i+1] == '\n' {
			current = "\r\n"
		}
		if newline != "" && newline != current {
			return ""
		}
		newline = current
		content = content[i+len(current):]
	}
}

// replacementLineEndings adapts replacement text to the file's uniform style.
func replacementLineEndings(newline, oldText, newText string) (string, string) {
	if newline == "" {
		return oldText, newText
	}
	convert := func(s string) string {
		s = strings.ReplaceAll(s, "\r\n", "\n")
		s = strings.ReplaceAll(s, "\r", "\n")
		return strings.ReplaceAll(s, "\n", newline)
	}
	return convert(oldText), convert(newText)
}
