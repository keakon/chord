package agent

import (
	"errors"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/filelock"
	"github.com/keakon/chord/internal/tools"
)

func applyPatchReadPreconditionError(path string) error {
	return &filelock.UnreadFileError{
		Path: path,
		Message: fmt.Sprintf(
			"file %s has not been observed in this conversation; use Read or a system-resolved @file mention before ApplyPatch, then retry with a small unique patch hunk",
			path,
		),
	}
}

func ensureTrackedApplyPatchPreconditions(track *filelock.FileTracker, agentID, path, toolName string) error {
	if toolName != tools.NameApplyPatch {
		return nil
	}
	path = strings.TrimSpace(path)
	if path == "" || track == nil || strings.TrimSpace(agentID) == "" {
		return nil
	}
	if track.HasRead(path, agentID) {
		return nil
	}
	return applyPatchReadPreconditionError(path)
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
