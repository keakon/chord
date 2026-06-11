package markdownutil

import "strings"

// Fence describes a Markdown code fence opener.
type Fence struct {
	Indent    string
	Delimiter byte
	Length    int
	Info      string
}

func NormalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

func FirstFenceInfoField(info string) string {
	if fields := strings.Fields(strings.TrimSpace(info)); len(fields) > 0 {
		return fields[0]
	}
	return ""
}

func ParseFenceLine(line string) (Fence, bool) {
	line = strings.TrimRight(line, "\n")
	if line == "" {
		return Fence{}, false
	}
	indentLen := 0
	for indentLen < len(line) && (line[indentLen] == ' ' || line[indentLen] == '\t') {
		indentLen++
	}
	if indentLen >= len(line) {
		return Fence{}, false
	}
	delim := line[indentLen]
	if delim != '`' && delim != '~' {
		return Fence{}, false
	}
	i := indentLen
	for i < len(line) && line[i] == delim {
		i++
	}
	length := i - indentLen
	if length < 3 {
		return Fence{}, false
	}
	rest := line[i:]
	if delim == '`' && strings.ContainsRune(rest, '`') {
		return Fence{}, false
	}
	trimmedRest := strings.TrimSpace(rest)
	return Fence{
		Indent:    line[:indentLen],
		Delimiter: delim,
		Length:    length,
		Info:      trimmedRest,
	}, true
}

func IsFenceClose(line string, open Fence) bool {
	fence, ok := ParseFenceLine(line)
	if !ok {
		return false
	}
	if fence.Delimiter != open.Delimiter {
		return false
	}
	if fence.Length < open.Length {
		return false
	}
	return fence.Info == ""
}

func fenceString(delim byte, length int, info string) string {
	if length < 3 {
		length = 3
	}
	fence := strings.Repeat(string(delim), length)
	if strings.TrimSpace(info) == "" {
		return fence
	}
	return fence + strings.TrimSpace(info)
}

// StreamingFrontierScanner incrementally finds the settled frontier of
// append-only streaming markdown content. It saves internal scan state so
// successive Advance calls only process new text, keeping per-flush cost
// proportional to the delta rather than the full content.
type StreamingFrontierScanner struct {
	// Content is the full text scanned so far, used for monotonicity checks.
	Content string
	// Frontier is the last computed frontier byte offset.
	Frontier int

	offset       int
	inFence      bool
	prevBlank    bool
	currentFence Fence
	// scanned is how far the newline search has progressed inside the
	// uncommitted final line, so a long unterminated line is not re-searched
	// byte-by-byte on every call.
	scanned int
}

// Advance resumes the scan from saved state over content. If content is
// append-only relative to the previous call, only the new suffix is scanned.
// If content shrank or changed non-monotonically, the scanner resets.
//
// Saved state is only committed at definite line boundaries: the final line
// may still be growing (no terminator yet, or a trailing '\r' that could
// become '\r\n'), so it is rescanned from its start on the next call. This
// keeps incremental results identical to FindStreamingSettledFrontier.
func (s *StreamingFrontierScanner) Advance(content string) int {
	if !strings.HasPrefix(content, s.Content) {
		// Content changed non-monotonically – reset.
		*s = StreamingFrontierScanner{}
	}
	if content == "" {
		return 0
	}
	i := s.offset
	// If we already scanned to the end, return cached frontier.
	if i >= len(content) {
		return s.Frontier
	}
	frontier := s.Frontier
	offset := s.offset
	inFence := s.inFence
	prevBlank := s.prevBlank
	currentFence := s.currentFence

	commitFrontier := frontier
	commitOffset := offset
	commitInFence := inFence
	commitPrevBlank := prevBlank
	commitFence := currentFence

	// Resume the newline search inside the uncommitted final line where the
	// previous call left off; only line-level state is recomputed on replay.
	searchResume := s.scanned

	for i < len(content) {
		lineStart := i
		if searchResume > i {
			i = searchResume
		}
		for i < len(content) && content[i] != '\n' && content[i] != '\r' {
			i++
		}
		line := content[lineStart:i]
		newlineLen := 0
		if i < len(content) {
			if content[i] == '\r' && i+1 < len(content) && content[i+1] == '\n' {
				newlineLen = 2
			} else {
				newlineLen = 1
			}
		}

		isBlank := strings.TrimSpace(line) == ""
		if !inFence {
			if fence, ok := ParseFenceLine(line); ok {
				currentFence = fence
				inFence = true
				prevBlank = false
			} else if isBlank {
				if !prevBlank {
					frontier = offset
				}
				prevBlank = true
			} else {
				prevBlank = false
			}
		} else if IsFenceClose(line, currentFence) {
			inFence = false
			currentFence = Fence{}
			frontier = offset + len(line) + newlineLen
		}

		offset += len(line) + newlineLen
		i += newlineLen

		if newlineLen > 0 && !(content[i-1] == '\r' && i == len(content)) {
			commitFrontier = frontier
			commitOffset = offset
			commitInFence = inFence
			commitPrevBlank = prevBlank
			commitFence = currentFence
		}
	}

	s.Content = content
	s.Frontier = commitFrontier
	s.offset = commitOffset
	s.inFence = commitInFence
	s.prevBlank = commitPrevBlank
	s.currentFence = commitFence
	// A trailing lone '\r' may still become '\r\n', so it must be re-examined
	// as a terminator candidate on the next call.
	s.scanned = len(content)
	if content[len(content)-1] == '\r' {
		s.scanned--
	}
	return frontier
}

// FindStreamingSettledFrontier returns the byte offset in content up to
// which the markdown structure is stable enough to render during streaming.
// It scans line-by-line without allocating intermediate slices so long
// append-only responses stay cheap to re-evaluate.
func FindStreamingSettledFrontier(content string) int {
	if content == "" {
		return 0
	}

	frontier := 0
	offset := 0
	inFence := false
	prevBlank := false
	var currentFence Fence

	for i := 0; i < len(content); {
		lineStart := i
		for i < len(content) && content[i] != '\n' && content[i] != '\r' {
			i++
		}
		line := content[lineStart:i]
		newlineLen := 0
		if i < len(content) {
			if content[i] == '\r' && i+1 < len(content) && content[i+1] == '\n' {
				newlineLen = 2
			} else {
				newlineLen = 1
			}
		}

		isBlank := strings.TrimSpace(line) == ""
		if !inFence {
			if fence, ok := ParseFenceLine(line); ok {
				currentFence = fence
				inFence = true
				prevBlank = false
			} else if isBlank {
				if !prevBlank {
					frontier = offset
				}
				prevBlank = true
			} else {
				prevBlank = false
			}
		} else if IsFenceClose(line, currentFence) {
			inFence = false
			currentFence = Fence{}
			frontier = offset + len(line) + newlineLen
		}

		offset += len(line) + newlineLen
		i += newlineLen
	}

	return frontier
}

func RepairForDisplay(content string) string {
	content = NormalizeNewlines(content)
	if content == "" {
		return content
	}

	lines := strings.SplitAfter(content, "\n")
	var out strings.Builder
	var current Fence
	inFence := false

	for _, line := range lines {
		if !inFence {
			if fence, ok := ParseFenceLine(line); ok {
				current = fence
				inFence = true
			}
			out.WriteString(line)
			continue
		}
		out.WriteString(line)
		if IsFenceClose(line, current) {
			inFence = false
			current = Fence{}
		}
	}

	if inFence {
		if out.Len() > 0 && !strings.HasSuffix(out.String(), "\n") {
			out.WriteByte('\n')
		}
		out.WriteString(current.Indent)
		out.WriteString(fenceString(current.Delimiter, current.Length, ""))
	}
	return out.String()
}
