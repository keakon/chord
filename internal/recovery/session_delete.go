package recovery

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrDeleteCurrentSession = errors.New("cannot delete current session")

// DeleteSessionByID removes a non-current session directory under sessionsDir.
// It validates the session ID stays within sessionsDir and refuses to delete
// the current active session or a session held by another live process.
// Removal goes through the shared deletion protocol: the lock is acquired
// before the directory is renamed, so a concurrent resume either fails its own
// lock acquisition or observes a directory that no longer exists.
func DeleteSessionByID(sessionsDir, currentSessionDir, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("delete session: empty session ID")
	}
	if !ValidResumeSessionID(sessionID) {
		return fmt.Errorf("delete session %q: invalid session ID", sessionID)
	}
	sessionsDir = filepath.Clean(sessionsDir)
	targetDir := filepath.Clean(filepath.Join(sessionsDir, sessionID))
	rel, err := filepath.Rel(sessionsDir, targetDir)
	if err != nil {
		return fmt.Errorf("delete session %q: resolve path: %w", sessionID, err)
	}
	if rel == "." || rel == "" || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return fmt.Errorf("delete session %q: invalid session ID", sessionID)
	}
	if currentSessionDir != "" && filepath.Clean(targetDir) == filepath.Clean(currentSessionDir) {
		return fmt.Errorf("delete session %q: %w", sessionID, ErrDeleteCurrentSession)
	}
	info, err := os.Stat(targetDir)
	if err != nil {
		return fmt.Errorf("delete session %q: stat session dir: %w", sessionID, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("delete session %q: session path is not a directory", sessionID)
	}
	if err := DeleteSessionDir(targetDir, nil); err != nil {
		return fmt.Errorf("delete session %q: %w", sessionID, err)
	}
	return nil
}
