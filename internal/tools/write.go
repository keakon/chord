package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/keakon/chord/internal/lsp"
)

// WriteTool writes content to a file, creating parent directories as needed.
// If LSP is set, notifies LSP of the change after a successful write.
type WriteTool struct {
	LSP *lsp.Manager // nil when LSP not configured
}

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (t WriteTool) Name() string { return NameWrite }

func (WriteTool) ConcurrencyPolicy(args json.RawMessage) ConcurrencyPolicy {
	return normalizeConcurrencyPolicy("Write", fileToolConcurrencyPolicy(args, false))
}

func (t WriteTool) Description() string {
	return "Write the full contents of a file, creating parent directories as needed. This replaces the entire file rather than appending to it. Prefer Edit for localized changes to existing files. If the path should still exist afterward with new full contents, use Write directly rather than deleting it first. Empty content truncates the file to zero bytes but does not delete it; use Delete only when the file should no longer exist."
}

func (t WriteTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Absolute or relative path to the file to write. Supports ~ for the current user's home directory.",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "The full file content to write. This replaces the entire file.",
			},
		},
		"required":             []string{"path", "content"},
		"additionalProperties": false,
	}
}

func (t WriteTool) IsReadOnly() bool { return false }

func writtenLineCount(content string) int {
	if content == "" {
		return 0
	}
	lineCount := 1
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' && i < len(content)-1 {
			lineCount++
		}
	}
	return lineCount
}

func writeFileNoFollow(path string, data []byte, perm os.FileMode) error {
	f, err := openFileNoFollow(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	n, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return closeErr
}

func (t WriteTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a writeArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	resolvedPath, err := resolveToolPath(a.Path)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	if isBlockedDevicePath(resolvedPath) {
		return "", fmt.Errorf("cannot write blocked device path: %s", a.Path)
	}

	content, err := decodeToolStringArg(a.Content)
	if err != nil {
		return "", fmt.Errorf("content encoding unsupported: %w", err)
	}

	dir := filepath.Dir(resolvedPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("creating directories: %w", err)
	}

	data := []byte(content)
	lineCount := writtenLineCount(content)
	lineLabel := "lines"
	if lineCount == 1 {
		lineLabel = "line"
	}
	byteLabel := "bytes"
	if len(data) == 1 {
		byteLabel = "byte"
	}
	invalidatePathCache(resolvedPath)
	if err := writeFileNoFollow(resolvedPath, data, 0644); err != nil {
		return "", fmt.Errorf("writing file: %w", err)
	}
	warmDecodedFileCacheAsync(resolvedPath, data, decodedText{Text: content, Encoding: utf8Encoding})

	out := fmt.Sprintf("Successfully wrote %d %s, %d %s", lineCount, lineLabel, len(data), byteLabel)
	if t.LSP != nil {
		absPath, absErr := resolveToolPathAbs(a.Path)
		if absErr == nil {
			t.LSP.MarkTouched(absPath)
			out = t.LSP.AfterWriteToolResult(ctx, absPath, content, out, true)
		}
	}
	return out, nil
}
