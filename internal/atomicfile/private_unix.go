//go:build !windows

package atomicfile

import (
	"fmt"
	"os"
	"syscall"
)

// CreatePrivateFile exclusively creates a file with restricted access before
// any contents are written. Existing paths are never modified.
func CreatePrivateFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
}

// ValidatePrivatePath checks a private directory or regular file without
// following its final symlink. Callers check intermediate components as well.
func ValidatePrivatePath(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0o077 != 0 || info.Mode()&os.ModeSymlink != 0 || info.IsDir() != directory || (!directory && !info.Mode().IsRegular()) {
		return fmt.Errorf("private path has unsafe ownership, permissions or type")
	}
	return nil
}

// EnsurePrivateDirectory creates a private directory tree with durable entries.
// Existing permissions are not changed; callers validate their trust boundary.
func EnsurePrivateDirectory(path string) error { return makeDurableDirectory(path) }

// ValidatePrivateContainer permits public read/traversal on the configuration
// root, but rejects other users' write access to credential path components.
func ValidatePrivateContainer(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0o022 != 0 || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("private container has unsafe ownership, permissions or type")
	}
	return nil
}
