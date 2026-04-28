package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/lsp"
)

// EditTool performs exact string replacements in files.
// If LSP is set, notifies LSP of the change after a successful edit.
type EditTool struct {
	LSP *lsp.Manager // nil when LSP not configured
}

type editArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

func (t EditTool) Name() string { return "Edit" }

func (EditTool) ConcurrencyPolicy(args json.RawMessage) ConcurrencyPolicy {
	return normalizeConcurrencyPolicy("Edit", fileToolConcurrencyPolicy(args, false))
}

func (t EditTool) Description() string {
	return "Perform exact string replacement in an existing file. Prefer this tool for localized changes instead of rewriting the whole file with Write. old_string must match the file's raw text exactly, including indentation, tabs, spaces, and newlines. If the text came from Read output, do not include the displayed line-number gutter or separator tab. Replaces one occurrence by default; set replace_all to replace every occurrence."
}

func (t EditTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Absolute or relative path to the file to edit.",
			},
			"old_string": map[string]any{
				"type":        "string",
				"description": "The exact raw text to find in the file. Match indentation, tabs, spaces, and newlines exactly; if the text came from Read output, do not include the displayed line-number gutter or separator tab.",
			},
			"new_string": map[string]any{
				"type":        "string",
				"description": "The text to replace old_string with. Ensure new_string preserves required indentation/newlines when needed.",
			},
			"replace_all": map[string]any{
				"type":        "boolean",
				"description": "If true, replace all occurrences of old_string. Default is false.",
			},
		},
		"required":             []string{"path", "old_string", "new_string"},
		"additionalProperties": false,
	}
}

func (t EditTool) IsReadOnly() bool { return false }

func (t EditTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a editArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	if a.OldString == "" {
		return "", fmt.Errorf("old_string is required")
	}

	decodedOld, err := decodeToolStringArg(a.OldString)
	if err != nil {
		return "", fmt.Errorf("old_string encoding unsupported: %w", err)
	}
	decodedNew, err := decodeToolStringArg(a.NewString)
	if err != nil {
		return "", fmt.Errorf("new_string encoding unsupported: %w", err)
	}

	// Read the file.
	decodedFile, data, err := ReadAndDecodeTextFile(a.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("file not found: %s", a.Path)
		}
		if errors.Is(err, ErrBinaryFile) {
			return "", fmt.Errorf("cannot edit binary file: %s", a.Path)
		}
		return "", fmt.Errorf("reading file: %w", err)
	}
	content := decodedFile.Text

	// Check for identical old/new.
	if decodedOld == decodedNew {
		return "", fmt.Errorf("oldString and newString are identical, no change needed")
	}

	// Count occurrences.
	count := strings.Count(content, decodedOld)
	if count == 0 {
		hint := buildEditOldStringNotFoundHint(content, decodedOld)
		if hint != "" {
			return "", fmt.Errorf("oldString not found in file. %s", hint)
		}
		return "", fmt.Errorf("oldString not found in file")
	}
	if count > 1 && !a.ReplaceAll {
		return "", fmt.Errorf("oldString found %d times, provide more context or set replaceAll", count)
	}

	// Perform replacement.
	var newContent string
	if a.ReplaceAll {
		newContent = strings.ReplaceAll(content, decodedOld, decodedNew)
	} else {
		newContent = strings.Replace(content, decodedOld, decodedNew, 1)
	}

	encodedBytes, err := encodeString(newContent, decodedFile.Encoding)
	if err != nil {
		return "", fmt.Errorf("edited text cannot be encoded back to %s: %w", decodedFile.Encoding.Name, err)
	}

	// Write back.
	invalidatePathCache(a.Path)
	if err := os.WriteFile(a.Path, encodedBytes, 0644); err != nil {
		return "", fmt.Errorf("writing file: %w", err)
	}
	warmDecodedFileCacheAsync(a.Path, encodedBytes, decodedText{Text: newContent, Encoding: decodedFile.Encoding})

	oldBytes := len(data)
	newBytes := len(encodedBytes)
	encSuffix := ""
	if decodedFile.Encoding.Name != "utf-8" {
		encSuffix = fmt.Sprintf(", encoding=%s", decodedFile.Encoding.Name)
	}
	var out string
	if a.ReplaceAll && count > 1 {
		out = fmt.Sprintf("Replaced %d occurrences (%d bytes -> %d bytes)%s", count, oldBytes, newBytes, encSuffix)
	} else {
		out = fmt.Sprintf("Replaced 1 occurrence (%d bytes -> %d bytes)%s", oldBytes, newBytes, encSuffix)
	}
	if t.LSP != nil {
		absPath, _ := filepath.Abs(a.Path)
		t.LSP.MarkTouched(absPath)
		out = t.LSP.AfterWriteToolResult(ctx, absPath, newContent, out, false)
	}
	return out, nil
}
