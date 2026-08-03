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
	LSP     *lsp.Manager // nil when LSP not configured
	BaseDir string       // session working directory for relative paths; empty keeps process cwd behavior
}

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (t WriteTool) Name() string { return NameWrite }

func (t WriteTool) ConcurrencyPolicy(args json.RawMessage) ConcurrencyPolicy {
	return normalizeConcurrencyPolicy(NameWrite, fileToolConcurrencyPolicyInDir(args, false, t.BaseDir))
}

func (t WriteTool) Description() string {
	return "Write the full contents of a file, creating parent directories as needed. This replaces the entire file rather than appending to it. Overwriting an existing file requires that you know its current version in full: a successful complete read, or the file's current content being your own previous whole-file write; partial reads do not authorize whole-file replacement. Prefer edit for localized changes to existing files. If the path should still exist afterward with new full contents, use write directly rather than deleting it first. Empty content truncates the file to zero bytes but does not delete it; use delete only when the file should no longer exist."
}

func (t WriteTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Absolute or relative path to the file to write. Relative paths resolve from the session working directory. Supports ~ for the current user's home directory.",
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
	return writeFileNoFollowMode(path, data, perm, false)
}

func writeFileNoFollowExactMode(path string, data []byte, mode os.FileMode) error {
	return writeFileNoFollowMode(path, data, mode, true)
}

func writeFileNoFollowMode(path string, data []byte, mode os.FileMode, exact bool) error {
	f, err := openFileNoFollow(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	return writeOpenedFile(f, data, mode, exact)
}

// writeNewFileNoFollowMode reports whether it created the path before a later
// write/close error. Callers use that signal to remove only their own partial
// file, never a target that appeared after planning.
func writeNewFileNoFollowMode(path string, data []byte, mode os.FileMode, exact bool) (bool, error) {
	f, err := openFileNoFollow(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return false, err
	}
	return true, writeOpenedFile(f, data, mode, exact)
}

func writeOpenedFile(f *os.File, data []byte, mode os.FileMode, exact bool) error {
	n, writeErr := f.Write(data)
	if writeErr != nil {
		_ = f.Close()
		return writeErr
	}
	if n != len(data) {
		_ = f.Close()
		return io.ErrShortWrite
	}
	if exact {
		if err := f.Chmod(mode); err != nil {
			_ = f.Close()
			return err
		}
	}
	return f.Close()
}

func (t WriteTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a writeArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	resolvedPath, err := resolveToolPathInDir(a.Path, t.BaseDir)
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
	reportToolProgress(ctx, ToolProgressSnapshot{
		Text: fmt.Sprintf("writing %d %s", len(data), byteLabel),
	})
	_, statErr := os.Lstat(resolvedPath)
	existed := statErr == nil
	invalidatePathCache(resolvedPath)
	if err := writeFileNoFollow(resolvedPath, data, 0644); err != nil {
		return "", fmt.Errorf("writing file: %w", err)
	}
	warmDecodedFileCache(resolvedPath, data, decodedText{Text: content, Encoding: utf8Encoding})

	out := fmt.Sprintf("Successfully wrote %d %s, %d %s", lineCount, lineLabel, len(data), byteLabel)
	if t.LSP != nil {
		absPath, absErr := resolveToolPathAbsInDir(a.Path, t.BaseDir)
		if absErr == nil {
			t.LSP.MarkTouched(absPath)
			changeType := lsp.WatchedFileCreated
			if existed {
				changeType = lsp.WatchedFileChanged
			}
			out = t.LSP.AfterFileWriteToolResult(ctx, absPath, content, out, true, changeType)
		}
	}
	return out, nil
}
