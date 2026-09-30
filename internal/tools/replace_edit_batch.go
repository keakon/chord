package tools

import (
	"context"
	"errors"
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

// batchReplacementPlan is the accepted outcome of planning one batch: the
// spliced content, how many replacements it contains, the prepare notes, and
// the identical entries skipped before matching.
type batchReplacementPlan struct {
	content      string
	replacements int
	notes        string
	skipped      []int
}

// batchEntryProblem is one failed batch entry. Missing text is retained so
// closest-match details can be computed after the report budget is known.
type batchEntryProblem struct {
	entry       int
	summary     string
	missingText string
}

// planExactReplacements matches every entry against the same original text and
// collects every failure instead of stopping at the first one, so one result
// names all the entries the model has to fix. Nothing is written until
// matching and overlap checks succeed. An entry whose old_string and
// new_string are identical requests no text change and is skipped, reported
// separately from both applied and failed entries.
func planExactReplacements(content string, edits []textReplacement) (batchReplacementPlan, error) {
	if len(edits) == 0 {
		return batchReplacementPlan{}, fmt.Errorf("edits must contain at least one replacement; no changes were written")
	}
	newline := fileLineEnding(content)
	var spans []replacementSpan
	var notes []string
	var problems []batchEntryProblem
	var matched, skipped []int
	for i, edit := range edits {
		if edit.OldString == "" {
			problems = append(problems, batchEntryProblem{entry: i, summary: "old_string is required"})
			continue
		}
		if edit.NewString == nil {
			problems = append(problems, batchEntryProblem{entry: i, summary: "new_string is required; use an empty string for deletion"})
			continue
		}
		oldText, newText, note, err := prepareReplacementText(edit.OldString, *edit.NewString)
		if err != nil {
			problems = append(problems, batchEntryProblem{entry: i, summary: err.Error()})
			continue
		}
		oldText, newText = replacementLineEndings(newline, oldText, newText)
		if oldText == newText {
			skipped = append(skipped, i)
			continue
		}
		if note != "" {
			notes = append(notes, fmt.Sprintf("edits[%d]%s", i, note))
		}

		entrySpans, count := entryReplacementSpans(content, newline, oldText, newText, i, edit.ReplaceAll)
		switch {
		case count == 0:
			problems = append(problems, batchEntryProblem{
				entry:       i,
				summary:     "old_string not found in the original file",
				missingText: oldText,
			})
		case count > 1 && !edit.ReplaceAll:
			problems = append(problems, batchEntryProblem{
				entry:   i,
				summary: fmt.Sprintf("old_string found %d times at lines %s in the original file; provide unique context or set replace_all", count, formatMatchLines(lineNumbersAt(content, spanStarts(entrySpans)), count)),
			})
		default:
			matched = append(matched, i)
			spans = append(spans, entrySpans...)
		}
	}
	// Stable: entries matching at the same offset keep their edits[] order, so
	// an overlap message names the entry listed first there first; an unstable
	// sort leaves the order of equal starts unspecified.
	sort.SliceStable(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	// Keep one representative conflict per entry, rather than one report per
	// match location. Every conflicting entry is named even when replace_all
	// creates many spans or one span contains several others.
	overlapping := make(map[int]int)
	maxEnd, maxEndEntry := -1, -1
	for _, span := range spans {
		if maxEndEntry >= 0 && span.start < maxEnd {
			if _, exists := overlapping[maxEndEntry]; !exists {
				overlapping[maxEndEntry] = span.entry
			}
			if _, exists := overlapping[span.entry]; !exists {
				overlapping[span.entry] = maxEndEntry
			}
		}
		if span.end > maxEnd {
			maxEnd, maxEndEntry = span.end, span.entry
		}
	}
	if len(problems) > 0 || len(overlapping) > 0 {
		var remaining []int
		for _, entry := range matched {
			if peer, exists := overlapping[entry]; exists {
				problems = append(problems, batchEntryProblem{
					entry:   entry,
					summary: fmt.Sprintf("overlaps edits[%d] in the original file; merge overlapping replacements", peer),
				})
			} else {
				remaining = append(remaining, entry)
			}
		}
		sort.SliceStable(problems, func(i, j int) bool { return problems[i].entry < problems[j].entry })
		return batchReplacementPlan{}, formatBatchFailureReport(content, problems, remaining, skipped)
	}
	return batchReplacementPlan{
		content:      spliceReplacements(content, spans),
		replacements: len(spans),
		notes:        strings.Join(notes, "; "),
		skipped:      skipped,
	}, nil
}

const (
	// maxBatchEntryDetailLines bounds the closest-match detail attached to one
	// failing entry. batchReportLineBudget allows details only when the full
	// report fits this budget. Complete failure summaries and retry guidance
	// always take priority and can exceed it for large batches.
	maxBatchEntryDetailLines = 3
	batchReportLineBudget    = 40

	// batchToleranceNote states the matching difference between a batch entry
	// and a single edit as a diagnostic clue. It must not read as "retry this
	// entry alone": the batch still needs the complete corrected set.
	batchToleranceNote = "A single edit accepts a trailing-newline or " + tolerantMatchNote + " difference, but batch entries match exactly; check the missing entries above for that kind of difference."
)

// formatBatchFailureReport renders every failed entry of one batch. The report
// always names every failing entry; closest-match details use the remaining
// line budget. Summaries alone may exceed it, in which case no details are
// computed. Details are emitted whole so a fresh-read hint is never cut off.
func formatBatchFailureReport(content string, problems []batchEntryProblem, remaining, skipped []int) error {
	missing := false
	for _, problem := range problems {
		if problem.missingText != "" {
			missing = true
			break
		}
	}
	fixed := 1 + len(problems) + 1 // header, one line per entry, footer
	if missing {
		fixed++
	}
	if len(remaining) > 0 {
		fixed++
	}
	if len(skipped) > 0 {
		fixed++
	}
	detailBudget := max(batchReportLineBudget-fixed, 0)

	lines := []string{"No changes were written (batch edits are atomic)."}
	for _, problem := range problems {
		lines = append(lines, fmt.Sprintf("edits[%d]: %s", problem.entry, problem.summary))
		if problem.missingText != "" && detailBudget >= maxBatchEntryDetailLines {
			detail := editClosestMatchDetail(content, problem.missingText, maxBatchEntryDetailLines)
			lines = append(lines, detail...)
			detailBudget -= len(detail)
		}
	}
	if missing {
		lines = append(lines, batchToleranceNote)
	}
	if len(remaining) > 0 {
		lines = append(lines, fmt.Sprintf("Remaining entries (%s) passed per-entry matching but were not applied.", formatEditIndexRange(remaining)))
	}
	if len(skipped) > 0 {
		lines = append(lines, skippedEntriesNote(skipped)+".")
	}
	lines = append(lines, "Fix the failing entries above and resubmit the complete batch.")
	return errors.New(strings.Join(lines, "\n"))
}

// skippedEntriesNote names the batch entries that requested no text change.
// Skipping happens before matching, so the note must not claim their text was
// found in the file.
func skippedEntriesNote(indexes []int) string {
	return fmt.Sprintf("Skipped %s: old_string and new_string are identical, so no change was requested", formatEditIndexRange(indexes))
}

// formatEditIndexRange renders ascending entry indexes as compact ranges:
// edits[0-2,5].
func formatEditIndexRange(indexes []int) string {
	var b strings.Builder
	b.WriteString("edits[")
	for i := 0; i < len(indexes); {
		j := i
		for j+1 < len(indexes) && indexes[j+1] == indexes[j]+1 {
			j++
		}
		if i > 0 {
			b.WriteByte(',')
		}
		if j > i {
			fmt.Fprintf(&b, "%d-%d", indexes[i], indexes[j])
		} else {
			fmt.Fprintf(&b, "%d", indexes[i])
		}
		i = j + 1
	}
	b.WriteString("]")
	return b.String()
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
	plan, err := planExactReplacements(file.Decoded.Text, edits)
	if err != nil {
		return "", err
	}
	if plan.replacements == 0 {
		// Every entry was identical, so the batch requests no text change:
		// nothing is written (not even the same bytes) and no LSP change
		// notification fires.
		return fmt.Sprintf("No changes: all %d edits have identical old_string and new_string, so there is nothing to write", len(edits)), nil
	}
	encoded, err := encodeString(plan.content, file.Decoded.Encoding)
	if err != nil {
		return "", fmt.Errorf("edited text cannot be encoded back to %s: %w", file.Decoded.Encoding.Name, err)
	}
	encSuffix := ""
	if file.Decoded.Encoding.Name != "utf-8" {
		encSuffix = fmt.Sprintf(", encoding=%s", file.Decoded.Encoding.Name)
	}
	result := fmt.Sprintf("Applied %d edits (%d replacements, %d bytes -> %d bytes)%s", len(edits)-len(plan.skipped), plan.replacements, len(file.Bytes), len(encoded), encSuffix)
	if plan.notes != "" {
		result += "\n" + plan.notes
	}
	if len(plan.skipped) > 0 {
		result += "\n" + skippedEntriesNote(plan.skipped)
	}
	return writeEncodedEditedFile(ctx, path, encoded, file.Decoded, plan.content, fmt.Sprintf("writing %d bytes", len(encoded)), t.LSP, result, t.BaseDir)
}
