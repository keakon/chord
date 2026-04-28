package tools

import (
	"context"
	"encoding/json"
	"fmt"
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

func (t WriteTool) Name() string { return "Write" }

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
				"description": "Absolute or relative path to the file to write.",
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

func (t WriteTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a writeArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Path == "" {
		return "", fmt.Errorf("path is required")
	}

	content, err := decodeToolStringArg(a.Content)
	if err != nil {
		return "", fmt.Errorf("content encoding unsupported: %w", err)
	}

	dir := filepath.Dir(a.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("creating directories: %w", err)
	}

	data := []byte(content)
	invalidatePathCache(a.Path)
	if err := os.WriteFile(a.Path, data, 0644); err != nil {
		return "", fmt.Errorf("writing file: %w", err)
	}
	warmDecodedFileCacheAsync(a.Path, data, decodedText{Text: content, Encoding: utf8Encoding})

	out := fmt.Sprintf("Successfully wrote %d bytes", len(data))
	if t.LSP != nil {
		absPath, _ := filepath.Abs(a.Path)
		t.LSP.MarkTouched(absPath)
		out = t.LSP.AfterWriteToolResult(ctx, absPath, content, out, true)
	}
	return out, nil
}
