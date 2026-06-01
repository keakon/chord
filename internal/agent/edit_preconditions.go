package agent

import (
	"errors"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/filelock"
	"github.com/keakon/chord/internal/tools"
)

func editReadPreconditionError(path string) error {
	return &filelock.UnreadFileError{
		Path: path,
		Message: fmt.Sprintf(
			"file %s has not been observed in this conversation; use Read or a system-resolved @file mention before Edit, then retry with a small unique patch hunk",
			path,
		),
	}
}

func ensureTrackedEditPreconditions(track *filelock.FileTracker, agentID, path, toolName string) error {
	if toolName != tools.NameEdit {
		return nil
	}
	path = strings.TrimSpace(path)
	if path == "" || track == nil || strings.TrimSpace(agentID) == "" {
		return nil
	}
	if track.HasRead(path, agentID) {
		return nil
	}
	return editReadPreconditionError(path)
}

func wrapTrackedWriteError(err error) error {
	if err == nil {
		return nil
	}
	var unread *filelock.UnreadFileError
	if errors.As(err, &unread) {
		return err
	}
	return fmt.Errorf("file conflict: %w", err)
}
