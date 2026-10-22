package recovery

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/privatefs"
)

// nonDurableSessionDirs names the top-level session directories holding
// diagnostics written outside the recovery read set: LLM request dumps, LLM
// traces, and background job logs. The handoff barrier skips them so debug
// output never becomes part of the session's durability contract. Their
// producers own the names, so a rename here has to follow the producer.
var nonDurableSessionDirs = []string{"dumps", "traces", "job-logs"}

// nonDurableSessionDir reports whether path is one of those diagnostic
// directories. Only a directory directly under the session root matches; a
// nested directory that happens to share the name is still part of the tree
// the barrier has to cover.
func nonDurableSessionDir(sessionDir, path string) bool {
	if filepath.Dir(path) != filepath.Clean(sessionDir) {
		return false
	}
	return slices.Contains(nonDurableSessionDirs, filepath.Base(path))
}

// Sync seals all manager-owned logs, journals and attachments, then makes the
// session directory durable for a handoff to another process. Producers using
// other writers must be stopped before calling it; their paths are synced too.
// The context is checked between filesystem calls, so a tree that slows to a
// crawl fails the handoff instead of blocking it indefinitely; a single call
// that hangs still blocks until it returns. Close does not repeat this barrier.
func (r *RecoveryManager) Sync(ctx context.Context) error {
	r.journalMu.Lock()
	defer r.journalMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	start := time.Now()
	writerErr := r.syncOpenWritersLocked()
	stats, treeErr := r.syncSessionTreeLocked(ctx)
	err := errors.Join(writerErr, treeErr)
	log.Infof(
		"session sync barrier session=%v files=%v dirs=%v bytes=%v skipped_dirs=%v duration=%v error=%v",
		filepath.Base(r.sessionDir), stats.files, stats.dirs, stats.bytes,
		stats.skippedDirs, time.Since(start), err,
	)
	return err
}

func (r *RecoveryManager) syncOpenWritersLocked() error {
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
	return errors.Join(errs...)
}

type sessionSyncStats struct {
	files       int
	dirs        int
	bytes       int64
	skippedDirs int
}

func (r *RecoveryManager) syncSessionTreeLocked(ctx context.Context) (sessionSyncStats, error) {
	var stats sessionSyncStats
	var errs []error
	// Native receipts and path-only attachments have no retained file handle.
	// Sync descendants first and then directories so newly created references
	// cannot outlive the files or directory entries they refer to.
	var dirs []string
	err := filepath.WalkDir(r.sessionDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
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
			if path != r.sessionDir && nonDurableSessionDir(r.sessionDir, path) {
				stats.skippedDirs++
				return filepath.SkipDir
			}
			dirs = append(dirs, path)
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("session persistence path is not regular %s", path)
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat persistence file: %w", err)
		}
		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open persistence file: %w", err)
		}
		syncErr := f.Sync()
		closeErr := f.Close()
		if err := errors.Join(syncErr, closeErr); err != nil {
			return fmt.Errorf("sync persistence file %s: %w", path, err)
		}
		stats.files++
		stats.bytes += info.Size()
		return nil
	})
	if err != nil {
		errs = append(errs, fmt.Errorf("sync session files: %w", err))
	}
	for _, dir := range slices.Backward(dirs) {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		if err := privatefs.SyncDir(dir); err != nil {
			errs = append(errs, fmt.Errorf("sync session directory: %w", err))
			continue
		}
		stats.dirs++
	}
	return stats, errors.Join(errs...)
}

// Close flushes and closes every open writer once. It leaves the session tree
// to Sync; a process exit keeps written data in the page cache, while a handoff
// needs the explicit barrier. A file therefore stays durable across a crash
// after Close only when its writer synced it — privatefs.WriteFileSynced and
// the retained handles here do, privatefs.WriteFile leaves it to the kernel.
// Failure is retained for all subsequent calls; a second close cannot turn an
// unknown saved state into success. Neither Sync nor writes are admitted after
// this method returns.
func (r *RecoveryManager) Close() error {
	r.journalMu.Lock()
	defer r.journalMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return r.closeErr
	}
	errs := []error{r.syncOpenWritersLocked()}
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
