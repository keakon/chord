package tools

import (
	"fmt"
	"strings"
)

const editNotFoundHintMaxContext = 120

// buildEditOldStringNotFoundHint tries to produce a human-actionable hint when
// old_string is not found. It focuses on common causes:
// - indentation mismatch (tabs vs spaces)
// - leading/trailing whitespace differences
//
// It intentionally avoids printing the entire file content.
func buildEditOldStringNotFoundHint(fileText, oldText string) string {
	fileText = normalizeNewlines(fileText)
	oldText = normalizeNewlines(oldText)
	if strings.TrimSpace(oldText) == "" {
		return "old_string is empty/whitespace after normalization"
	}
	// Indentation normalization: treat leading tabs/spaces as equivalent and try
	// to find a unique match by lines.
	fileLines := strings.Split(fileText, "\n")
	oldLines := strings.Split(oldText, "\n")
	if len(oldLines) == 1 {
		// single-line old_string
		needle := strings.TrimLeft(oldLines[0], " \t")
		if needle != "" {
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
				sample = truncateHint(sample)
				return fmt.Sprintf("Indentation mismatch? A unique match exists if leading whitespace is ignored. Example line: %q", sample)
			}
		}
	} else {
		// multi-line old_string: compare blocks by trimming left whitespace.
		trimmedOld := make([]string, 0, len(oldLines))
		for _, l := range oldLines {
			trimmedOld = append(trimmedOld, strings.TrimLeft(l, " \t"))
		}
		matches := 0
		var firstAt int
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
			sample = truncateHint(sample)
			return fmt.Sprintf("Indentation mismatch? A unique match exists if leading whitespace is ignored. Example block:\n%s", sample)
		}
	}

	// Whitespace-trim match.
	if strings.Contains(fileText, strings.TrimSpace(oldText)) {
		return "Leading/trailing whitespace mismatch? A match exists after trimming old_string."
	}
	return "Ensure old_string matches raw file text exactly (including tabs/spaces/newlines). If you copied from Read output, do not include the line-number gutter."
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
