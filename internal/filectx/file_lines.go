package filectx

import (
	"bytes"
	"iter"
	"unicode/utf8"
)

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// fileLine marks one logical line in the original file bytes. start includes a
// leading UTF-8 BOM for the first line, while contentStart skips it: the BOM is
// kept in the injected body but is not text and must not become a line of its
// own. end is the separator start (or len(data) for a line without one), and
// next is just past the separator, so a complete line is data[start:next].
type fileLine struct {
	start        int
	contentStart int
	end          int
	next         int
}

// scanFileLines counts lines the way the read tool does after newline
// normalization: LF, CRLF and CR are each one separator, a trailing separator
// does not add an empty line, and an empty file has no lines. Bytes are never
// rewritten, so spans address the original data. Iteration avoids allocating
// an entry for every line when only a bounded prefix will be injected.
func scanFileLines(data []byte) iter.Seq[fileLine] {
	return func(yield func(fileLine) bool) {
		bomLen := 0
		if bytes.HasPrefix(data, utf8BOM) {
			bomLen = len(utf8BOM)
		}
		if bomLen == len(data) {
			return
		}
		start := 0
		for i := bomLen; i < len(data); {
			sep := 0
			switch data[i] {
			case '\r':
				sep = 1
				if i+1 < len(data) && data[i+1] == '\n' {
					sep = 2
				}
			case '\n':
				sep = 1
			}
			if sep == 0 {
				i++
				continue
			}
			contentStart := start
			if start == 0 {
				contentStart = bomLen
			}
			if !yield(fileLine{start: start, contentStart: contentStart, end: i, next: i + sep}) {
				return
			}
			start = i + sep
			i += sep
		}
		if start < len(data) {
			contentStart := start
			if start == 0 {
				contentStart = bomLen
			}
			yield(fileLine{start: start, contentStart: contentStart, end: len(data), next: len(data)})
		}
	}
}

// fileLineFactsSupported limits the line metadata contract to plain UTF-8
// text. The read tool also decodes BOM-marked and regional encodings; injecting
// raw bytes for those would make total_lines disagree with a decoded read, so
// they keep the previous byte-budget handling without any line claims. A NUL
// byte is the conservative marker for UTF-16-like data that is still valid
// UTF-8 at the byte level.
func fileLineFactsSupported(data []byte) bool {
	return utf8.Valid(data) && bytes.IndexByte(data, 0) < 0
}

// utf8SafePrefix returns the longest prefix of data within limit bytes that
// ends on a UTF-8 rune boundary and does not split a CRLF separator.
func utf8SafePrefix(data []byte, limit int) []byte {
	if limit <= 0 {
		return nil
	}
	end := 0
	for end < len(data) && end < limit {
		_, size := utf8.DecodeRune(data[end:])
		if end+size > limit {
			break
		}
		end += size
	}
	if end > 0 && end < len(data) && data[end-1] == '\r' && data[end] == '\n' {
		end--
	}
	return data[:end]
}

// filePartContent is the retained raw body together with the facts derived
// from the same complete read. hasFacts is false for data that cannot promise
// read-compatible line numbers; ok is false when a requested range has no
// lines at all and the file must be skipped.
type filePartContent struct {
	body            []byte
	totalLines      int
	coveredStart    int
	coveredEnd      int
	truncated       bool
	partialLastLine bool
	hasFacts        bool
	ok              bool
}

func prepareFilePart(data []byte, requested LineRange, maxBytes int) filePartContent {
	if !fileLineFactsSupported(data) {
		return prepareUnverifiedFilePart(data, requested, maxBytes)
	}
	first := 1
	if requested.IsSet() {
		if !requested.IsValid() {
			return filePartContent{}
		}
		first = requested.Start
	}
	total, last := 0, 0
	start, stop := 0, len(data)
	keptEnd, lastKept := 0, 0
	var firstLine fileLine
	for line := range scanFileLines(data) {
		total++
		if total < first || requested.IsSet() && total > requested.End {
			continue
		}
		if total == first {
			firstLine = line
			start, keptEnd = line.start, line.start
		}
		stop, last = line.next, total
		if line.next-start <= maxBytes {
			keptEnd, lastKept = line.next, total
		}
	}
	if requested.IsSet() && first > total {
		return filePartContent{}
	}
	selected := data[start:stop]
	out := filePartContent{totalLines: total, hasFacts: true, ok: true}
	if len(selected) <= maxBytes {
		out.body = selected
		if first <= last {
			out.coveredStart, out.coveredEnd = first, last
		}
		return out
	}
	out.truncated = true
	if lastKept > 0 {
		out.body = data[start:keptEnd]
		out.coveredStart, out.coveredEnd = first, lastKept
		return out
	}
	// Even the first selected line exceeds the budget. Keep a bounded prefix
	// instead of dropping the file, and derive coverage from that prefix.
	prefix := utf8SafePrefix(selected, maxBytes)
	out.body = prefix
	if total > 0 && len(prefix) > firstLine.contentStart-start {
		out.coveredStart, out.coveredEnd = first, first
		out.partialLastLine = start+len(prefix) < firstLine.end
	}
	return out
}

// prepareUnverifiedFilePart keeps the previous behavior for data whose line
// numbers cannot be promised: LF-only range selection and a plain byte cut.
func prepareUnverifiedFilePart(data []byte, requested LineRange, maxBytes int) filePartContent {
	if requested.IsSet() {
		if !requested.IsValid() {
			return filePartContent{}
		}
		selected, ok := selectLineRange(data, requested)
		if !ok {
			return filePartContent{}
		}
		data = selected
	}
	out := filePartContent{ok: true}
	if len(data) > maxBytes {
		data = data[:maxBytes]
		if idx := bytes.LastIndexByte(data, '\n'); idx > 0 {
			data = data[:idx]
		}
		out.truncated = true
	}
	out.body = data
	return out
}
