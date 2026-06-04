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
	"github.com/keakon/chord/internal/toolname"
)

const editUnsupportedOperationHint = "edit only updates one existing file. Use write to create files and delete to remove whole files."

// EditTool applies a single-file update patch to an existing text file.
// If LSP is set, notifies LSP of the change after a successful patch.
type EditTool struct {
	LSP     *lsp.Manager // nil when LSP not configured
	BaseDir string       // optional base directory for relative patch paths
}

type editArgs struct {
	Path  string `json:"path"`
	Patch string `json:"patch"`
}

type EditPlan struct {
	Path    string
	Before  string
	After   string
	Diff    string
	Added   int
	Removed int
}

type editPlanContextKey struct{}

func ContextWithEditPlan(ctx context.Context, plan EditPlan) context.Context {
	return context.WithValue(ctx, editPlanContextKey{}, plan)
}

func editPlanFromContext(ctx context.Context) (EditPlan, bool) {
	plan, ok := ctx.Value(editPlanContextKey{}).(EditPlan)
	return plan, ok
}

type parsedEdit struct {
	Path  string
	Hunks []editHunk
}

type editHunk struct {
	Header string
	Lines  []editLine
}

type editLine struct {
	Kind byte
	Text string
}

func (t EditTool) Name() string { return NameEdit }

func (EditTool) ConcurrencyPolicy(args json.RawMessage) ConcurrencyPolicy {
	return normalizeConcurrencyPolicy(NameEdit, fileToolConcurrencyPolicy(args, false))
}

func (t EditTool) Description() string {
	return "Edit one existing file with patch hunks. Input is JSON {\"path\":\"...\",\"patch\":\"...\"}. The path may be absolute or relative and supports ~ for the current user's home directory. Patch contains hunk text only: use @@ hunk headers, context lines with a leading space, removed lines with -, and added lines with +. Codex apply_patch envelope lines such as *** Begin Patch, a leading *** Update File matching path, and *** End Patch are ignored when present. Hunks are applied in order by matching the first occurrence after the current search position, so include enough nearby context for the intended location; for repeated blocks such as tests or fixtures, include the surrounding function, test, or case name in the same @@ hunk, for example @@ func name(...), only after verifying that exact header exists in the current file. Do not guess or approximate anchors, and do not use a separate @@ hunk only as an anchor. Use " + toolname.Write + " to create a new file or intentionally replace an entire file, and " + toolname.Delete + " to remove whole files. Before " + toolname.Edit + ", make sure the file has been observed via " + toolname.Read + " or a system-resolved @file mention; if you need more surrounding context, " + toolname.Read + " the target area. If " + toolname.Edit + " fails, diagnose the reported cause first; re-read or grep stale text/anchors before retrying, and do not retry the same hunk unchanged. If the file changed since it was observed, the tool validates hunks against current contents and may report a backup for risky writes. Do not use " + toolname.Shell + " to run apply_patch."
}

func (t EditTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the existing file to update. May be absolute or relative and supports ~ for the current user's home directory.",
			},
			"patch": map[string]any{
				"type":        "string",
				"description": "Patch hunk text. Use @@ hunk headers, context lines with a leading space, removed lines with -, and added lines with +. Codex apply_patch envelope lines such as *** Begin Patch, a leading *** Update File matching path, and *** End Patch are ignored when present. Hunks are applied in order by matching the first occurrence after the current search position. Keep hunks small and include enough nearby context for the intended location. You may put a function/class/test header after @@, such as @@ func TestName(t *testing.T) {, to anchor that hunk, but only after verifying that exact header exists in the current file with read/grep; do not guess or approximate anchors, and do not use a separate earlier @@ hunk only as an anchor. If an edit fails, diagnose the error first, re-read/grep stale text or anchors, and do not retry the same hunk unchanged.",
			},
		},
		"required":             []string{"path", "patch"},
		"additionalProperties": false,
	}
}

func (t EditTool) IsReadOnly() bool { return false }

func (t EditTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a editArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		err = fmt.Errorf("invalid arguments: %w", err)
		return formatEditErrorResult(a.Patch, err), err
	}
	plan, ok := editPlanFromContext(ctx)
	if !ok {
		var err error
		plan, err = BuildEditPlanInDir(a.Path, a.Patch, t.BaseDir)
		if err != nil {
			return formatEditErrorResult(a.Patch, err), err
		}
	}
	if plan.Before == plan.After {
		err := fmt.Errorf("patch makes no changes. No files were modified")
		return formatEditErrorResult(a.Patch, err), err
	}
	baseDir := t.BaseDir
	resolvedPath, err := resolveEditPathForBase(plan.Path, baseDir)
	if err != nil {
		err = fmt.Errorf("resolve path: %w. No files were modified", err)
		return formatEditErrorResult(a.Patch, err), err
	}
	decodedFile, err := ReadDecodedTextFile(resolvedPath)
	if err != nil {
		if os.IsNotExist(err) {
			err = fmt.Errorf("file not found: %s. No files were modified", plan.Path)
			return formatEditErrorResult(a.Patch, err), err
		}
		if errors.Is(err, ErrBinaryFile) {
			err = fmt.Errorf("cannot patch binary file: %s. No files were modified", plan.Path)
			return formatEditErrorResult(a.Patch, err), err
		}
		err = fmt.Errorf("reading file: %w. No files were modified", err)
		return formatEditErrorResult(a.Patch, err), err
	}
	if decodedFile.Text != plan.Before {
		err := fmt.Errorf("file %s changed while planning patch; re-read it before applying the patch. No files were modified", plan.Path)
		return formatEditErrorResult(a.Patch, err), err
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

	displayPath := plan.Path
	if rel, relErr := filepath.Rel(baseDirOrCwd(t.BaseDir), plan.Path); relErr == nil && rel != "" && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
		displayPath = filepath.Clean(rel)
	}
	out := formatEditSuccessResult(displayPath, plan.Added, plan.Removed, decodedFile.Encoding.Name)
	if t.LSP != nil {
		absPath, absErr := filepath.Abs(resolvedPath)
		if absErr == nil {
			t.LSP.MarkTouched(absPath)
			out = t.LSP.AfterWriteToolResult(ctx, absPath, plan.After, out, false)
		}
	}
	return out, nil
}

func BuildEditPlanInDir(path, patchText, baseDir string) (EditPlan, error) {
	return buildEditPlan(path, patchText, baseDir)
}

func BuildEditPlan(path, patchText string) (EditPlan, error) {
	return buildEditPlan(path, patchText, "")
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

func formatEditSuccessResult(displayPath string, added, removed int, encoding string) string {
	result := fmt.Sprintf("Applied patch to %s (+%d -%d)", displayPath, added, removed)
	if encoding != "" && encoding != "utf-8" {
		result += fmt.Sprintf(", encoding=%s", encoding)
	}
	return result
}

func formatEditErrorResult(patch string, err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	patch = strings.TrimSpace(patch)
	if patch == "" || !shouldShowEditPatchExcerpt(msg) {
		return msg
	}
	return msg + "\n\nPatch excerpt:\n" + fencedPatchExcerpt(patch)
}

func shouldShowEditPatchExcerpt(msg string) bool {
	msg = strings.ToLower(strings.TrimSpace(msg))
	for _, needle := range []string{
		"hunk not found",
		"failed to locate @@ header",
		"invalid patch",
		"patch is required",
		"unsupported patch operation",
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

func buildEditPlan(path, patchText, baseDir string) (EditPlan, error) {
	parsed, err := ParseEdit(path, patchText)
	if err != nil {
		return EditPlan{}, err
	}
	resolvedPath, err := resolveEditPathForBase(parsed.Path, baseDir)
	if err != nil {
		return EditPlan{}, fmt.Errorf("resolve path: %w. No files were modified", err)
	}
	if isBlockedDevicePath(resolvedPath) {
		return EditPlan{}, fmt.Errorf("cannot patch blocked device path: %s. No files were modified", parsed.Path)
	}
	info, err := os.Stat(resolvedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return EditPlan{}, fmt.Errorf("file not found: %s. edit only updates one existing file. Use write to create files. No files were modified", parsed.Path)
		}
		return EditPlan{}, fmt.Errorf("accessing path: %w. No files were modified", err)
	}
	if err := ensureRegularFilePath(parsed.Path, info); err != nil {
		return EditPlan{}, fmt.Errorf("%w. No files were modified", err)
	}
	decodedFile, err := ReadDecodedTextFile(resolvedPath)
	if err != nil {
		if errors.Is(err, ErrBinaryFile) {
			return EditPlan{}, fmt.Errorf("cannot patch binary file: %s. No files were modified", parsed.Path)
		}
		return EditPlan{}, fmt.Errorf("reading file: %w. No files were modified", err)
	}
	after, err := applyParsedPatch(decodedFile.Text, parsed)
	if err != nil {
		return EditPlan{}, err
	}
	diff := GenerateUnifiedDiffSummary(decodedFile.Text, after, parsed.Path)
	return EditPlan{Path: resolvedPath, Before: decodedFile.Text, After: after, Diff: diff.Text, Added: diff.Added, Removed: diff.Removed}, nil
}

func ParseEdit(path, patchText string) (parsedEdit, error) {
	cleanPath, err := validateEditPath(path)
	if err != nil {
		return parsedEdit{}, err
	}
	patchText = stripEditEnvelopeMarkers(cleanPath, patchText)
	if strings.TrimSpace(patchText) == "" {
		return parsedEdit{}, fmt.Errorf("patch is required")
	}
	lines := splitPatchLines(strings.ReplaceAll(patchText, "\r\n", "\n"))
	parsed := parsedEdit{Path: cleanPath}
	var current *editHunk
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "*** Add File:") || strings.HasPrefix(line, "*** Delete File:") || strings.HasPrefix(line, "*** Move to:") || strings.HasPrefix(line, "*** Update File:"):
			return parsedEdit{}, fmt.Errorf("unsupported patch operation. %s No files were modified", editUnsupportedOperationHint)
		case strings.HasPrefix(line, "***"):
			return parsedEdit{}, fmt.Errorf("unsupported patch operation %q. %s No files were modified", line, editUnsupportedOperationHint)
		case strings.HasPrefix(line, "@@"):
			header := strings.TrimSpace(strings.TrimPrefix(line, "@@"))
			parsed.Hunks = append(parsed.Hunks, editHunk{Header: header})
			current = &parsed.Hunks[len(parsed.Hunks)-1]
		default:
			if current == nil {
				if strings.TrimSpace(line) == "" {
					continue
				}
				return parsedEdit{}, fmt.Errorf("invalid patch: expected @@ hunk before patch lines. No files were modified")
			}
			if line == "" {
				// A bare empty line inside a hunk is a common model formatting
				// choice for a blank context line; treat it as an unchanged empty
				// line instead of rejecting the whole patch.
				current.Lines = append(current.Lines, editLine{Kind: ' ', Text: ""})
				continue
			}
			kind := line[0]
			if kind != ' ' && kind != '+' && kind != '-' {
				return parsedEdit{}, fmt.Errorf("invalid patch line %q: hunk lines must start with space, +, or -. No files were modified", line)
			}
			current.Lines = append(current.Lines, editLine{Kind: kind, Text: line[1:]})
		}
	}
	if len(parsed.Hunks) == 0 {
		return parsedEdit{}, fmt.Errorf("invalid patch: missing hunk. No files were modified")
	}
	for _, h := range parsed.Hunks {
		if len(h.Lines) == 0 {
			return parsedEdit{}, fmt.Errorf("invalid patch: empty hunk. No files were modified")
		}
	}
	return parsed, nil
}

func stripEditEnvelopeMarkers(path, patchText string) string {
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
	var parsed editArgs
	if json.Unmarshal(unwrapToolArgs(args), &parsed) != nil {
		return ""
	}
	path := parsed.Path
	if path == "" {
		path = extractLegacyEditEnvelopePath(parsed.Patch)
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

func applyParsedPatch(content string, patch parsedEdit) (string, error) {
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
		if hunk.Header != "" {
			headerMatch, _, err := findFirstHunkMatch(fileLines, []string{hunk.Header}, searchStart)
			if err != nil {
				return "", fmt.Errorf("failed to locate @@ header %q: %s No files were modified", hunk.Header, diagnoseMissingHunkHeader(fileLines, hunk.Header, searchStart))
			}
			searchStart = headerMatch + 1
		}
		oldSeq := hunkOldSequence(hunk)
		match, _, err := findFirstHunkMatch(fileLines, oldSeq, searchStart)
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

func hunkOldSequence(h editHunk) []string {
	var seq []string
	for _, line := range h.Lines {
		if line.Kind == ' ' || line.Kind == '-' {
			seq = append(seq, line.Text)
		}
	}
	return seq
}

func buildHunkReplacement(matchedOld []string, h editHunk) []string {
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

func trailingContextLineCount(h editHunk) int {
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
	return 0, hunkMatchResult{}, fmt.Errorf("hunk not found. %s No files were modified", diagnoseMissingHunk(fileLines, oldSeq, start))
}

func diagnoseMissingHunk(fileLines, oldSeq []string, start int) string {
	if len(oldSeq) == 0 {
		return "Add at least one unchanged or removed line to anchor the hunk."
	}
	if hunkLooksLikeReadOutput(oldSeq) {
		return "The hunk appears to include Read output line numbers or the tab separator; remove the left line-number gutter and use only source text."
	}
	if start >= len(fileLines) {
		return "The previous hunk matched near the end of the file, so this hunk starts searching past EOF; combine nearby edits or include context after the previous match."
	}
	if hasExactLineMatches(fileLines, oldSeq, start) {
		return "Some hunk lines exist in the file, but not as one contiguous block; Re-read the target area and include the current surrounding lines."
	}
	if hasTrimmedLineMatches(fileLines, oldSeq, start) {
		return "The expected text is present only after trimming whitespace; rebuild the hunk with the file's exact indentation and blank-line spacing."
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

func hasExactLineMatches(fileLines, oldSeq []string, start int) bool {
	for _, want := range oldSeq {
		for i := start; i < len(fileLines); i++ {
			if fileLines[i] == want {
				return true
			}
		}
	}
	return false
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
