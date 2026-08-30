package tools

import (
	"cmp"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// tolerantMatchNote is the user-visible marker for edits matched via the
// punctuation/whitespace tolerance fallback. Both Edit and ApplyPatch report
// it so the model knows the match was not exact (it may have been a
// punctuation variant or a dropped/inserted word space) and can avoid
// repeating the same variant.
const tolerantMatchNote = "punctuation/whitespace-tolerant"

func resolveEditPathForBase(path, baseDir string) (string, error) {
	resolved, err := resolveToolPathInDir(path, baseDir)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	return filepath.Clean(resolved), nil
}

func ResolveEditPathInDir(path, baseDir string) (string, error) {
	return resolveEditPathForBase(path, baseDir)
}

func extractEditPathArg(args json.RawMessage) string {
	var parsed replaceEditArgs
	if err := json.Unmarshal(unwrapToolArgs(args), &parsed); err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Path)
}

// ExtractEditPathFromArgsInDir returns the effective path used by EditTool.
// Keep this parser aligned with replaceEditArgs so permission, locking, and
// file-state bookkeeping follow the same legacy alias as execution.
func ExtractEditPathFromArgsInDir(args json.RawMessage, baseDir string) string {
	path := extractEditPathArg(args)
	if path == "" {
		return ""
	}
	resolved, err := ResolveEditPathInDir(path, baseDir)
	if err != nil {
		return ""
	}
	return resolved
}

// ExtractEditPathFromArgs extracts the file path from Edit or ApplyPatch tool
// arguments for display purposes. It does not resolve the path against a
// BaseDir — callers without session context (e.g. TUI) use this to get the
// model-facing path for display. Agent-internal code should use the pipeline's
// trackedEditPathFromArgs instead.
func ExtractEditPathFromArgs(args json.RawMessage) string {
	path := extractEditPathArg(args)
	if path == "" {
		return ""
	}
	if resolved, err := resolveToolPath(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// normalizeProsePunctuationRune folds a single rune of prose punctuation to
// its ASCII equivalent: curly quotes to straight, dashes to ASCII hyphen, and
// full-width CJK punctuation to its half-width form. The mapping is 1:1 (one
// rune in, one rune out), so rune offsets are preserved when matching
// normalized text against the original.
//
// It is the shared tolerance surface for both ApplyPatch (line-level prose
// matching) and Edit (last-resort old_string matching): both treat these
// punctuation variants as equivalent when exact matching fails, and both
// preserve the file's original bytes for unchanged context. The mapping is
// rune-level and never folds whitespace; both tools additionally fold a
// single typesetting space adjacent to separator punctuation at the sequence
// level (see normalizePunctWithSpaceFolding), which a rune-level function
// cannot express because folding removes a rune.
func normalizeProsePunctuationRune(r rune) rune {
	switch r {
	case '“', '”', '„', '‟':
		return '"'
	case '‘', '’', '‚', '‛':
		return '\''
	case '–', '—', '−':
		return '-'
	case '，':
		return ','
	case '；':
		return ';'
	case '：':
		return ':'
	case '。':
		return '.'
	// U+FF0E FULLWIDTH FULL STOP is the CJK-IME period variant; it appears
	// when the model's tokenizer treats ": " or ". " as one token and
	// re-emits it in full-width form. It is folded alongside the ideographic
	// full stop (U+3002) to the same ASCII period.
	case '．':
		return '.'
	case '！':
		return '!'
	case '？':
		return '?'
	case '（':
		return '('
	case '）':
		return ')'
	}
	return r
}

// punctSpan maps one normalized rune back to the rune range [start, end)
// it came from in the pre-normalization text. When a separator punctuation
// absorbs an adjacent space, the span grows to include that space, so
// splicing a normalized match back into the file keeps the original bytes.
type punctSpan struct{ start, end int }

// isSpaceFoldingPunct reports whether the (already normalized) rune is a
// separator or closing punctuation that tolerates one adjacent typesetting
// space. Quotes and the hyphen are deliberately absent: a space after a quote
// or a dash carries meaning (" foo" vs "foo", "- foo" list items), while a
// space after a separator is pure typesetting.
func isSpaceFoldingPunct(r rune) bool {
	switch r {
	case ',', ';', ':', '.', '!', '?', '(', ')':
		return true
	}
	return false
}

// isWordRune reports whether r is a word character (letter or digit).
// Inter-word space folding only absorbs a space between two word runes:
// spaces next to quotes, dashes, or separator punctuation stay significant
// (" foo" vs "foo", "- foo" list items, "a : b" alignment).
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// isIgnorableRune reports whether r is a non-printing format rune with no
// visual width: variation selectors (U+FE00–U+FE0F, including the text/emoji
// presentation selectors U+FE0E/U+FE0F), zero-width space (U+200B),
// zero-width non-joiner/joiner (U+200C–U+200D), and word joiner
// (U+2060). Tools and IMEs leak these into copied text (drag-drop,
// tokenizer boundaries, CJK input), and editors may store them in files;
// they carry no content, so Edit comparisons fold them out of the comparison
// sequence. Splice-back preserves them by merging each rune into the previous
// rune's span (see normalizePunctWithSpaceFolding).
func isIgnorableRune(r rune) bool {
	switch r {
	case '\uFE00', '\uFE01', '\uFE02', '\uFE03', '\uFE04', '\uFE05', '\uFE06', '\uFE07', '\uFE08', '\uFE09', '\uFE0A', '\uFE0B', '\uFE0C', '\uFE0D', '\uFE0E', '\uFE0F':
		return true
	case '\u200B', '\u200C', '\u200D', '\u2060':
		return true
	}
	return false
}

// countStrippedInvisible reports, per rune, the invisible characters that the
// strip step actually removed from original (occurrences in original minus
// occurrences in stripped). Counting against the post-strip result keeps the
// report and the write result the same source of truth as the strip itself:
// runes the strip preserves — a ZWJ joining two emoji, a leading BOM, a
// variation selector carried by a base character — are never reported as
// cleaned.
func countStrippedInvisible(original, stripped string) map[rune]int {
	remaining := make(map[rune]int)
	for _, r := range stripped {
		if isZeroWidthFormatRune(r) || r == '\ufe0e' || r == '\ufe0f' {
			remaining[r]++
		}
	}
	var counts map[rune]int
	for _, r := range original {
		if !isZeroWidthFormatRune(r) && r != '\ufe0e' && r != '\ufe0f' {
			continue
		}
		if remaining[r] > 0 {
			remaining[r]--
			continue
		}
		if counts == nil {
			counts = make(map[rune]int)
		}
		counts[r]++
	}
	return counts
}

// describeInvisibleCounts renders the per-rune counts from
// countStrippedInvisible as a stable "U+XXXX×n" list (sorted by code point)
// with the shared trailing explanation, so every tool reports the cleaned
// characters identically.
func describeInvisibleCounts(counts map[rune]int) string {
	runes := make([]int, 0, len(counts))
	for r := range counts {
		runes = append(runes, int(r))
	}
	slices.Sort(runes)
	var b strings.Builder
	for i, r := range runes {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "U+%04X×%d", r, counts[rune(r)])
	}
	b.WriteString(" (invisible formatting characters that carry no content; do not include them in tool arguments)")
	return b.String()
}

// mergeInvisibleCounts combines per-rune counts (e.g. old_string and
// new_string) into one map for reporting.
func mergeInvisibleCounts(maps ...map[rune]int) map[rune]int {
	out := make(map[rune]int)
	for _, m := range maps {
		for r, n := range m {
			out[r] += n
		}
	}
	return out
}

// normalizePunctWithSpaceFolding applies the 1:1 punctuation mapping
// (normalizeProsePunctuationRune) and then folds whitespace out of the
// normalized sequence in two narrow cases: one ASCII space adjacent to
// separator punctuation (after ", ; : . ! ? (" or before ")"), making "："
// and ": " equivalent; and one inter-word space (exactly one U+0020 between
// two word characters), making "diff and" and "diffand" equivalent. Both
// cover models that tokenize ": " as one token and re-emit it as "：", drop
// the space ("refusal:the caller"), or drop/insert a word-boundary space.
//
// Folding is deliberately narrow: double spaces, leading/trailing spaces,
// tabs, and spaces adjacent to quotes, dashes, or punctuation stay
// significant, so indentation, alignment, and list structure still fail with
// the fresh-read hint. Each absorbed space is merged into the previous rune's
// span, so splicing a normalized match back into the file keeps the original
// bytes. Invisible format runes do not count as space neighbors and are looked
// through in both directions: a copy that leaks one beside a word-boundary
// space must normalize to the file's clean shape instead of keeping the space
// significant on one side only.
func normalizePunctWithSpaceFolding(rs []rune) (norm []rune, spans []punctSpan) {
	norm = make([]rune, 0, len(rs))
	spans = make([]punctSpan, 0, len(rs))
	for i := 0; i < len(rs); i++ {
		// Absorb invisible format runes (variation selectors, zero-width
		// spaces/joiners): they carry no visible text and models leak them
		// into old_string copies, while files may hold them from editors.

		// Fold them out of the comparison, merging each into the previous
		// rune's span so splice-back keeps the file's original bytes.

		if isIgnorableRune(rs[i]) {
			if len(spans) > 0 && spans[len(spans)-1].end == i {
				spans[len(spans)-1].end = i + 1
			}
			continue

		}
		// Fold one inter-word space: exactly one U+0020 between two word
		// characters. Merge it into the previous rune's span so splice-back
		// keeps the file's original bytes for it. Invisible format runes next
		// to the space are transparent in both directions: without the skip,
		// "return␣<FE0F>0" keeps the space significant on the model side while
		// the file's "return␣0" folds it, and the tolerant layers reject a
		// copy that differs only by the leaked rune.
		if rs[i] == ' ' && i > 0 {
			prev := i - 1
			for prev >= 0 && isIgnorableRune(rs[prev]) {
				prev--
			}
			next := i + 1
			for next < len(rs) && isIgnorableRune(rs[next]) {
				next++
			}
			if prev >= 0 && next < len(rs) && isWordRune(rs[prev]) && isWordRune(rs[next]) {
				if len(spans) > 0 && spans[len(spans)-1].end == i {
					spans[len(spans)-1].end = i + 1
				}
				continue
			}
		}
		r := normalizeProsePunctuationRune(rs[i])
		start := i
		if isSpaceFoldingPunct(r) {
			if r == ')' {
				// Absorb one preceding space. The space was already emitted
				// as a standalone normalized rune when it was scanned, so
				// pop it and widen this span to cover it — unless an earlier
				// punctuation already claimed it as its own trailing space
				// (then it is part of that span, not a standalone rune).
				if i > 0 && rs[i-1] == ' ' && len(norm) > 0 && norm[len(norm)-1] == ' ' && spans[len(spans)-1].end == i {
					norm = norm[:len(norm)-1]
					spans = spans[:len(spans)-1]
					start = i - 1
				}
			} else if i+1 < len(rs) && rs[i+1] == ' ' {
				i++
			}
		}
		norm = append(norm, r)
		spans = append(spans, punctSpan{start: start, end: i + 1})
	}
	return norm, spans
}

// normalizePatchPunctuationLine is the line-level punctuation tolerance: the
// shared punctuation core (1:1 mapping plus separator-space folding) followed
// by folding any remaining Unicode space to a plain space, plus the narrow
// no-break spaces (NBSP, figure space, narrow no-break space) the core leaves
// alone because they are meaningful inside prose. Line-level matching cannot
// cross lines, so folding whitespace inside a line cannot mask line
// structure; Edit deliberately keeps whitespace significant at the sequence
// level because old_string can span lines.
func normalizePatchPunctuationLine(s string) string {
	norm, _ := normalizePunctWithSpaceFolding([]rune(s))
	var b strings.Builder
	b.Grow(len(norm))
	for _, r := range norm {
		// unicode.IsSpace already covers the narrow no-break spaces
		// (U+00A0, U+2007, U+202F) a model may emit instead of a plain space.
		if unicode.IsSpace(r) {
			r = ' '
		}
		b.WriteRune(r)
	}
	return b.String()
}

// normalizePatchTolerantLine is the single tolerance normalizer for apply_patch
// line matching: the shared punctuation core applied to whitespace-trimmed
// text. Trimming first is what makes a line whose only difference is leading
// or trailing whitespace match, on top of the punctuation and inter-word
// spacing the core already folds.
//
// There is deliberately only one tolerance normalizer. Matching that needs it
// runs through findUniqueApplyPatchSequence, which rejects ambiguous matches
// and splices the replacement back over the file's original bytes; folding
// tolerance into the plain first-match cascade would silently pick one of
// several equally plausible positions.
func normalizePatchTolerantLine(s string) string {
	return normalizePatchPunctuationLine(strings.TrimSpace(s))
}

// editDiffLine is one differing line inside the matched window: the file's
// actual line and the model's expected line at the same block-relative
// position. Only truly differing lines are listed (each compared under the
// tolerance normalizer), so a multi-line drift shows every line the model
// must fix, not just the first.
type editDiffLine struct {
	// FileLine is the 1-based absolute file line of the difference.
	FileLine int
	// ExpectedLine is the 1-based line inside the model's old_string block.
	ExpectedLine int
	// Expected is the model's line as originally written (not normalized),
	// truncated for display.
	Expected string
	// Actual is the file's line as originally written (not normalized),
	// truncated for display.
	Actual string
}

// editClosestMatchResult is the best fuzzy window found when exact and
// tolerant matching both fail, so the error can point the model at the exact
// line and difference instead of forcing a full re-read.
type editClosestMatchResult struct {
	// StartLine is the 1-based first line of the matched window.
	StartLine int
	// FileDiffLine is the 1-based absolute file line of the first differing
	// line inside the window.
	FileDiffLine int
	// ExpectedDiffLine is the 1-based line of the model's old_string block
	// (relative to the block, not the file) where the difference starts.
	ExpectedDiffLine int
	// Expected is the model's differing line as originally written (not
	// normalized), truncated for display.
	Expected string
	// Actual is the file's differing line as originally written (not
	// normalized), truncated for display.
	Actual string
	// ExpectedRaw and ActualRaw are the untruncated first-diff lines, used
	// to name the exact first differing rune (see firstRuneDiffLoc).
	ExpectedRaw string
	ActualRaw   string
	// DiffRunes is the Levenshtein distance between the two normalized
	// blocks, i.e. how many character-level changes separate them.
	DiffRunes int
	// Similarity is 0..1 (1 = identical after normalization).
	Similarity float64
	// LineDiffOldExtra / LineDiffSrcExtra count whole lines one side has and
	// the other lacks under a line-level alignment of the window (see
	// alignEditWindowLines). They separate a line-count drift — a blank line
	// added or dropped, a heading shifted by an extra line — from content
	// differences at position-shifted lines.
	LineDiffOldExtra int
	LineDiffSrcExtra int
	// LineDiffBlankOnly reports that every unpaired line in the alignment is
	// blank, so the drift is a blank-line count mismatch.
	LineDiffBlankOnly bool
	// Diffs lists the first few differing lines inside the window (see
	// editDiffLine), for multi-line drifts. The first entry duplicates
	// FileDiffLine/Expected/Actual for single-line cases.
	Diffs []editDiffLine
}

// maxEditSuggestionLines caps the fuzzy window scan so a failed edit on a
// pathological file does not turn into a full-file O(L×N) scan.
// 10k-line guard: past this depth the generic re-read hint is more honest than a needle-in-haystack candidate; the per-window work cap below bounds scan cost separately.
const maxEditSuggestionLines = 10000

// maxEditSuggestionRunes caps old_string size for the fuzzy search; a block
// longer than this is assumed to be a fundamentally different target and the
// generic re-read hint is more useful than a noisy near-match.
const maxEditSuggestionRunes = 2000

// minEditSuggestionSimilarity is the lowest similarity (0..1, after
// normalization) at which a window is presented as a "closest match". Below
// this the block is too different to guide a retry and the generic re-read
// hint is more honest than a misleading near-match.
const minEditSuggestionSimilarity = 0.6

// editClosestMatch scans content in line windows of the same height as
// oldText and returns the most similar window after punctuation/whitespace
// normalization. It is the Tier-2 failure path: Tier 1 (exact, trailing
// newline, punctuation tolerance) already failed, so the remaining mismatch
// is a character-level difference (dropped/inserted rune, extra line, real
// stale content). A window whose similarity clears the threshold gives the
// model the exact file lines to copy, avoiding a re-read; otherwise ok is
// false and the caller falls back to the generic re-read error.
// maxDiffLinesShown caps how many differing lines the closest-match error
// lists. A couple of lines is enough to guide the rebuild; a pathological
// multi-line drift degrades to the generic hint rather than a wall of text.
const maxDiffLinesShown = 3

func editClosestMatch(content, oldText string) (editClosestMatchResult, bool) {
	oldLines := strings.Split(oldText, "\n")
	// A trailing newline in oldText yields a trailing empty element; drop it
	// so the window height matches the visible block.
	for len(oldLines) > 0 && oldLines[len(oldLines)-1] == "" {
		oldLines = oldLines[:len(oldLines)-1]
	}
	if len(oldLines) == 0 {
		return editClosestMatchResult{}, false
	}
	normOld := make([]string, len(oldLines))
	for i, l := range oldLines {
		normOld[i] = normalizePatchPunctuationLine(l)
	}
	oldBlock := strings.Join(normOld, "\n")
	oldRunes := len([]rune(oldBlock))
	if oldRunes == 0 || oldRunes > maxEditSuggestionRunes {
		return editClosestMatchResult{}, false
	}

	srcLines := strings.Split(content, "\n")
	// Content usually ends with a newline; drop the trailing empty element so
	// windows align with visible lines.
	for len(srcLines) > 0 && srcLines[len(srcLines)-1] == "" {
		srcLines = srcLines[:len(srcLines)-1]
	}
	// CRLF files: a trailing carriage return would leak into the quoted
	// suggestion and silently penalize the similarity of every line (the
	// punctuation normalizer maps it to a space). Matching already tolerates
	// it — the whitespace layer strips it — so drop it here too.
	for i, l := range srcLines {
		srcLines[i] = strings.TrimSuffix(l, "\r")
	}
	if len(srcLines) > maxEditSuggestionLines || len(srcLines) < len(normOld) {
		return editClosestMatchResult{}, false
	}

	normSrc := make([]string, len(srcLines))
	srcLen := make([]int, len(srcLines))
	// Prefix sums make a window's rune count O(1); the length pre-filter and
	// the candidate ranking both work on lengths, and without them the
	// verification loop re-decodes every file line once per window.
	srcPrefix := make([]int, len(srcLines)+1)
	for i, l := range srcLines {
		normSrc[i] = normalizePatchPunctuationLine(l)
		srcLen[i] = len([]rune(normSrc[i]))
		srcPrefix[i+1] = srcPrefix[i] + srcLen[i]
	}

	n := len(normOld)
	// Runes per old line, over the same normalized text as srcLen.
	oldLen := make([]int, n)
	for i, l := range normOld {
		oldLen[i] = len([]rune(l))
	}
	best := editClosestMatchResult{}
	bestSim := -1.0
	bestDist := -1

	// Seed-and-verify candidate windows. Scanning every line window with a
	// full-block edit distance (bounded by a global rune-pair budget) starves
	// deep windows: the budget runs out near the top of the file, so a block
	// whose real match sits hundreds of lines down gets the generic re-read
	// hint instead of the precise difference. Instead, every normalized old
	// line is a seed looked up against normalized file lines; the rarest seed
	// (fewest file hits) yields the fewest candidate window starts. Only
	// those windows are verified with a banded edit distance capped by the
	// similarity threshold (sim >= minEditSuggestionSimilarity means
	// dist <= 40% of oldBlock), so verification cost tracks the distance
	// actually present — not block size × file size — and works regardless
	// of where the match sits in the file.
	oldLineSet := make(map[string]struct{}, len(normOld))
	for _, l := range normOld {
		oldLineSet[l] = struct{}{}
	}
	seedHits := make(map[string][]int, len(normOld))
	for i, l := range normSrc {
		if _, ok := oldLineSet[l]; !ok {
			continue
		}
		seedHits[l] = append(seedHits[l], i)
	}
	seed := ""
	seedLine := -1 // index of the seed line inside normOld
	for k, l := range normOld {
		if len(seedHits[l]) == 0 {
			continue
		}
		// seedLine carries the "not chosen yet" state. The seed string
		// cannot: a blank line normalizes to "", which is a perfectly good
		// seed, and testing the string would both let any later line
		// displace it and discard it once chosen.
		if seedLine < 0 || len(seedHits[l]) < len(seedHits[seed]) {
			seed = l
			seedLine = k
		}
	}
	// Candidate window starts. With a surviving seed line the rarest one
	// yields the fewest candidates; without one, a small block still gets a
	// full-window scan so a single transcription error in the block's only
	// line is located — the cost stays bounded because the band is capped by
	// the similarity threshold. A large block with no surviving line is too
	// far gone for a noisy guess; the generic re-read hint is more honest.
	const maxFallbackScanOldRunes = 256
	var starts []int
	if seedLine >= 0 {
		for _, pos := range seedHits[seed] {
			start := pos - seedLine
			if start >= 0 && start+n <= len(normSrc) {
				starts = append(starts, start)
			}
		}
	} else if oldRunes <= maxFallbackScanOldRunes {
		for i := 0; i+n <= len(normSrc); i++ {
			starts = append(starts, i)
		}
	} else {
		return editClosestMatchResult{}, false
	}

	// sim >= minEditSuggestionSimilarity ⇔ dist <= (1-sim)*oldRunes.
	maxDist := int(float64(oldRunes) * (1 - minEditSuggestionSimilarity))
	// A very common seed line (e.g. a blank line in a file full of them)
	// could yield thousands of candidates; cap verification so the failure
	// path stays bounded and degrades to the generic hint beyond that.
	// The cap decides *which* windows are reachable, so candidates are
	// ranked by promise first — verifying the first N in file order would
	// bring back the depth bias the seed lookup exists to remove.
	const maxSeedWindows = 256
	if seedLine >= 0 && len(starts) > maxSeedWindows {
		starts = rankEditWindowStarts(starts, oldLen, srcLen)
	}
	// Each candidate window is verified with a band of width 2*band+1 over
	// oldRunes rows; charge an upper bound per window and stop scanning new
	// candidates once it balloons, mirroring the old total budget guard's
	// intent (the generic re-read hint is more honest than a seconds-long
	// failure path on a pathological file).
	const maxTotalBandedCells = 30_000_000
	totalCells := 0
	checked := 0
	for _, start := range starts {
		if seedLine >= 0 && checked >= maxSeedWindows {
			break
		}
		checked++
		// One "\n" separator per interior boundary, matching oldRunes.
		windowRunes := (n - 1) + srcPrefix[start+n] - srcPrefix[start]
		if absInt(windowRunes-oldRunes) > maxDist {
			continue // banded distance would bail on the length gap alone
		}
		band := maxDist
		if bestDist >= 0 && bestDist-1 < band {
			band = bestDist - 1
		}
		// Charge the in-band columns (2*band+1) plus the two boundary-clear
		// passes (each at most band columns), so the cap tracks real work
		// rather than a loose lower bound.
		totalCells += oldRunes * (4*band + 2)
		if totalCells > maxTotalBandedCells {
			break
		}
		window := strings.Join(normSrc[start:start+n], "\n")
		var dist int
		var ok bool
		if bestDist < 0 {
			dist, ok = bandedEditDistance(oldBlock, window, maxDist)
		} else {
			// Only a strictly closer window can improve the answer.
			dist, ok = bandedEditDistance(oldBlock, window, bestDist-1)
		}
		if !ok {
			continue
		}
		sim := 1.0 - float64(dist)/float64(oldRunes)
		if sim <= bestSim {
			continue
		}
		bestSim = sim
		bestDist = dist
		best = editClosestMatchResult{
			StartLine:  start + 1,
			DiffRunes:  dist,
			Similarity: sim,
		}
		// First differing line within the window. FileDiffLine is the
		// absolute file position; ExpectedDiffLine is the line inside the
		// model's block (relative), so "your line N" never shows a file
		// offset. Expected/Actual carry the original lines verbatim — the
		// model must copy the file text exactly, and a normalized variant
		// (spaces collapsed, punctuation folded) is not copyable.
		for k := range n {
			if normOld[k] != normSrc[start+k] {
				if len(best.Diffs) == 0 {
					best.FileDiffLine = start + k + 1
					best.ExpectedDiffLine = k + 1
					best.Expected = oldLines[k]
					best.Actual = srcLines[start+k]
					best.ExpectedRaw = oldLines[k]
					best.ActualRaw = srcLines[start+k]
				}
				best.Diffs = append(best.Diffs, editDiffLine{
					FileLine:     start + k + 1,
					ExpectedLine: k + 1,
					Expected:     truncateToolLine(oldLines[k]),
					Actual:       truncateToolLine(srcLines[start+k]),
				})
				if len(best.Diffs) >= maxDiffLinesShown {
					break
				}
			}
		}
		if sim == 1.0 {
			break // identical window cannot be beaten
		}
	}
	if bestSim < minEditSuggestionSimilarity {
		return editClosestMatchResult{}, false
	}
	// The window is chosen; now report its differences. A whole-line drift
	// (extra blank lines, a heading shifted by one line) is reported as a
	// line-count difference via a line-level alignment, so the model is told
	// "you have one extra line" instead of shown two unrelated lines that a
	// position-shifted comparison picked.
	start := best.StartLine - 1
	best.LineDiffOldExtra, best.LineDiffSrcExtra, best.LineDiffBlankOnly = alignEditWindowLines(oldLines, srcLines, start, normalizePatchPunctuationLine)
	// First differing line within the window. FileDiffLine is the absolute
	// file position; ExpectedDiffLine is the line inside the model's block
	// (relative), so "your line N" never shows a file offset. Expected/Actual
	// carry the original lines verbatim — the model must copy the file text
	// exactly, and a normalized variant (spaces collapsed, punctuation
	// folded) is not copyable.
	for k := range n {
		if normOld[k] != normSrc[start+k] {
			if len(best.Diffs) == 0 {
				best.FileDiffLine = start + k + 1
				best.ExpectedDiffLine = k + 1
				best.Expected = oldLines[k]
				best.Actual = srcLines[start+k]
				best.ExpectedRaw = oldLines[k]
				best.ActualRaw = srcLines[start+k]
			}
			best.Diffs = append(best.Diffs, editDiffLine{
				FileLine:     start + k + 1,
				ExpectedLine: k + 1,
				Expected:     truncateToolLine(oldLines[k]),
				Actual:       truncateToolLine(srcLines[start+k]),
			})
			if len(best.Diffs) >= maxDiffLinesShown {
				break
			}
		}
	}
	best.Expected = truncateToolLine(best.Expected)
	best.Actual = truncateToolLine(best.Actual)
	return best, true
}

// alignEditWindowLines aligns the old block's lines against the n-line file
// window starting at srcStart under the tolerance normalizer and reports the
// whole-line drift: how many lines each side has that the other lacks, and
// whether every unpaired line is blank. Line-level pairing (an LCS over
// normalized lines) separates "the block shifted by an extra blank line" from
// "this specific line has different content", which a position-by-position
// comparison cannot: with one blank line too many in the model's block, every
// following line compares against its neighbor and the real difference is
// masked by a wall of phantom mismatches.
func alignEditWindowLines(oldLines, srcLines []string, srcStart int, norm func(string) string) (oldExtra, srcExtra int, blankOnly bool) {
	n := len(oldLines)
	// The window is the block height when the caller passes a full-height
	// window (edit), but apply_patch can pass a shorter tail window when the
	// file runs out of lines — clamp so the shorter side wins.
	m := min(n, len(srcLines)-srcStart)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := 1; i <= n; i++ {
		o := norm(oldLines[i-1])
		for j := 1; j <= m; j++ {
			if o == norm(srcLines[srcStart+j-1]) {
				dp[i][j] = dp[i-1][j-1] + 1
			} else {
				dp[i][j] = max(dp[i-1][j], dp[i][j-1])
			}
		}
	}
	lcs := dp[n][m]
	oldExtra = n - lcs
	srcExtra = m - lcs
	if oldExtra+srcExtra == 0 {
		return 0, 0, false
	}
	// blankOnly: every unpaired line is blank. This must not depend on one
	// arbitrary backtrack path of the LCS — equal-length alternatives can
	// leave different lines unpaired, so a path that happens to leave a
	// non-blank line out would misreport a blank-line drift. Restrict the
	// LCS to non-blank lines instead: the drift is purely blank exactly when
	// both sides have the same non-blank count and every non-blank line
	// pairs.
	nbOld := 0
	for _, l := range oldLines {
		if strings.TrimSpace(l) != "" {
			nbOld++
		}
	}
	nbSrc := 0
	for k := range m {
		if strings.TrimSpace(srcLines[srcStart+k]) != "" {
			nbSrc++
		}
	}
	blankOnly = nbOld == nbSrc && nbOld == lcsNonBlank(oldLines, srcLines, srcStart, m, norm)
	return oldExtra, srcExtra, blankOnly
}

// lcsNonBlank is the line-level LCS restricted to non-blank lines: it counts
// how many non-blank lines of the block find an equal partner in the window,
// which is what blankOnly needs to tell a pure blank-line drift from a real
// content difference.
func lcsNonBlank(oldLines, srcLines []string, srcStart, m int, norm func(string) string) int {
	var a, b []string
	for _, l := range oldLines {
		if strings.TrimSpace(l) != "" {
			a = append(a, norm(l))
		}
	}
	for k := range m {
		if l := srcLines[srcStart+k]; strings.TrimSpace(l) != "" {
			b = append(b, norm(l))
		}
	}
	dp := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		prev := 0
		for j := 1; j <= len(b); j++ {
			cur := dp[j]
			if a[i-1] == b[j-1] {
				dp[j] = prev + 1
			} else if dp[j] < dp[j-1] {
				dp[j] = dp[j-1]
			}
			prev = cur
		}
	}
	return dp[len(b)]
}

// editWindowCandidate pairs a candidate window start with its length gap.
type editWindowCandidate struct {
	start int
	gap   int
}

// rankEditWindowStarts reorders candidate window starts so the most promising
// survive the verification cap no matter where they sit in the file.
//
// Order decides reachability: the cap is what makes the failure path
// affordable, and a block of boilerplate ("}", blank lines, "func x() {")
// seeds thousands of candidates — taking the first ones in file order means
// a match deep in the file is never verified, which is the very depth bias
// the seed lookup was introduced to remove.
//
// The signal is the sum over the block's lines of the per-line rune length
// gap, a lower bound on the window's edit distance: each line needs at least
// |Δlen| insertions or deletions before any substitution is even considered.
// Ordering by it ascending therefore orders by how close the window can
// possibly be. It beats the two obvious alternatives — a whole-window length
// filter is cancelled out by one short plus one long line, and exact-line
// alignment cannot see a line that appears nowhere in the file, which is the
// common case for the misquoted line that caused the failure.
func rankEditWindowStarts(starts []int, oldLen, srcLen []int) []int {
	// Ranking costs one pass over (candidate × block line). On a
	// pathological file that is 10k × 10k — more than the verification it
	// orders — so thin the sampled lines rather than the candidates:
	// sampling keeps every depth reachable, dropping candidates would not.
	const maxRankCells = 2_000_000
	stride := 1
	if cells := len(starts) * len(oldLen); cells > maxRankCells {
		stride = (cells + maxRankCells - 1) / maxRankCells
	}
	cands := make([]editWindowCandidate, len(starts))
	for i, start := range starts {
		gap := 0
		for k := 0; k < len(oldLen); k += stride {
			gap += absInt(srcLen[start+k] - oldLen[k])
		}
		cands[i] = editWindowCandidate{start: start, gap: gap}
	}
	// Stable: equally promising candidates keep file order, so the reported
	// line stays deterministic when a file repeats the same block.
	slices.SortStableFunc(cands, func(a, b editWindowCandidate) int {
		return cmp.Compare(a.gap, b.gap)
	})
	out := make([]int, len(cands))
	for i, c := range cands {
		out[i] = c.start
	}
	return out
}

// bandedEditDistance is levenshteinDistance restricted to a diagonal band of
// width maxDist (Ukkonen's bounded edit distance). It returns the exact
// distance when it is <= maxDist and (maxDist+1, false) as soon as the
// distance is provably larger: a length difference beyond maxDist bails out
// in O(1), and cells outside the band are capped at maxDist+1 — their true
// value exceeds maxDist (dp[i][j] >= |i-j|), so capping never under-reports
// and only over-reports positions that cannot affect an in-band answer.
// editClosestMatch uses it because candidate windows are near-identical by
// construction; a full-matrix DP per window would dominate the failure path.
func bandedEditDistance(a, b string, maxDist int) (int, bool) {
	if maxDist < 0 {
		return maxDist + 1, false
	}
	ra := []rune(a)
	rb := []rune(b)
	if absInt(len(ra)-len(rb)) > maxDist {
		return maxDist + 1, false
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		lo := max(0, i-maxDist)
		hi := min(len(rb), i+maxDist)
		// Column 0 is the one in-band column whose value stays exact
		// (dp[i][0] = i); when the band does not reach it, everything left
		// of lo is capped. left carries dp[i][j-1] across the row.
		left := maxDist + 1
		if lo == 0 {
			left = i
			cur[0] = left
		}
		for j := max(1, lo); j <= hi; j++ {
			cost := 0
			if ra[i-1] != rb[j-1] {
				cost = 1
			}
			v := prev[j] + 1 // deletion
			if d := prev[j-1] + cost; d < v {
				v = d // substitution / match
			}
			if d := left + 1; d < v {
				v = d // insertion
			}
			if v > maxDist {
				v = maxDist + 1
			}
			cur[j] = v
			left = v
		}
		for j := range lo {
			cur[j] = maxDist + 1
		}
		for j := hi + 1; j <= len(rb); j++ {
			cur[j] = maxDist + 1
		}
		prev, cur = cur, prev
	}
	if prev[len(rb)] > maxDist {
		return maxDist + 1, false
	}
	return prev[len(rb)], true
}

// truncateToolLine caps a single file/expected line quoted into a mismatch
// diagnostic, so a pathological line cannot inflate the error message. edit and
// apply_patch share it: the same failure quotes the same way whichever tool the
// model reached for, and the model copies these lines verbatim to retry. The
// text is always quoted because trailing whitespace and empty lines are exactly
// the differences that make a retry fail when shown bare.
func truncateToolLine(s string) string {
	const max = 120
	r := []rune(s)
	if len(r) <= max {
		return quoteToolLine(s)
	}
	return quoteToolLine(string(r[:max])) + "..."
}

// quoteToolLine quotes s for the closest-match hint. strconv.Quote already
// escapes control and invisible runes, but it passes U+FE0E/U+FE0F through
// verbatim (they are category Mn, which counts as printable), so a model line
// carrying an orphan variation selector renders identically to the clean line
// — exactly the invisible difference that makes retries fail. Escaping them
// explicitly makes the selector visible as \ufe0e/\ufe0f in the hint.
func quoteToolLine(s string) string {
	q := strconv.Quote(s)
	if strings.ContainsRune(q, '\ufe0e') || strings.ContainsRune(q, '\ufe0f') {
		q = strings.ReplaceAll(q, "\ufe0f", `\ufe0f`)
		q = strings.ReplaceAll(q, "\ufe0e", `\ufe0e`)
	}
	return q
}

// firstRuneDiffLoc reports the rune offset of the first difference between two
// lines, plus each side's rune at that offset. The "present" booleans cover
// length differences: when one line is a prefix of the other, the shorter side
// reports absent at the longer side's next rune. The closest-match hint uses it
// to name the exact offending rune with %U, which makes invisible bytes
// (variation selectors, zero-width runs) visible instead of leaving the model
// to diff two quoted lines by eye.
func firstRuneDiffLoc(expected, actual string) (offset int, expectRune, actualRune rune, expectPresent, actualPresent bool) {
	re := []rune(expected)
	ra := []rune(actual)
	n := min(len(re), len(ra))
	for i := range n {
		if re[i] != ra[i] {
			return i, re[i], ra[i], true, true
		}
	}
	if len(re) < len(ra) {
		return n, 0, ra[n], false, true
	}
	if len(re) > len(ra) {
		return n, re[n], 0, true, false
	}
	return 0, 0, 0, false, false
}

// toolRuneToken renders a rune for the first-mismatch hint as a stable
// "U+XXXX" token — visible for every code point including invisible ones — or
// "<absent>" when the side ran out of runes.
func toolRuneToken(r rune, present bool) string {
	if !present {
		return "<absent>"
	}
	return fmt.Sprintf("%U", r)
}
