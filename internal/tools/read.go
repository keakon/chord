package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

func (ReadTool) Name() string { return "Read" }

func (ReadTool) ConcurrencyPolicy(args json.RawMessage) ConcurrencyPolicy {
	return normalizeConcurrencyPolicy("Read", fileToolConcurrencyPolicy(args, true))
}

func (ReadTool) Description() string {
	return "Read file contents with optional offset/limit paging, up to 2000 lines, formatted with line numbers (cat -n format). Very large formatted reads are truncated to fit the approximate 20k-token read budget with a tail note; use offset/limit to narrow the range. The displayed line-number gutter and separator tab are not part of the file content; copy exact text from the raw source portion only, not from the gutter. When using Lsp line/character positions, count from the raw source line only."
}

func (ReadTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Absolute or relative path to the file to read.",
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

func buildReadContent(prefixLines, numberedLines []string, footer string) string {
	var b strings.Builder
	for _, line := range prefixLines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	for _, line := range numberedLines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if footer != "" {
		b.WriteByte('\n')
		b.WriteString(footer)
	}
	return b.String()
}

func readRangeFooter(startLine, endLine, totalLines int) string {
	return fmt.Sprintf("(showing lines %d-%d of %d total)", startLine, endLine, totalLines)
}

func readBudgetTruncationFooter(startLine, endLine, totalLines int) string {
	return fmt.Sprintf(
		"(showing lines %d-%d of %d total; content truncated to fit the approximate %d-token read budget; use offset/limit or Grep to inspect more)",
		startLine,
		endLine,
		totalLines,
		MaxReadOutputTokens,
	)
}

func truncateReadContentToBudget(prefixLines, numberedLines []string, startLine, totalLines int) string {
	if len(numberedLines) == 0 {
		return buildReadContent(prefixLines, nil, "")
	}

	lo, hi := 1, len(numberedLines)
	best := ""
	for lo <= hi {
		mid := lo + (hi-lo)/2
		candidate := buildReadContent(
			prefixLines,
			numberedLines[:mid],
			readBudgetTruncationFooter(startLine, startLine+mid-1, totalLines),
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
	return buildReadContent(
		prefixLines,
		nil,
		fmt.Sprintf(
			"(content truncated to fit the approximate %d-token read budget; use offset/limit or Grep to inspect more)",
			MaxReadOutputTokens,
		),
	)
}

func (t ReadTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var a readArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Path == "" {
		return "", fmt.Errorf("path is required")
	}

	info, err := os.Stat(a.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("file not found: %s", a.Path)
		}
		if os.IsPermission(err) {
			return "", fmt.Errorf("permission denied: %s", a.Path)
		}
		return "", fmt.Errorf("reading file: %w", err)
	}
	if info.Size() > MaxReadFileBytes {
		return "", fmt.Errorf("file too large (%d bytes, max %d); use offset/limit to read a portion or Grep to search", info.Size(), MaxReadFileBytes)
	}

	decoded, err := ReadDecodedTextFile(a.Path)
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
	if len(lines) == 0 {
		return "(empty file)", nil
	}

	// Determine offset.
	offset := 0
	if a.Offset != nil {
		offset = *a.Offset
		if offset < 0 {
			offset = 0
		}
		if offset > len(lines) {
			offset = len(lines)
		}
	}

	// Determine limit.
	limit := MaxOutputLines // 2000
	if a.Limit != nil && *a.Limit > 0 {
		limit = *a.Limit
	}

	end := offset + limit
	if end > len(lines) {
		end = len(lines)
	}

	selected := lines[offset:end]
	numberedLines := make([]string, 0, len(selected))

	for i, line := range selected {
		lineNum := offset + i + 1 // 1-based
		// Truncate excessively long lines.
		if len(line) > MaxLineLength {
			line = line[:MaxLineLength] + "..."
		}
		numberedLines = append(numberedLines, fmt.Sprintf("%6d\t%s", lineNum, line))
	}

	var prefixLines []string
	if decoded.Encoding.Name != "utf-8" {
		prefixLines = append(prefixLines, fmt.Sprintf("(encoding: %s)", decoded.Encoding.Name))
	}

	footer := ""
	totalLines := len(lines)
	if end < totalLines {
		footer = readRangeFooter(offset+1, end, totalLines)
	}

	content := buildReadContent(prefixLines, numberedLines, footer)
	if content == "" {
		return "(empty file)", nil
	}
	if !readOutputFitsBudget(content) {
		content = truncateReadContentToBudget(prefixLines, numberedLines, offset+1, totalLines)
	}

	if t.LSP != nil {
		if absPath, absErr := filepath.Abs(a.Path); absErr == nil {
			t.LSP.Start(context.Background(), absPath)
		}
	}

	return content, nil
}
