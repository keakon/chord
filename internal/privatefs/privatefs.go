package privatefs

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	DirMode  = 0o700
	FileMode = 0o600
)

// EnsureDir creates path and restricts every directory from root through path.
func EnsureDir(root, path string) error {
	rel, err := relativePath(root, path)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(root); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("private root %q is a symbolic link", root)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(root, DirMode); err != nil {
		return err
	}
	if err := os.Chmod(root, DirMode); err != nil {
		return err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
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
		if err := r.Chmod(current, DirMode); err != nil {
			return err
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
	if info, err := r.Lstat(rel); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("private file %q is a symbolic link", path)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := r.OpenFile(rel, flag, FileMode)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(FileMode); err != nil {
		_ = f.Close()
		return nil, err
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
