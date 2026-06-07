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
	return "Read file contents with optional offset/limit paging, up to 2000 lines. Successful output starts with one READ_RESULT metadata line; everything after that first line is exact file text without line-number gutters or extra indentation, so copy only the text after READ_RESULT into edit hunks. Very large reads are truncated to fit the approximate 20k-token read budget and report that in the READ_RESULT line; use offset/limit to narrow the range. Before edit, the file must have been observed via read or a system-resolved @file mention; if the mention may be truncated or you need more surrounding context, read the intended nearby block before patching. For edit, include a few unchanged source lines around the intended change. read output normalizes line endings to LF; edit preserves the file's existing line-ending style when writing."
}

func (ReadTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Absolute or relative path to the file to read. Supports ~ for the current user's home directory.",
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

func readResultHeader(displayPath string, startLine, endLine, contentLineCount, totalLines int, truncated bool, encoding string, budgetTruncated bool) string {
	if encoding == "" {
		encoding = "utf-8"
	}
	header := fmt.Sprintf(
		"READ_RESULT path=%s lines=%d-%d/%d content_lines=%d truncated=%t encoding=%s",
		quoteReadHeaderValue(displayPath),
		startLine,
		endLine,
		totalLines,
		contentLineCount,
		truncated,
		quoteReadHeaderValue(encoding),
	)
	if budgetTruncated {
		header += fmt.Sprintf(" budget_truncated=true token_budget=%d", MaxReadOutputTokens)
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

func truncateReadContentToBudget(displayPath string, contentLines []string, startLine, totalLines int, encoding string) string {
	if len(contentLines) == 0 {
		line := 0
		if totalLines > 0 {
			line = startLine
		}
		return buildReadContent(readResultHeader(displayPath, line, max(line-1, 0), 0, totalLines, startLine <= totalLines, encoding, false), nil)
	}

	lo, hi := 1, len(contentLines)
	best := ""
	for lo <= hi {
		mid := lo + (hi-lo)/2
		header := readResultHeader(displayPath, startLine, startLine+mid-1, mid, totalLines, true, encoding, true)
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
	header := readResultHeader(displayPath, startLine, max(startLine-1, 0), 0, totalLines, true, encoding, true)
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
		if offset > totalLines {
			offset = totalLines
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
	truncated := end < totalLines
	content := buildReadContent(readResultHeader(a.Path, startLine, endLine, len(contentLines), totalLines, truncated, decoded.Encoding.Name, false), contentLines)
	if !readOutputFitsBudget(content) {
		content = truncateReadContentToBudget(a.Path, contentLines, offset+1, totalLines, decoded.Encoding.Name)
	}

	if t.LSP != nil {
		if absPath, absErr := resolveToolPathAbs(a.Path); absErr == nil {
			t.LSP.Start(ctx, absPath)
		}
	}

	return content, nil
}
