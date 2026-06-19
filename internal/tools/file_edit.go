package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/keakon/chord/internal/lsp"
)

type fileEditRead struct {
	Decoded decodedText
	Bytes   []byte
	Info    os.FileInfo
}

func readFileForEdit(path, displayPath, binaryAction string) (fileEditRead, error) {
	decodedFile, data, err := ReadAndDecodeTextFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fileEditRead{}, fmt.Errorf("file not found: %s", displayPath)
		}
		if errors.Is(err, ErrBinaryFile) {
			return fileEditRead{}, fmt.Errorf("cannot %s binary file: %s", binaryAction, displayPath)
		}
		return fileEditRead{}, fmt.Errorf("reading file: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fileEditRead{}, fmt.Errorf("accessing path: %w", err)
	}
	if err := ensureRegularFilePath(displayPath, info); err != nil {
		return fileEditRead{}, err
	}
	return fileEditRead{Decoded: decodedFile, Bytes: data, Info: info}, nil
}

func writeEncodedEditedFile(ctx context.Context, path string, encodedBytes []byte, decodedFile decodedText, newContent, progressText string, lspMgr *lsp.Manager, baseResult string) (string, error) {
	if progressText != "" {
		reportToolProgress(ctx, ToolProgressSnapshot{Text: progressText})
	}
	invalidatePathCache(path)
	if err := writeFileNoFollow(path, encodedBytes, 0644); err != nil {
		return "", fmt.Errorf("writing file: %w", err)
	}
	warmDecodedFileCache(path, encodedBytes, decodedText{Text: newContent, Encoding: decodedFile.Encoding})
	out := baseResult
	if lspMgr != nil {
		absPath, absErr := filepath.Abs(path)
		if absErr == nil {
			lspMgr.MarkTouched(absPath)
			out = lspMgr.AfterWriteToolResult(ctx, absPath, newContent, out, false)
		}
	}
	return out, nil
}
