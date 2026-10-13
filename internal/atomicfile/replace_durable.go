package atomicfile

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ReplaceOutcome describes whether a replacement was durably committed.
type ReplaceOutcome string

const (
	NotReplaced        ReplaceOutcome = "not_replaced"
	ReplacementUnknown ReplaceOutcome = "replacement_unknown"
	Durable            ReplaceOutcome = "durable"
)

// ReplaceDurable replaces a complete file and confirms its persistence. An error
// with ReplacementUnknown never implies that the previous contents survived.
// Callers must serialize writers and confirmation reads with the same lock, and
// validate existing directories and files before passing secret data here.
func ReplaceDurable(path string, data []byte, mode os.FileMode) (ReplaceOutcome, error) {
	return replaceDurable(path, data, mode, commitDurable)
}

func replaceDurable(path string, data []byte, mode os.FileMode, commit func(string, string) (ReplaceOutcome, error)) (ReplaceOutcome, error) {
	if path == "" {
		return NotReplaced, fmt.Errorf("file path is empty")
	}
	dir := filepath.Dir(path)
	if err := makeDurableDirectory(dir); err != nil {
		return NotReplaced, fmt.Errorf("create durable file directory: %w", err)
	}
	file, err := createDurableTemp(dir, filepath.Base(path), mode)
	if err != nil {
		return NotReplaced, fmt.Errorf("create private temporary file: %w", err)
	}
	defer func() { _ = file.Close(); _ = os.Remove(file.Name()) }()
	if n, err := file.Write(data); err != nil {
		return NotReplaced, fmt.Errorf("write temporary file: %w", err)
	} else if n != len(data) {
		return NotReplaced, fmt.Errorf("write temporary file: %w", io.ErrShortWrite)
	}
	if err := file.Sync(); err != nil {
		return NotReplaced, fmt.Errorf("sync temporary file: %w", err)
	}
	if err := file.Close(); err != nil {
		return NotReplaced, fmt.Errorf("close temporary file: %w", err)
	}
	return commit(file.Name(), path)
}
