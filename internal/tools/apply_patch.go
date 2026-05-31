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
	Patch string `json:"patch"`
}

type ApplyPatchPlan struct {
	Path    string
	Before  string
	After   string
	Diff    string
	Added   int
	Removed int
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
	Lines []applyPatchLine
}

type applyPatchLine struct {
	Kind byte
	Text string
}

func (t ApplyPatchTool) Name() string { return NameApplyPatch }

func (ApplyPatchTool) ConcurrencyPolicy(args json.RawMessage) ConcurrencyPolicy {
	path := applyPatchConcurrencyPath(args)
	if path == "" {
		return normalizeConcurrencyPolicy(NameApplyPatch, ConcurrencyPolicy{})
	}
	return filePathConcurrencyPolicy(path, false)
}

func applyPatchConcurrencyPath(args json.RawMessage) string {
	var parsed applyPatchArgs
	if json.Unmarshal(unwrapToolArgs(args), &parsed) != nil {
		return ""
	}
	patch, err := ParseApplyPatch(parsed.Patch)
	if err != nil {
		return ""
	}
	return patch.Path
}

func (t ApplyPatchTool) Description() string {
	return "Apply a patch to modify the contents of one existing file. Input is JSON {\"patch\":\"...\"} containing a single Codex-style patch with exactly one Update File section. Use small hunks with unique unchanged context; for repeated blocks such as tests or fixtures, include the surrounding function, test, or case name. Use Write to create a new file or intentionally replace an entire file, and Delete to remove whole files. Before ApplyPatch, read the file first; if the file changed since it was read, read it again before patching. Do not use Shell to run apply_patch."
}

func (t ApplyPatchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"patch": map[string]any{
				"type":        "string",
				"description": "Single-file patch text in Codex apply_patch format. Must contain exactly one *** Update File: <relative path> section between *** Begin Patch and *** End Patch. Use context lines with a leading space, removed lines with -, and added lines with +. Keep hunks small and include enough unique unchanged context to disambiguate repeated blocks, such as the enclosing function, test, or case name.",
			},
		},
		"required":             []string{"patch"},
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
		plan, err = BuildApplyPatchPlanInDir(a.Patch, t.BaseDir)
		if err != nil {
			return "", err
		}
	}
	if plan.Before == plan.After {
		return "", fmt.Errorf("patch makes no changes. No files were modified")
	}
	baseDir := t.BaseDir
	if baseDir == "" {
		baseDir = os.Getenv("CHORD_PROJECT_ROOT")
	}
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
	if t.LSP != nil {
		absPath, absErr := filepath.Abs(resolvedPath)
		if absErr == nil {
			t.LSP.MarkTouched(absPath)
			out = t.LSP.AfterWriteToolResult(ctx, absPath, plan.After, out, false)
		}
	}
	return out, nil
}

func BuildApplyPatchPlanInDir(patchText, baseDir string) (ApplyPatchPlan, error) {
	return buildApplyPatchPlan(patchText, baseDir)
}

func BuildApplyPatchPlan(patchText string) (ApplyPatchPlan, error) {
	return buildApplyPatchPlan(patchText, "")
}

func buildApplyPatchPlan(patchText, baseDir string) (ApplyPatchPlan, error) {
	parsed, err := ParseApplyPatch(patchText)
	if err != nil {
		return ApplyPatchPlan{}, err
	}
	if baseDir == "" {
		baseDir = os.Getenv("CHORD_PROJECT_ROOT")
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
	after, err := applyParsedPatch(decodedFile.Text, parsed)
	if err != nil {
		return ApplyPatchPlan{}, err
	}
	diff := GenerateUnifiedDiffSummary(decodedFile.Text, after, parsed.Path)
	return ApplyPatchPlan{Path: resolvedPath, Before: decodedFile.Text, After: after, Diff: diff.Text, Added: diff.Added, Removed: diff.Removed}, nil
}

func ParseApplyPatch(patchText string) (parsedApplyPatch, error) {
	if strings.TrimSpace(patchText) == "" {
		return parsedApplyPatch{}, fmt.Errorf("patch is required")
	}
	lines := splitPatchLines(strings.ReplaceAll(patchText, "\r\n", "\n"))
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "*** Begin Patch" || strings.TrimSpace(lines[len(lines)-1]) != "*** End Patch" {
		return parsedApplyPatch{}, fmt.Errorf("invalid patch: expected *** Begin Patch and *** End Patch. No files were modified")
	}
	var parsed parsedApplyPatch
	var current *applyPatchHunk
	seenUpdate := false
	for i := 1; i < len(lines)-1; i++ {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "*** Update File:"):
			if seenUpdate {
				return parsedApplyPatch{}, fmt.Errorf("invalid patch: multiple Update File sections are not supported. No files were modified")
			}
			seenUpdate = true
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Update File:"))
			clean, err := validateApplyPatchPath(path)
			if err != nil {
				return parsedApplyPatch{}, err
			}
			parsed.Path = clean
			current = nil
		case strings.HasPrefix(line, "*** Add File:") || strings.HasPrefix(line, "*** Delete File:") || strings.HasPrefix(line, "*** Move to:"):
			return parsedApplyPatch{}, fmt.Errorf("unsupported patch operation. %s No files were modified", applyPatchUnsupportedOperationHint)
		case strings.HasPrefix(line, "***"):
			return parsedApplyPatch{}, fmt.Errorf("unsupported patch operation %q. %s No files were modified", line, applyPatchUnsupportedOperationHint)
		case strings.HasPrefix(line, "@@"):
			if !seenUpdate {
				return parsedApplyPatch{}, fmt.Errorf("invalid patch: hunk appears before Update File. No files were modified")
			}
			parsed.Hunks = append(parsed.Hunks, applyPatchHunk{})
			current = &parsed.Hunks[len(parsed.Hunks)-1]
		default:
			if !seenUpdate || current == nil {
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
	if !seenUpdate {
		return parsedApplyPatch{}, fmt.Errorf("invalid patch: missing Update File section. %s No files were modified", applyPatchUnsupportedOperationHint)
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
	patch, err := ParseApplyPatch(parsed.Patch)
	if err != nil {
		return ""
	}
	resolved, err := resolveApplyPatchPathForBase(patch.Path, baseDir)
	if err != nil {
		return patch.Path
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

func resolveApplyPatchPathForBase(path, baseDir string) (string, error) {
	resolved, err := resolveToolPath(path)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(baseDir) != "" && !filepath.IsAbs(resolved) {
		resolved = filepath.Join(baseDir, resolved)
	} else if !filepath.IsAbs(resolved) {
		if root := strings.TrimSpace(os.Getenv("CHORD_PROJECT_ROOT")); root != "" {
			resolved = filepath.Join(root, resolved)
		}
	}
	return filepath.Clean(resolved), nil
}

func validateApplyPatchPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("invalid patch path: path is required. No files were modified")
	}
	if strings.HasPrefix(path, "~") || filepath.IsAbs(path) {
		return "", fmt.Errorf("invalid patch path %q: ApplyPatch requires a relative path. No files were modified", path)
	}
	clean := filepath.Clean(path)
	if clean == "." || clean == "" {
		return "", fmt.Errorf("invalid patch path %q: path is required. No files were modified", path)
	}
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if part == ".." {
			return "", fmt.Errorf("invalid patch path %q: parent directory escapes are not allowed. No files were modified", path)
		}
	}
	return clean, nil
}

func applyParsedPatch(content string, patch parsedApplyPatch) (string, error) {
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
	searchStart := 0
	for _, hunk := range patch.Hunks {
		oldSeq := hunkOldSequence(hunk)
		match, err := findUniqueHunkMatch(fileLines, oldSeq, searchStart)
		if err != nil {
			return "", err
		}
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
	return out, nil
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

func findUniqueHunkMatch(fileLines, oldSeq []string, start int) (int, error) {
	if len(oldSeq) == 0 {
		return 0, fmt.Errorf("hunk has no context or removed lines; add unchanged lines under @@ so the insertion point is unique. Re-read the target area and include one or two nearby unchanged lines before or after the change. No files were modified")
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
		switch len(idxs) {
		case 1:
			return idxs[0], nil
		case 0:
			continue
		default:
			return 0, fmt.Errorf("hunk is not unique %s; matching candidates start near line(s): %s. Re-read around the intended occurrence and include nearby unique unchanged context in the same @@ hunk as the changed lines; a separate earlier @@ hunk does not anchor a later hunk. If changing repeated blocks, split the patch into smaller hunks with different local context. No files were modified", layer.name, formatPatchLineNumbers(idxs))
		}
	}
	return 0, fmt.Errorf("hunk not found. Re-read the target area and rebuild the hunk from the latest file contents; make sure context/removal lines omit Read's line-number gutter, match the current indentation and surrounding lines, and keep the hunk small and unique. No files were modified")
}

func formatPatchLineNumbers(idxs []int) string {
	if len(idxs) == 0 {
		return "unknown"
	}
	parts := make([]string, 0, len(idxs))
	for _, idx := range idxs {
		parts = append(parts, fmt.Sprintf("%d", idx+1))
	}
	return strings.Join(parts, ", ")
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
