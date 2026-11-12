package recovery

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/keakon/chord/internal/identity"
)

// SessionDeletingSuffix marks a session directory that won deletion and is
// pending removal. Renaming to this name is atomic: concurrent readers never
// observe a half-deleted session at the original path, listings and resume
// paths reject the tombstone by name, and a crash before the actual removal
// leaves nothing that a later sweep cannot finish.
const SessionDeletingSuffix = ".deleting"

// IsDeletingSessionDirName reports whether a directory name is a deletion
// tombstone. Listings, --continue, and explicit resume must reject these
// directories: their contents are logically deleted even while still on disk.
func IsDeletingSessionDirName(name string) bool {
	return strings.HasSuffix(name, SessionDeletingSuffix)
}

// ValidResumeSessionID accepts a single directory name for a live session.
func ValidResumeSessionID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, `/\`) &&
		!IsDeletingSessionDirName(id)
}

// DeleteSessionDir takes exclusive deletion rights, rechecks activity, and
// removes the directory by rename. Tombstones are already logically deleted
// and are removed under the same guard without applying a retention cutoff.
func DeleteSessionDir(dir string, cutoff *time.Time) error {
	directory, err := acquireSessionDirectoryLock(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	owner, err := readSessionLock(directory.root)
	if err == nil && isProcessAlive(owner.PID) {
		return currentSessionLockedError(dir, filepath.Join(dir, sessionLockFile))
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read session owner before deletion: %w", err)
	}
	tombstone := dir
	deleting := IsDeletingSessionDirName(filepath.Base(dir))
	if !deleting && cutoff != nil {
		age, hasActivity, err := sessionActivityAt(directory.root)
		if err != nil {
			return err
		}
		if !hasActivity {
			// Captured before creating a missing guard: the deletion attempt
			// itself must not extend the retention window.
			age = directory.info.ModTime()
		}
		if age.After(*cutoff) {
			return fmt.Errorf("session became active during cleanup")
		}
	}
	if err := directory.verify(dir); err != nil {
		return err
	}
	if !deleting {
		tombstone += SessionDeletingSuffix
		if err := os.Rename(dir, tombstone); err != nil {
			return fmt.Errorf("rename session dir for deletion: %w", err)
		}
	}
	if err := os.RemoveAll(tombstone); err != nil {
		return fmt.Errorf("remove session dir: %w", err)
	}
	forgetSessionDirCaches(dir)
	if deleting {
		forgetSessionDirCaches(strings.TrimSuffix(dir, SessionDeletingSuffix))
	}
	return nil
}

// forgetSessionDirCaches evicts in-memory state derived from a session
// directory that no longer exists.
func forgetSessionDirCaches(sessionDir string) {
	messageCountCache.Delete(filepath.Join(sessionDir, identity.MainSessionLogFilename))
}
