package recovery

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
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
	path      string   // path to session.lock
	guardPath string   // path to session.lock.guard
	guardFile *os.File // held open for the lifetime of the ownership lock
	ownerID   string
}

var sessionGuardProbeMu sync.Mutex

// AcquireSessionLock tries to acquire exclusive ownership of sessionDir.
// A kernel file lock on session.lock.guard provides the actual cross-process
// exclusion, while session.lock stores readable owner metadata for errors,
// listings, and safe release.
func AcquireSessionLock(sessionDir string) (*SessionLock, error) {
	lockPath := filepath.Join(sessionDir, sessionLockFile)
	guardPath := filepath.Join(sessionDir, sessionGuardFile)
	ownerID := newOwnerID()
	hostname, _ := os.Hostname()
	info := sessionLockInfo{
		PID:        os.Getpid(),
		OwnerID:    ownerID,
		Hostname:   hostname,
		AcquiredAt: time.Now(),
	}
	data, err := json.Marshal(info)
	if err != nil {
		return nil, fmt.Errorf("marshal session lock: %w", err)
	}
	data = append(data, '\n')

	guardFile, err := os.OpenFile(guardPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open session guard lock: %w", err)
	}
	if err := syscall.Flock(int(guardFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = guardFile.Close()
		if isWouldBlockError(err) {
			return nil, currentSessionLockedError(sessionDir, lockPath)
		}
		return nil, fmt.Errorf("acquire session guard lock: %w", err)
	}

	guardHeld := true
	defer func() {
		if guardHeld {
			_ = unlockAndCloseGuard(guardFile)
		}
	}()

	existing, readErr := readLockFile(lockPath)
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
		var corruptErr *sessionLockParseError
		if errors.As(readErr, &corruptErr) {
			return nil, &SessionLockCorruptError{SessionDir: sessionDir, Err: corruptErr.Err}
		}
		return nil, fmt.Errorf("read session lock: %w", readErr)
	}

	if err := writeLockFile(lockPath, data); err != nil {
		return nil, err
	}

	guardHeld = false
	return &SessionLock{path: lockPath, guardPath: guardPath, guardFile: guardFile, ownerID: ownerID}, nil
}

// Release removes the session lock file only if this instance still owns it.
// It is safe to call multiple times.
func (l *SessionLock) Release() error {
	if l == nil {
		return nil
	}
	if l.guardFile == nil {
		return nil
	}

	var firstErr error
	existing, err := readLockFile(l.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			var corruptErr *sessionLockParseError
			if !errors.As(err, &corruptErr) {
				firstErr = fmt.Errorf("read session lock for release: %w", err)
			}
		}
	} else if existing.OwnerID == l.ownerID {
		if err := os.Remove(l.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			firstErr = fmt.Errorf("remove session lock: %w", err)
		}
	}

	if err := unlockAndCloseGuard(l.guardFile); err != nil && firstErr == nil {
		firstErr = err
	}
	l.guardFile = nil
	return firstErr
}

type sessionLockParseError struct{ Err error }

func (e *sessionLockParseError) Error() string { return fmt.Sprintf("parse session lock: %v", e.Err) }
func (e *sessionLockParseError) Unwrap() error { return e.Err }

// readLockFile reads and parses the lock file at path.
func readLockFile(path string) (*sessionLockInfo, error) {
	data, err := os.ReadFile(path)
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
func writeLockFile(lockPath string, data []byte) error {
	tmp := lockPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write session lock tmp: %w", err)
	}
	if err := os.Rename(tmp, lockPath); err != nil {
		_ = os.Remove(tmp)
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
	guardPath := filepath.Join(sessionPath, sessionGuardFile)
	lockPath := filepath.Join(sessionPath, sessionLockFile)

	// Serialize local probe attempts so we don't deadlock ourselves by trying to
	// lock the same guard file from multiple goroutines in one process.
	sessionGuardProbeMu.Lock()
	defer sessionGuardProbeMu.Unlock()

	guardFile, err := os.OpenFile(guardPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, fmt.Errorf("open session guard lock: %w", err)
	}
	defer func() {
		_ = guardFile.Close()
	}()

	if err := syscall.Flock(int(guardFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if isWouldBlockError(err) {
			return true, nil
		}
		return false, fmt.Errorf("probe session guard lock: %w", err)
	}
	defer func() {
		_ = syscall.Flock(int(guardFile.Fd()), syscall.LOCK_UN)
	}()

	info, err := readLockFile(lockPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		var parseErr *sessionLockParseError
		if errors.As(err, &parseErr) {
			return false, &SessionLockCorruptError{SessionDir: sessionPath, Err: parseErr.Err}
		}
		return false, err
	}
	return isProcessAlive(info.PID), nil
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
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil && !errors.Is(err, syscall.EBADF) {
		firstErr = fmt.Errorf("release session guard lock: %w", err)
	}
	if err := f.Close(); err != nil && !errors.Is(err, os.ErrClosed) && firstErr == nil {
		firstErr = fmt.Errorf("close session guard lock: %w", err)
	}
	return firstErr
}

func isWouldBlockError(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}

// newOwnerID generates a random hex string to uniquely identify this lock acquisition.
func newOwnerID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// isProcessAlive returns true if a process with the given PID exists and
// accepts signals (i.e. is not a zombie waiting for wait()).
func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}
