package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

type textReplacement struct {
	OldString  string  `json:"old_string"`
	NewString  *string `json:"new_string"`
	ReplaceAll bool    `json:"replace_all,omitempty"`
}

type replacementSpan struct {
	start, end, entry int
	text              string
}

// planExactReplacements matches every entry against the same original text.
// Nothing is written until matching, overlap checks and encoding all succeed.
func planExactReplacements(content string, edits []textReplacement) (string, int, string, error) {
	if len(edits) == 0 {
		return "", 0, "", fmt.Errorf("edits must contain at least one replacement")
	}
	newline := fileLineEnding(content)
	var spans []replacementSpan
	var notes []string
	for i, edit := range edits {
		if edit.OldString == "" {
			return "", 0, "", fmt.Errorf("edits[%d]: old_string is required", i)
		}
		if edit.NewString == nil {
			return "", 0, "", fmt.Errorf("edits[%d]: new_string is required; use an empty string for deletion", i)
		}
		oldText, newText, note, err := prepareReplacementText(edit.OldString, *edit.NewString)
		if err != nil {
			return "", 0, "", fmt.Errorf("edits[%d]: %w", i, err)
		}
		oldText, newText = replacementLineEndings(newline, oldText, newText)
		if oldText == newText {
			return "", 0, "", fmt.Errorf("edits[%d]: old_string and new_string are identical", i)
		}
		if note != "" {
			notes = append(notes, fmt.Sprintf("edits[%d]%s", i, note))
		}

		entrySpans, count := entryReplacementSpans(content, newline, oldText, newText, i, edit.ReplaceAll)
		if count == 0 {
			if diagnostic := editClosestMatchDiagnostic(content, oldText); diagnostic != "" {
				return "", 0, "", fmt.Errorf("edits[%d]: old_string not found in the original file; no changes written. Batch entries match exactly; for a trailing newline or %s difference, retry this entry as a single edit. %s", i, tolerantMatchNote, diagnostic)
			}
			return "", 0, "", fmt.Errorf("edits[%d]: old_string not found in the original file; no changes written. Batch entries match exactly: if this entry differs only by a trailing newline or a %s difference, retry it on its own as a single edit; otherwise read the target range and rebuild this entry", i, tolerantMatchNote)
		}
		if count > 1 && !edit.ReplaceAll {
			return "", 0, "", fmt.Errorf("edits[%d]: old_string found %d times at lines %s in the original file; no changes written. Provide unique context or set replace_all", i, count, formatMatchLines(lineNumbersAt(content, spanStarts(entrySpans)), count))
		}
		spans = append(spans, entrySpans...)
	}
	// Stable: entries matching at the same offset keep their edits[] order, so
	// the overlap message below names the lower-numbered entry first; an
	// unstable sort leaves the order of equal starts unspecified.
	sort.SliceStable(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	for i := 1; i < len(spans); i++ {
		if spans[i].start < spans[i-1].end {
			// Name the entry whose match comes first in the file first, so the
			// message reads in file order even when edits[] lists them out of order.
			return "", 0, "", fmt.Errorf("edits[%d] overlaps edits[%d] in the original file; merge overlapping replacements; no changes written", spans[i-1].entry, spans[i].entry)
		}
	}
	return spliceReplacements(content, spans), len(spans), strings.Join(notes, "; "), nil
}

// entryReplacementSpans collects the spans one entry replaces and its total
// match count. Without replace_all only a unique match is applied, so the scan
// stops at maxMatchLinesShown — enough to name the ambiguous lines — and only
// a scan that hits that cap pays for strings.Count to report the total. In a
// mixed line-ending file a multi-line entry matches all equivalent line endings
// before exact matching, so exact matches cannot hide equivalent ones.
func entryReplacementSpans(content, fileEOL, oldText, newText string, entry int, replaceAll bool) ([]replacementSpan, int) {
	limit := -1
	if !replaceAll {
		limit = maxMatchLinesShown
	}
	if spans := lineBreakTolerantSpans(content, fileEOL, oldText, newText, entry); len(spans) > 0 {
		return spans, len(spans)
	}
	starts := exactMatchOffsets(content, oldText, limit)
	total := len(starts)
	if total == limit {
		total = strings.Count(content, oldText)
	}
	spans := make([]replacementSpan, len(starts))
	for i, start := range starts {
		spans[i] = replacementSpan{start: start, end: start + len(oldText), entry: entry, text: newText}
	}
	return spans, total
}

// spanStarts returns the byte offsets of at most maxMatchLinesShown spans, the
// locations an ambiguity error names.
func spanStarts(spans []replacementSpan) []int {
	starts := make([]int, min(len(spans), maxMatchLinesShown))
	for i := range starts {
		starts[i] = spans[i].start
	}
	return starts
}

// spliceReplacements swaps every span for its text in one pre-sized write;
// spans must be sorted by start and disjoint.
func spliceReplacements(content string, spans []replacementSpan) string {
	size := len(content)
	for _, span := range spans {
		size += len(span.text) - (span.end - span.start)
	}
	var out strings.Builder
	out.Grow(size)
	offset := 0
	for _, span := range spans {
		out.WriteString(content[offset:span.start])
		out.WriteString(span.text)
		offset = span.end
	}
	out.WriteString(content[offset:])
	return out.String()
}

func (t EditTool) executeBatch(ctx context.Context, path, displayPath string, edits []textReplacement) (string, error) {
	file, err := readFileForEdit(path, displayPath, t.BaseDir, "edit")
	if err != nil {
		return "", err
	}
	content, count, note, err := planExactReplacements(file.Decoded.Text, edits)
	if err != nil {
		return "", err
	}
	encoded, err := encodeString(content, file.Decoded.Encoding)
	if err != nil {
		return "", fmt.Errorf("edited text cannot be encoded back to %s: %w", file.Decoded.Encoding.Name, err)
	}
	encSuffix := ""
	if file.Decoded.Encoding.Name != "utf-8" {
		encSuffix = fmt.Sprintf(", encoding=%s", file.Decoded.Encoding.Name)
	}
	result := fmt.Sprintf("Applied %d edits (%d replacements, %d bytes -> %d bytes)%s", len(edits), count, len(file.Bytes), len(encoded), encSuffix)
	if note != "" {
		result += "\n" + note
	}
	return writeEncodedEditedFile(ctx, path, encoded, file.Decoded, content, fmt.Sprintf("writing %d bytes", len(encoded)), t.LSP, result, t.BaseDir)
}
