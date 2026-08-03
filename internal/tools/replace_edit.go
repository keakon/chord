package tools

import (
	"context"
	"encoding/json"
	"fmt"
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
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll *bool  `json:"replace_all,omitempty"`
}

// UnmarshalJSON accepts the deprecated "filePath" alias for "path" (current
// field always wins) so calls shaped with the legacy field name still execute.
// The alias is intentionally not exposed in Parameters(); validation accepts it
// via legacyArgAliases instead.
func (a *replaceEditArgs) UnmarshalJSON(data []byte) error {
	var raw struct {
		Path       string `json:"path"`
		FilePath   string `json:"filePath"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll *bool  `json:"replace_all,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	a.Path = raw.Path
	if a.Path == "" {
		a.Path = raw.FilePath
	}
	a.OldString = raw.OldString
	a.NewString = raw.NewString
	a.ReplaceAll = raw.ReplaceAll
	return nil
}

// legacyArgAliases lets validation accept the deprecated "filePath" field name
// without exposing it in the schema, mirroring Glob/Grep singular aliases.
func (EditTool) legacyArgAliases() map[string]string {
	return map[string]string{"filePath": "path"}
}

func (EditTool) Name() string { return NameEdit }

func (t EditTool) ConcurrencyPolicy(args json.RawMessage) ConcurrencyPolicy {
	return normalizeConcurrencyPolicy(NameEdit, filePathConcurrencyPolicyInDir(extractEditPathArg(args), false, t.BaseDir))
}

func (t EditTool) Description() string {
	return `Perform exact string replacement in an existing file. Prefer this tool for localized changes instead of rewriting the whole file with Write. Reading the target area first is recommended when you have not already verified the exact current text, but not required; Edit reads current on-disk content at execution time. Re-read before retrying after any mismatch or other change. old_string must match the file's raw text exactly, including indentation, tabs, spaces, newlines (including CRLF vs LF), and quote characters. If the text came from Read output, do not include the displayed line-number gutter or separator tab. Prefer the smallest unique 2-4 line block instead of a large stale context block. Replaces one occurrence by default; set replace_all to replace every occurrence.`
}

func (EditTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Absolute or relative path to the file to edit. Relative paths resolve from the session working directory. Supports ~ for the current user's home directory.",
			},
			"old_string": map[string]any{
				"type":        "string",
				"description": "The exact raw text to find in the file. Match indentation, tabs, spaces, and newlines exactly; if the text came from Read output, do not include the displayed line-number gutter or separator tab.",
			},
			"new_string": map[string]any{
				"type":        "string",
				"description": "The text to replace old_string with. Ensure new_string preserves required indentation/newlines when needed.",
			},
			"replace_all": map[string]any{
				"type":        "boolean",
				"description": "If true, replace all occurrences of old_string. Default is false.",
			},
		},
		"required":             []string{"path", "old_string", "new_string"},
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
	if a.OldString == "" {
		return "", fmt.Errorf("old_string is required")
	}

	decodedOld, err := decodeToolStringArg(a.OldString)
	if err != nil {
		return "", fmt.Errorf("old_string encoding unsupported: %w", err)
	}
	decodedNew, err := decodeToolStringArg(a.NewString)
	if err != nil {
		return "", fmt.Errorf("new_string encoding unsupported: %w", err)
	}

	// Read the file.
	editRead, err := readFileForEdit(resolvedPath, a.Path, t.BaseDir, "edit")
	if err != nil {
		return "", err
	}
	content := editRead.Decoded.Text

	// Check for identical old/new.
	if decodedOld == decodedNew {
		return "", fmt.Errorf("old_string and new_string are identical, no change needed")
	}

	replaceAll := a.ReplaceAll != nil && *a.ReplaceAll

	// Count occurrences with exact matching.
	count := strings.Count(content, decodedOld)
	if count == 0 {
		// Try trailing-newline tolerance.
		if altOld, altNew, altCount, ok := trailingNewlineTolerantEdit(content, decodedOld, decodedNew); ok {
			count = altCount
			decodedOld, decodedNew = altOld, altNew
		}
	}
	if count == 0 {
		// Try quote-punctuation tolerance: some models cannot reproduce the
		// file's curly quotes ("") verbatim and emit straight ("') quotes
		// (or vice versa). Normalize only the quote characters and re-match;
		// when it succeeds, apply an intent-preserving replacement that keeps
		// the file's original bytes for the unchanged prefix/suffix shared by
		// old_string and new_string, so surrounding context does not drift to
		// the model's quote style.
		if altNew, altCount, ok := quoteTolerantEdit(content, decodedOld, decodedNew, replaceAll); ok {
			if altCount > 1 && !replaceAll {
				return "", fmt.Errorf("old_string found %d times under quote-tolerant matching, provide more context or set replace_all to true", altCount)
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
				out = fmt.Sprintf("Replaced %d occurrences via quote-tolerant match (%d bytes -> %d bytes)%s", altCount, oldBytes, newBytes, encSuffix)
			} else {
				out = fmt.Sprintf("Replaced 1 occurrence via quote-tolerant match (%d bytes -> %d bytes)%s", oldBytes, newBytes, encSuffix)
			}
			out, err = writeEncodedEditedFile(ctx, resolvedPath, encodedBytes, editRead.Decoded, qc, fmt.Sprintf("writing %d bytes", newBytes), t.LSP, out)
			if err != nil {
				return "", err
			}
			return out, nil
		}
		return "", fmt.Errorf("old_string not found in file. The target text may be stale or already changed. Re-read the small target range from current file contents, then rebuild old_string using exact text from that fresh read. Do not retry the same edit unchanged")
	}
	if count > 1 && !replaceAll {
		return "", fmt.Errorf("old_string found %d times, provide more context or set replace_all to true", count)
	}

	// Perform replacement.
	var newContent string
	if replaceAll {
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
	if replaceAll && count > 1 {
		out = fmt.Sprintf("Replaced %d occurrences (%d bytes -> %d bytes)%s", count, oldBytes, newBytes, encSuffix)
	} else {
		out = fmt.Sprintf("Replaced 1 occurrence (%d bytes -> %d bytes)%s", oldBytes, newBytes, encSuffix)
	}
	out, err = writeEncodedEditedFile(ctx, resolvedPath, encodedBytes, editRead.Decoded, newContent, fmt.Sprintf("writing %d bytes", newBytes), t.LSP, out)
	if err != nil {
		return "", err
	}
	return out, nil
}

func trailingNewlineTolerantEdit(content, oldText, newText string) (altOld, altNew string, altCount int, ok bool) {
	// Only consider a single final "\n" variance.
	if before, ok0 := strings.CutSuffix(oldText, "\n"); ok0 {
		altOld = before
		if altOld == "" {
			return "", "", 0, false
		}
		altCount = strings.Count(content, altOld)
		if altCount != 1 {
			return "", "", 0, false
		}
		altNew = strings.TrimSuffix(newText, "\n")
		return altOld, altNew, altCount, true
	}

	altOld = oldText + "\n"
	altCount = strings.Count(content, altOld)
	if altCount != 1 {
		return "", "", 0, false
	}
	altNew = newText
	if !strings.HasSuffix(altNew, "\n") {
		altNew += "\n"
	}
	return altOld, altNew, altCount, true
}

// quoteTolerantEdit finds oldText in content after normalizing quote
// punctuation (curly/straight quotes are treated as equivalent), and returns
// a replacement that preserves the file's original bytes for the unchanged
// prefix/suffix shared by oldText and newText. This is a last-resort fallback
// for models that cannot reproduce the file's curly quotes verbatim, applied
// only after exact and trailing-newline matching both fail.
//
// The normalization is 1:1 rune-to-rune, so a rune index in the normalized
// content maps directly to the same rune index in the original content; this
// keeps byte-exact positions for splicing in the original bytes.
//
// The returned count is the number of normalized matches; ok is false when
// normalization does not yield a match.
func quoteTolerantEdit(content, oldText, newText string, replaceAll bool) (newContent string, count int, ok bool) {
	if oldText == "" {
		return "", 0, false
	}
	contentRunes := []rune(content)
	oldRunes := []rune(oldText)
	if len(oldRunes) == 0 || len(oldRunes) > len(contentRunes) {
		return "", 0, false
	}
	normContent := make([]rune, len(contentRunes))
	for i, r := range contentRunes {
		normContent[i] = normalizeEditQuoteRune(r)
	}
	normOld := make([]rune, len(oldRunes))
	for i, r := range oldRunes {
		normOld[i] = normalizeEditQuoteRune(r)
	}

	// Collect rune-space start indices of normalized matches. Matches never
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
			starts = append(starts, i)
			if !replaceAll && len(starts) >= 2 {
				// Report ambiguity to the caller; it surfaces the same
				// "provide more context" error as the exact path.
				return "", len(starts), true
			}
			i += len(normOld)
			continue
		}
		i++
	}
	if len(starts) == 0 {
		return "", 0, false
	}

	prefixRunes, prefixBytes := commonPrefixLen(oldText, newText)
	suffixRunes := commonSuffixLen(oldText, newText, prefixBytes)
	suffixRunes = min(suffixRunes, len(oldRunes)-prefixRunes)
	newR := []rune(newText)
	deltaStart := prefixRunes
	deltaEnd := max(len(newR)-suffixRunes, deltaStart)
	delta := string(newR[deltaStart:deltaEnd])

	var b strings.Builder
	prev := 0
	for _, start := range starts {
		end := start + len(oldRunes)
		b.WriteString(string(contentRunes[prev:start]))
		// Preserve the file's original quote bytes for the unchanged
		// prefix/suffix; only the model's delta is inserted verbatim.
		b.WriteString(string(contentRunes[start : start+prefixRunes]))
		b.WriteString(delta)
		b.WriteString(string(contentRunes[end-suffixRunes : end]))
		prev = end
	}
	b.WriteString(string(contentRunes[prev:]))
	return b.String(), len(starts), true
}

// normalizeEditQuoteRune maps curly and other quote-pair runes to their ASCII
// equivalents for tolerance matching. It is 1:1 (one rune in, one rune out) so
// rune offsets are preserved when mapping between normalized and original
// text. Mirrors the shared patch Unicode quote handling but
// intentionally omits whitespace/dash/ellipsis collapsing so that only genuine
// quote-punctuation variance is tolerated.
func normalizeEditQuoteRune(r rune) rune {
	switch r {
	case '\u201c', '\u201d', '\u201e', '\u201f': // “ ” „ ‟
		return '"'
	case '\u2018', '\u2019', '\u201a', '\u201b': // ‘ ’ ‚ ‛
		return '\''
	}
	return r
}
