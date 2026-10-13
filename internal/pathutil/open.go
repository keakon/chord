package pathutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// OpenCanonicalRegular opens a canonical absolute path without accepting new
// symlinks. Each directory handle pins the next lookup; identity checks reject
// replacements between inspection and open before any file content is read.
func OpenCanonicalRegular(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("canonical absolute path required: %s", path)
	}
	volume := filepath.VolumeName(path)
	root, err := os.OpenRoot(volume + string(filepath.Separator))
	if err != nil {
		return nil, fmt.Errorf("open filesystem root: %w", err)
	}
	defer func() { root.Close() }()
	parts := strings.Split(strings.TrimPrefix(path[len(volume):], string(filepath.Separator)), string(filepath.Separator))
	for i, part := range parts {
		info, err := root.Lstat(part)
		if err != nil {
			return nil, fmt.Errorf("inspect path component: %w", err)
		}
		if i < len(parts)-1 {
			if !info.IsDir() {
				return nil, fmt.Errorf("path component is not a real directory: %s", part)
			}
			next, err := root.OpenRoot(part)
			if err != nil {
				return nil, fmt.Errorf("open path component: %w", err)
			}
			opened, err := next.Stat(".")
			if err != nil || !os.SameFile(info, opened) {
				next.Close()
				return nil, fmt.Errorf("directory changed during open: %s", part)
			}
			root.Close()
			root = next
			continue
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("target is not a regular file: %s", path)
		}
		f, err := root.OpenFile(part, canonicalReadFlags, 0)
		if err != nil {
			return nil, fmt.Errorf("open regular file: %w", err)
		}
		opened, err := f.Stat()
		if err != nil || !os.SameFile(info, opened) {
			f.Close()
			return nil, fmt.Errorf("file changed during open: %s", path)
		}
		return f, nil
	}
	return nil, fmt.Errorf("file path required")
}
