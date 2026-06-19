package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ReadTool reads file contents with optional offset/limit paging.
type ReadTool struct {
	LSP lspStarter // nil when LSP not configured
}

// lspStarter is the minimal interface ReadTool needs from lsp.Manager.
type lspStarter interface {
	Start(ctx context.Context, path string)
}

type readArgs struct {
	Path   string `json:"path"`
	Offset *int   `json:"offset,omitempty"` // 0-based line offset
	Limit  *int   `json:"limit,omitempty"`  // number of lines; defaults to 2000
}

func (ReadTool) Name() string { return NameRead }

func (ReadTool) ConcurrencyPolicy(args json.RawMessage) ConcurrencyPolicy {
	return normalizeConcurrencyPolicy(NameRead, fileToolConcurrencyPolicy(args, true))
}

func (ReadTool) Description() string {
	return "Read file contents with optional offset/limit paging, up to 2000 lines. Successful output starts with one READ_RESULT metadata line of the form `READ_RESULT lines=a-b total=N` (1-based inclusive returned range and total file line count), or `READ_RESULT lines=none total=N` when no line was returned (empty file, or an offset exactly at end of file which is the normal end of paging; an offset strictly past the last line is an error); everything after that first line is exact file text without line-number gutters or extra indentation, so copy only the text after READ_RESULT into edit hunks. A read that simply did not reach the end of the file is not truncation. Only when the tool itself drops requested lines to fit the approximate 20k-token read budget does the header add `truncated=budget requested_lines=a-d`, where a-d is the range you originally requested; compare it with the returned lines=a-b and page further with offset/limit. A later context-reduction pass may instead mark an aged read output with `truncated=stale`, keeping only its leading lines (lines=a-b then covers just the kept head) to save tokens; re-read with offset/limit if you need the rest. The header omits encoding for UTF-8 files and reports it only for other encodings. For edit, include a few unchanged source lines around the intended change; if you need more surrounding context, read the intended nearby block before patching. read output normalizes line endings to LF; edit preserves the file's existing line-ending style when writing."
}

func (ReadTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Absolute or relative path to an existing file to read. Supports ~ for the current user's home directory. Do not guess paths; verify uncertain paths before reading.",
			},
			"offset": map[string]any{
				"type":        "integer",
				"description": "0-based line offset to start reading from.",
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
// requested lines at call time to fit the output budget; stale means a later
// context-reduction pass trimmed an aged read output to save tokens.
const (
	ReadTruncatedBudget = "budget"
	ReadTruncatedStale  = "stale"
)

// FormatReadResultHeader builds the single READ_RESULT metadata line shared by
// the read tool and the context-reduction summary so both truncation paths
// (truncated=budget and truncated=stale) render identically and differ only in
// the truncation reason.
//
// linesField is the already-formatted returned range: "a-b", a multi-segment
// "a-b,c-d" when only head/tail lines survive, or "none" when no line was
// returned. truncatedKind is "", ReadTruncatedBudget or ReadTruncatedStale.
// requestedLines (e.g. "a-d") is appended only for budget truncation. encoding
// is appended only for non-UTF-8 files.
func FormatReadResultHeader(linesField string, totalLines int, truncatedKind, requestedLines, encoding string) string {
	header := fmt.Sprintf("READ_RESULT lines=%s total=%d", linesField, totalLines)
	switch truncatedKind {
	case ReadTruncatedBudget:
		header += " truncated=" + ReadTruncatedBudget
		if requestedLines != "" {
			header += " requested_lines=" + requestedLines
		}
	case ReadTruncatedStale:
		header += " truncated=" + ReadTruncatedStale
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
	resolvedPath, info, err := resolveExistingToolPath(a.Path, PathTargetRegularFile, "read")
	if err != nil {
		if strings.Contains(err.Error(), "path not found") {
			return "", fmt.Errorf("file not found: %s", a.Path)
		}
		return "", err
	}

	if info.Size() > MaxReadFileBytes {
		return "", fmt.Errorf("file too large (%d bytes, max %d); use offset/limit to read a portion or grep to search", info.Size(), MaxReadFileBytes)
	}

	decoded, err := ReadDecodedTextFile(resolvedPath)
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
	lines := splitReadToolLines(decoded.Text)
	totalLines := len(lines)

	// Determine offset.
	offset := 0
	if a.Offset != nil {
		offset = max(*a.Offset, 0)
		// offset == totalLines is the natural end of paging (nothing left to
		// read) and stays valid; offset strictly past the last line means the
		// caller has the wrong idea of the file size, so surface it as an error.
		if offset > totalLines {
			return "", fmt.Errorf("offset %d exceeds file length (%d lines)", offset, totalLines)
		}
	}

	// Determine limit.
	limit := MaxOutputLines // 2000
	if a.Limit != nil && *a.Limit > 0 {
		limit = *a.Limit
	}

	end := min(offset+limit, totalLines)

	selected := lines[offset:end]
	contentLines := make([]string, 0, len(selected))

	for _, line := range selected {
		// Truncate excessively long lines.
		if len(line) > MaxLineLength {
			line = line[:MaxLineLength] + "..."
		}
		contentLines = append(contentLines, line)
	}

	startLine := 0
	endLine := 0
	if len(contentLines) > 0 {
		startLine = offset + 1
		endLine = offset + len(contentLines)
	} else if totalLines > 0 {
		startLine = offset + 1
		endLine = offset
	}
	content := buildReadContent(readResultHeader(startLine, endLine, totalLines, 0, decoded.Encoding.Name, false), contentLines)
	if !readOutputFitsBudget(content) {
		content = truncateReadContentToBudget(contentLines, offset+1, totalLines, decoded.Encoding.Name)
	}

	if t.LSP != nil {
		if absPath, absErr := resolveToolPathAbs(a.Path); absErr == nil {
			t.LSP.Start(ctx, absPath)
		}
	}

	return content, nil
}
