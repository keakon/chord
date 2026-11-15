package recovery

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// WithInactiveSession holds the session directory guard while fn operates on
// the existing directory. It never creates the directory or writes ownership
// metadata. Resume and deletion use the same guard and cannot overlap fn.
func WithInactiveSession(dir string, fn func(*os.Root) error) (err error) {
	directory, err := acquireInactiveSessionDirectoryLock(dir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, directory.Close()) }()
	if err := directory.verify(dir); err != nil {
		return err
	}
	return fn(directory.root)
}

func acquireInactiveSessionDirectoryLock(dir string) (*sessionDirectoryLock, error) {
	directory, err := acquireSessionDirectoryLock(dir)
	if err != nil {
		return nil, err
	}
	owner, err := readSessionLock(directory.root)
	if err == nil && isProcessAlive(owner.PID) {
		err = currentSessionLockedError(dir, filepath.Join(dir, sessionLockFile))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		err = fmt.Errorf("read session owner: %w", err)
	} else {
		return directory, nil
	}
	return nil, errors.Join(err, directory.Close())
}
