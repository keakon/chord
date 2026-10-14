package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/keakon/chord/internal/lsp"
	"github.com/keakon/chord/internal/textfile"
)

type fileEditRead struct {
	Decoded textfile.Decoded
	Bytes   []byte
	Info    os.FileInfo
}

func readFileForEdit(path, displayPath, baseDir, binaryAction string) (fileEditRead, error) {
	decodedFile, data, err := textfile.ReadWithBytes(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fileEditRead{}, fileNotFoundErrorWithPathSuggestionsInDir(displayPath, baseDir, PathTargetRegularFile)
		}
		if errors.Is(err, textfile.ErrBinaryFile) {
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

func writeEncodedEditedFile(ctx context.Context, path string, encodedBytes []byte, decodedFile textfile.Decoded, newContent, progressText string, lspMgr *lsp.Manager, baseResult, displayBaseDir string) (string, error) {
	if progressText != "" {
		reportToolProgress(ctx, ToolProgressSnapshot{Text: progressText})
	}
	textfile.Invalidate(path)
	if err := writeFileNoFollow(path, encodedBytes, 0644); err != nil {
		return "", fmt.Errorf("writing file: %w", err)
	}
	textfile.Warm(path, encodedBytes, textfile.Decoded{Text: newContent, Encoding: decodedFile.Encoding})
	out := baseResult
	if lspMgr != nil {
		absPath, absErr := filepath.Abs(path)
		if absErr == nil {
			lspMgr.MarkTouched(absPath)
			out = lspMgr.AfterFileWriteToolResult(ctx, absPath, newContent, out, false, lsp.WatchedFileChanged, displayBaseDir)
		}
	}
	return out, nil
}
