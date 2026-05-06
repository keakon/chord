package worktree

import (
	"fmt"
	"os"
	"path/filepath"
)

// repoIndexLock holds a held cross-process lock for one repo's index
// file. Close releases it.
type repoIndexLock struct {
	file *os.File
}

// WithRepoIndexLock acquires a cross-process lock on the index file,
// invokes fn with the loaded index (or a fresh empty one when missing),
// saves the result, and releases the lock. Use for any read-modify-write
// path that races with other chord processes.
func WithRepoIndexLock(stateDir, repoID string, fn func(*RepoIndex) error) error {
	path := repoIndexPath(stateDir, repoID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir repo index dir: %w", err)
	}
	lock, err := lockRepoIndex(path)
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
