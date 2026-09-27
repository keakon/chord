package tools

import (
	"slices"
	"strings"
)

// fileLineEnding returns a uniform non-LF convention without copying the file;
// it returns "" for LF-only and mixed files. It jumps between '\r' bytes and
// checks each gap for a bare '\n', so the scan is a sequence of vectorized
// byte searches rather than a per-byte loop.
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
		return strings.ReplaceAll(lfLineBreaks(s), "\n", newline)
	}
	return convert(oldText), convert(newText)
}

// lineBreakTolerantNote marks single edits matched with line breaks spanning a
// mixed file's different line endings.
const lineBreakTolerantNote = "line-ending-tolerant"

// lineBreakTolerantSpans matches oldText in a file with mixed line endings.
// read shows every line ending as LF, so a multi-line old_string copied from it
// cannot reproduce the file's per-line CRLF/CR/LF bytes; here each line break
// in oldText matches any one line ending. Each replacement takes the line
// ending of the block it replaces (its first line break), so edited lines keep
// their neighbours' convention. It returns nil for uniform files, whose text
// replacementLineEndings already adapted, and for single-line oldText, which
// exact matching covers.
//
// Matching runs on an LF copy of the file. A bare CR maps to LF byte for byte,
// so only collapsed CRLFs shift offsets: a logical offset maps back by adding
// the CRLFs collapsed before it.
func lineBreakTolerantSpans(content, fileEOL, oldText, newText string, entry int) []replacementSpan {
	if fileEOL != "" || strings.IndexByte(content, '\r') < 0 || !strings.ContainsAny(oldText, "\r\n") {
		return nil
	}
	var logical strings.Builder
	logical.Grow(len(content))
	// collapsed holds the logical offset of every LF that replaced a CRLF.
	var collapsed []int
	for rest := content; rest != ""; {
		i := strings.IndexByte(rest, '\r')
		if i < 0 {
			logical.WriteString(rest)
			break
		}
		logical.WriteString(rest[:i])
		if i+1 < len(rest) && rest[i+1] == '\n' {
			collapsed = append(collapsed, logical.Len())
			rest = rest[i+2:]
		} else {
			rest = rest[i+1:]
		}
		logical.WriteByte('\n')
	}
	oldLF := lfLineBreaks(oldText)
	starts := exactMatchOffsets(logical.String(), oldLF, -1)
	if len(starts) == 0 {
		return nil
	}
	original := func(offset int) int {
		before, _ := slices.BinarySearch(collapsed, offset)
		return offset + before
	}
	newLF := lfLineBreaks(newText)
	spans := make([]replacementSpan, len(starts))
	for i, start := range starts {
		from, to := original(start), original(start+len(oldLF))
		text := newLF
		if eol := firstLineBreak(content[from:to]); eol != "\n" {
			text = strings.ReplaceAll(newLF, "\n", eol)
		}
		spans[i] = replacementSpan{start: from, end: to, entry: entry, text: text}
	}
	return spans
}

func lfLineBreaks(s string) string {
	if strings.IndexByte(s, '\r') < 0 {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// firstLineBreak returns the first line ending in s, or "\n" when s has none.
func firstLineBreak(s string) string {
	i := strings.IndexAny(s, "\r\n")
	switch {
	case i < 0 || s[i] == '\n':
		return "\n"
	case i+1 < len(s) && s[i+1] == '\n':
		return "\r\n"
	default:
		return "\r"
	}
}
