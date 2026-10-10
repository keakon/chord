package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// openImageDirectory pins each directory while rejecting symbolic links below
// the trusted base. Comparing the opened inode closes the lstat/open race even
// when a link points to another directory inside that same root.
func openImageDirectory(base, path string) (*os.Root, error) {
	base, err := filepath.Abs(base)
	if err != nil {
		return nil, fmt.Errorf("resolve image base: %w", err)
	}
	// A workspace may use the platform's symlinked temp prefix (for example
	// /var on macOS). That trusted prefix is separate from user path components.
	spelledBase := base
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		return nil, fmt.Errorf("resolve image root: %w", err)
	}
	if rel, err := filepath.Rel(spelledBase, path); err == nil && filepath.IsLocal(rel) {
		path = filepath.Join(base, rel)
	}
	rel, err := filepath.Rel(base, path)
	if err != nil || !filepath.IsLocal(rel) {
		base = filepath.VolumeName(path) + string(filepath.Separator)
		rel, err = filepath.Rel(base, path)
		if err != nil || !filepath.IsLocal(rel) {
			return nil, fmt.Errorf("invalid image path")
		}
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, fmt.Errorf("open image path root: %w", err)
	}
	if rel == "." {
		return root, nil
	}
	for component := range strings.SplitSeq(rel, string(filepath.Separator)) {
		info, err := root.Lstat(component)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			_ = root.Close()
			return nil, fmt.Errorf("image path must use existing directories without symbolic links")
		}
		next, err := root.OpenRoot(component)
		_ = root.Close()
		if err != nil {
			return nil, fmt.Errorf("open image directory: %w", err)
		}
		f, err := next.Open(".")
		if err != nil {
			_ = next.Close()
			return nil, fmt.Errorf("inspect image directory: %w", err)
		}
		opened, err := f.Stat()
		_ = f.Close()
		if err != nil || !os.SameFile(info, opened) {
			_ = next.Close()
			return nil, fmt.Errorf("image directory changed while opening")
		}
		root = next
	}
	return root, nil
}

func openImageSnapshot(base, path string) (*os.File, error) {
	root, err := openImageDirectory(base, filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name := filepath.Base(path)
	info, err := root.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("inspect reference image: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("reference image must be a regular file without symbolic links")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open reference image: %w", err)
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = f.Close()
		return nil, fmt.Errorf("reference image changed while opening")
	}
	return f, nil
}

func verifyImagePublication(base, path string, pinned *os.Root, name string) error {
	current, err := openImageDirectory(base, filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("output_path directory changed after approval: %w", err)
	}
	defer current.Close()
	a, err := pinned.Lstat(name)
	if err != nil {
		return fmt.Errorf("inspect published image: %w", err)
	}
	b, err := current.Lstat(name)
	if err != nil {
		return fmt.Errorf("inspect output_path: %w", err)
	}
	if !os.SameFile(a, b) || !b.Mode().IsRegular() {
		return fmt.Errorf("output_path changed after approval; use the session original")
	}
	return nil
}
