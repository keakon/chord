package inputhistory

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
)

func privateFile(f *os.File) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Mode().Perm() != 0600 {
		return f.Chmod(0600)
	}
	return nil
}
func (s *Store) acquire(ctx context.Context) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.pathLock, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = privateFile(f); err == nil {
		err = lockFile(ctx, f)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
func release(f *os.File) { _ = unlockFile(f); _ = f.Close() }
