package worktree

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// stateFileLock holds a held cross-process lock for one state directory lock
// file. Close releases it.
type stateFileLock struct {
	file *os.File
}

// ErrCheckoutGone reports that the checkout a caller wanted to claim is no
// longer there: a removal held the mutation lock first and deleted it.
var ErrCheckoutGone = errors.New("checkout is gone")

// WithRepoIndexLock acquires a cross-process lock on the index file,
// invokes fn with the loaded index (or a fresh empty one when missing),
// saves the result, and releases the lock. Use for any read-modify-write
// path that races with other chord processes.
func WithRepoIndexLock(stateDir, repoID string, fn func(*RepoIndex) error) error {
	path := repoIndexPath(stateDir, repoID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir repo index dir: %w", err)
	}
	lock, err := lockStateFile(path + ".lock")
	if err != nil {
		return err
	}
	defer func() {
		_ = lock.Close()
	}()
	idx, err := LoadRepoIndex(stateDir, repoID)
	if err != nil {
		return err
	}
	if idx == nil {
		idx = &RepoIndex{RepoID: repoID}
	}
	if err := fn(idx); err != nil {
		return err
	}
	return SaveRepoIndex(stateDir, idx)
}

// WithCheckoutLock holds the cross-process mutation lock for one checkout and
// runs fn while it is held. Removal of a checkout and every write of "this
// session works in that checkout" take the same lock, which is what makes the
// pair safe: a removal that runs first has the directory gone before a writer
// gets the lock, and a writer that runs first has its record on disk before the
// removal scans for holders.
//
// The lock covers one mutation, not a session's lifetime: a process releases it
// when fn returns, and the OS releases it if the process dies, so no stale-lock
// handling is needed.
func WithCheckoutLock(stateDir, checkoutPath string, fn func() error) error {
	lockPath := checkoutLockPath(stateDir, checkoutPath)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return fmt.Errorf("mkdir checkout lock dir: %w", err)
	}
	lock, err := lockStateFile(lockPath)
	if err != nil {
		return err
	}
	defer func() {
		_ = lock.Close()
	}()
	return fn()
}

// ClaimCheckout runs fn under the checkout mutation lock, after confirming the
// checkout is still a directory. It returns ErrCheckoutGone instead of running
// fn when the checkout is gone, so a writer never records a path a removal
// already deleted.
func ClaimCheckout(stateDir, checkoutPath string, fn func() error) error {
	return WithCheckoutLock(stateDir, checkoutPath, func() error {
		if !CheckoutPresent(checkoutPath) {
			return ErrCheckoutGone
		}
		return fn()
	})
}

// CheckoutPresent reports whether path is still a directory. Callers probe it
// while holding the checkout mutation lock.
func CheckoutPresent(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// checkoutLockPath is the lock file for one checkout. The key is the hash of
// the cleaned path: the lock must be derivable from the path alone, so a
// removal and a writer agree on it without a registry lookup that could itself
// race the removal.
func checkoutLockPath(stateDir, checkoutPath string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(strings.TrimSpace(checkoutPath))))
	return filepath.Join(stateDir, "locks", "checkout-"+hex.EncodeToString(sum[:])+".lock")
}
