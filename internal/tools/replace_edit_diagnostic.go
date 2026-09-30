package tools

import (
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

// editClosestMatchDiagnostic renders the full closest-match report shown when
// a single edit matches nothing.
func editClosestMatchDiagnostic(content, oldText string) string {
	report, ok := newEditClosestMatchReport(content, oldText)
	if !ok {
		return ""
	}
	return strings.Join(report.lines(), "\n")
}

// editClosestMatchDetail renders the same report for one entry of a failed
// batch, capped at maxLines. A batch can fail many entries at once, so each
// entry gets only its most actionable lines (see compactLines).
func editClosestMatchDetail(content, oldText string, maxLines int) []string {
	if maxLines < maxBatchEntryDetailLines {
		return nil
	}
	report, ok := newEditClosestMatchReport(content, oldText)
	if !ok {
		return nil
	}
	return report.compactLines(oldText)
}

// editClosestMatchReport splits one closest-match finding into the parts the
// full diagnostic and the batch entry detail render differently: the full
// report keeps every difference and the rebuild guidance, while a batch entry
// must fit a small line budget.
type editClosestMatchReport struct {
	closest   editClosestMatchResult
	header    string
	fileLine  string
	yourLine  string
	hint      string
	lineCount string
	diffs     []string
	guidance  string
}

// newEditClosestMatchReport compares line windows, so it first maps every
// line ending of the file and of oldText to LF: line breaks are not what the
// model got wrong (the matchers already adapt them), and a leftover CR would
// show up as a phantom difference on every line — or, in a CR-only file,
// leave no line break to split on at all.
func newEditClosestMatchReport(content, oldText string) (editClosestMatchReport, bool) {
	content, oldText = lfLineBreaks(content), lfLineBreaks(oldText)
	closest, ok := editClosestMatch(content, oldText)
	if !ok {
		return editClosestMatchReport{}, false
	}
	report := editClosestMatchReport{
		closest:  closest,
		header:   fmt.Sprintf("Closest match is at line %d (%d%% similar, %d character difference):", closest.StartLine, int(math.Round(closest.Similarity*100)), closest.DiffRunes),
		fileLine: fmt.Sprintf("  file line %d: %s", closest.FileDiffLine, closest.Actual),
		yourLine: fmt.Sprintf("  your line %d: %s", closest.ExpectedDiffLine, closest.Expected),
	}
	if hint := firstMismatchHint(closest.ExpectedRaw, closest.ActualRaw); hint != "" {
		report.hint = "  " + hint
	}
	if closest.LineDiffOldExtra > 0 || closest.LineDiffSrcExtra > 0 {
		blankNote := ""
		if closest.LineDiffBlankOnly {
			blankNote = " — the extra lines are blank, so the blank-line count differs"
		}
		report.lineCount = fmt.Sprintf("  line-count difference: your old_string has %d extra line(s), the file has %d extra line(s)%s", closest.LineDiffOldExtra, closest.LineDiffSrcExtra, blankNote)
	}
	// Diffs[0] is already rendered as the first mismatch above; the rest are
	// listed here. The slice can be empty when the diagnostic normalizer is
	// more tolerant than the matcher that rejected the block — the window is
	// "closest" yet has no differing line under the looser comparison — so the
	// skip must not assume an element.
	for _, d := range restAfterFirst(closest.Diffs) {
		report.diffs = append(report.diffs,
			fmt.Sprintf("  differing line %d (file %d): expected %s", d.ExpectedLine, d.FileLine, d.Expected),
			fmt.Sprintf("    actual %s", d.Actual))
	}
	report.guidance = report.rebuildGuidance(oldText)
	return report, true
}

// lines returns the full report: every difference plus the rebuild guidance.
func (r editClosestMatchReport) lines() []string {
	out := []string{r.header, r.fileLine, r.yourLine}
	for _, line := range []string{r.hint, r.lineCount} {
		if line != "" {
			out = append(out, line)
		}
	}
	out = append(out, r.diffs...)
	return append(out, r.guidance)
}

// compactLines shows only the first differing line. Any omitted differences,
// shifted lines or clipped text require a fresh read of the target range.
// That decision is independent of the full single-edit diagnostic's capacity.
func (r editClosestMatchReport) compactLines(oldText string) []string {
	out := []string{"  " + r.header, r.fileLine}
	if r.closest.DiffLines != 1 || r.closest.LineDiffOldExtra+r.closest.LineDiffSrcExtra > 0 ||
		utf8.RuneCountInString(r.closest.ExpectedRaw) > maxToolLineRunes || utf8.RuneCountInString(r.closest.ActualRaw) > maxToolLineRunes {
		out = append(out, "  "+r.freshReadGuidance(oldText, "The displayed lines are not enough to rebuild old_string"))
	} else {
		out = append(out, r.yourLine)
	}
	return out
}

// driftedBeyondRebuild reports that the shown differing lines are not enough
// to reconstruct old_string, so the model needs a fresh bounded read.
func (r editClosestMatchReport) driftedBeyondRebuild() bool {
	return r.closest.LineDiffOldExtra+r.closest.LineDiffSrcExtra > maxDiffLinesShown || r.closest.DiffLines > maxDiffLinesShown
}

func (r editClosestMatchReport) rebuildGuidance(oldText string) string {
	if !r.driftedBeyondRebuild() {
		return "Rebuild old_string from the file lines above (copy them exactly), then retry"
	}
	var reason string
	if drifted := r.closest.LineDiffOldExtra + r.closest.LineDiffSrcExtra; drifted > 0 {
		reason = fmt.Sprintf("Whole lines drifted (%d vs %d extra line(s) between you and the file)", r.closest.LineDiffOldExtra, r.closest.LineDiffSrcExtra)
		if r.closest.DiffLines > maxDiffLinesShown {
			reason += fmt.Sprintf(" and %d+ lines differ in place", maxDiffLinesShown)
		}
	} else {
		reason = fmt.Sprintf("More than %d lines differ in place", maxDiffLinesShown)
	}
	return r.freshReadGuidance(oldText, reason+", so the lines above are not enough to rebuild old_string")
}

func (r editClosestMatchReport) freshReadGuidance(oldText, reason string) string {
	oldLineCount := strings.Count(strings.TrimSuffix(lfLineBreaks(oldText), "\n"), "\n") + 1
	return fmt.Sprintf("%s: read the file with offset=%d limit=%d (the closest match range), rebuild old_string from that fresh output, or use a smaller 2-4 line anchor; do not retype the block from memory",
		reason, r.closest.StartLine, oldLineCount)
}
