package privatefs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

const (
	DirMode  = 0o700
	FileMode = 0o600
)

// EnsureDir creates path with DirMode and restricts every directory from root
// through path. Permissions are applied only to directories this call creates;
// pre-existing directories keep their current permissions (they may carry
// intentional group/ACL settings that Chord must not silently overwrite).
// Symlink components are still rejected.
func EnsureDir(root, path string) error {
	rel, err := relativePath(root, path)
	if err != nil {
		return err
	}
	rootExisted := true
	if info, err := os.Lstat(root); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("private root %q is a symbolic link", root)
		}
	} else if os.IsNotExist(err) {
		rootExisted = false
	} else {
		return err
	}
	if err := os.MkdirAll(root, DirMode); err != nil {
		return err
	}
	if !rootExisted {
		if err := os.Chmod(root, DirMode); err != nil {
			return err
		}
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	// Record which components already exist so only newly created directories
	// are restricted to DirMode.
	existed := make(map[string]bool)
	if rel != "." {
		current := "."
		for _, part := range splitPath(rel) {
			current = filepath.Join(current, part)
			if _, err := r.Lstat(current); err == nil {
				existed[current] = true
			} else if !os.IsNotExist(err) {
				return err
			}
		}
	}
	if err := r.MkdirAll(rel, DirMode); err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	current := "."
	for _, part := range splitPath(rel) {
		current = filepath.Join(current, part)
		info, err := r.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("private directory %q contains a symbolic link", path)
		}
		if !existed[current] {
			if err := r.Chmod(current, DirMode); err != nil {
				return err
			}
		}
	}
	return nil
}

func WriteFile(root, path string, data []byte) (err error) {
	f, err := OpenFile(root, path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}()
	_, err = f.Write(data)
	return err
}

// WriteFileSynced writes a private file and syncs its contents before close.
func WriteFileSynced(root, path string, data []byte) (err error) {
	f, err := OpenFile(root, path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}()
	if _, err = f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

// SyncDir syncs a directory so a completed rename survives a crash. It is
// best-effort on platforms and filesystems that reject directory fsync.
func SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		if runtime.GOOS == "windows" || os.IsPermission(err) || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) {
			return nil
		}
		return err
	}
	return nil
}

func OpenFile(root, path string, flag int) (*os.File, error) {
	if err := EnsureDir(root, filepath.Dir(path)); err != nil {
		return nil, err
	}
	rel, err := relativePath(root, path)
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	existed := false
	if info, err := r.Lstat(rel); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("private file %q is a symbolic link", path)
	} else if err == nil {
		existed = true
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	f, err := r.OpenFile(rel, flag, FileMode)
	if err != nil {
		return nil, err
	}
	// Only newly created files are restricted to FileMode; pre-existing files
	// keep their current permissions (they may carry intentional settings).
	if !existed {
		if err := f.Chmod(FileMode); err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	return f, nil
}

func relativePath(root, path string) (string, error) {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || filepath.IsAbs(rel) || (len(rel) > 3 && rel[:3] == ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("private path %q is outside root %q", path, root)
	}
	return rel, nil
}

func splitPath(path string) []string {
	var parts []string
	for path != "." {
		dir, base := filepath.Split(path)
		parts = append(parts, base)
		path = filepath.Clean(dir)
	}
	for left, right := 0, len(parts)-1; left < right; left, right = left+1, right-1 {
		parts[left], parts[right] = parts[right], parts[left]
	}
	return parts
}
