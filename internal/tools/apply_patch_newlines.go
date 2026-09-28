package tools

import "strings"

// applyPatchLineEndings records how a file terminates its lines so hunks can
// match on bare line text and the result can be written back in the file's
// own convention. LF-only and uniform CRLF/CR files carry one ending; a file
// that mixes them keeps every line's ending in perLine instead.
type applyPatchLineEndings struct {
	uniform string
	perLine []string
}

// splitApplyPatchLines splits content into the lines hunks are matched
// against. A final line without a terminator is a line of its own, as in the
// LF-only case.
func splitApplyPatchLines(content string) ([]string, applyPatchLineEndings) {
	if strings.IndexByte(content, '\r') < 0 {
		return splitLFLines(content), applyPatchLineEndings{uniform: "\n"}
	}
	if eol := fileLineEnding(content); eol != "" {
		return splitLFLines(lfLineBreaks(content)), applyPatchLineEndings{uniform: eol}
	}
	var lines, eols []string
	for rest := content; rest != ""; {
		i := strings.IndexAny(rest, "\r\n")
		if i < 0 {
			lines = append(lines, rest)
			eols = append(eols, "")
			break
		}
		eol := firstLineBreak(rest[i:])
		lines = append(lines, rest[:i])
		eols = append(eols, eol)
		rest = rest[i+len(eol):]
	}
	return lines, applyPatchLineEndings{perLine: eols}
}

func splitLFLines(content string) []string {
	if content == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(content, "\n"), "\n")
}

// applyPatchNewLineEndings returns the endings of a hunk's replacement lines
// in a mixed file, aligned with the sequence the hunk writes (its context and
// added lines, in order). Context lines keep their own ending; an added line
// takes the ending of the file line it replaces or sits next to, so edited
// lines follow their neighbours' convention.
func applyPatchNewLineEndings(hunk applyPatchHunk, eols []string, match, oldLen int) []string {
	out := make([]string, 0, len(hunk.Lines))
	oldIndex := 0
	last := ""
	for _, line := range hunk.Lines {
		switch line.Kind {
		case ' ':
			last = eols[match+oldIndex]
			out = append(out, last)
			oldIndex++
		case '-':
			last = eols[match+oldIndex]
			oldIndex++
		case '+':
			eol := last
			if eol == "" && oldIndex < oldLen {
				eol = eols[match+oldIndex]
			}
			if eol == "" && match > 0 {
				eol = eols[match-1]
			}
			out = append(out, eol)
		}
	}
	return out
}

// joinApplyPatchLines writes lines back with their endings. Every line ends
// with a terminator, as in the LF-only case; a line that had none (the old
// final line) takes the ending of the line before it.
func joinApplyPatchLines(lines []string, endings applyPatchLineEndings) string {
	if len(lines) == 0 {
		return ""
	}
	if endings.perLine == nil {
		out := strings.Join(lines, "\n") + "\n"
		if endings.uniform != "\n" {
			out = strings.ReplaceAll(out, "\n", endings.uniform)
		}
		return out
	}
	size := 0
	for _, line := range lines {
		size += len(line) + 2
	}
	var b strings.Builder
	b.Grow(size)
	prev := "\n"
	for i, line := range lines {
		eol := endings.perLine[i]
		if eol == "" {
			eol = prev
		}
		b.WriteString(line)
		b.WriteString(eol)
		prev = eol
	}
	return b.String()
}
