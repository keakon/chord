package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/keakon/chord/internal/lsp"
)

const applyPatchUnsupportedOperationHint = "ApplyPatch only updates one existing file. Use Write to create files and Delete to remove whole files."

// ApplyPatchTool applies a single-file update patch to an existing text file.
// If LSP is set, notifies LSP of the change after a successful patch.
type ApplyPatchTool struct {
	LSP     *lsp.Manager // nil when LSP not configured
	BaseDir string       // optional base directory for relative patch paths
}

type applyPatchArgs struct {
	Path  string `json:"path"`
	Patch string `json:"patch"`
}

type ApplyPatchPlan struct {
	Path    string
	Before  string
	After   string
	Diff    string
	Added   int
	Removed int
	Matches []ApplyPatchMatchSummary
}

type ApplyPatchMatchSummary struct {
	HunkIndex            int
	Line                 int
	CandidateLines       []int
	Layer                string
	WeakContext          bool
	Header               string
	HeaderLine           int
	HeaderCandidateLines []int
	HeaderLayer          string
}

type applyPatchPlanContextKey struct{}

func ContextWithApplyPatchPlan(ctx context.Context, plan ApplyPatchPlan) context.Context {
	return context.WithValue(ctx, applyPatchPlanContextKey{}, plan)
}

func applyPatchPlanFromContext(ctx context.Context) (ApplyPatchPlan, bool) {
	plan, ok := ctx.Value(applyPatchPlanContextKey{}).(ApplyPatchPlan)
	return plan, ok
}

type parsedApplyPatch struct {
	Path  string
	Hunks []applyPatchHunk
}

type applyPatchHunk struct {
	Header string
	Lines  []applyPatchLine
}

type applyPatchLine struct {
	Kind byte
	Text string
}

func (t ApplyPatchTool) Name() string { return NameApplyPatch }

func (ApplyPatchTool) ConcurrencyPolicy(args json.RawMessage) ConcurrencyPolicy {
	return normalizeConcurrencyPolicy(NameApplyPatch, fileToolConcurrencyPolicy(args, false))
}

func (t ApplyPatchTool) Description() string {
	return "Apply a patch to modify the contents of one existing file. Input is JSON {\"path\":\"...\",\"patch\":\"...\"}. The path may be absolute or relative and supports ~ for the current user's home directory. Patch contains hunk text only: use @@ hunk headers, context lines with a leading space, removed lines with -, and added lines with +. Do not include Codex apply_patch envelope lines such as *** Begin Patch, *** Update File, or *** End Patch. Hunks are applied in order by matching the first occurrence after the current search position, so include enough nearby context for the intended location; for repeated blocks such as tests or fixtures, include the surrounding function, test, or case name in the same @@ hunk, for example @@ func name(...). Do not use a separate @@ hunk only as an anchor. Use Write to create a new file or intentionally replace an entire file, and Delete to remove whole files. Before ApplyPatch, make sure the file has been observed via Read or a system-resolved @file mention; if you need more surrounding context, Read the target area. If the file changed since it was observed, the tool validates hunks against current contents and may report a backup for risky writes. Do not use Shell to run apply_patch."
}

func (t ApplyPatchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the existing file to update. May be absolute or relative and supports ~ for the current user's home directory.",
			},
			"patch": map[string]any{
				"type":        "string",
				"description": "Patch hunk text only. Do not include *** Begin Patch, *** Update File, or *** End Patch. Use @@ hunk headers, context lines with a leading space, removed lines with -, and added lines with +. Hunks are applied in order by matching the first occurrence after the current search position. Keep hunks small and include enough nearby context for the intended location. You may put a function/class/test header after @@, such as @@ func TestName(t *testing.T) {, to anchor that hunk; do not use a separate earlier @@ hunk only as an anchor.",
			},
		},
		"required":             []string{"path", "patch"},
		"additionalProperties": false,
	}
}

func (t ApplyPatchTool) IsReadOnly() bool { return false }

func (t ApplyPatchTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a applyPatchArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	plan, ok := applyPatchPlanFromContext(ctx)
	if !ok {
		var err error
		plan, err = BuildApplyPatchPlanInDir(a.Path, a.Patch, t.BaseDir)
		if err != nil {
			return "", err
		}
	}
	if plan.Before == plan.After {
		return "", fmt.Errorf("patch makes no changes. No files were modified")
	}
	baseDir := t.BaseDir
	resolvedPath, err := resolveApplyPatchPathForBase(plan.Path, baseDir)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w. No files were modified", err)
	}
	decodedFile, _, err := ReadAndDecodeTextFile(resolvedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("file not found: %s. No files were modified", plan.Path)
		}
		if errors.Is(err, ErrBinaryFile) {
			return "", fmt.Errorf("cannot patch binary file: %s. No files were modified", plan.Path)
		}
		return "", fmt.Errorf("reading file: %w. No files were modified", err)
	}
	if decodedFile.Text != plan.Before {
		return "", fmt.Errorf("file %s changed while planning patch; re-read it before applying the patch. No files were modified", plan.Path)
	}
	encodedBytes, err := encodeString(plan.After, decodedFile.Encoding)
	if err != nil {
		return "", fmt.Errorf("patched text cannot be encoded back to %s: %w. No files were modified", decodedFile.Encoding.Name, err)
	}
	invalidatePathCache(resolvedPath)
	if err := writeFileNoFollow(resolvedPath, encodedBytes, 0644); err != nil {
		return "", fmt.Errorf("writing file: %w", err)
	}
	warmDecodedFileCacheAsync(resolvedPath, encodedBytes, decodedText{Text: plan.After, Encoding: decodedFile.Encoding})

	out := fmt.Sprintf("Applied patch to %s (+%d -%d)", plan.Path, plan.Added, plan.Removed)
	if decodedFile.Encoding.Name != "utf-8" {
		out += fmt.Sprintf(", encoding=%s", decodedFile.Encoding.Name)
	}
	if summary := formatApplyPatchMatchSummary(plan.Matches); summary != "" {
		out += "\n" + summary
	}
	if t.LSP != nil {
		absPath, absErr := filepath.Abs(resolvedPath)
		if absErr == nil {
			t.LSP.MarkTouched(absPath)
			out = t.LSP.AfterWriteToolResult(ctx, absPath, plan.After, out, false)
		}
	}
	return out, nil
}

func BuildApplyPatchPlanInDir(path, patchText, baseDir string) (ApplyPatchPlan, error) {
	return buildApplyPatchPlan(path, patchText, baseDir)
}

func BuildApplyPatchPlan(path, patchText string) (ApplyPatchPlan, error) {
	return buildApplyPatchPlan(path, patchText, "")
}

func buildApplyPatchPlan(path, patchText, baseDir string) (ApplyPatchPlan, error) {
	parsed, err := ParseApplyPatch(path, patchText)
	if err != nil {
		return ApplyPatchPlan{}, err
	}
	resolvedPath, err := resolveApplyPatchPathForBase(parsed.Path, baseDir)
	if err != nil {
		return ApplyPatchPlan{}, fmt.Errorf("resolve path: %w. No files were modified", err)
	}
	if isBlockedDevicePath(resolvedPath) {
		return ApplyPatchPlan{}, fmt.Errorf("cannot patch blocked device path: %s. No files were modified", parsed.Path)
	}
	info, err := os.Stat(resolvedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ApplyPatchPlan{}, fmt.Errorf("file not found: %s. ApplyPatch only updates one existing file. Use Write to create files. No files were modified", parsed.Path)
		}
		return ApplyPatchPlan{}, fmt.Errorf("accessing path: %w. No files were modified", err)
	}
	if err := ensureRegularFilePath(parsed.Path, info); err != nil {
		return ApplyPatchPlan{}, fmt.Errorf("%w. No files were modified", err)
	}
	decodedFile, err := ReadDecodedTextFile(resolvedPath)
	if err != nil {
		if errors.Is(err, ErrBinaryFile) {
			return ApplyPatchPlan{}, fmt.Errorf("cannot patch binary file: %s. No files were modified", parsed.Path)
		}
		return ApplyPatchPlan{}, fmt.Errorf("reading file: %w. No files were modified", err)
	}
	after, matches, err := applyParsedPatch(decodedFile.Text, parsed)
	if err != nil {
		return ApplyPatchPlan{}, err
	}
	diff := GenerateUnifiedDiffSummary(decodedFile.Text, after, parsed.Path)
	return ApplyPatchPlan{Path: resolvedPath, Before: decodedFile.Text, After: after, Diff: diff.Text, Added: diff.Added, Removed: diff.Removed, Matches: matches}, nil
}

func ParseApplyPatch(path, patchText string) (parsedApplyPatch, error) {
	cleanPath, err := validateApplyPatchPath(path)
	if err != nil {
		return parsedApplyPatch{}, err
	}
	if strings.TrimSpace(patchText) == "" {
		return parsedApplyPatch{}, fmt.Errorf("patch is required")
	}
	lines := splitPatchLines(strings.ReplaceAll(patchText, "\r\n", "\n"))
	parsed := parsedApplyPatch{Path: cleanPath}
	var current *applyPatchHunk
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "*** Add File:") || strings.HasPrefix(line, "*** Delete File:") || strings.HasPrefix(line, "*** Move to:") || strings.HasPrefix(line, "*** Update File:"):
			return parsedApplyPatch{}, fmt.Errorf("unsupported patch operation. %s No files were modified", applyPatchUnsupportedOperationHint)
		case strings.HasPrefix(line, "***"):
			return parsedApplyPatch{}, fmt.Errorf("unsupported patch operation %q. %s No files were modified", line, applyPatchUnsupportedOperationHint)
		case strings.HasPrefix(line, "@@"):
			header := strings.TrimSpace(strings.TrimPrefix(line, "@@"))
			parsed.Hunks = append(parsed.Hunks, applyPatchHunk{Header: header})
			current = &parsed.Hunks[len(parsed.Hunks)-1]
		default:
			if current == nil {
				if strings.TrimSpace(line) == "" {
					continue
				}
				return parsedApplyPatch{}, fmt.Errorf("invalid patch: expected @@ hunk before patch lines. No files were modified")
			}
			if line == "" {
				return parsedApplyPatch{}, fmt.Errorf("invalid patch line: hunk lines must start with space, +, or -. No files were modified")
			}
			kind := line[0]
			if kind != ' ' && kind != '+' && kind != '-' {
				return parsedApplyPatch{}, fmt.Errorf("invalid patch line %q: hunk lines must start with space, +, or -. No files were modified", line)
			}
			current.Lines = append(current.Lines, applyPatchLine{Kind: kind, Text: line[1:]})
		}
	}
	if len(parsed.Hunks) == 0 {
		return parsedApplyPatch{}, fmt.Errorf("invalid patch: missing hunk. No files were modified")
	}
	for _, h := range parsed.Hunks {
		if len(h.Lines) == 0 {
			return parsedApplyPatch{}, fmt.Errorf("invalid patch: empty hunk. No files were modified")
		}
	}
	return parsed, nil
}

func ExtractApplyPatchPathFromArgs(args json.RawMessage) string {
	return ExtractApplyPatchPathFromArgsInDir(args, "")
}

func ExtractApplyPatchPathFromArgsInDir(args json.RawMessage, baseDir string) string {
	var parsed applyPatchArgs
	if json.Unmarshal(unwrapToolArgs(args), &parsed) != nil {
		return ""
	}
	path := parsed.Path
	if path == "" {
		path = extractLegacyApplyPatchEnvelopePath(parsed.Patch)
	}
	path, err := validateApplyPatchPath(path)
	if err != nil {
		return ""
	}
	resolved, err := resolveApplyPatchPathForBase(path, baseDir)
	if err != nil {
		return path
	}
	abs, err := filepath.Abs(resolved)
	if err == nil {
		resolved = abs
	}
	if eval, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = eval
	}
	return filepath.Clean(resolved)
}

func extractLegacyApplyPatchEnvelopePath(patchText string) string {
	lines := splitPatchLines(strings.ReplaceAll(patchText, "\r\n", "\n"))
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "*** Begin Patch" {
		return ""
	}
	for _, line := range lines[1:] {
		if path, ok := strings.CutPrefix(strings.TrimSpace(line), "*** Update File:"); ok {
			return strings.TrimSpace(path)
		}
	}
	return ""
}

func resolveApplyPatchPathForBase(path, baseDir string) (string, error) {
	resolved, err := resolveToolPath(path)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	if strings.TrimSpace(baseDir) != "" && !filepath.IsAbs(resolved) {
		resolved = filepath.Join(baseDir, resolved)
	}
	return filepath.Clean(resolved), nil
}

func validateApplyPatchPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is required. No files were modified")
	}
	clean, err := resolveToolPath(path)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	if clean == "." || clean == "" {
		return "", fmt.Errorf("invalid patch path %q: path is required. No files were modified", path)
	}
	return clean, nil
}

func applyParsedPatch(content string, patch parsedApplyPatch) (string, []ApplyPatchMatchSummary, error) {
	newline := "\n"
	if strings.Contains(content, "\r\n") {
		newline = "\r\n"
	}
	logical := strings.ReplaceAll(content, "\r\n", "\n")
	finalNewline := strings.HasSuffix(logical, "\n")
	fileLines := strings.Split(logical, "\n")
	if finalNewline {
		fileLines = fileLines[:len(fileLines)-1]
	}
	matches := make([]ApplyPatchMatchSummary, 0, len(patch.Hunks))
	searchStart := 0
	for i, hunk := range patch.Hunks {
		summary := ApplyPatchMatchSummary{HunkIndex: i + 1, Header: hunk.Header}
		if hunk.Header != "" {
			headerMatch, headerResult, err := findFirstHunkMatch(fileLines, []string{hunk.Header}, searchStart)
			if err != nil {
				return "", nil, fmt.Errorf("failed to locate @@ header %q: %w", hunk.Header, err)
			}
			summary.HeaderLine = headerMatch + 1
			summary.HeaderCandidateLines = toPatchLineNumbers(headerResult.Candidates)
			summary.HeaderLayer = headerResult.Layer
			searchStart = headerMatch + 1
		}
		oldSeq := hunkOldSequence(hunk)
		match, result, err := findFirstHunkMatch(fileLines, oldSeq, searchStart)
		if err != nil {
			return "", nil, err
		}
		summary.Line = match + 1
		summary.CandidateLines = toPatchLineNumbers(result.Candidates)
		summary.Layer = result.Layer
		summary.WeakContext = isWeakHunkContext(oldSeq)
		matches = append(matches, summary)
		newSeq := buildHunkReplacement(fileLines[match:match+len(oldSeq)], hunk)
		replaced := make([]string, 0, len(fileLines)-len(oldSeq)+len(newSeq))
		replaced = append(replaced, fileLines[:match]...)
		replaced = append(replaced, newSeq...)
		replaced = append(replaced, fileLines[match+len(oldSeq):]...)
		fileLines = replaced
		searchStart = match + len(newSeq) - trailingContextLineCount(hunk)
		if searchStart < 0 {
			searchStart = 0
		}
	}
	out := strings.Join(fileLines, "\n")
	if finalNewline {
		out += "\n"
	}
	if newline == "\r\n" {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	return out, matches, nil
}

func hunkOldSequence(h applyPatchHunk) []string {
	var seq []string
	for _, line := range h.Lines {
		if line.Kind == ' ' || line.Kind == '-' {
			seq = append(seq, line.Text)
		}
	}
	return seq
}

func buildHunkReplacement(matchedOld []string, h applyPatchHunk) []string {
	seq := make([]string, 0, len(h.Lines))
	oldIdx := 0
	for _, line := range h.Lines {
		switch line.Kind {
		case ' ':
			if oldIdx < len(matchedOld) {
				seq = append(seq, matchedOld[oldIdx])
			} else {
				seq = append(seq, line.Text)
			}
			oldIdx++
		case '-':
			oldIdx++
		case '+':
			seq = append(seq, line.Text)
		}
	}
	return seq
}

func trailingContextLineCount(h applyPatchHunk) int {
	count := 0
	for i := len(h.Lines) - 1; i >= 0; i-- {
		if h.Lines[i].Kind != ' ' {
			break
		}
		count++
	}
	return count
}

type hunkMatchResult struct {
	Layer      string
	Candidates []int
}

func findFirstHunkMatch(fileLines, oldSeq []string, start int) (int, hunkMatchResult, error) {
	if len(oldSeq) == 0 {
		return 0, hunkMatchResult{}, fmt.Errorf("hunk has no context or removed lines; add unchanged lines in the same @@ hunk so the insertion point is clear. For inserting a new function/test, include the previous function's ending lines, the added block, and the next function signature in one hunk. Re-read the target area before rebuilding the patch. No files were modified")
	}
	if start < 0 {
		start = 0
	}
	layers := []struct {
		name string
		norm func(string) string
	}{
		{name: "exact", norm: func(s string) string { return s }},
		{name: "ignoring trailing whitespace", norm: func(s string) string { return strings.TrimRightFunc(s, unicode.IsSpace) }},
		{name: "ignoring surrounding whitespace", norm: strings.TrimSpace},
		{name: "after normalizing common Unicode punctuation and whitespace", norm: normalizePatchUnicodeLine},
	}
	for _, layer := range layers {
		idxs := findMatchesWithNorm(fileLines, oldSeq, start, layer.norm)
		if len(idxs) == 0 {
			continue
		}
		return idxs[0], hunkMatchResult{Layer: layer.name, Candidates: idxs}, nil
	}
	return 0, hunkMatchResult{}, fmt.Errorf("hunk not found. Re-read the target area and rebuild the hunk from the latest file contents; make sure context/removal lines omit Read's line-number gutter, match the current indentation and surrounding lines, and keep the hunk small. No files were modified")
}

func formatApplyPatchMatchSummary(matches []ApplyPatchMatchSummary) string {
	if len(matches) == 0 {
		return ""
	}
	lines := make([]string, 0, len(matches))
	for _, match := range matches {
		if match.Line <= 0 {
			continue
		}
		lines = append(lines, fmt.Sprintf("%d", match.Line))
	}
	if len(lines) == 0 {
		return ""
	}
	out := "Matched hunk"
	if len(lines) > 1 {
		out += "s"
	}
	out += " near line(s): " + strings.Join(lines, ", ")
	var notes []string
	for _, match := range matches {
		if len(match.CandidateLines) > 1 {
			notes = append(notes, formatApplyPatchAmbiguousNote("hunk", match.HunkIndex, match.CandidateLines, match.WeakContext))
		}
		if len(match.HeaderCandidateLines) > 1 {
			notes = append(notes, formatApplyPatchAmbiguousNote("@@ header", match.HunkIndex, match.HeaderCandidateLines, false))
		}
	}
	if len(notes) > 0 {
		out += "\n" + strings.Join(notes, "\n")
	}
	return out
}

func formatApplyPatchAmbiguousNote(kind string, hunkIndex int, candidates []int, weak bool) string {
	if len(candidates) == 0 {
		return ""
	}
	other := candidates[1:]
	note := fmt.Sprintf("Note: %s %d matched multiple locations; applied the first match near line %d after the current search position", kind, hunkIndex, candidates[0])
	if len(other) > 0 {
		note += ". Other candidate line(s): " + formatIntList(other)
	}
	if weak {
		note += ". The hunk used weak context such as a brace, parenthesis, bracket, or blank line; verify the matched location if needed"
	}
	return note + "."
}

func formatIntList(values []int) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, fmt.Sprintf("%d", value))
	}
	return strings.Join(parts, ", ")
}

func isWeakHunkContext(oldSeq []string) bool {
	if len(oldSeq) > 2 {
		return false
	}
	for _, line := range oldSeq {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		weak := true
		for _, r := range trimmed {
			switch r {
			case '{', '}', '(', ')', '[', ']', ',', ';':
			default:
				weak = false
			}
		}
		if !weak {
			return false
		}
	}
	return true
}

func toPatchLineNumbers(idxs []int) []int {
	if len(idxs) == 0 {
		return nil
	}
	out := make([]int, len(idxs))
	for i, idx := range idxs {
		out[i] = idx + 1
	}
	return out
}

func findMatchesWithNorm(fileLines, oldSeq []string, start int, norm func(string) string) []int {
	if len(oldSeq) > len(fileLines) {
		return nil
	}
	normFile := make([]string, len(fileLines))
	for i, line := range fileLines {
		normFile[i] = norm(line)
	}
	normOld := make([]string, len(oldSeq))
	for i, line := range oldSeq {
		normOld[i] = norm(line)
	}
	var matches []int
	for i := start; i <= len(fileLines)-len(oldSeq); i++ {
		ok := true
		for j := range oldSeq {
			if normFile[i+j] != normOld[j] {
				ok = false
				break
			}
		}
		if ok {
			matches = append(matches, i)
			if len(matches) >= 2 {
				return matches
			}
		}
	}
	return matches
}

func normalizePatchUnicodeLine(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\u00a0', '\u2007', '\u202f':
			r = ' '
		case '“', '”', '„', '‟':
			r = '"'
		case '‘', '’', '‚', '‛':
			r = '\''
		case '–', '—', '−':
			r = '-'
		case '…':
			b.WriteString("...")
			continue
		}
		if unicode.IsSpace(r) {
			r = ' '
		}
		b.WriteRune(r)
	}
	return b.String()
}

func splitPatchLines(s string) []string {
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
