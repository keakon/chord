package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/lsp"
)

// EditTool performs exact string replacements in files.
// This tool uses the old_string/new_string format, which is more intuitive
// for models that haven't been specifically trained on patch formats.
//
// Why two editing tools?
//   - EditTool (this): Uses text matching (old_string → new_string).
//     More widely recognized format, better for models trained with Claude Code
//     or similar string-replacement interfaces.
//   - ApplyPatchTool: Uses unified diff hunks (@@-style). Native format for models
//     trained with OpenAI's apply_patch or similar patch-based interfaces.
//
// The system automatically selects the appropriate tool based on the active
// model's training background. Both tools share the same permission system
// (file family, path-based authorization) and concurrent editing controls.
//
// If LSP is set, notifies LSP of the change after a successful edit.
type EditTool struct {
	LSP     *lsp.Manager // nil when LSP not configured
	BaseDir string       // optional base directory for relative paths
}

type replaceEditArgs struct {
	Path       string            `json:"path"`
	Edits      []textReplacement `json:"edits,omitempty"`
	OldString  string            `json:"old_string"`
	NewString  string            `json:"new_string"`
	ReplaceAll *bool             `json:"replace_all,omitempty"`
}

// UnmarshalJSON accepts the tolerated "filePath" alias for "path" (current
// field always wins) so model calls with that common spelling still execute.
// The alias is intentionally not exposed in Parameters(); validation accepts it
// via argumentAliases instead.
func (a *replaceEditArgs) UnmarshalJSON(data []byte) error {
	var raw struct {
		Path       string            `json:"path"`
		Edits      []textReplacement `json:"edits,omitempty"`
		FilePath   string            `json:"filePath"`
		OldString  string            `json:"old_string"`
		NewString  string            `json:"new_string"`
		ReplaceAll *bool             `json:"replace_all,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	a.Edits = raw.Edits
	a.Path = raw.Path
	if a.Path == "" {
		a.Path = raw.FilePath
	}
	a.OldString = raw.OldString
	a.NewString = raw.NewString
	a.ReplaceAll = raw.ReplaceAll
	return nil
}

// argumentAliases lets validation accept the tolerated "filePath" field name
// without exposing it in the schema, mirroring Glob/Grep singular aliases.
func (EditTool) argumentAliases() map[string]string {
	return map[string]string{"filePath": "path"}
}

func (EditTool) Name() string { return NameEdit }

func (t EditTool) ConcurrencyPolicy(args json.RawMessage) ConcurrencyPolicy {
	return normalizeConcurrencyPolicy(NameEdit, filePathConcurrencyPolicyInDir(extractEditPathArg(args), false, t.BaseDir))
}

// ConcurrencyBatchable admits the call to concurrent batching: the policy is
// scoped to the target file, so edits of different files may overlap while
// same-file calls stay ordered. Arguments that do not resolve to a path fall
// back to the exclusive default and remain a boundary.
func (t EditTool) ConcurrencyBatchable(args json.RawMessage) bool { return true }

func (t EditTool) Description() string {
	// LSP diagnostic follow-up guidance lives in the system prompt
	// (## LSP diagnostic follow-up), not per-tool descriptions; see
	// lspDiagnosticPromptBlock.
	// The edit-vs-write choice is stated once in the shared Tool Selection
	// block, which only names `write` when it is visible in the role, so this
	// description keeps only the tool's own contract.
	return "Perform exact string replacement in an existing file. " +
		"Line breaks adapt to the file: with uniform CRLF or CR line endings, LF text takes that ending; with mixed line endings, each line break in old_string matches any line ending and the replacement takes the matched block's. " +
		"For several disjoint changes in one file, use edits instead of separate calls: entries that request changes match the original file exactly after line-ending adaptation; overlaps are rejected, and all such entries must pass validation before writing. One failed batch reports all failing entries at once; fix them and resubmit the complete batch. Batch entries whose old_string and new_string are identical are skipped without verifying their text exists and never counted as replacements; a batch that requests no text change reports no changes and writes nothing. A single edit with identical strings returns an error. Do not mix edits with top-level replacement fields. " +
		"Prefer the smallest unique 2-4 line block instead of a large stale context block; re-read before retrying after any mismatch. Replaces one occurrence by default; set replace_all to replace every occurrence."
}

func (EditTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Relative (preferred) or absolute path to the file to edit. Relative paths resolve from the session working directory. Supports ~ for the current user's home directory. Generate path before replacement text or edits.",
			},
			"edits": map[string]any{
				"type": "array", "minItems": 1,
				"description": "Disjoint replacements matched exactly against the original file after line-ending adaptation, without trailing-newline or punctuation/whitespace tolerance; validated together before one write.",
				"items": map[string]any{"type": "object", "properties": map[string]any{
					"old_string":  map[string]any{"type": "string", "minLength": 1},
					"new_string":  map[string]any{"type": "string"},
					"replace_all": map[string]any{"type": "boolean"},
				}, "required": []string{"old_string", "new_string"}, "additionalProperties": false},
			},
			"old_string": map[string]any{
				"type":        "string",
				"description": "Exact source text to replace, copied from read output without the READ_RESULT metadata line; preserve indentation, whitespace, and quotes.",
			},
			"new_string": map[string]any{
				"type":        "string",
				"description": "The text to replace old_string with (must be different from old_string). Ensure new_string preserves required indentation/newlines when needed.",
			},
			"replace_all": map[string]any{
				"type":        "boolean",
				"description": "If true, replace all occurrences of old_string. Default is false.",
			},
		},
		"required":             []string{"path"},
		"anyOf":                []map[string]any{{"required": []string{"old_string", "new_string"}}, {"required": []string{"edits"}}},
		"additionalProperties": false,
	}
}

func (EditTool) IsReadOnly() bool { return false }

func (t EditTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a replaceEditArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	resolvedPath, err := resolveEditPathForBase(a.Path, t.BaseDir)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	if isBlockedDevicePath(resolvedPath) {
		return "", fmt.Errorf("cannot edit blocked device path: %s", a.Path)
	}
	if a.Edits != nil {
		// Models often echo the single-replacement fields at their zero values
		// next to edits; only a field that would change the request conflicts.
		switch {
		case a.OldString != "":
			return "", fmt.Errorf("edits cannot be combined with old_string")
		case a.NewString != "":
			return "", fmt.Errorf("edits cannot be combined with new_string")
		case a.ReplaceAll != nil && *a.ReplaceAll:
			return "", fmt.Errorf("edits cannot be combined with replace_all")
		}
		return t.executeBatch(ctx, resolvedPath, a.Path, a.Edits)
	}
	if a.OldString == "" {
		return "", fmt.Errorf("old_string is required")
	}

	decodedOld, decodedNew, abs, err := prepareReplacementText(a.OldString, a.NewString)
	if err != nil {
		return "", err
	}

	// Read the file.
	editRead, err := readFileForEdit(resolvedPath, a.Path, t.BaseDir, "edit")
	if err != nil {
		return "", err
	}
	content := editRead.Decoded.Text
	fileEOL := fileLineEnding(content)
	decodedOld, decodedNew = replacementLineEndings(fileEOL, decodedOld, decodedNew)

	// Check for identical old/new.
	if decodedOld == decodedNew {
		return "", fmt.Errorf("old_string and new_string are identical, no change needed")
	}

	replaceAll := a.ReplaceAll != nil && *a.ReplaceAll

	var count int
	newlineTolerant := false
	// In a mixed line-ending file, let each line break of a multi-line
	// old_string match any line ending; read showed them all as LF.
	var lineBreakSpans []replacementSpan
	if spans := lineBreakTolerantSpans(content, fileEOL, decodedOld, decodedNew, 0); len(spans) > 0 {
		if len(spans) > 1 && !replaceAll {
			return "", fmt.Errorf("old_string found %d times under %s matching at lines %s; provide more context or set replace_all to true", len(spans), lineBreakTolerantNote, formatMatchLines(lineNumbersAt(content, spanStarts(spans)), len(spans)))
		}
		if !replaceAll {
			spans = spans[:1]
		}
		lineBreakSpans = spans
		count = len(spans)
	} else {
		count = strings.Count(content, decodedOld)
	}

	if count == 0 {
		// Try trailing-newline tolerance.
		if altOld, altNew, altCount, ok := trailingNewlineTolerantEdit(content, decodedOld, decodedNew, fileEOL); ok {
			count = altCount
			decodedOld, decodedNew = altOld, altNew
			newlineTolerant = true
		}
	}

	if count == 0 {
		// Try punctuation tolerance: some models cannot reproduce the file's
		// punctuation verbatim — they emit straight quotes for curly ones,
		// half-width punctuation for full-width CJK punctuation (or vice
		// versa), or collapse a separator's trailing space ("：", ": " and
		// ":the" are treated as the same separator). When the normalized
		// old_string matches uniquely, an intent-preserving replacement keeps
		// the file's original bytes for the unchanged prefix/suffix shared
		// by old_string and new_string, so surrounding context does not drift
		// to the model's punctuation style.
		if altNew, altCount, matchLines, ok := punctuationTolerantEdit(content, decodedOld, decodedNew, replaceAll); ok {
			if altCount > 1 && !replaceAll {
				return "", fmt.Errorf("old_string found %d times under %s matching at lines %s; provide more context or set replace_all to true", altCount, tolerantMatchNote, formatMatchLines(matchLines, altCount))
			}
			qc := altNew
			encodedBytes, err := encodeString(qc, editRead.Decoded.Encoding)
			if err != nil {
				return "", fmt.Errorf("edited text cannot be encoded back to %s: %w", editRead.Decoded.Encoding.Name, err)
			}
			oldBytes := len(editRead.Bytes)
			newBytes := len(encodedBytes)
			encSuffix := ""
			if editRead.Decoded.Encoding.Name != "utf-8" {
				encSuffix = fmt.Sprintf(", encoding=%s", editRead.Decoded.Encoding.Name)
			}
			var out string
			if altCount > 1 {
				out = fmt.Sprintf("Replaced %d occurrences via %s match%s%s (%d bytes -> %d bytes)%s", altCount, tolerantMatchNote, formatTolerantMatchLines(matchLines), abs, oldBytes, newBytes, encSuffix)
			} else {
				out = fmt.Sprintf("Replaced 1 occurrence via %s match%s%s (%d bytes -> %d bytes)%s", tolerantMatchNote, formatTolerantMatchLines(matchLines), abs, oldBytes, newBytes, encSuffix)
			}
			out, err = writeEncodedEditedFile(ctx, resolvedPath, encodedBytes, editRead.Decoded, qc, fmt.Sprintf("writing %d bytes", newBytes), t.LSP, out, t.BaseDir)
			if err != nil {
				return "", err
			}
			return out, nil
		}
		// All matching has failed (exact, trailing-newline, and
		// punctuation/whitespace tolerance). Locate the closest matching
		// block so the model sees the exact file lines and the precise
		// difference, which usually lets it retry without a re-read. When
		// no window is close enough, the generic re-read hint below
		// applies.
		if diagnostic := editClosestMatchDiagnostic(content, decodedOld); diagnostic != "" {
			return "", fmt.Errorf("old_string not found in file, even after punctuation/whitespace tolerance. %s", diagnostic)
		}
		return "", fmt.Errorf("old_string not found in file, even after punctuation/whitespace tolerance. The target text may be stale or already changed, or differs beyond punctuation and spacing. Re-read the small target range from current file contents, then rebuild old_string using exact text from that fresh read. Do not retry the same edit unchanged")
	}
	if count > 1 && !replaceAll {
		return "", fmt.Errorf("old_string found %d times at lines %s; provide more context or set replace_all to true", count, formatMatchLines(matchLineNumbers(content, decodedOld, maxMatchLinesShown), count))
	}

	// Perform replacement.
	var newContent string
	if lineBreakSpans != nil {
		newContent = spliceReplacements(content, lineBreakSpans)
	} else if replaceAll {
		newContent = strings.ReplaceAll(content, decodedOld, decodedNew)
	} else {
		newContent = strings.Replace(content, decodedOld, decodedNew, 1)
	}

	encodedBytes, err := encodeString(newContent, editRead.Decoded.Encoding)
	if err != nil {
		return "", fmt.Errorf("edited text cannot be encoded back to %s: %w", editRead.Decoded.Encoding.Name, err)
	}

	oldBytes := len(editRead.Bytes)
	newBytes := len(encodedBytes)
	encSuffix := ""
	if editRead.Decoded.Encoding.Name != "utf-8" {
		encSuffix = fmt.Sprintf(", encoding=%s", editRead.Decoded.Encoding.Name)
	}
	var out string
	if lineBreakSpans != nil {
		occurrences := "1 occurrence"
		if count > 1 {
			occurrences = fmt.Sprintf("%d occurrences", count)
		}
		out = fmt.Sprintf("Replaced %s via %s match (%d bytes -> %d bytes)%s%s", occurrences, lineBreakTolerantNote, oldBytes, newBytes, abs, encSuffix)
	} else if replaceAll && count > 1 {
		out = fmt.Sprintf("Replaced %d occurrences (%d bytes -> %d bytes)%s%s", count, oldBytes, newBytes, abs, encSuffix)
	} else if newlineTolerant {
		out = fmt.Sprintf("Replaced 1 occurrence via trailing-newline-tolerant match (%d bytes -> %d bytes)%s%s", oldBytes, newBytes, abs, encSuffix)
	} else {
		out = fmt.Sprintf("Replaced 1 occurrence (%d bytes -> %d bytes)%s%s", oldBytes, newBytes, abs, encSuffix)
	}
	out, err = writeEncodedEditedFile(ctx, resolvedPath, encodedBytes, editRead.Decoded, newContent, fmt.Sprintf("writing %d bytes", newBytes), t.LSP, out, t.BaseDir)
	if err != nil {
		return "", err
	}
	return out, nil
}

func prepareReplacementText(oldString, newString string) (string, string, string, error) {
	decodedOld, err := decodeToolStringArg(oldString)
	if err != nil {
		return "", "", "", fmt.Errorf("old_string encoding unsupported: %w", err)
	}
	decodedNew, err := decodeToolStringArg(newString)
	if err != nil {
		return "", "", "", fmt.Errorf("new_string encoding unsupported: %w", err)
	}

	// Strip orphaned variation selectors that models sometimes emit inside
	// numeric literals and plain text (e.g. "️0" instead of "0").  The file
	// content never contains them, so they cause every matching layer to fail.
	// We count stripped selectors so the success message can report them.
	// The per-rune counts come from what the strip actually removed (original
	// minus result), so the report names exactly which invisible characters the
	// model leaked and never counts preserved ones (emoji-joining ZWJ, leading
	// BOM, base-character variation selectors).
	oldLen := len([]rune(decodedOld))
	// The search text keeps only the zero-width/variation-selector cleaning it
	// already had: orphaned combining marks are left for the tolerance matcher
	// to fold, which is what fires the "punctuation/whitespace-tolerant" note.
	strippedOldText := StripZeroWidthFormat(StripOrphanVariationSelectors(decodedOld))
	oldInvisible := CountStrippedInvisible(decodedOld, strippedOldText)
	decodedOld = strippedOldText
	strippedOld := oldLen - len([]rune(decodedOld))
	newLen := len([]rune(decodedNew))
	strippedNewText := stripEditInvisible(decodedNew)
	newInvisible := CountStrippedInvisible(decodedNew, strippedNewText)
	decodedNew = strippedNewText
	strippedNew := newLen - len([]rune(decodedNew))
	strippedSelectors := strippedOld + strippedNew
	// An old_string made only of orphaned selectors strips to the empty
	// string, which matches everywhere: strings.Count(content, "") reports
	// rune count + 1 hits and replace_all would splice new_string between
	// every rune. Reject it with an actionable message instead.
	if decodedOld == "" {
		return "", "", "", fmt.Errorf("old_string contains only invisible characters (%d invisible character(s) were stripped) and cannot be matched; re-read the target range and rebuild old_string from the visible file text you want to replace", strippedOld)
	}
	// A new_string that strips to empty had its visible content lost before
	// the model ever sent it; applying it would turn the replacement into a
	// deletion on unknowable intent. A new_string that arrives empty is a
	// legitimate deletion and stays allowed.
	if decodedNew == "" && newLen > 0 {
		return "", "", "", fmt.Errorf("new_string contains only invisible characters (%d invisible character(s) were stripped); rebuild new_string with the visible text the file should contain (send an empty new_string if you intend to delete the old_string text)", strippedNew)
	}
	// new_string is written to the file verbatim, so control characters would
	// land in it — reject and route binary content to a shell command or
	// script. old_string needs no such guard: control characters there simply
	// fail to match.
	if err := validateWritableText(decodedNew); err != nil {
		return "", "", "", fmt.Errorf("new_string %w", err)
	}

	// Both success paths report every invisible character the model leaked
	// (variation selectors and zero-width format runes stripped above).
	abs := ""
	if strippedSelectors > 0 {
		// Per-rune counts reflect what the strip removed (oldInvisible /
		// newInvisible), so the report names exactly which invisible
		// characters the model leaked.
		abs = fmt.Sprintf(", cleaned %d invisible character(s) from your arguments: %s", strippedSelectors, describeInvisibleCounts(mergeInvisibleCounts(oldInvisible, newInvisible)))
	}
	return decodedOld, decodedNew, abs, nil
}

func trailingNewlineTolerantEdit(content, oldText, newText, fileEOL string) (altOld, altNew string, altCount int, ok bool) {
	// Only consider a single final line-ending variance, in the file's own
	// convention: after replacementLineEndings the arguments carry the file's
	// EOL, so a CRLF file must not be compared against a hardcoded "\n".
	eol := fileEOL
	if eol == "" {
		eol = "\n"
	}
	if before, ok0 := strings.CutSuffix(oldText, eol); ok0 {
		altOld = before
		if altOld == "" {
			return "", "", 0, false
		}
		altCount = strings.Count(content, altOld)
		if altCount != 1 {
			return "", "", 0, false
		}
		altNew = strings.TrimSuffix(newText, eol)
		return altOld, altNew, altCount, true
	}

	altOld = oldText + eol
	altCount = strings.Count(content, altOld)
	if altCount != 1 {
		return "", "", 0, false
	}
	altNew = newText
	if !strings.HasSuffix(altNew, eol) {
		altNew += eol
	}
	return altOld, altNew, altCount, true
}

// punctuationTolerantEdit finds oldText in content after normalizing prose
// punctuation (curly/straight quotes, dashes, and full-width CJK punctuation
// are treated as their ASCII equivalents, and one typesetting space adjacent
// to separator punctuation is optional), and returns a replacement that
// preserves the file's original bytes for the unchanged prefix/suffix shared
// by oldText and newText. This is a last-resort fallback for models that
// cannot reproduce the file's punctuation verbatim, applied only after exact
// and trailing-newline matching both fail. Indentation and word-boundary
// whitespace stay significant, so a real layout mismatch still fails with
// the fresh-read hint.
//
// Matching happens in the normalized rune sequence; each normalized rune
// carries a span back to the original runes, so spliced output keeps the
// file's exact bytes for anything the model did not intend to change. The
// common prefix/suffix is extended only while oldText and newText agree in
// their original bytes too — where they differ in original bytes (e.g. "："
// vs ": "), that difference is the model's intended delta and comes from
// newText verbatim.
//
// The returned count is the number of normalized matches; ok is false when
// normalization does not yield a match.
func punctuationTolerantEdit(content, oldText, newText string, replaceAll bool) (newContent string, count int, lines []int, ok bool) {
	if oldText == "" {
		return "", 0, nil, false
	}
	contentRunes := []rune(content)
	oldRunes := []rune(oldText)
	newRunes := []rune(newText)
	normContent, contentSpans := normalizePunctWithSpaceFolding(contentRunes)
	normOld, oldSpans := normalizePunctWithSpaceFolding(oldRunes)
	if len(normOld) == 0 || len(normOld) > len(normContent) {
		return "", 0, nil, false
	}

	// Collect normalized-space start indices of matches. Matches never
	// overlap: after a match the scan resumes past it, mirroring
	// strings.ReplaceAll. Overlapping matches (e.g. curly “““ matching
	// straight "") would otherwise share rune ranges and make the splice
	// below index contentRunes[prev:start] with prev > start.
	var starts []int
	for i := 0; i+len(normOld) <= len(normContent); {
		match := true
		for j := range normOld {
			if normContent[i+j] != normOld[j] {
				match = false
				break
			}
		}
		if match {
			count++
			// Without replace_all only a unique match is applied, so keep
			// just enough starts to name the ambiguous lines while counting
			// the rest.
			if replaceAll || len(starts) < maxMatchLinesShown {
				starts = append(starts, i)
			}
			i += len(normOld)
			continue
		}
		i++
	}
	if count == 0 {
		return "", 0, nil, false
	}
	if count > 1 && !replaceAll {
		// Report ambiguity to the caller; it surfaces the same "provide more
		// context" error as the exact path, with the same landing lines.
		return "", count, tolerantMatchLines(content, contentSpans, starts), true
	}

	normNew, newSpans := normalizePunctWithSpaceFolding(newRunes)

	// Common prefix/suffix in normalized space, extended only while the
	// original bytes also match: where old/new differ in original bytes
	// (e.g. "：" vs ": "), the difference is the model's intended delta and
	// must come from newText, not from the file's bytes.
	prefixN := 0
	for prefixN < len(normOld) && prefixN < len(normNew) &&
		normOld[prefixN] == normNew[prefixN] &&
		slices.Equal(oldRunes[oldSpans[prefixN].start:oldSpans[prefixN].end],
			newRunes[newSpans[prefixN].start:newSpans[prefixN].end]) {
		prefixN++
	}
	suffixN := 0
	for suffixN < len(normOld)-prefixN && suffixN < len(normNew)-prefixN {
		oi := len(normOld) - 1 - suffixN
		ni := len(normNew) - 1 - suffixN
		if normOld[oi] != normNew[ni] ||
			!slices.Equal(oldRunes[oldSpans[oi].start:oldSpans[oi].end],
				newRunes[newSpans[ni].start:newSpans[ni].end]) {
			break
		}
		suffixN++
	}

	deltaStart := prefixN
	deltaEnd := len(normNew) - suffixN

	var b strings.Builder
	prev := 0
	for _, m := range starts {
		n := len(normOld)
		b.WriteString(string(contentRunes[prev:contentSpans[m].start]))
		// Preserve the file's original bytes for the unchanged prefix and
		// suffix (including any space the punctuation absorbed); only the
		// model's delta is inserted verbatim from newText.
		if prefixN > 0 {
			b.WriteString(string(contentRunes[contentSpans[m].start:contentSpans[m+prefixN-1].end]))
		}
		if deltaStart < deltaEnd {
			b.WriteString(string(newRunes[newSpans[deltaStart].start:newSpans[deltaEnd-1].end]))
		}
		if suffixN > 0 {
			b.WriteString(string(contentRunes[contentSpans[m+n-suffixN].start:contentSpans[m+n-1].end]))
		}
		prev = contentSpans[m+n-1].end
	}
	b.WriteString(string(contentRunes[prev:]))
	lines = tolerantMatchLines(content, contentSpans, starts)
	return b.String(), count, lines, true
}

// restAfterFirst returns everything after the first element, or nothing when
// the slice is empty. It exists so the closest-match diagnostic can skip the
// already-rendered first difference without indexing into a slice that a more
// tolerant diagnostic normalizer may have left empty.
func restAfterFirst[T any](s []T) []T {
	if len(s) <= 1 {
		return nil
	}
	return s[1:]
}
