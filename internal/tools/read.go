package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ReadTool reads file contents with optional offset/limit paging.
type ReadTool struct {
	LSP     lspStarter // nil when LSP not configured
	BaseDir string     // session working directory for relative paths; empty keeps process cwd behavior
}

// lspStarter is the minimal interface ReadTool needs from lsp.Manager.
type lspStarter interface {
	Start(ctx context.Context, path string)
}

type readArgs struct {
	Path   string `json:"path"`
	Offset *int   `json:"offset,omitempty"` // 1-based start line; 0 or absent means the first line
	Limit  *int   `json:"limit,omitempty"`  // number of lines; defaults to 2000
}

// ExtractReadPathFromArgsInDir returns the absolute path targeted by a Read
// call. Agent-side retry bookkeeping uses the same argument decoding and path
// resolution as ReadTool so relative and absolute spellings share one key.
func ExtractReadPathFromArgsInDir(args json.RawMessage, baseDir string) string {
	var parsed readArgs
	if err := json.Unmarshal(unwrapToolArgs(args), &parsed); err != nil || strings.TrimSpace(parsed.Path) == "" {
		return ""
	}
	resolved, err := resolveToolPathAbsInDir(parsed.Path, baseDir)
	if err != nil {
		return ""
	}
	return resolved
}

func (ReadTool) Name() string { return NameRead }

func (t ReadTool) ConcurrencyPolicy(args json.RawMessage) ConcurrencyPolicy {
	return normalizeConcurrencyPolicy(NameRead, fileToolConcurrencyPolicyInDir(args, true, t.BaseDir))
}

func (ReadTool) Description() string {
	return "Read file contents by line for code inspection and edits, with optional offset/limit line paging (up to 2000 lines by default).\n" +
		"Usage:\n" +
		"- Prefer grep or lsp to locate symbols before reading a small nearby block.\n" +
		"- For a file you will edit or consult repeatedly, prefer reading it in full once (a single read covers up to 2000 lines by default) over paging it in many small windows; each extra window costs a full model round trip.\n" +
		"- offset is a 1-based line number (1 = the first line); omit it to start from the beginning.\n" +
		"- If a file or saved output is a huge single line that cannot fit in read output, use grep to locate patterns or a script/parser via shell for structured processing instead of character-range reads.\n" +
		"Output format:\n" +
		"- Normal output starts with one READ_RESULT metadata line of the form `READ_RESULT lines=a-b total=N` (1-based inclusive returned range and total file line count), or `READ_RESULT lines=none total=N` when no line was returned (an empty file, or offset=N+1 — the one-past-the-last-line value that is the normal end of paging; an offset larger than N+1 is an error); everything after that first line is exact file text without line-number gutters or extra indentation, so copy only the text after READ_RESULT into edit hunks.\n" +
		"- The header omits encoding for UTF-8 files and reports it only for other encodings.\n" +
		"- read output normalizes line endings to LF; edit preserves the file's existing line-ending style when writing.\n" +
		"Truncation semantics (a read that simply did not reach the end of the file is not truncation):\n" +
		"- `truncated=budget`: the tool itself dropped requested lines to fit the approximate 20k-token read budget; the header adds `requested_lines=a-d` (the range you originally requested) — compare it with the returned lines=a-b and page further with offset/limit.\n" +
		"- `truncated=stale`: a context-reduction pass trimmed this old read output because the file was modified after the read; do not trust its content and re-read before editing.\n" +
		"- `truncated=superseded`: a newer read of the same range appears later in this conversation; use that newer output.\n" +
		"- Context reduction trims an old read only when its content is no longer the current view, keeping the leading lines (lines=a-b then covers just the kept head). A read that is still current is never trimmed, so do not re-read files just to refresh them.\n" +
		"For edit, include a few unchanged source lines around the intended change; if you need more surrounding context, read the intended nearby block before patching."
}

func (ReadTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Absolute or relative path to an existing file to read. Relative paths resolve from the session working directory. Supports ~ for the current user's home directory. Do not guess paths; verify uncertain paths before reading.",
			},
			"offset": map[string]any{
				"type":        "integer",
				"minimum":     0,
				"description": "1-based line number to start reading from (1 = the first line); 0 or omitted means the first line. Defaults to 1.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum number of lines to return. Defaults to 2000.",
			},
		},
		"required":             []string{"path"},
		"additionalProperties": false,
	}
}

// MaxReadFileBytes is the maximum file size (50 MiB) that Read will load into memory
// to avoid OOM when reading very large files. Use offset/limit to read portions of
// larger files, or delegate to a sub-agent with Grep/Read on specific ranges.
const MaxReadFileBytes = 50 * 1024 * 1024

// MaxReadOutputTokens is the approximate token budget for the formatted Read output
// that is sent back into the conversation. This is intentionally stricter than the
// file-size gate so large-but-readable files still require paging.
const MaxReadOutputTokens = 20_000

func (ReadTool) IsReadOnly() bool { return true }

func (ReadTool) ConcurrencySafeReadOnly(json.RawMessage) bool { return true }

func (ReadTool) CanRenderBeforeToolUseEnd(json.RawMessage) bool { return true }

func splitReadToolLines(content string) []string {
	if content == "" {
		return nil
	}
	// Normalize common newline conventions first so Read paging and formatting are
	// independent of the source file's line endings.
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	trimmed := strings.TrimSuffix(normalized, "\n")
	if trimmed == "" {
		return []string{""}
	}
	return strings.Split(trimmed, "\n")
}

func estimateReadOutputTokens(content string) int {
	n := len(content) / 3
	if n < 1 {
		return 1
	}
	return n
}

func maxReadInlineBytes() int {
	maxBytes := MaxReadOutputTokens * 3
	if MaxOutputBytes < maxBytes {
		return MaxOutputBytes
	}
	return maxBytes
}

func readOutputFitsBudget(content string) bool {
	return len(content) <= maxReadInlineBytes() && estimateReadOutputTokens(content) <= MaxReadOutputTokens
}

func quoteReadHeaderValue(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "\"\""
	}
	return string(encoded)
}

// readResultHeader builds the single metadata line that precedes the raw file
// text. It is intentionally minimal: the model already knows the path it asked
// for, so only information it cannot derive from the request is reported.
//
//   - lines=a-b total=N is always present so the model knows which 1-based range
//     it received and how many lines the file has (it cannot infer the total
//     from its own offset/limit). When no line was returned (empty file or an
//     offset at/after EOF) the range is reported as lines=none instead of a
//     confusing start>end pair.
//   - truncated=budget is emitted ONLY when this read tool actively returned
//     fewer lines than requested to fit the output budget. It is paired with
//     requested_lines=a-d (the range the caller originally asked for) so the
//     model can see it received fewer lines than requested and page further.
//     A read that simply did not reach EOF because of offset/limit is NOT
//     truncation and carries no truncated field.
//   - encoding is reported only for non-UTF-8 files, since UTF-8 is the norm.
func readResultHeader(startLine, endLine, totalLines, requestedEndLine int, encoding string, budgetTruncated bool) string {
	linesField := "none"
	if startLine >= 1 && endLine >= startLine {
		linesField = fmt.Sprintf("%d-%d", startLine, endLine)
	}
	requestedLines := ""
	truncatedKind := ""
	if budgetTruncated {
		truncatedKind = ReadTruncatedBudget
		requestedLines = fmt.Sprintf("%d-%d", startLine, requestedEndLine)
	}
	return FormatReadResultHeader(linesField, totalLines, truncatedKind, requestedLines, encoding)
}

// Read header truncation kinds. budget means the read tool itself dropped
// requested lines at call time to fit the output budget; the other kinds are
// applied by a later context-reduction pass: stale means the file was modified
// after this read (content untrustworthy), superseded means a newer read of
// the same range appears later in the conversation. Still-valid reads are
// never trimmed.
const (
	ReadTruncatedBudget     = "budget"
	ReadTruncatedStale      = "stale"
	ReadTruncatedSuperseded = "superseded"
)

// FormatReadResultHeader builds the single READ_RESULT metadata line shared by
// the read tool and the context-reduction summary so all truncation paths
// render identically and differ only in the truncation reason.
//
// linesField is the already-formatted returned range: "a-b", a multi-segment
// "a-b,c-d" when only head/tail lines survive, or "none" when no line was
// returned. truncatedKind is "", ReadTruncatedBudget, ReadTruncatedStale or
// ReadTruncatedSuperseded. requestedLines (e.g. "a-d") is appended only for
// budget truncation. encoding is appended only for non-UTF-8 files.
func FormatReadResultHeader(linesField string, totalLines int, truncatedKind, requestedLines, encoding string) string {
	header := fmt.Sprintf("READ_RESULT lines=%s total=%d", linesField, totalLines)
	switch truncatedKind {
	case ReadTruncatedBudget:
		header += " truncated=" + ReadTruncatedBudget
		if requestedLines != "" {
			header += " requested_lines=" + requestedLines
		}
	case ReadTruncatedStale, ReadTruncatedSuperseded:
		header += " truncated=" + truncatedKind
	}
	if normalized := strings.ToLower(strings.TrimSpace(encoding)); normalized != "" && normalized != "utf-8" {
		header += " encoding=" + quoteReadHeaderValue(encoding)
	}
	return header
}

func buildReadContent(header string, contentLines []string) string {
	var b strings.Builder
	b.WriteString(header)
	b.WriteByte('\n')
	for _, line := range contentLines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func truncateReadContentToBudget(contentLines []string, startLine, totalLines int, encoding string) string {
	if len(contentLines) == 0 {
		line := 0
		if totalLines > 0 {
			line = startLine
		}
		return buildReadContent(readResultHeader(line, max(line-1, 0), totalLines, 0, encoding, false), nil)
	}

	// The caller already narrowed contentLines to the requested offset/limit
	// window, so its last line is what the model asked for; budget truncation
	// only returns a prefix of it.
	requestedEndLine := startLine + len(contentLines) - 1
	lo, hi := 1, len(contentLines)
	best := ""
	for lo <= hi {
		mid := lo + (hi-lo)/2
		header := readResultHeader(startLine, startLine+mid-1, totalLines, requestedEndLine, encoding, true)
		candidate := buildReadContent(
			header,
			contentLines[:mid],
		)
		if readOutputFitsBudget(candidate) {
			best = candidate
			lo = mid + 1
			continue
		}
		hi = mid - 1
	}
	if best != "" {
		return best
	}
	header := readResultHeader(startLine, max(startLine-1, 0), totalLines, requestedEndLine, encoding, true)
	return buildReadContent(header, nil)
}

func readOffsetPastEndError(startLine, totalLines int, limit *int) error {
	effectiveLimit := MaxOutputLines
	if limit != nil && *limit > 0 {
		effectiveLimit = *limit
	}
	if totalLines == 0 {
		return fmt.Errorf("offset %d exceeds this file length (0 lines); the file is empty, so read from offset 1 (or omit it) to get an empty result", startLine)
	}
	// tailStart is the 1-based first line of the last effectiveLimit lines.
	tailStart := max(totalLines-effectiveLimit+1, 1)
	lastLines := totalLines - tailStart + 1
	return fmt.Errorf("offset %d exceeds this file length (%d lines); suggested_offset=%d reads the last %d lines with limit=%d; eof_offset=%d is valid but returns no lines", startLine, totalLines, tailStart, lastLines, effectiveLimit, totalLines+1)
}

func (t ReadTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var a readArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	resolvedPath, info, err := resolveExistingToolPathInDir(a.Path, t.BaseDir, PathTargetRegularFile, "read")
	if err != nil {
		if strings.Contains(err.Error(), "path not found") {
			return "", fileNotFoundErrorWithPathSuggestionsInDir(a.Path, t.BaseDir, PathTargetRegularFile)
		}
		return "", err
	}

	if info.Size() > MaxReadFileBytes {
		return "", fmt.Errorf("file too large (%d bytes, max %d); use offset/limit to read a portion or grep to search", info.Size(), MaxReadFileBytes)
	}

	decoded, rawBytes, err := ReadAndDecodeTextFile(resolvedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("file not found: %s", a.Path)
		}
		if os.IsPermission(err) {
			return "", fmt.Errorf("permission denied: %s", a.Path)
		}
		if errors.Is(err, ErrBinaryFile) {
			return "", fmt.Errorf("cannot read binary file: %s", a.Path)
		}
		return "", fmt.Errorf("reading file: %w", err)
	}
	if sink, ok := readObservationSinkFromContext(ctx); ok {
		sum := sha256.Sum256(rawBytes)
		observedPath := resolvedPath
		if absPath, absErr := resolveToolPathAbsInDir(a.Path, t.BaseDir); absErr == nil {
			observedPath = absPath
		}
		sink.SetReadObservation(ReadObservation{Path: observedPath, SHA256: hex.EncodeToString(sum[:])})
	}
	lines := splitReadToolLines(decoded.Text)
	totalLines := len(lines)

	// Determine start line. The public offset is a 1-based line number
	// (1 = the first line), matching the READ_RESULT range the tool reports
	// and the CLI conventions of the major read tools. 0 or absent both mean
	// the first line, so models that reason in 0-based offsets cannot silently
	// off-by-one when they pass 0 for the top of a file. Internally this maps
	// back to a 0-based slice index.
	sliceOffset := 0
	if a.Offset != nil && *a.Offset > 0 {
		sliceOffset = *a.Offset - 1
	}
	// First requested line (1-based) == sliceOffset+1 == natural end of paging
	// when it is totalLines+1 (nothing left to read) and stays valid; startLine
	// strictly past the last line means the caller has the wrong idea of the
	// file size, so surface it as an error.
	if sliceOffset > totalLines {
		return "", readOffsetPastEndError(sliceOffset+1, totalLines, a.Limit)
	}

	// Determine limit.
	limit := MaxOutputLines // 2000
	if a.Limit != nil && *a.Limit > 0 {
		limit = *a.Limit
	}

	end := min(sliceOffset+limit, totalLines)

	selected := lines[sliceOffset:end]
	contentLines := selected

	startLine := 0
	endLine := 0
	if len(contentLines) > 0 {
		startLine = sliceOffset + 1
		endLine = sliceOffset + len(contentLines)
	} else if totalLines > 0 {
		startLine = sliceOffset + 1
		endLine = sliceOffset
	}
	content := buildReadContent(readResultHeader(startLine, endLine, totalLines, 0, decoded.Encoding.Name, false), contentLines)
	if !readOutputFitsBudget(content) {
		content = truncateReadContentToBudget(contentLines, sliceOffset+1, totalLines, decoded.Encoding.Name)
	}

	if t.LSP != nil {
		if absPath, absErr := resolveToolPathAbsInDir(a.Path, t.BaseDir); absErr == nil {
			t.LSP.Start(ctx, absPath)
		}
	}

	return content, nil
}
