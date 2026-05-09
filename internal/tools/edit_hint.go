package tools

import (
	"fmt"
	"regexp"
	"strings"
)

const editNotFoundHintMaxContext = 120

var readLineNumberGutterRE = regexp.MustCompile(`(?m)^\s*\d+\t`)

var quoteNormalizationReplacer = strings.NewReplacer(
	"“", `"`,
	"”", `"`,
	"‘", `'`,
	"’", `'`,
)

// buildEditOldStringNotFoundHint tries to produce a human-actionable hint when
// old_string is not found. It focuses on common causes while keeping edit
// semantics conservative and exact-match only.
func buildEditOldStringNotFoundHint(fileText, oldText string) string {
	if strings.TrimSpace(normalizeNewlines(oldText)) == "" {
		return "old_string is empty/whitespace after normalization"
	}

	if hint := buildIndentationMismatchHint(fileText, oldText); hint != "" {
		return hint
	}
	if hint := buildTrailingNewlineMismatchHint(fileText, oldText); hint != "" {
		return hint
	}
	if hint := buildLineEndingMismatchHint(fileText, oldText); hint != "" {
		return hint
	}
	if hint := buildQuoteMismatchHint(fileText, oldText); hint != "" {
		return hint
	}
	if hint := buildReadGutterHint(fileText, oldText); hint != "" {
		return hint
	}
	if hint := buildTrimMismatchHint(fileText, oldText); hint != "" {
		return hint
	}

	return "Ensure old_string matches raw file text exactly, including tabs, spaces, and newlines. If you copied from Read output, do not include the displayed line-number gutter or separator tab. Re-read the smallest unique block before retrying, prefer 2-4 uniquely identifying lines instead of large stale context, and if line endings may differ or nearby text changed, re-copy from the latest file view. If multiple matches exist, add a little more surrounding context or set replace_all."
}

func buildIndentationMismatchHint(fileText, oldText string) string {
	fileLines, oldLines, ok := comparableLogicalLines(fileText, oldText)
	if !ok {
		return ""
	}

	if len(oldLines) == 1 {
		needle := strings.TrimLeft(oldLines[0], " \t")
		if needle == "" || needle == oldLines[0] {
			return ""
		}
		matches := 0
		var sample string
		for _, line := range fileLines {
			if strings.TrimLeft(line, " \t") == needle {
				matches++
				if matches == 1 {
					sample = line
				}
			}
		}
		if matches == 1 {
			return fmt.Sprintf("Indentation mismatch? A unique match exists if leading whitespace is ignored. Example line: %q", truncateHint(sample))
		}
		return ""
	}

	trimmedOld := make([]string, len(oldLines))
	changed := false
	for i, line := range oldLines {
		trimmed := strings.TrimLeft(line, " \t")
		trimmedOld[i] = trimmed
		if trimmed != line {
			changed = true
		}
	}
	if !changed {
		return ""
	}

	matches := 0
	firstAt := -1
	for i := 0; i+len(trimmedOld) <= len(fileLines); i++ {
		ok := true
		for j := range trimmedOld {
			if strings.TrimLeft(fileLines[i+j], " \t") != trimmedOld[j] {
				ok = false
				break
			}
		}
		if ok {
			matches++
			if matches == 1 {
				firstAt = i
			}
		}
	}
	if matches == 1 {
		sample := strings.Join(fileLines[firstAt:firstAt+min(3, len(trimmedOld))], "\n")
		return fmt.Sprintf("Indentation mismatch? A unique match exists if leading whitespace is ignored. Example block:\n%s", truncateHint(sample))
	}
	return ""
}

func buildTrailingNewlineMismatchHint(fileText, oldText string) string {
	variants := []struct {
		candidate string
		message   string
	}{
		{
			candidate: strings.TrimSuffix(oldText, "\n"),
			message:   "Trailing newline mismatch? A unique match exists after removing one final newline from old_string.",
		},
		{
			candidate: strings.TrimSuffix(oldText, "\r\n"),
			message:   "Trailing newline mismatch? A unique match exists after removing one final newline from old_string.",
		},
		{
			candidate: oldText + "\n",
			message:   "Trailing newline mismatch? A unique match exists after adding one final newline to old_string.",
		},
	}

	seen := map[string]struct{}{}
	for _, variant := range variants {
		if variant.candidate == oldText {
			continue
		}
		if _, ok := seen[variant.candidate]; ok {
			continue
		}
		seen[variant.candidate] = struct{}{}
		if countExactOccurrences(fileText, variant.candidate) == 1 {
			return variant.message
		}
	}
	return ""
}

func buildLineEndingMismatchHint(fileText, oldText string) string {
	if !hasLineEndingMismatchPotential(fileText, oldText) {
		return ""
	}
	if countExactOccurrences(normalizeNewlines(fileText), normalizeNewlines(oldText)) == 1 {
		return "Line-ending mismatch? A unique match exists after CRLF/LF normalization. Read displays normalized line endings, so re-copy the raw file text carefully before retrying."
	}
	return ""
}

func buildQuoteMismatchHint(fileText, oldText string) string {
	normalizedFile := normalizeQuotes(fileText)
	normalizedOld := normalizeQuotes(oldText)
	if normalizedFile == fileText && normalizedOld == oldText {
		return ""
	}
	if normalizedOld == oldText {
		return ""
	}
	if countExactOccurrences(normalizedFile, normalizedOld) == 1 {
		return "Quote mismatch? A unique match exists after normalizing curly vs straight quotes. Re-copy the raw file text so old_string uses the exact quote characters from the file."
	}
	return ""
}

func buildReadGutterHint(fileText, oldText string) string {
	if !readLineNumberGutterRE.MatchString(oldText) {
		return ""
	}
	stripped, removed := stripReadLineNumberGutter(oldText)
	if !removed || stripped == oldText {
		return ""
	}
	if countExactOccurrences(fileText, stripped) == 1 {
		return "Read output gutter included? A unique match exists after removing Read's displayed line-number gutter and separator tab from old_string."
	}
	return ""
}

func buildTrimMismatchHint(fileText, oldText string) string {
	trimmed := strings.TrimSpace(oldText)
	if trimmed == "" || trimmed == oldText {
		return ""
	}
	if countExactOccurrences(fileText, trimmed) == 1 {
		return "Leading/trailing whitespace mismatch? A unique match exists after trimming old_string."
	}
	return ""
}

func comparableLogicalLines(fileText, oldText string) ([]string, []string, bool) {
	fileLines := splitComparableLines(fileText)
	oldLines := splitComparableLines(oldText)
	if len(fileLines) == 0 || len(oldLines) == 0 {
		return nil, nil, false
	}
	return fileLines, oldLines, true
}

func splitComparableLines(s string) []string {
	normalized := normalizeNewlines(s)
	trimmed := strings.TrimSuffix(normalized, "\n")
	if trimmed == "" {
		if normalized == "" {
			return nil
		}
		return []string{""}
	}
	return strings.Split(trimmed, "\n")
}

func countExactOccurrences(haystack, needle string) int {
	if needle == "" {
		return 0
	}
	return strings.Count(haystack, needle)
}

func hasLineEndingMismatchPotential(fileText, oldText string) bool {
	return strings.Contains(fileText, "\r") || strings.Contains(oldText, "\r")
}

func stripReadLineNumberGutter(s string) (string, bool) {
	lines := strings.Split(s, "\n")
	removedAny := false
	for i, line := range lines {
		if line == "" && i == len(lines)-1 {
			continue
		}
		idx := strings.IndexByte(line, '\t')
		if idx <= 0 {
			return "", false
		}
		prefix := line[:idx]
		if strings.TrimSpace(prefix) == "" {
			return "", false
		}
		for _, r := range prefix {
			if r != ' ' && (r < '0' || r > '9') {
				return "", false
			}
		}
		lines[i] = line[idx+1:]
		removedAny = true
	}
	if !removedAny {
		return "", false
	}
	return strings.Join(lines, "\n"), true
}

func normalizeQuotes(s string) string {
	return quoteNormalizationReplacer.Replace(s)
}

func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s
}

func truncateHint(s string) string {
	if len(s) <= editNotFoundHintMaxContext {
		return s
	}
	return s[:editNotFoundHintMaxContext] + "..."
}
