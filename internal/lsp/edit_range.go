package lsp

import "strings"

// EditRange is a 0-based inclusive line range touched by a text edit.
type EditRange struct {
	StartLine int
	EndLine   int
}

func normalizeEditRanges(ranges []EditRange) []EditRange {
	if len(ranges) == 0 {
		return nil
	}
	out := make([]EditRange, 0, len(ranges))
	for _, r := range ranges {
		if r.StartLine < 0 {
			r.StartLine = 0
		}
		if r.EndLine < r.StartLine {
			r.EndLine = r.StartLine
		}
		out = append(out, r)
	}
	return out
}

func EditRangesForReplacement(content, oldText, newText string, replaceAll bool) []EditRange {
	if oldText == "" {
		return nil
	}
	var ranges []EditRange
	startAt := 0
	for {
		idx := strings.Index(content[startAt:], oldText)
		if idx < 0 {
			break
		}
		absIdx := startAt + idx
		startLine := strings.Count(content[:absIdx], "\n")
		changedLines := max(strings.Count(oldText, "\n"), strings.Count(newText, "\n"))
		ranges = append(ranges, EditRange{StartLine: startLine, EndLine: startLine + changedLines})
		if !replaceAll {
			break
		}
		startAt = absIdx + len(oldText)
		if startAt > len(content) {
			break
		}
	}
	return normalizeEditRanges(ranges)
}
