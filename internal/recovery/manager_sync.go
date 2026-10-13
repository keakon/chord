package recovery

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/keakon/chord/internal/privatefs"
)

// Sync seals all manager-owned logs, journals and attachments. Producers using
// other writers must be stopped before calling it; their paths are synced too.
func (r *RecoveryManager) Sync() error {
	r.journalMu.Lock()
	defer r.journalMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrClosed
	}
	return r.syncLocked()
}

func (r *RecoveryManager) syncLocked() error {
	var errs []error
	for id, f := range r.handles {
		if err := f.Sync(); err != nil {
			errs = append(errs, fmt.Errorf("sync %s transcript: %w", id, err))
		}
	}
	if r.journalHandle != nil {
		if err := r.journalHandle.Sync(); err != nil {
			errs = append(errs, fmt.Errorf("sync tool journal: %w", err))
		}
	}
	// Native receipts and path-only attachments have no retained file handle.
	// Sync descendants first and then directories so newly created references
	// cannot outlive the files or directory entries they refer to.
	var dirs []string
	err := filepath.WalkDir(r.sessionDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == r.sessionDir && os.IsNotExist(walkErr) && len(r.handles) == 0 && r.journalHandle == nil {
				return nil
			}
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("session persistence path contains symbolic link %s", path)
		}
		if entry.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("session persistence path is not regular %s", path)
		}
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return fmt.Errorf("open persistence file: %w", err)
		}
		syncErr := f.Sync()
		closeErr := f.Close()
		if err := errors.Join(syncErr, closeErr); err != nil {
			return fmt.Errorf("sync persistence file %s: %w", path, err)
		}
		return nil
	})
	if err != nil {
		errs = append(errs, fmt.Errorf("sync session files: %w", err))
	}
	for _, dir := range slices.Backward(dirs) {
		if err := privatefs.SyncDir(dir); err != nil {
			errs = append(errs, fmt.Errorf("sync session directory: %w", err))
		}
	}
	return errors.Join(errs...)
}

// Close synchronizes and closes every writer once. Failure is retained for all
// subsequent calls; a second close cannot turn an unknown saved state into
// success. Neither Sync nor writes are admitted after this method returns.
func (r *RecoveryManager) Close() error {
	r.journalMu.Lock()
	defer r.journalMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return r.closeErr
	}
	errs := []error{r.syncLocked()}
	r.closed = true
	r.journalClosed = true
	if r.journalHandle != nil {
		if err := r.journalHandle.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close tool journal: %w", err))
		}
		r.journalHandle = nil
	}
	for id, f := range r.handles {
		if err := f.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close %s transcript: %w", id, err))
		}
	}
	r.handles = make(map[string]*os.File)
	r.closeErr = errors.Join(errs...)
	return r.closeErr
}
