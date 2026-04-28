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

// FindStreamingSettledFrontier returns the byte offset in content up to
// which the markdown structure is stable enough to render during streaming.
func FindStreamingSettledFrontier(content string) int {
	content = NormalizeNewlines(content)
	if content == "" {
		return 0
	}

	lines := strings.SplitAfter(content, "\n")
	frontier := 0
	offset := 0
	inFence := false
	prevBlank := false
	var currentFence Fence

	for _, line := range lines {
		lineLen := len(line)
		trimmed := strings.TrimRight(line, "\n")
		isBlank := strings.TrimSpace(trimmed) == ""

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
		} else {
			if IsFenceClose(line, currentFence) {
				inFence = false
				currentFence = Fence{}
				frontier = offset + lineLen
			}
		}
		offset += lineLen
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
