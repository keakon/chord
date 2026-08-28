package tools

import (
	"encoding/json"
	"fmt"
	"path/filepath"
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
// visual width: variation selectors (U+FE00–U+FE0F,, including the text/emoji
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

// countIgnorableRunes counts the invisible format runes (variation selectors,
// zero-width spaces/joiners) inside s, letting callers tell the model how
// many leaked into its old_string so it can strip them before retrying.

func countIgnorableRunes(s string) int {
	n := 0
	for _, r := range s {
		if isIgnorableRune(r) {
			n++
		}
	}
	return n
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
// bytes.
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
		// keeps the file's original bytes for it.
		if rs[i] == ' ' && i > 0 && i+1 < len(rs) && isWordRune(rs[i-1]) && isWordRune(rs[i+1]) {
			if len(spans) > 0 && spans[len(spans)-1].end == i {
				spans[len(spans)-1].end = i + 1
			}
			continue
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
	// DiffRunes is the Levenshtein distance between the two normalized
	// blocks, i.e. how many character-level changes separate them.
	DiffRunes int
	// Similarity is 0..1 (1 = identical after normalization).
	Similarity float64
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
	for i, l := range srcLines {
		normSrc[i] = normalizePatchPunctuationLine(l)
	}

	n := len(normOld)
	best := editClosestMatchResult{}
	bestSim := -1.0
	bestDist := -1
	// Guard the failure path: full-block Levenshtein per window on a large
	// file (2000 lines × 2000 runes) can reach billions of rune pairs.
	// Pre-filter windows whose rune-length delta against oldBlock already
	// exceeds the current best distance (they can never beat it), and cap
	// the total work so the suggestion degrades to the generic re-read hint
	// instead of stalling the edit.
	const windowScanBudget = 200_000 // total (oldBlock × window) rune-pair budget
	budget := windowScanBudget
	for i := 0; i+n <= len(normSrc); i++ {
		// Count the window from its lines (one "\n" separator per interior
		// boundary, so this matches the joined length exactly): a window
		// the budget rejects never pays for building its joined string.

		windowRunes := n - 1
		for _, l := range normSrc[i : i+n] {
			windowRunes += len([]rune(l))
		}
		if bestDist >= 0 && absInt(windowRunes-oldRunes) >= bestDist {
			continue // cannot beat the current best edit distance
		}
		work := oldRunes * windowRunes
		if budget <= 0 || work > budget {
			// An exhausted budget stops the scan; a single oversized window
			// must not — later windows can be far cheaper, and the budget is
			// only spent by windows actually processed, so skipping keeps the
			// total work bounded while letting the whole file compete.
			if budget <= 0 {
				break
			}
			continue
		}
		budget -= work
		window := strings.Join(normSrc[i:i+n], "\n")
		dist := levenshteinDistance(oldBlock, window)
		sim := 1.0 - float64(dist)/float64(oldRunes)
		if sim <= bestSim {
			continue
		}
		bestSim = sim
		bestDist = dist
		best = editClosestMatchResult{
			StartLine:  i + 1,
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
			if normOld[k] != normSrc[i+k] {
				if len(best.Diffs) == 0 {
					best.FileDiffLine = i + k + 1
					best.ExpectedDiffLine = k + 1
					best.Expected = oldLines[k]
					best.Actual = srcLines[i+k]
				}
				best.Diffs = append(best.Diffs, editDiffLine{
					FileLine:     i + k + 1,
					ExpectedLine: k + 1,
					Expected:     truncateToolLine(oldLines[k]),
					Actual:       truncateToolLine(srcLines[i+k]),
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
	best.Expected = truncateToolLine(best.Expected)
	best.Actual = truncateToolLine(best.Actual)
	return best, true
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
		return fmt.Sprintf("%q", s)
	}
	return fmt.Sprintf("%q...", string(r[:max]))
}
