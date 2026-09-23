package tools

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode"
)

func applyApplyPatchHunks(ctx context.Context, content string, hunks []applyPatchHunk) (string, int, int, []applyPatchFuzzyReplacement, error) {
	newline := "\n"
	if strings.Contains(content, "\r\n") {
		newline = "\r\n"
	}
	logical := strings.ReplaceAll(content, "\r\n", "\n")
	var fileLines []string
	if logical != "" {
		fileLines = strings.Split(strings.TrimSuffix(logical, "\n"), "\n")
	}
	searchStart := 0
	punctuationHunks := 0
	fuzzyHunks := 0
	var fuzzyReplacements []applyPatchFuzzyReplacement
	for i, hunk := range hunks {
		if len(hunks) > 1 {
			reportToolProgress(ctx, ToolProgressSnapshot{Text: fmt.Sprintf("matching hunk %d/%d", i+1, len(hunks))})
		}
		headerPos := -1
		if hunk.Header != "" {
			if pos := findApplyPatchSequence(fileLines, []string{hunk.Header}, searchStart, false); pos >= 0 {
				headerPos = pos
				searchStart = pos + 1
			}
		}
		oldSeq := make([]string, 0, len(hunk.Lines))
		for _, line := range hunk.Lines {
			if line.Kind == ' ' || line.Kind == '-' {
				oldSeq = append(oldSeq, line.Text)
			}
		}
		match := -1
		if len(oldSeq) == 0 {
			match = len(fileLines)
		} else {
			match = findApplyPatchSequence(fileLines, oldSeq, searchStart, hunk.EndOfFile)
		}
		if len(oldSeq) > 0 && match < 0 && headerPos >= 0 {
			// The canonical format puts context strictly after the @@ header, but
			// models often repeat the header text as the hunk's first context line.
			// Retry from the header itself so both styles anchor to one location.
			match = findApplyPatchSequence(fileLines, oldSeq, headerPos, hunk.EndOfFile)
		}
		punctuationMatch := false
		fuzzyMatch := false
		fuzzyRemovedIndex := -1
		var punctuationCandidates []int
		var fuzzyCandidates []int
		if match < 0 && len(oldSeq) > 0 {
			match, punctuationCandidates = findUniqueApplyPatchSequence(
				fileLines, oldSeq, searchStart, hunk.EndOfFile, normalizePatchTolerantLine,
			)
			if match >= 0 {
				punctuationMatch = true
			}
		}
		if match < 0 && len(oldSeq) > 0 {
			match, fuzzyRemovedIndex, fuzzyCandidates = findUniqueFuzzyApplyPatchMatch(fileLines, hunk, oldSeq, searchStart)
			if match >= 0 {
				fuzzyMatch = true
			}
		}
		if match < 0 {
			return "", 0, 0, nil, applyPatchPartialHunkError(applyPatchHunkNotFoundErrorWithHints(fileLines, oldSeq, searchStart, i, len(hunks), hunk.EndOfFile, punctuationCandidates, fuzzyCandidates, hunkHasWhitespaceOnlyContext(hunk)), i, len(hunks))
		}
		matched := fileLines[match : match+len(oldSeq)]
		newSeq := buildApplyPatchNewSequence(hunk, matched)
		if punctuationMatch {
			var ok bool
			newSeq, ok = buildPunctuationTolerantApplyPatchSequence(hunk, matched)
			if !ok {
				return "", 0, 0, nil, applyPatchPartialHunkError(applyPatchUnsafePunctuationMatchError(oldSeq, i, len(hunks), match), i, len(hunks))
			}
			punctuationHunks++
		}
		if fuzzyMatch {
			fuzzyHunks++
			// The removed line's offset comes from the matcher itself, which
			// derived it from the same guards that admitted the hunk. Deriving
			// it a second time here would be a copy of those guards that a
			// future relaxation could silently outgrow — the old copy indexed
			// oldSeq[-1] whenever the removed line was no longer guaranteed to
			// exist.
			var addedText string
			for _, line := range hunk.Lines {
				if line.Kind == '+' {
					addedText = line.Text
				}
			}
			fuzzyReplacements = append(fuzzyReplacements, applyPatchFuzzyReplacement{
				removed: oldSeq[fuzzyRemovedIndex],
				actual:  fileLines[match+fuzzyRemovedIndex],
				added:   addedText,
				line:    match + fuzzyRemovedIndex + 1,
			})
		}
		replaced := make([]string, 0, len(fileLines)-len(oldSeq)+len(newSeq))
		replaced = append(replaced, fileLines[:match]...)
		replaced = append(replaced, newSeq...)
		replaced = append(replaced, fileLines[match+len(oldSeq):]...)
		fileLines = replaced
		if len(oldSeq) > 0 {
			searchStart = match + len(newSeq)
		}
	}
	out := strings.Join(fileLines, "\n")
	if len(fileLines) > 0 {
		out += "\n"
	}
	if newline == "\r\n" {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	return out, punctuationHunks, fuzzyHunks, fuzzyReplacements, nil
}

// The fuzzy layer's acceptance gates. It is the only layer that writes the
// model's line over a file line the model quoted wrongly, so it is bounded on
// two axes at once:
//
//   - maxApplyPatchFuzzyRuneDistance is the hard limit: at most one rune of
//     the normalized removed line may differ from the file's. Everything a
//     tolerant normalizer can forgive (punctuation variants, indentation,
//     inter-word and repeated whitespace) has already been folded out by the
//     time a candidate reaches here, so a surviving difference is real
//     content, and one rune is where a transcription slip stops and a
//     semantic change begins.
//   - minApplyPatchFuzzySimilarity keeps the proportional check as a second,
//     narrower gate: on a very short line even one rune is most of the line.
//     It cannot be the only gate — a ratio grows the allowance with line
//     length (0.9 permits six runes on a 66-rune line), which is backwards:
//     "const n = 30" vs "const n = 10" scores 0.909 while being a real value
//     change.
//
// minApplyPatchFuzzyContextRunes is the distinctiveness floor for context:
// at least one context line must carry this many runes after normalization,
// so a hunk anchored only on "}" and "return" is not treated as uniquely
// placed.
const (
	maxApplyPatchFuzzyRuneDistance = 1
	minApplyPatchFuzzySimilarity   = 0.9
	minApplyPatchFuzzyContextRunes = 4
)

// applyPatchFuzzyReplacement records one fuzzy hunk replacement for the result
// Note so the model can audit what was actually overwritten: removed is the
// hunk's claimed line, actual is the file line it replaced, added is the line
// written in their place, and line is the 1-based position of the replaced
// line in the file as it stood when the hunk was applied (earlier hunks of the
// same patch have already shifted it).
type applyPatchFuzzyReplacement struct {
	removed string
	actual  string
	added   string
	line    int
}

// findUniqueFuzzyApplyPatchMatch permits only a narrow stale-line recovery:
// one changed removed line differing from the file by at most
// maxApplyPatchFuzzyRuneDistance runes after normalization, distinctive
// unchanged context on both sides of it, and exactly one candidate window. The
// matched current line is replaced, while all context bytes are preserved from
// the file.
//
// It returns the matched window start, the removed line's offset inside the
// window (so the caller can quote and locate the replaced line without
// recomputing an offset that must stay in step with these guards), and the
// candidate list for the ambiguity hint. removedIndex is -1 when there is no
// unique match.
func findUniqueFuzzyApplyPatchMatch(fileLines []string, hunk applyPatchHunk, oldSeq []string, searchStart int) (match, removedIndex int, candidates []int) {
	removedIndex = -1
	removedCount := 0
	addedCount := 0
	distinctiveContext := false
	for _, line := range hunk.Lines {
		switch line.Kind {
		case '-':
			removedCount++
		case '+':
			addedCount++
		case ' ':
			norm := normalizePatchTolerantLine(line.Text)
			if norm == "" {
				return -1, -1, nil
			}
			// Distinctiveness is a per-line property, not a sum: "}" plus
			// "return" clears a combined four runes while anchoring the hunk
			// to boilerplate that repeats all over the file.
			if len([]rune(norm)) >= minApplyPatchFuzzyContextRunes {
				distinctiveContext = true
			}
		}
	}
	if removedCount != 1 || addedCount != 1 || !distinctiveContext {
		return -1, -1, nil
	}
	oldIndex := 0
	contextBefore := false
	contextAfter := false
	for _, line := range hunk.Lines {
		switch line.Kind {
		case '-':
			removedIndex = oldIndex
			oldIndex++
		case ' ':
			if removedIndex < 0 {
				contextBefore = true
			} else {
				contextAfter = true
			}
			oldIndex++
		}
	}
	// contextBefore && contextAfter already implies at least two context
	// lines, so no separate count check is needed.
	if removedIndex < 0 || !contextBefore || !contextAfter || oldIndex != len(oldSeq) {
		return -1, -1, nil
	}
	normOld := make([]string, len(oldSeq))
	for i, line := range oldSeq {
		normOld[i] = normalizePatchTolerantLine(line)
	}
	oldRemoved := normOld[removedIndex]
	if oldRemoved == "" {
		return -1, -1, nil
	}
	oldRemovedRunes := len([]rune(oldRemoved))

	maxStart := len(fileLines) - len(oldSeq)
	if maxStart < 0 {
		return -1, -1, nil
	}
	start := max(0, searchStart)
	if hunk.EndOfFile {
		start = maxStart
		maxStart = start
	}
	if start > maxStart {
		return -1, -1, nil
	}
	// Normalize each file line in the search window once, not once per
	// candidate position it participates in — mirroring
	// findUniqueApplyPatchSequence, where the same quadratic re-normalization
	// was the cost being removed.
	normFile := make([]string, len(fileLines)-start)
	for i := start; i < len(fileLines); i++ {
		normFile[i-start] = normalizePatchTolerantLine(fileLines[i])
	}
	// Distance work is charged against a budget, like
	// applyPatchHunkClosestLine's closestScanBudget: a large file must not turn
	// a near-miss hunk into a full-file Levenshtein sweep. An exhausted budget
	// abandons the fuzzy layer entirely (returning no candidate) rather than
	// accepting whatever it found first, so the verdict never depends on where
	// the budget happened to run out.
	const fuzzyScanBudget = 200_000 // total (removed × line) rune-pair budget
	budget := fuzzyScanBudget
	for candidate := start; candidate <= maxStart; candidate++ {
		matches := true
		for i := range oldSeq {
			if i == removedIndex {
				continue
			}
			if normFile[candidate-start+i] != normOld[i] {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		actual := normFile[candidate-start+removedIndex]
		if actual == oldRemoved {
			continue
		}
		actualRunes := len([]rune(actual))
		if actualRunes == 0 {
			continue
		}
		// A length gap alone is a lower bound on the edit distance, so an
		// over-long candidate is rejected without running the distance.
		if absInt(actualRunes-oldRemovedRunes) > maxApplyPatchFuzzyRuneDistance {
			continue
		}
		work := oldRemovedRunes * actualRunes
		if work > budget {
			return -1, -1, nil
		}
		budget -= work
		distance := levenshteinDistance(oldRemoved, actual)
		if distance > maxApplyPatchFuzzyRuneDistance {
			continue
		}
		longer := max(oldRemovedRunes, actualRunes)
		if 1-float64(distance)/float64(longer) < minApplyPatchFuzzySimilarity {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 1 {
		return candidates[0], removedIndex, candidates
	}
	return -1, -1, candidates
}

func buildApplyPatchNewSequence(hunk applyPatchHunk, matched []string) []string {
	newSeq := make([]string, 0, len(hunk.Lines))
	oldIndex := 0
	for _, line := range hunk.Lines {
		switch line.Kind {
		case ' ':
			newSeq = append(newSeq, matched[oldIndex])
			oldIndex++
		case '-':
			oldIndex++
		case '+':
			newSeq = append(newSeq, line.Text)
		}
	}
	return newSeq
}

func buildPunctuationTolerantApplyPatchSequence(hunk applyPatchHunk, matched []string) ([]string, bool) {
	newSeq := make([]string, 0, len(hunk.Lines))
	oldIndex := 0
	for i := 0; i < len(hunk.Lines); {
		if hunk.Lines[i].Kind == ' ' {
			newSeq = append(newSeq, matched[oldIndex])
			oldIndex++
			i++
			continue
		}

		var removed, added []string
		var matchedRemoved []string
		for i < len(hunk.Lines) && hunk.Lines[i].Kind != ' ' {
			switch hunk.Lines[i].Kind {
			case '-':
				removed = append(removed, hunk.Lines[i].Text)
				matchedRemoved = append(matchedRemoved, matched[oldIndex])
				oldIndex++
			case '+':
				added = append(added, hunk.Lines[i].Text)
			}
			i++
		}

		switch {
		case len(removed) == 0:
			newSeq = append(newSeq, added...)
		case len(added) == 0:
			continue
		case len(removed) != len(added):
			return nil, false
		default:
			for j := range removed {
				line, ok := punctuationTolerantReplacementLine(matchedRemoved[j], removed[j], added[j])
				if !ok {
					return nil, false
				}
				newSeq = append(newSeq, line)
			}
		}
	}
	return newSeq, true
}

func punctuationTolerantReplacementLine(current, oldText, newText string) (string, bool) {
	if current == oldText {
		return newText, true
	}
	currentRunes := []rune(current)
	oldRunes := []rune(oldText)
	newRunes := []rune(newText)
	normCurrent, currentSpans := normalizePunctLineWithSpaceFolding(currentRunes)
	normOld, oldSpans := normalizePunctLineWithSpaceFolding(oldRunes)
	if !slices.Equal(normCurrent, normOld) {
		return "", false
	}
	normNew, newSpans := normalizePunctLineWithSpaceFolding(newRunes)

	// Common prefix/suffix in normalized space, extended only while the
	// original bytes also match: where old/new differ in original bytes
	// (e.g. "：" vs ": "), the difference is the model's intended delta and
	// must come from newText, not from the file's bytes. This keeps the
	// file's own punctuation in any unchanged context.
	prefix := 0
	for prefix < len(normOld) && prefix < len(normNew) &&
		normOld[prefix] == normNew[prefix] &&
		slices.Equal(oldRunes[oldSpans[prefix].start:oldSpans[prefix].end],
			newRunes[newSpans[prefix].start:newSpans[prefix].end]) {
		prefix++
	}
	suffix := 0
	for suffix < len(normOld)-prefix && suffix < len(normNew)-prefix {
		oi := len(normOld) - 1 - suffix
		ni := len(normNew) - 1 - suffix
		if normOld[oi] != normNew[ni] ||
			!slices.Equal(oldRunes[oldSpans[oi].start:oldSpans[oi].end],
				newRunes[newSpans[ni].start:newSpans[ni].end]) {
			break
		}
		suffix++
	}
	// prefix == 0 && suffix == 0 (every rune of the replacement differs from
	// the file's bytes) needs no special refusal: there is no unchanged text
	// to preserve, so splicing the model's new line verbatim is exactly the
	// requested edit — the same outcome the edit tool's tolerant path allows.

	var b strings.Builder
	if prefix > 0 {
		b.WriteString(string(currentRunes[currentSpans[0].start:currentSpans[prefix-1].end]))
	}
	deltaStart := prefix
	deltaEnd := len(normNew) - suffix
	if deltaStart < deltaEnd {
		b.WriteString(string(newRunes[newSpans[deltaStart].start:newSpans[deltaEnd-1].end]))
	}
	if suffix > 0 {
		b.WriteString(string(currentRunes[currentSpans[len(normCurrent)-suffix].start:currentSpans[len(normCurrent)-1].end]))
	}
	return b.String(), true
}

func findUniqueApplyPatchSequence(lines, pattern []string, start int, eof bool, normalize func(string) string) (int, []int) {
	if len(pattern) == 0 || len(pattern) > len(lines) {
		return -1, nil
	}
	from := max(start, 0)
	to := len(lines) - len(pattern)
	if eof {
		from = to
	}
	var candidates []int
	// See findApplyPatchSequence: normalize the pattern once, not per position.
	// File lines are also normalized once (into a slice aligned with `from`)
	// rather than once per candidate position, so a long file does not pay the
	// per-line normalization cost once for every position it appears in.
	normalizedPattern := make([]string, len(pattern))
	for j, line := range pattern {
		normalizedPattern[j] = normalize(line)
	}
	normalizedLines := make([]string, len(lines)-from)
	for i := from; i < len(lines); i++ {
		normalizedLines[i-from] = normalize(lines[i])
	}
	for i := from; i <= to; i++ {
		matched := true
		for j := range normalizedPattern {
			if normalizedLines[i-from+j] != normalizedPattern[j] {
				matched = false
				break
			}
		}
		if matched {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) == 1 {
		return candidates[0], candidates
	}
	return -1, candidates
}

func hunkHasWhitespaceOnlyContext(hunk applyPatchHunk) bool {
	for _, line := range hunk.Lines {
		if line.Kind == ' ' && strings.TrimSpace(line.Text) == "" {
			return true
		}
	}
	return false
}

func applyPatchHunkNotFoundErrorWithHints(fileLines, oldSeq []string, searchStart, index, total int, hunkEndOfFile bool, punctuationCandidates, fuzzyCandidates []int, whitespaceOnlyContext bool) error {
	parts := []string{fmt.Sprintf("hunk not found (%d/%d)", index+1, total)}
	if whitespaceOnlyContext {
		parts = append(parts, "whitespace-only context lines are literal source lines; the leading space marks context, so do not use them as omitted-line placeholders")
	}
	if expected := applyPatchExpectedLineDescription(oldSeq); expected != "" {
		parts = append(parts, expected)
	}
	if len(fuzzyCandidates) > 1 {
		parts = append(parts, "safe fuzzy matching is ambiguous at lines "+formatApplyPatchCandidateLines(fuzzyCandidates))
	}
	if len(punctuationCandidates) > 1 {
		parts = append(parts, tolerantMatchNote+" matching is ambiguous at lines "+formatApplyPatchCandidateLines(punctuationCandidates))
	}
	if earlier := findApplyPatchSequence(fileLines, oldSeq, 0, false); earlier >= 0 && earlier < searchStart {
		parts = append(parts, fmt.Sprintf("matching context exists earlier at line %d, but hunks must follow file order", earlier+1))
	} else if line := findApplyPatchSubstringLine(fileLines, oldSeq, searchStart, hunkEndOfFile); line >= 0 {
		parts = append(parts, fmt.Sprintf("the expected text is only part of current line %d; include that complete line in the hunk", line+1))
	} else if line, matched := applyPatchHunkMismatchLine(fileLines, oldSeq, searchStart, hunkEndOfFile); matched >= 1 && matched < len(oldSeq) {
		// The first expected line exists in the file (normalized, at or after
		// the hunk's legal search window), but the hunk's multi-line sequence
		// breaks somewhere. Pinpoint the first diverging line so the model can
		// see what actually changed.
		detail := fmt.Sprintf("the first %d line(s) of the hunk match at line %d, but the next expected line differs from the file", matched, line+1)
		expected := truncateToolLine(oldSeq[matched])
		if line+matched < len(fileLines) {
			detail += fmt.Sprintf(": expected %s, found %s", expected, truncateToolLine(fileLines[line+matched]))
			// Same invisible-difference visibility as edit: name the exact
			// first differing rune by code point, so a dropped space or an
			// orphan variation selector shows up instead of rendering
			// identically to the expected line.
			if hint := firstMismatchHint(oldSeq[matched], fileLines[line+matched]); hint != "" {
				detail += "; " + hint
			}
		} else {
			detail += fmt.Sprintf(": expected %s, but the file has no more lines", expected)
		}
		// Same whole-line-drift visibility as edit: when the hunk and the
		// file window differ by whole lines (extra blanks, a heading shifted
		// by one line), the line-level alignment names the drift so the model
		// is not left staring at two unrelated position-shifted lines.
		window := fileLines
		if line+len(oldSeq) <= len(fileLines) {
			window = fileLines[line : line+len(oldSeq)]
		} else if line < len(fileLines) {
			window = fileLines[line:]
		}
		oldExtra, srcExtra, blankOnly := alignEditWindowLines(oldSeq, window, 0, normalizePatchTolerantLine)
		if oldExtra > 0 || srcExtra > 0 {
			blankNote := ""
			if blankOnly {
				blankNote = " — the extra lines are blank, so the blank-line count differs"
			}
			detail += fmt.Sprintf("; line-count difference: your hunk has %d extra line(s), the file has %d extra line(s)%s", oldExtra, srcExtra, blankNote)
		}
		parts = append(parts, detail+"; the file may have changed — re-read the current target range and rebuild this hunk from current complete lines")
	} else if len(punctuationCandidates) <= 1 && len(fuzzyCandidates) <= 1 {
		// With multiple tolerant candidates the match is ambiguous; a single
		// closest line would masquerade as the unique suggestion and
		// contradict the ambiguity note above, so require more context
		// instead of guessing.
		if line, sim := applyPatchHunkClosestLine(fileLines, oldSeq, searchStart, hunkEndOfFile); line >= 0 && sim >= minEditSuggestionSimilarity {
			// No line of the file matches the hunk's first expected line (even
			// under normalization). Point at the file line most similar to it
			// so the model sees what the file
			// actually contains instead of guessing. Below the threshold the
			// mismatch is too large for a helpful near-match, so fall through to
			// the generic missing-line hint.
			detail := fmt.Sprintf("the first line of the hunk matches no file line; the closest file line is %d (%d%% similar): found %s, expected %s", line+1, int(math.Round(sim*100)), truncateToolLine(fileLines[line]), truncateToolLine(oldSeq[0]))
			parts = append(parts, detail+"; the file may have changed — re-read the current target range and rebuild this hunk from current complete lines")
		} else if applyPatchExpectedLineMissing(fileLines, oldSeq) {
			parts = append(parts, "the expected line does not exist in the current file; the file may have changed since it was last read, or the line was invented — re-read the current target range and rebuild this hunk from current complete lines")
		}
	}
	parts = append(parts, "do not retry the same hunk unchanged")
	return fmt.Errorf("%s", strings.Join(parts, "; "))
}

// applyPatchHunkWindowStart returns the first file line index a hunk may
// match at. Hunks must follow file order (the searchStart bound), so all
// diagnostic scans share this lower bound; an EOF hunk's old sequence must
// also match the file's tail, shifting the window start to the suffix
// position. All scans must respect the same window so a suggestion never
// points at a position a retry could not use.
func applyPatchHunkWindowStart(fileLines, oldSeq []string, searchStart int, eof bool) int {
	from := max(searchStart, 0)
	if eof && len(oldSeq) > 0 {
		// EOF hunks may only match against the tail: the whole file, since
		// oldSeq must be a suffix. A suffix match also cannot start before
		// searchStart (hunks must follow file order), so start at the later
		// of the two bounds.
		from = max(from, len(fileLines)-len(oldSeq))
	}
	if from < 0 {
		return 0
	}
	return from
}

// applyPatchHunkClosestLine scans fileLines for the line most similar to the
// hunk's first expected line (compared under the tolerance normalizer}, when
// no line of the hunk matches at all. It only considers the legal match
// window: lines at or after searchStart for ordered hunks, and the tail
// window for EOF hunks — mirroring findUniqueApplyPatchSequence — so a
// suggestion never points at a position a retry could not use. It returns the
// 0-based line index and the similarity (0..1); line is -1 when the file is
// empty or the window has no comparable lines. This is the "hunk is
// completely unrelated" fallback: the model sees the file's actual closest
// content instead of a bare re-read hint.
func applyPatchHunkClosestLine(fileLines, oldSeq []string, searchStart int, eof bool) (line int, sim float64) {
	if len(oldSeq) == 0 || len(fileLines) == 0 {
		return -1, 0
	}
	needle := normalizePatchTolerantLine(oldSeq[0])
	if needle == "" {
		return -1, 0
	}
	needleRunes := len([]rune(needle))
	from := applyPatchHunkWindowStart(fileLines, oldSeq, searchStart, eof)
	if from >= len(fileLines) {
		return -1, 0
	}
	// Guard the failure path against pathological inputs: a full Levenshtein
	// on every file line of a large file with long lines can reach billions
	// of rune operations. Pre-filter by rune-length delta (a candidate whose
	// length differs from the needle by more than the current best distance
	// can never beat it) and cap the total work; past the budget the
	// suggestion degrades to the generic re-read hint instead of stalling.
	const closestScanBudget = 200_000 // total (needle × line) rune-pair budget
	bestLine, bestSim := -1, -1.0
	bestDist := -1
	budget := closestScanBudget
	for i := from; i < len(fileLines); i++ {
		norm := normalizePatchTolerantLine(fileLines[i])
		if norm == "" {
			continue
		}
		normRunes := len([]rune(norm))
		if bestDist >= 0 && absInt(normRunes-needleRunes) >= bestDist {
			continue // cannot beat the current best edit distance
		}
		work := needleRunes * normRunes
		if budget <= 0 || work > budget {
			// An exhausted budget stops the scan; a single oversized line must
			// not — later lines can be far cheaper, and the budget is only
			// spent by lines actually processed, so skipping keeps the total
			// work bounded while letting the whole window compete.
			if budget <= 0 {
				break
			}
			continue
		}
		budget -= work
		dist := levenshteinDistance(needle, norm)
		// Similarity relative to the longer of the two lines so a short
		// file line next to a long hunk line is not over-rated.
		longer := max(needleRunes, normRunes)
		s := 1.0 - float64(dist)/float64(longer)
		if s > bestSim {
			bestSim, bestLine, bestDist = s, i, dist
		}
	}
	if bestLine < 0 {
		return -1, 0
	}
	return bestLine, bestSim
}

// applyPatchHunkMismatchLine locates the longest contiguous run of oldSeq
// (compared under the tolerance normalizer) that appears in fileLines within
// the hunk's legal match window (see applyPatchHunkWindowStart), returning
// the file line index where it starts and how many lines matched. The window
// bound keeps the suggestion from pointing at a position a retry could not
// use. It diagnoses multi-line hunks whose first expected line exists but
// whose full sequence does not, so the error can report which line first
// diverges.
func applyPatchHunkMismatchLine(fileLines, oldSeq []string, searchStart int, eof bool) (line, matched int) {
	if len(oldSeq) == 0 || len(fileLines) == 0 {
		return -1, 0
	}
	normLines := make([]string, len(fileLines))
	for i, l := range fileLines {
		normLines[i] = normalizePatchTolerantLine(l)
	}
	normSeq := make([]string, len(oldSeq))
	for i, l := range oldSeq {
		normSeq[i] = normalizePatchTolerantLine(l)
	}
	bestLine, bestMatched := -1, 0
	for i := applyPatchHunkWindowStart(fileLines, oldSeq, searchStart, eof); i < len(normLines); i++ {
		k := 0
		for k < len(normSeq) && i+k < len(normLines) && normLines[i+k] == normSeq[k] {
			k++
		}
		if k > bestMatched {
			bestMatched, bestLine = k, i
		}
	}
	return bestLine, bestMatched
}

// applyPatchExpectedLineMissing reports whether the first expected line of
// oldSeq exists anywhere in fileLines under the tolerance normalizer (or as a
// substring of a longer line, which applyPatchHunkNotFoundErrorWithHints
// reports separately). The tool matches the on-disk file only — read history
// is the model's context, not a matching source — so a missing line means the
// patch is based on stale or invented content and the model should re-read.
func applyPatchExpectedLineMissing(fileLines, oldSeq []string) bool {
	if len(oldSeq) == 0 {
		return false
	}
	needle := normalizePatchTolerantLine(oldSeq[0])
	if needle == "" {
		return false
	}
	for _, line := range fileLines {
		if normalizePatchTolerantLine(line) == needle {
			return false
		}
	}
	return true
}

// applyPatchPartialHunkError layers a "prior hunks matched but were not
// applied" note onto a single-file hunk failure when earlier hunks in the same
// file already matched successfully. The file is atomic, so those earlier
// hunks must be included again even though they matched in memory.
func applyPatchPartialHunkError(err error, index, total int) error {
	if err == nil || index == 0 {
		return err
	}
	return fmt.Errorf("%w; note: hunks 1..%d matched successfully in memory but were not applied because this hunk failed, so keep all hunks 1..%d together when rebuilding this operation", err, index, total)
}

func applyPatchUnsafePunctuationMatchError(oldSeq []string, index, total, match int) error {
	parts := []string{
		fmt.Sprintf("hunk not found (%d/%d)", index+1, total),
		fmt.Sprintf("a %s candidate exists at line %d, but the replacement cannot preserve unchanged text safely", tolerantMatchNote, match+1),
	}
	if expected := applyPatchExpectedLineDescription(oldSeq); expected != "" {
		parts = append(parts, expected)
	}
	parts = append(parts, "re-read the current target range and use exact complete lines")
	return fmt.Errorf("%s", strings.Join(parts, "; "))
}

func applyPatchExpectedLineDescription(oldSeq []string) string {
	if len(oldSeq) == 0 {
		return ""
	}
	runes := []rune(oldSeq[0])
	const maxRunes = 120
	if len(runes) > maxRunes {
		return "first expected line prefix: " + quoteToolLine(string(runes[:maxRunes]))
	}
	return "first expected complete line: " + quoteToolLine(string(runes))
}

func findApplyPatchSubstringLine(fileLines, oldSeq []string, searchStart int, eof bool) int {
	if len(oldSeq) != 1 {
		return -1
	}
	needle := normalizePatchTolerantLine(oldSeq[0])
	if needle == "" {
		return -1
	}
	for i := applyPatchHunkWindowStart(fileLines, oldSeq, searchStart, eof); i < len(fileLines); i++ {
		normalized := normalizePatchTolerantLine(fileLines[i])
		if normalized != needle && strings.Contains(normalized, needle) {
			return i
		}
	}
	return -1
}

func formatApplyPatchCandidateLines(candidates []int) string {
	const maxCandidates = 3
	parts := make([]string, 0, min(len(candidates), maxCandidates))
	for _, candidate := range candidates[:min(len(candidates), maxCandidates)] {
		parts = append(parts, fmt.Sprintf("%d", candidate+1))
	}
	if len(candidates) > maxCandidates {
		parts = append(parts, "…")
	}
	return strings.Join(parts, ", ")
}

func findApplyPatchSequence(lines, pattern []string, start int, eof bool) int {
	if len(pattern) == 0 || len(pattern) > len(lines) {
		return -1
	}
	from := start
	if eof {
		// An "*** End of File" hunk only matches the tail of the file, so the
		// scan is pinned to the single trailing position.
		from = len(lines) - len(pattern)
	}
	if from < 0 {
		from = 0
	}
	// Whitespace-only layers. Punctuation tolerance is deliberately NOT a layer
	// here: it runs through findUniqueApplyPatchSequence instead, which rejects
	// ambiguous matches and splices the replacement over the file's original
	// bytes. Folding it into this first-match-wins cascade would silently pick
	// one of several equally plausible positions.
	normalizers := []func(string) string{
		func(s string) string { return s },
		func(s string) string { return strings.TrimRightFunc(s, unicode.IsSpace) },
		strings.TrimSpace,
	}
	for layer, normalize := range normalizers {
		// Normalize the pattern once per layer instead of at every scan
		// position: on a mismatch-heavy file (the common hunk-not-found
		// path) the inner loop otherwise re-normalizes pattern lines
		// O(positions) times, and the unicode layer allocates per call. Keep
		// the exact-match layer allocation-free because it is the common path.
		normalizedPattern := pattern
		if layer > 0 {
			normalizedPattern = make([]string, len(pattern))
			for j, line := range pattern {
				normalizedPattern[j] = normalize(line)
			}
		}
		for i := from; i <= len(lines)-len(pattern); i++ {
			matched := true
			for j := range normalizedPattern {
				if normalize(lines[i+j]) != normalizedPattern[j] {
					matched = false
					break
				}
			}
			if matched {
				return i
			}
		}
	}
	return -1
}
