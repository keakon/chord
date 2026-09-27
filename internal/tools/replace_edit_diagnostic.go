package tools

import (
	"fmt"
	"math"
	"strings"
)

// editClosestMatchDiagnostic compares line windows, so it first maps every
// line ending of the file and of oldText to LF: line breaks are not what the
// model got wrong (the matchers already adapt them), and a leftover CR would
// show up as a phantom difference on every line — or, in a CR-only file, leave
// no line break to split on at all.
func editClosestMatchDiagnostic(content, oldText string) string {
	content, oldText = lfLineBreaks(content), lfLineBreaks(oldText)
	if closest, ok := editClosestMatch(content, oldText); ok {
		sim := int(math.Round(closest.Similarity * 100))
		var b strings.Builder
		fmt.Fprintf(&b, "Closest match is at line %d (%d%% similar, %d character difference):\n", closest.StartLine, sim, closest.DiffRunes)
		fmt.Fprintf(&b, "  file line %d: %s\n", closest.FileDiffLine, closest.Actual)
		fmt.Fprintf(&b, "  your line %d: %s\n", closest.ExpectedDiffLine, closest.Expected)
		if hint := firstMismatchHint(closest.ExpectedRaw, closest.ActualRaw); hint != "" {
			fmt.Fprintf(&b, "  %s\n", hint)
		}
		if closest.LineDiffOldExtra > 0 || closest.LineDiffSrcExtra > 0 {
			blankNote := ""
			if closest.LineDiffBlankOnly {
				blankNote = " — the extra lines are blank, so the blank-line count differs"
			}
			fmt.Fprintf(&b, "  line-count difference: your old_string has %d extra line(s), the file has %d extra line(s)%s\n", closest.LineDiffOldExtra, closest.LineDiffSrcExtra, blankNote)
		}
		// Diffs[0] is already rendered above as the first mismatch; the
		// rest are listed here. The slice can be empty when the diagnostic
		// normalizer is more tolerant than the matcher that rejected the
		// block — the window is "closest" yet has no differing line under
		// the looser comparison — so the skip must not assume an element.
		for _, d := range restAfterFirst(closest.Diffs) {
			fmt.Fprintf(&b, "  differing line %d (file %d): expected %s\n    actual %s\n", d.ExpectedLine, d.FileLine, d.Expected, d.Actual)
		}
		// When whole lines drifted, the few differing lines shown above
		// cannot reconstruct the target block: the model would have to
		// retype the lines between them from memory, which is exactly
		// how drift compounds. Send it to a fresh bounded read of the
		// target range instead.
		drifted := closest.LineDiffOldExtra + closest.LineDiffSrcExtra
		oldLineCount := strings.Count(strings.TrimSuffix(oldText, "\n"), "\n") + 1
		if drifted > maxDiffLinesShown || closest.DiffLines > maxDiffLinesShown {
			var reason string
			if drifted > 0 {
				reason = fmt.Sprintf("Whole lines drifted (%d vs %d extra line(s) between you and the file)", closest.LineDiffOldExtra, closest.LineDiffSrcExtra)
				if closest.DiffLines > maxDiffLinesShown {
					reason += fmt.Sprintf(" and %d+ lines differ in place", maxDiffLinesShown)
				}
			} else {
				reason = fmt.Sprintf("More than %d lines differ in place", maxDiffLinesShown)
			}
			fmt.Fprintf(&b, "%s, so the lines above are not enough to rebuild old_string: read the file with offset=%d limit=%d (the closest match range), rebuild old_string from that fresh output, or use a smaller 2-4 line anchor; do not retype the block from memory",
				reason, closest.StartLine, oldLineCount)
		} else {
			b.WriteString("Rebuild old_string from the file lines above (copy them exactly), then retry")
		}
		return b.String()
	}
	return ""
}
