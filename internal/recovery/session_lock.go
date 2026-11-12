package recovery

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/keakon/chord/internal/jsonx"
	"github.com/keakon/chord/internal/privatefs"
)

const (
	sessionLockFile  = "session.lock"
	sessionGuardFile = "session.lock.guard"
)

// sessionLockInfo is the content written to session.lock.
type sessionLockInfo struct {
	PID        int       `json:"pid"`
	OwnerID    string    `json:"owner_id"`
	Hostname   string    `json:"hostname,omitempty"`
	AcquiredAt time.Time `json:"acquired_at"`
}

// SessionLockedError is returned when a sessionDir is already held by
// another live process.
type SessionLockedError struct {
	SessionDir string
	PID        int
	OwnerID    string
	Hostname   string
	AcquiredAt time.Time
}

func (e *SessionLockedError) Error() string {
	base := fmt.Sprintf("session %s is already open in another Chord process", filepath.Base(e.SessionDir))
	if e.PID <= 0 && e.Hostname == "" && e.AcquiredAt.IsZero() {
		return base
	}

	details := ""
	if e.PID > 0 {
		details = fmt.Sprintf("PID %d", e.PID)
	}
	if e.Hostname != "" {
		if details == "" {
			details = e.Hostname
		} else {
			details += fmt.Sprintf(" on %s", e.Hostname)
		}
	}
	if !e.AcquiredAt.IsZero() {
		if details == "" {
			details = fmt.Sprintf("since %s", e.AcquiredAt.Format("15:04:05"))
		} else {
			details += fmt.Sprintf(", since %s", e.AcquiredAt.Format("15:04:05"))
		}
	}
	if details == "" {
		return base
	}
	return fmt.Sprintf("%s (%s)", base, details)
}

// SessionLockCorruptError is returned when session.lock exists but cannot be
// parsed as valid lock metadata.
type SessionLockCorruptError struct {
	SessionDir string
	Err        error
}

func (e *SessionLockCorruptError) Error() string {
	return fmt.Sprintf("session %s has a corrupt session.lock: %v", filepath.Base(e.SessionDir), e.Err)
}

func (e *SessionLockCorruptError) Unwrap() error { return e.Err }

// SessionLock represents an acquired ownership lock on a sessionDir.
// Call Release when the session is no longer active.
type SessionLock struct {
	directory *sessionDirectoryLock
	ownerID   string
}

// AcquireSessionLock tries to acquire exclusive ownership of sessionDir,
// creating the directory when missing (fresh-session paths).
// A kernel file lock on session.lock.guard provides the actual cross-process
// exclusion, while session.lock stores readable owner metadata for errors,
// listings, and safe release.
func AcquireSessionLock(sessionDir string) (*SessionLock, error) {
	if IsDeletingSessionDirName(filepath.Base(filepath.Clean(sessionDir))) {
		return nil, fmt.Errorf("session is being deleted: %w", os.ErrNotExist)
	}
	if err := privatefs.EnsureDir(sessionDir, sessionDir); err != nil {
		return nil, fmt.Errorf("secure session directory: %w", err)
	}
	return acquireSessionLock(sessionDir)
}

// AcquireExistingSessionLock acquires exclusive ownership of a session
// directory that must already exist (resume and deletion paths). Unlike
// AcquireSessionLock it never creates the directory: a session that vanished —
// for example renamed for deletion by a concurrent cleanup — fails with an
// error wrapping fs.ErrNotExist instead of being silently recreated as an
// empty shell.
func AcquireExistingSessionLock(sessionDir string) (*SessionLock, error) {
	if IsDeletingSessionDirName(filepath.Base(filepath.Clean(sessionDir))) {
		return nil, fmt.Errorf("session is being deleted: %w", os.ErrNotExist)
	}
	return acquireSessionLock(sessionDir)
}

func acquireSessionLock(sessionDir string) (*SessionLock, error) {
	directory, err := acquireSessionDirectoryLock(sessionDir)
	if err != nil {
		return nil, err
	}
	owned := false
	defer func() {
		if !owned {
			_ = directory.Close()
		}
	}()
	ownerID := newOwnerID()
	hostname, _ := os.Hostname()
	info := sessionLockInfo{
		PID:        os.Getpid(),
		OwnerID:    ownerID,
		Hostname:   hostname,
		AcquiredAt: time.Now(),
	}
	data, err := jsonx.Marshal(info)
	if err != nil {
		return nil, fmt.Errorf("marshal session lock: %w", err)
	}
	data = append(data, '\n')

	existing, readErr := readSessionLock(directory.root)
	switch {
	case readErr == nil:
		if isProcessAlive(existing.PID) {
			return nil, &SessionLockedError{
				SessionDir: sessionDir,
				PID:        existing.PID,
				OwnerID:    existing.OwnerID,
				Hostname:   existing.Hostname,
				AcquiredAt: existing.AcquiredAt,
			}
		}
	case errors.Is(readErr, os.ErrNotExist):
		// No metadata yet — acceptable while we hold the guard lock.
	default:
		if corruptErr, ok := errors.AsType[*sessionLockParseError](readErr); ok {
			return nil, &SessionLockCorruptError{SessionDir: sessionDir, Err: corruptErr.Err}
		}
		return nil, fmt.Errorf("read session lock: %w", readErr)
	}

	if err := directory.verify(sessionDir); err != nil {
		return nil, err
	}
	if err := writeLockFile(directory.root, data); err != nil {
		return nil, err
	}

	owned = true
	return &SessionLock{directory: directory, ownerID: ownerID}, nil
}

// Release removes the session lock file only if this instance still owns it.
// It is safe to call multiple times.
func (l *SessionLock) Release() error {
	if l == nil {
		return nil
	}
	if l.directory == nil {
		return nil
	}

	var firstErr error
	existing, err := readSessionLock(l.directory.root)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			if _, ok := errors.AsType[*sessionLockParseError](err); !ok {
				firstErr = fmt.Errorf("read session lock for release: %w", err)
			}
		}
	} else if existing.OwnerID == l.ownerID {
		if err := l.directory.root.Remove(sessionLockFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			firstErr = fmt.Errorf("remove session lock: %w", err)
		}
	}

	if err := l.directory.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	l.directory = nil
	return firstErr
}

type sessionLockParseError struct{ Err error }

func (e *sessionLockParseError) Error() string { return fmt.Sprintf("parse session lock: %v", e.Err) }
func (e *sessionLockParseError) Unwrap() error { return e.Err }

// readLockFile reads and parses the lock file at path.
func readLockFile(path string) (*sessionLockInfo, error) {
	data, err := os.ReadFile(path)
	return parseSessionLock(data, err)
}

func readSessionLock(root *os.Root) (*sessionLockInfo, error) {
	data, err := root.ReadFile(sessionLockFile)
	return parseSessionLock(data, err)
}

func parseSessionLock(data []byte, err error) (*sessionLockInfo, error) {
	if err != nil {
		return nil, err
	}
	var info sessionLockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, &sessionLockParseError{Err: err}
	}
	return &info, nil
}

// writeLockFile atomically overwrites the lock file with new content.
func writeLockFile(root *os.Root, data []byte) error {
	tmp := sessionLockFile + ".tmp"
	f, err := openSessionLockFile(root, tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("open session lock tmp: %w", err)
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("write session lock tmp: %w", err)
	}
	if err := root.Rename(tmp, sessionLockFile); err != nil {
		_ = root.Remove(tmp)
		return fmt.Errorf("install session lock: %w", err)
	}
	return nil
}

func currentSessionLockedError(sessionDir, lockPath string) error {
	existing, err := readLockFile(lockPath)
	if err != nil {
		return &SessionLockedError{SessionDir: sessionDir}
	}
	return &SessionLockedError{
		SessionDir: sessionDir,
		PID:        existing.PID,
		OwnerID:    existing.OwnerID,
		Hostname:   existing.Hostname,
		AcquiredAt: existing.AcquiredAt,
	}
}

func sessionDirLockedByLiveOwner(sessionPath string) (bool, error) {
	lockPath := filepath.Join(sessionPath, sessionLockFile)

	// A status query must not create a guard or require write access. An absent
	// guard is an unlocked snapshot; still inspect any owner metadata below.
	guardFile, err := openSessionGuardForProbe(sessionPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("open session guard lock: %w", err)
	}
	if guardFile != nil {
		defer func() { _ = guardFile.Close() }()
		if err := lockSessionGuardForProbe(guardFile); err != nil {
			if isWouldBlockError(err) {
				return true, nil
			}
			return false, fmt.Errorf("probe session guard lock: %w", err)
		}
		defer func() { _ = unlockSessionGuardFile(guardFile) }()
	}

	info, err := readLockFile(lockPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if parseErr, ok := errors.AsType[*sessionLockParseError](err); ok {
			return false, &SessionLockCorruptError{SessionDir: sessionPath, Err: parseErr.Err}
		}
		return false, err
	}
	return isProcessAlive(info.PID), nil
}

// openSessionGuardForProbe confines the lookup to the session directory and
// preserves the private-file rule against symbolic links without creating or
// changing permissions on any directory or file.
func openSessionGuardForProbe(sessionPath string) (*os.File, error) {
	root, err := os.OpenRoot(sessionPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat(sessionGuardFile)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("session guard is not a regular file")
	}
	return root.Open(sessionGuardFile)
}

// SessionLockActive reports whether sessionDir is held by a live owner.
func SessionLockActive(sessionDir string) (bool, error) {
	return sessionDirLockedByLiveOwner(sessionDir)
}

func unlockAndCloseGuard(f *os.File) error {
	if f == nil {
		return nil
	}
	var firstErr error
	if err := unlockSessionGuardFile(f); err != nil && !errors.Is(err, os.ErrClosed) {
		firstErr = fmt.Errorf("release session guard lock: %w", err)
	}
	if err := f.Close(); err != nil && !errors.Is(err, os.ErrClosed) && firstErr == nil {
		firstErr = fmt.Errorf("close session guard lock: %w", err)
	}
	return firstErr
}

func isWouldBlockError(err error) bool {
	return isSessionGuardWouldBlock(err)
}

// newOwnerID generates a random hex string to uniquely identify this lock acquisition.
func newOwnerID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
