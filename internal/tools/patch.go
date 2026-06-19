package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/keakon/chord/internal/lsp"
	"github.com/keakon/chord/internal/toolname"
)

const (
	patchSingleFileHint      = "patch only updates the single existing file named by the JSON path."
	patchMultiUpdateHint     = "Split multi-file update patches into separate patch calls, one file per call."
	patchAddFileHint         = "Use write to create files."
	patchDeleteFileHint      = "Use delete to remove whole files."
	patchMoveFileHint        = "Use separate read/write/delete steps for rename or move workflows."
	patchUnsupportedLineHint = "Use direct @@ hunks for the JSON path only; do not include apply_patch envelope operations."
)

// PatchTool applies a single-file update patch to an existing text file using
// unified diff hunks (@@-style format).
//
// Why two editing tools?
//   - PatchTool (this): Uses unified diff hunks (@@-style). Native format for
//     models trained with OpenAI's apply_patch or similar patch-based interfaces.
//   - EditTool: Uses text matching (old_string → new_string). More widely
//     recognized format, better for models trained with Claude Code or similar
//     string-replacement interfaces.
//
// The system automatically selects the appropriate tool based on the active
// model's training background. Both tools share the same permission system
// (file family, path-based authorization) and concurrent editing controls.
//
// If LSP is set, notifies LSP of the change after a successful patch.
type PatchTool struct {
	LSP     *lsp.Manager // nil when LSP not configured
	BaseDir string       // optional base directory for relative patch paths
}

type editArgs struct {
	Path  string `json:"path"`
	Patch string `json:"patch"`
}

type PatchPlan struct {
	Path    string
	Before  string
	After   string
	Diff    string
	Added   int
	Removed int
}

type patchPlanContextKey struct{}

func ContextWithPatchPlan(ctx context.Context, plan PatchPlan) context.Context {
	return context.WithValue(ctx, patchPlanContextKey{}, plan)
}

func patchPlanFromContext(ctx context.Context) (PatchPlan, bool) {
	plan, ok := ctx.Value(patchPlanContextKey{}).(PatchPlan)
	return plan, ok
}

type parsedPatch struct {
	Path  string
	Hunks []patchHunk
}

type patchHunk struct {
	Header string
	Lines  []patchLine
}

type patchLine struct {
	Kind byte
	Text string
}

type unsupportedPatchOps struct {
	add    bool
	delete bool
	move   bool
	update bool
	other  bool
}

func (t PatchTool) Name() string { return NamePatch }

func (PatchTool) ConcurrencyPolicy(args json.RawMessage) ConcurrencyPolicy {
	return normalizeConcurrencyPolicy(NamePatch, fileToolConcurrencyPolicy(args, false))
}

func (t PatchTool) Description() string {
	return "Edit one existing file with patch hunks. Input is JSON {\"path\":\"...\",\"patch\":\"...\"}. This is a single-file patch tool, not a general apply_patch executor: the patch text must modify only the JSON path above, and it must not include changes for multiple files. The path may be absolute or relative and supports ~ for the current user's home directory. Use direct @@ patch hunks: @@ or @@ <verified header>, context lines with a leading space, removed lines with -, and added lines with +. Do not rely on unified diff line numbers or apply_patch wrappers; use verified headers and nearby context for positioning instead. Reading the target area first is recommended when you have not already verified the exact current lines or anchor, but not required; Patch reads current on-disk content at execution time. Hunks are applied in order by matching the first occurrence after the current search position, so include enough nearby context for the intended location; for repeated blocks such as tests or fixtures, include the surrounding function, test, or case name in the same @@ hunk, for example @@ func name(...), only after verifying that exact header exists in the current file. Do not guess or approximate anchors, and do not use a separate @@ hunk only as an anchor. Use " + toolname.Write + " to create a new file or intentionally replace an entire file, and " + toolname.Delete + " to remove whole files. If you need more surrounding context, inspect the target area with available tools before patching. If " + toolname.Patch + " fails, diagnose the reported cause first; re-inspect stale text or anchors before retrying, and do not retry the same hunk unchanged. If the file changed since its last tracked snapshot, the tool validates hunks against current contents and may report a backup for risky writes. Do not use " + toolname.Shell + " to run apply_patch."
}

func (t PatchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the verified existing file to update. May be absolute or relative and supports ~ for the current user's home directory. Do not guess paths.",
			},
			"patch": map[string]any{
				"type":        "string",
				"description": "Patch hunk text for the single JSON path above. Use direct @@ or @@ <verified header> hunk lines, context lines with a leading space, removed lines with -, and added lines with +. Do not rely on unified diff line numbers or apply_patch wrappers; use verified headers and nearby context for positioning instead. Do not include changes for multiple files. Keep hunks small and include enough nearby context for the intended location. You may put a function/class/test header after @@, such as @@ func TestName(t *testing.T) {, to anchor that hunk, but only after verifying that exact header exists in the current file with available inspection tools; do not guess or approximate anchors, and do not use a separate earlier @@ hunk only as an anchor. If an edit fails, diagnose the error first, re-inspect stale text or anchors before retrying, and do not retry the same hunk unchanged. Example:\n@@ func target() {\n old line\n-old value\n+new value\n }",
			},
		},
		"required":             []string{"path", "patch"},
		"additionalProperties": false,
	}
}

func (t PatchTool) IsReadOnly() bool { return false }

func (t PatchTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a editArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		err = fmt.Errorf("invalid arguments: %w", err)
		return formatPatchErrorResult(a.Patch, err), err
	}
	plan, ok := patchPlanFromContext(ctx)
	if !ok {
		var err error
		plan, err = BuildPatchPlanInDirWithContext(ctx, a.Path, a.Patch, t.BaseDir)
		if err != nil {
			return formatPatchErrorResult(a.Patch, err), err
		}
	}
	if plan.Before == plan.After {
		err := fmt.Errorf("patch makes no changes. No files were modified")
		return formatPatchErrorResult(a.Patch, err), err
	}
	baseDir := t.BaseDir
	resolvedPath, err := resolveEditPathForBase(plan.Path, baseDir)
	if err != nil {
		err = fmt.Errorf("resolve path: %w. No files were modified", err)
		return formatPatchErrorResult(a.Patch, err), err
	}
	editRead, err := readFileForEdit(resolvedPath, plan.Path, "patch")
	if err != nil {
		err = fmt.Errorf("%w. No files were modified", err)
		return formatPatchErrorResult(a.Patch, err), err
	}
	if editRead.Decoded.Text != plan.Before {
		err := fmt.Errorf("file %s changed while planning patch; re-read it before applying the patch. No files were modified", plan.Path)
		return formatPatchErrorResult(a.Patch, err), err
	}
	encodedBytes, err := encodeString(plan.After, editRead.Decoded.Encoding)
	if err != nil {
		return "", fmt.Errorf("patched text cannot be encoded back to %s: %w. No files were modified", editRead.Decoded.Encoding.Name, err)
	}

	displayPath := plan.Path
	if rel, relErr := filepath.Rel(baseDirOrCwd(t.BaseDir), plan.Path); relErr == nil && rel != "" && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
		displayPath = filepath.Clean(rel)
	}
	out := formatPatchSuccessResult(displayPath, plan.Added, plan.Removed, editRead.Decoded.Encoding.Name)
	out, err = writeEncodedEditedFile(ctx, resolvedPath, encodedBytes, editRead.Decoded, plan.After, fmt.Sprintf("applying patch (+%d -%d)", plan.Added, plan.Removed), t.LSP, out)
	if err != nil {
		return "", err
	}
	return out, nil
}

func BuildPatchPlanInDirWithContext(ctx context.Context, path, patchText, baseDir string) (PatchPlan, error) {
	return buildPatchPlanWithContext(ctx, path, patchText, baseDir)
}

func baseDirOrCwd(baseDir string) string {
	if strings.TrimSpace(baseDir) != "" {
		return baseDir
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

func formatPatchSuccessResult(displayPath string, added, removed int, encoding string) string {
	result := fmt.Sprintf("Applied patch to %s (+%d -%d)", displayPath, added, removed)
	if encoding != "" && encoding != "utf-8" {
		result += fmt.Sprintf(", encoding=%s", encoding)
	}
	return result
}

func formatPatchErrorResult(patch string, err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	patch = strings.TrimSpace(patch)
	if patch == "" || !shouldShowPatchExcerpt(msg) {
		return msg
	}
	return "Patch did not match the current file. Re-read the target lines and retry with the current text.\n\nPatch excerpt:\n" + fencedPatchExcerpt(patch) + "\n\nError: " + msg
}

func shouldShowPatchExcerpt(msg string) bool {
	msg = strings.ToLower(strings.TrimSpace(msg))
	for _, needle := range []string{
		"hunk not found",
		"failed to locate @@ header",
		"invalid patch",
		"patch is required",
		"unsupported patch operation",
		"only contains unchanged context lines",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func fencedPatchExcerpt(patch string) string {
	lines := strings.Split(strings.TrimSpace(patch), "\n")
	if len(lines) > 20 {
		lines = append(lines[:20], "... (patch truncated)")
	}
	return "```diff\n" + strings.Join(lines, "\n") + "\n```"
}

func buildPatchPlanWithContext(ctx context.Context, path, patchText, baseDir string) (PatchPlan, error) {
	parsed, err := ParsePatch(path, patchText)
	if err != nil {
		return PatchPlan{}, err
	}
	resolvedPath, err := resolveEditPathForBase(parsed.Path, baseDir)
	if err != nil {
		return PatchPlan{}, fmt.Errorf("resolve path: %w. No files were modified", err)
	}
	if isBlockedDevicePath(resolvedPath) {
		return PatchPlan{}, fmt.Errorf("cannot patch blocked device path: %s. No files were modified", parsed.Path)
	}
	info, err := os.Stat(resolvedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return PatchPlan{}, fmt.Errorf("file not found: %s. patch only updates one existing file. Use write to create files. No files were modified", parsed.Path)
		}
		return PatchPlan{}, fmt.Errorf("accessing path: %w. No files were modified", err)
	}
	if err := ensureRegularFilePath(parsed.Path, info); err != nil {
		return PatchPlan{}, fmt.Errorf("%w. No files were modified", err)
	}

	// Report progress before reading large files
	fileSize := info.Size()
	if fileSize > 100*1024 { // Files larger than 100KB
		reportToolProgress(ctx, ToolProgressSnapshot{
			Text: fmt.Sprintf("reading file (%s)", formatFileSize(fileSize)),
		})
	}

	decodedFile, err := ReadDecodedTextFile(resolvedPath)
	if err != nil {
		if errors.Is(err, ErrBinaryFile) {
			return PatchPlan{}, fmt.Errorf("cannot patch binary file: %s. No files were modified", parsed.Path)
		}
		return PatchPlan{}, fmt.Errorf("reading file: %w. No files were modified", err)
	}

	// Report progress before matching hunks
	if len(parsed.Hunks) > 1 {
		reportToolProgress(ctx, ToolProgressSnapshot{
			Text: fmt.Sprintf("matching %d hunks", len(parsed.Hunks)),
		})
	}

	after, err := applyParsedPatchWithContext(ctx, decodedFile.Text, parsed)
	if err != nil {
		return PatchPlan{}, err
	}
	diff := GenerateUnifiedDiffSummary(decodedFile.Text, after, parsed.Path)
	return PatchPlan{Path: resolvedPath, Before: decodedFile.Text, After: after, Diff: diff.Text, Added: diff.Added, Removed: diff.Removed}, nil
}

func formatFileSize(size int64) string {
	const kb = 1024
	const mb = kb * 1024
	if size >= mb {
		return fmt.Sprintf("%.1f MB", float64(size)/float64(mb))
	}
	if size >= kb {
		return fmt.Sprintf("%.1f KB", float64(size)/float64(kb))
	}
	return fmt.Sprintf("%d B", size)
}

func ParsePatch(path, patchText string) (parsedPatch, error) {
	cleanPath, err := validateEditPath(path)
	if err != nil {
		return parsedPatch{}, err
	}
	if unsupportedLines, unsupported := scanUnsupportedPatchOperations(cleanPath, patchText); len(unsupportedLines) > 0 {
		return parsedPatch{}, unsupportedPatchError(unsupportedLines, unsupported)
	}
	patchText = stripPatchEnvelopeMarkers(cleanPath, patchText)
	if strings.TrimSpace(patchText) == "" {
		return parsedPatch{}, fmt.Errorf("patch is required")
	}
	lines := splitPatchLines(strings.ReplaceAll(patchText, "\r\n", "\n"))
	parsed := parsedPatch{Path: cleanPath}
	var current *patchHunk
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "@@"):
			header := parsePatchHunkHeader(line)
			parsed.Hunks = append(parsed.Hunks, patchHunk{Header: header})
			current = &parsed.Hunks[len(parsed.Hunks)-1]
		default:
			if current == nil {
				if strings.TrimSpace(line) == "" {
					continue
				}
				return parsedPatch{}, fmt.Errorf("invalid patch: expected @@ hunk before patch lines. No files were modified")
			}
			if line == "" {
				// A bare empty line inside a hunk is a common model formatting
				// choice for a blank context line; treat it as an unchanged empty
				// line instead of rejecting the whole patch.
				current.Lines = append(current.Lines, patchLine{Kind: ' ', Text: ""})
				continue
			}
			kind := line[0]
			if kind != ' ' && kind != '+' && kind != '-' {
				return parsedPatch{}, fmt.Errorf("invalid patch line %q: hunk lines must start with space, +, or -. No files were modified", line)
			}
			current.Lines = append(current.Lines, patchLine{Kind: kind, Text: line[1:]})
		}
	}
	if len(parsed.Hunks) == 0 {
		return parsedPatch{}, fmt.Errorf("invalid patch: missing hunk. No files were modified")
	}
	for _, h := range parsed.Hunks {
		if len(h.Lines) == 0 {
			return parsedPatch{}, fmt.Errorf("invalid patch: empty hunk. No files were modified")
		}
	}
	// Check if patch has any actual changes (+ or - lines)
	hasChanges := false
	for _, h := range parsed.Hunks {
		for _, line := range h.Lines {
			if line.Kind == '+' || line.Kind == '-' {
				hasChanges = true
				break
			}
		}
		if hasChanges {
			break
		}
	}
	if !hasChanges {
		return parsedPatch{}, fmt.Errorf("patch only contains unchanged context lines (space prefix), missing +/- modification lines. Use - prefix for deletions and + prefix for additions. No files were modified")
	}
	return parsed, nil
}

func appendUnsupportedPatchLine(lines []string, line string) []string {
	line = strings.TrimSpace(line)
	if line == "" {
		return lines
	}
	for _, existing := range lines {
		if existing == line {
			return lines
		}
	}
	return append(lines, line)
}

func scanUnsupportedPatchOperations(path, patchText string) ([]string, unsupportedPatchOps) {
	lines := splitPatchLines(strings.ReplaceAll(patchText, "\r\n", "\n"))
	var unsupported unsupportedPatchOps
	var unsupportedLines []string
	seenHunk := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "*** Begin Patch" || trimmed == "*** End Patch":
			continue
		case !seenHunk && strings.HasPrefix(trimmed, "*** Update File:"):
			updatePath := strings.TrimSpace(strings.TrimPrefix(trimmed, "*** Update File:"))
			if cleanUpdatePath, err := validateEditPath(updatePath); err == nil && cleanUpdatePath == path {
				continue
			}
			unsupported.update = true
			unsupportedLines = appendUnsupportedPatchLine(unsupportedLines, trimmed)
		case strings.HasPrefix(trimmed, "*** Add File:"):
			unsupported.add = true
			unsupportedLines = appendUnsupportedPatchLine(unsupportedLines, trimmed)
		case strings.HasPrefix(trimmed, "*** Delete File:"):
			unsupported.delete = true
			unsupportedLines = appendUnsupportedPatchLine(unsupportedLines, trimmed)
		case strings.HasPrefix(trimmed, "*** Move to:"):
			unsupported.move = true
			unsupportedLines = appendUnsupportedPatchLine(unsupportedLines, trimmed)
		// The leading Update File marker for the requested path is allowed before
		// hunks start. A later marker means the patch switched files mid-stream,
		// which this single-file tool does not support.
		case strings.HasPrefix(trimmed, "*** Update File:"):
			unsupported.update = true
			unsupportedLines = appendUnsupportedPatchLine(unsupportedLines, trimmed)
		case strings.HasPrefix(trimmed, "***"):
			unsupported.other = true
			unsupportedLines = appendUnsupportedPatchLine(unsupportedLines, trimmed)
		}
		if strings.HasPrefix(line, "@@") {
			seenHunk = true
		}
	}
	return unsupportedLines, unsupported
}

func unsupportedPatchError(lines []string, ops unsupportedPatchOps) error {
	detail := formatUnsupportedPatchLines(lines)

	// Single-scenario templates keep the guidance compact and specific.
	switch {
	case ops.update && !ops.add && !ops.delete && !ops.move && !ops.other:
		return fmt.Errorf("%s %s %s No files were modified", detail, patchSingleFileHint, patchMultiUpdateHint)
	case ops.add && !ops.update && !ops.delete && !ops.move && !ops.other:
		return fmt.Errorf("%s %s %s No files were modified", detail, patchSingleFileHint, patchAddFileHint)
	case ops.delete && !ops.update && !ops.add && !ops.move && !ops.other:
		return fmt.Errorf("%s %s %s No files were modified", detail, patchSingleFileHint, patchDeleteFileHint)
	case ops.move && !ops.update && !ops.add && !ops.delete && !ops.other:
		return fmt.Errorf("%s %s %s No files were modified", detail, patchSingleFileHint, patchMoveFileHint)
	case ops.other && !ops.update && !ops.add && !ops.delete && !ops.move:
		return fmt.Errorf("%s %s %s No files were modified", detail, patchSingleFileHint, patchUnsupportedLineHint)
	}

	// Mixed scenarios mention only the actions actually present.
	parts := []string{patchSingleFileHint}
	if ops.update {
		parts = append(parts, patchMultiUpdateHint)
	}
	if ops.add {
		parts = append(parts, patchAddFileHint)
	}
	if ops.delete {
		parts = append(parts, patchDeleteFileHint)
	}
	if ops.move {
		parts = append(parts, patchMoveFileHint)
	}
	if ops.other {
		parts = append(parts, patchUnsupportedLineHint)
	}
	return fmt.Errorf("%s Mixed apply_patch-style operations were detected. %s No files were modified", detail, strings.Join(parts, " "))
}

func formatUnsupportedPatchLines(lines []string) string {
	if len(lines) == 1 {
		return fmt.Sprintf("unsupported patch operation %q.", lines[0])
	}
	var detail strings.Builder
	detail.WriteString("unsupported patch operations: ")
	for i, line := range lines {
		if i > 0 {
			detail.WriteString(", ")
		}
		detail.WriteString(strconv.Quote(line))
	}
	detail.WriteString(".")
	return detail.String()
}

func parsePatchHunkHeader(line string) string {
	header := strings.TrimSpace(strings.TrimPrefix(line, "@@"))
	if section, ok := parseUnifiedDiffRangeHeader(header); ok {
		return section
	}
	return header
}

func parseUnifiedDiffRangeHeader(header string) (string, bool) {
	firstRange, rest, ok := cutNextField(header)
	if !ok || !isUnifiedDiffRange(firstRange, '-') {
		return "", false
	}
	secondRange, rest, ok := cutNextField(rest)
	if !ok || !isUnifiedDiffRange(secondRange, '+') {
		return "", false
	}
	rest = strings.TrimLeft(rest, " \t")
	if !strings.HasPrefix(rest, "@@") {
		return "", false
	}
	section := strings.TrimPrefix(rest, "@@")
	section = strings.TrimPrefix(section, " ")
	return section, true
}

func cutNextField(s string) (field, rest string, ok bool) {
	s = strings.TrimLeft(s, " \t")
	if s == "" {
		return "", "", false
	}
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' {
			return s[:i], s[i:], true
		}
	}
	return s, "", true
}

func isUnifiedDiffRange(field string, prefix byte) bool {
	if len(field) < 2 || field[0] != prefix {
		return false
	}
	parts := strings.Split(field[1:], ",")
	if len(parts) > 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, r := range part {
			if !unicode.IsDigit(r) {
				return false
			}
		}
	}
	return true
}

func stripPatchEnvelopeMarkers(path, patchText string) string {
	lines := splitPatchLines(strings.ReplaceAll(patchText, "\r\n", "\n"))
	cleaned := make([]string, 0, len(lines))
	seenHunk := false
	changed := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case line == "*** Begin Patch" || line == "*** End Patch":
			changed = true
			continue
		case !seenHunk && strings.HasPrefix(trimmed, "*** Update File:"):
			updatePath := strings.TrimSpace(strings.TrimPrefix(trimmed, "*** Update File:"))
			if cleanUpdatePath, err := validateEditPath(updatePath); err == nil && cleanUpdatePath == path {
				changed = true
				continue
			}
		}
		if strings.HasPrefix(line, "@@") {
			seenHunk = true
		}
		cleaned = append(cleaned, line)
	}
	if !changed {
		return patchText
	}
	return strings.Join(cleaned, "\n")
}

func ExtractEditPathFromArgs(args json.RawMessage) string {
	return ExtractEditPathFromArgsInDir(args, "")
}

func ExtractEditPathFromArgsInDir(args json.RawMessage, baseDir string) string {
	// Try parsing as a common structure with path field
	var commonArgs struct {
		Path string `json:"path"`
	}
	unwrapped := unwrapToolArgs(args)
	if json.Unmarshal(unwrapped, &commonArgs) != nil {
		return ""
	}

	path := commonArgs.Path

	// If path is empty, try extracting from legacy patch envelope (PatchTool only)
	if path == "" {
		var patchArgs editArgs
		if json.Unmarshal(unwrapped, &patchArgs) == nil {
			path = extractLegacyEditEnvelopePath(patchArgs.Patch)
		}
	}

	// Validate and resolve the path
	if path == "" {
		return ""
	}
	path, err := validateEditPath(path)
	if err != nil {
		return ""
	}
	resolved, err := resolveEditPathForBase(path, baseDir)
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

func extractLegacyEditEnvelopePath(patchText string) string {
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

func resolveEditPathForBase(path, baseDir string) (string, error) {
	resolved, err := resolveToolPath(path)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	if strings.TrimSpace(baseDir) != "" && !filepath.IsAbs(resolved) {
		resolved = filepath.Join(baseDir, resolved)
	}
	return filepath.Clean(resolved), nil
}

func ResolveEditPathInDir(path, baseDir string) (string, error) {
	return resolveEditPathForBase(path, baseDir)
}

func validateEditPath(path string) (string, error) {
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

func applyParsedPatch(content string, patch parsedPatch) (string, error) {
	return applyParsedPatchWithContext(context.Background(), content, patch)
}

func applyParsedPatchWithContext(ctx context.Context, content string, patch parsedPatch) (string, error) {
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
	totalHunks := len(patch.Hunks)
	for hunkIdx, hunk := range patch.Hunks {
		// Report progress for each hunk in multi-hunk patches
		if totalHunks > 1 && hunkIdx > 0 {
			reportToolProgress(ctx, ToolProgressSnapshot{
				Text: fmt.Sprintf("matching hunk %d/%d", hunkIdx+1, totalHunks),
			})
		}

		headerMatchFound := false
		if hunk.Header != "" {
			headerMatch, _, err := findFirstHunkMatch(fileLines, []string{hunk.Header}, searchStart)
			if err == nil {
				searchStart = headerMatch + 1
				headerMatchFound = true
			} else {
				// Only treat header as soft anchor if it doesn't contain a specific identifier
				// (i.e., if it looks generic). A header with a function/test name should be required.
				if identifier := hunkHeaderIdentifier(hunk.Header); identifier != "" {
					// Header has a specific identifier but not found; report error immediately
					return "", fmt.Errorf("failed to locate @@ header %q: %s No files were modified", hunk.Header, diagnoseMissingHunkHeader(fileLines, hunk.Header, searchStart))
				}
				// Generic header (no identifier); fall through to oldSeq matching as soft anchor
			}
		}
		oldSeq := hunkOldSequence(hunk)
		match, _, err := findFirstHunkMatch(fileLines, oldSeq, searchStart)
		if err != nil {
			// If oldSeq also fails AND header was present but also failed, provide both diagnostics
			if hunk.Header != "" && !headerMatchFound {
				// This should only happen for generic headers (no identifier)
				return "", fmt.Errorf("@@ header %q not found, and hunk body also not found: %s No files were modified", hunk.Header, diagnoseMissingHunkHeader(fileLines, hunk.Header, searchStart))
			}
			return "", err
		}
		// If oldSeq matched but header didn't, check for ambiguity
		if hunk.Header != "" && !headerMatchFound {
			// Check ambiguity within the current forward-only search range.
			// Earlier matches that were already skipped by previous hunks must not
			// make the remaining unique match look ambiguous.
			allMatches := findMatchesWithNorm(fileLines, oldSeq, searchStart, func(s string) string { return s })
			if len(allMatches) > 1 {
				return "", fmt.Errorf("@@ header %q not found, and hunk body matches %d locations (ambiguous); use a valid header or add more context to make the hunk unique. No files were modified", hunk.Header, len(allMatches))
			}
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

func hunkOldSequence(h patchHunk) []string {
	var seq []string
	for _, line := range h.Lines {
		if line.Kind == ' ' || line.Kind == '-' {
			seq = append(seq, line.Text)
		}
	}
	return seq
}

func buildHunkReplacement(matchedOld []string, h patchHunk) []string {
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

func trailingContextLineCount(h patchHunk) int {
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

	// Match layer by layer, normalizing fileLines lazily. The exact layer reuses
	// fileLines/oldSeq directly (zero extra allocation) and most hunks match
	// there; the trailing-whitespace, surrounding-whitespace, and Unicode layers
	// only pay their normalization cost when an earlier layer fails to match.
	layers := []struct {
		name     string
		normFunc func(string) string // nil means identity (exact match)
	}{
		{name: "exact", normFunc: nil},
		{name: "ignoring trailing whitespace", normFunc: func(s string) string { return strings.TrimRightFunc(s, unicode.IsSpace) }},
		{name: "ignoring surrounding whitespace", normFunc: strings.TrimSpace},
		{name: "after normalizing common Unicode punctuation and whitespace", normFunc: normalizePatchUnicodeLine},
	}

	for _, layer := range layers {
		normFile, normOld := fileLines, oldSeq
		if layer.normFunc != nil {
			normFile = make([]string, len(fileLines))
			for i, line := range fileLines {
				normFile[i] = layer.normFunc(line)
			}
			normOld = make([]string, len(oldSeq))
			for i, line := range oldSeq {
				normOld[i] = layer.normFunc(line)
			}
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
					// Found enough matches to determine ambiguity
					return matches[0], hunkMatchResult{Layer: layer.name, Candidates: matches}, nil
				}
			}
		}

		if len(matches) > 0 {
			return matches[0], hunkMatchResult{Layer: layer.name, Candidates: matches}, nil
		}
	}

	return 0, hunkMatchResult{}, fmt.Errorf("hunk not found. %s No files were modified", diagnoseMissingHunk(fileLines, oldSeq, start))
}

func diagnoseMissingHunk(fileLines, oldSeq []string, start int) string {
	if len(oldSeq) == 0 {
		return "Add at least one unchanged or removed line to anchor the hunk."
	}
	if hunkLooksLikeReadOutput(oldSeq) {
		return "The hunk appears to include tool-added read metadata, copied line numbers, or a tab separator from old numbered output; remove the READ_RESULT line and any prefix so the hunk uses only source text."
	}
	if start >= len(fileLines) {
		return "The previous hunk matched near the end of the file, so this hunk starts searching past EOF; combine nearby edits or include context after the previous match."
	}

	// Try contiguous analysis first (gives more specific diagnostics)
	if diag := analyzeContiguousMatch(fileLines, oldSeq, start); diag != "" {
		return diag
	}

	// Check for trimmed whitespace matches
	if hasTrimmedLineMatches(fileLines, oldSeq, start) {
		return "The expected text is present only after trimming whitespace; rebuild the hunk with the file's exact indentation and blank-line spacing."
	}

	// Try detailed line-by-line comparison: a cheap bounded pass first, then a
	// full scan only when the bounded pass did not cover the whole file.
	if hint, complete := nearestLineDiagnostic(fileLines, oldSeq, start, start+100); hint != "" && complete {
		return hint
	}
	if hint, _ := nearestLineDiagnostic(fileLines, oldSeq, start, len(fileLines)); hint != "" {
		return hint
	}

	return "The hunk text was not found in the current file; Re-read the target area and rebuild the hunk from the latest contents."
}

func diagnoseMissingHunkHeader(fileLines []string, header string, start int) string {
	if start >= len(fileLines) {
		return "The previous hunk matched near the end of the file, so this @@ header starts searching past EOF; combine nearby edits or include context after the previous match."
	}
	if identifier := hunkHeaderIdentifier(header); identifier != "" && !fileContainsSubstring(fileLines, identifier, start) {
		return fmt.Sprintf("The @@ header anchor %q was not found in the current file; search/read the actual function, test, or symbol name before rebuilding the hunk.", identifier)
	}
	if hasTrimmedLineMatches(fileLines, []string{header}, start) {
		return "The @@ header text is present only after trimming whitespace; copy the exact header line, including indentation and spacing."
	}
	return "The @@ header text was not found in the current file; Re-read or grep the target area and use an existing function, test, or symbol name as the anchor."
}

func hunkHeaderIdentifier(header string) string {
	fields := strings.Fields(header)
	for i, field := range fields {
		if (field == "func" || field == "type" || field == "const" || field == "var") && i+1 < len(fields) {
			return trimIdentifierSuffix(fields[i+1])
		}
	}
	for _, field := range fields {
		candidate := trimIdentifierSuffix(field)
		if strings.HasPrefix(candidate, "Test") || strings.HasPrefix(candidate, "Benchmark") || strings.HasPrefix(candidate, "Example") {
			return candidate
		}
	}
	return ""
}

func trimIdentifierSuffix(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for i, r := range s {
		if !(r == '_' || r == '.' || unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return strings.Trim(s[:i], ".")
		}
	}
	return strings.Trim(s, ".")
}

func fileContainsSubstring(fileLines []string, needle string, start int) bool {
	if needle == "" {
		return false
	}
	for i := start; i < len(fileLines); i++ {
		if strings.Contains(fileLines[i], needle) {
			return true
		}
	}
	return false
}

func hunkLooksLikeReadOutput(lines []string) bool {
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "READ_RESULT ") {
			return true
		}
		trimmed := strings.TrimLeft(line, " ")
		digits := 0
		for digits < len(trimmed) && trimmed[digits] >= '0' && trimmed[digits] <= '9' {
			digits++
		}
		if digits > 0 && digits < len(trimmed) && trimmed[digits] == '\t' {
			return true
		}
	}
	return false
}

// analyzeContiguousMatch examines why a hunk's old sequence does not form a
// contiguous block in the file and returns a targeted diagnostic message.
func analyzeContiguousMatch(fileLines, oldSeq []string, start int) string {
	// Determine how many old-seq lines exist in the file, whether they appear
	// in order, and the longest contiguous sub-run that still matches.
	exactCount := 0
	for _, want := range oldSeq {
		for i := start; i < len(fileLines); i++ {
			if fileLines[i] == want {
				exactCount++
				break
			}
		}
	}
	if exactCount == 0 {
		return ""
	}

	// Find the longest contiguous sub-run of oldSeq that still matches
	// sequentially in the file starting from 'start'.
	bestRun, bestFileLine := longestContiguousRun(fileLines, oldSeq, start)

	switch {
	case exactCount < len(oldSeq):
		// Some lines are missing from the file entirely, suggesting the
		// file has been modified since the hunk was written.
		return fmt.Sprintf("Some hunk lines no longer exist in the file (%d of %d lines found); the file likely changed since the hunk was written. Re-read the target area and rebuild the hunk from the current contents.", exactCount, len(oldSeq))
	case bestRun < len(oldSeq):
		// All lines exist individually but not as a contiguous block.
		if bestRun > 0 && bestFileLine >= 0 {
			return fmt.Sprintf("All hunk lines exist in the file, but not as one contiguous block (longest adjacent match: %d of %d lines starting at line %d); extra content may have been inserted or removed between them. Re-read the target area and include the current surrounding lines.", bestRun, len(oldSeq), bestFileLine+1)
		}
		return "All hunk lines exist in the file, but not as one contiguous block; Re-read the target area and include the current surrounding lines."
	default:
		// All lines exist and form a contiguous block — shouldn't happen
		// because the caller already failed to find a match, but provide a
		// fallback.
		return "Some hunk lines exist in the file, but not as one contiguous block; Re-read the target area and include the current surrounding lines."
	}
}

// longestContiguousRun finds the longest sub-sequence of oldSeq that appears
// as a contiguous run in fileLines starting at or after 'start'. It returns
// the length of that run and the 0-based file line index where it starts.
// It first analyzes a bounded window for speed, then falls back to a full scan
// only when the best match hits the fast window boundary and may be truncated.
func longestContiguousRun(fileLines, oldSeq []string, start int) (runLen int, fileLine int) {
	bestLen, bestStart, hitWindowEdge := longestContiguousRunInWindow(fileLines, oldSeq, start, min(len(fileLines), start+500))
	if hitWindowEdge && bestLen < len(oldSeq) {
		fullLen, fullStart, _ := longestContiguousRunInWindow(fileLines, oldSeq, start, len(fileLines))
		if fullLen > bestLen {
			return fullLen, fullStart
		}
	}
	return bestLen, bestStart
}

func longestContiguousRunInWindow(fileLines, oldSeq []string, start, searchLimit int) (runLen int, fileLine int, hitWindowEdge bool) {
	bestLen := 0
	bestStart := -1

	linePositions := make(map[string][]int)
	for i := start; i < searchLimit; i++ {
		line := fileLines[i]
		linePositions[line] = append(linePositions[line], i)
	}

	for from := 0; from < len(oldSeq); from++ {
		positions, exists := linePositions[oldSeq[from]]
		if !exists {
			continue
		}

		for _, j := range positions {
			run := 1
			for run < len(oldSeq)-from && j+run < searchLimit && fileLines[j+run] == oldSeq[from+run] {
				run++
			}
			if j+run == searchLimit && searchLimit < len(fileLines) {
				hitWindowEdge = true
			}
			if run > bestLen {
				bestLen = run
				bestStart = j
			}
		}
	}
	return bestLen, bestStart, hitWindowEdge
}

func hasTrimmedLineMatches(fileLines, oldSeq []string, start int) bool {
	for _, want := range oldSeq {
		trimmedWant := strings.TrimSpace(want)
		if trimmedWant == "" {
			continue
		}
		for i := start; i < len(fileLines); i++ {
			if strings.TrimSpace(fileLines[i]) == trimmedWant {
				return true
			}
		}
	}
	return false
}

// nearestLineDiagnostic finds the file line in [start, searchLimit) most similar
// to one of the hunk's context/removed lines and, when the overlap is high,
// reports the 1-based column of the first difference plus a short excerpt from
// each side. This is aimed at long single lines (doc strings, prompts, URLs)
// where exact whole-line matching fails over a single stray character and the
// model otherwise cannot tell what differs. It returns "" when no line is
// similar enough to be useful. complete reports whether searchLimit already
// covered the full file, so callers can try a cheap bounded pass first and only
// fall back to a full scan when the bounded pass was incomplete.
func nearestLineDiagnostic(fileLines, oldSeq []string, start, searchLimit int) (hint string, complete bool) {
	if searchLimit > len(fileLines) {
		searchLimit = len(fileLines)
	}
	bestOverlap := -1
	var bestWant, bestFile string
	bestLine := -1

	for _, want := range oldSeq {
		if strings.TrimSpace(want) == "" {
			continue
		}
		for i := start; i < searchLimit; i++ {
			file := fileLines[i]
			if file == want {
				continue
			}
			prefix, prefixBytes := commonPrefixLen(want, file)
			suffix := commonSuffixLen(want, file, prefixBytes)
			overlap := prefix + suffix
			longer := max(utf8.RuneCountInString(want), utf8.RuneCountInString(file))
			// Require substantial overlap so we only flag genuine near-misses.
			if longer == 0 || overlap*5 < longer*3 {
				continue
			}
			if overlap > bestOverlap {
				bestOverlap = overlap
				bestWant, bestFile = want, file
				bestLine = i
			}
		}
	}

	if bestLine < 0 {
		return "", searchLimit == len(fileLines)
	}
	prefix, prefixBytes := commonPrefixLen(bestWant, bestFile)
	col := prefix + 1
	return fmt.Sprintf(
		"The hunk text was not found, but file line %d is almost identical and first differs at column %d: file has %s, hunk has %s. Re-read that line and copy it verbatim (watch for whitespace, punctuation, or a single changed character).",
		bestLine+1,
		col,
		quoteDiffExcerpt(bestFile, prefixBytes),
		quoteDiffExcerpt(bestWant, prefixBytes),
	), searchLimit == len(fileLines)
}

func commonPrefixLen(a, b string) (runes int, bytes int) {
	for bytes < len(a) && bytes < len(b) {
		ra, sizeA := utf8.DecodeRuneInString(a[bytes:])
		rb, sizeB := utf8.DecodeRuneInString(b[bytes:])
		if sizeA != sizeB || ra != rb || a[bytes:bytes+sizeA] != b[bytes:bytes+sizeB] {
			break
		}
		bytes += sizeA
		runes++
	}
	return runes, bytes
}

// commonSuffixLen counts trailing equal runes without overlapping the already
// matched prefix in either string.
func commonSuffixLen(a, b string, prefixBytes int) int {
	count := 0
	for ai, bi := len(a), len(b); ai > prefixBytes && bi > prefixBytes; {
		ra, sizeA := utf8.DecodeLastRuneInString(a[:ai])
		rb, sizeB := utf8.DecodeLastRuneInString(b[:bi])
		if sizeA != sizeB || ra != rb || ai-sizeA < prefixBytes || bi-sizeB < prefixBytes || a[ai-sizeA:ai] != b[bi-sizeB:bi] {
			break
		}
		ai -= sizeA
		bi -= sizeB
		count++
	}
	return count
}

// quoteDiffExcerpt returns a quoted UTF-8-safe ~40-byte window of s starting at
// byte offset off, so a difference deep inside a long line is shown without
// dumping it all.
func quoteDiffExcerpt(s string, off int) string {
	if off < 0 {
		off = 0
	}
	if off > len(s) {
		off = len(s)
	}
	for off > 0 && off < len(s) && !utf8.RuneStart(s[off]) {
		off--
	}
	const window = 40
	end := min(off+window, len(s))
	for end > off && end < len(s) && !utf8.RuneStart(s[end]) {
		end--
	}
	excerpt := s[off:end]
	if end < len(s) {
		excerpt += "…"
	}
	return strconv.Quote(excerpt)
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
