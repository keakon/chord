package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/atomicfile"
)

func writeProjectionOutput(sessionDir, outPath string, data []byte) error {
	path, err := resolveProjectionOutput(sessionDir, outPath)
	if err != nil {
		return err
	}
	// Replacement leaves other hard links to the old inode unchanged, even if
	// the destination is linked to a session file after the alias check.
	if err := atomicfile.Replace(path, data, 0o600); err != nil {
		return fmt.Errorf("write projection file: %w", err)
	}
	return nil
}

// resolveProjectionOutput resolves an --out path and refuses one that would replace the session
// it is reading. A path inside the session directory, or a hard link to a file
// already there, is rejected. The check runs before any write.
func resolveProjectionOutput(sessionDir, outPath string) (string, error) {
	sessionAbs, err := filepath.Abs(sessionDir)
	if err != nil {
		return "", fmt.Errorf("resolve session directory: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(sessionAbs); err == nil {
		sessionAbs = resolved
	}
	// MkdirAll and WriteFile do not clean ".." the way filepath.Abs does, so a
	// symlink followed by ".." lands inside the session even though the
	// cleaned path looks like a sibling. The check has to follow that same
	// spelling, which means building the absolute path without Clean.
	outAbs, err := absWithoutClean(outPath)
	if err != nil {
		return "", fmt.Errorf("resolve output path: %w", err)
	}
	outAbs, err = resolveExistingAncestor(outAbs)
	if err != nil {
		return "", err
	}
	if info, err := os.Lstat(outAbs); err == nil && info.Mode()&os.ModeSymlink != 0 {
		// resolveExistingAncestor could not follow this link, so a write would
		// create its target: refuse instead of letting WriteFile pick a spot.
		return "", fmt.Errorf("refusing to write the projection through the symlink %s", outAbs)
	}
	rel, err := filepath.Rel(sessionAbs, outAbs)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("refusing to write the projection inside the session directory %s", sessionAbs)
	}
	outInfo, err := os.Stat(outAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return outAbs, nil
		}
		return "", fmt.Errorf("inspect output file: %w", err)
	}
	err = filepath.WalkDir(sessionAbs, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if os.SameFile(outInfo, info) {
			return fmt.Errorf("refusing to overwrite session file %s", path)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("check session output aliases: %w", err)
	}
	return outAbs, nil
}

// absWithoutClean makes path absolute without filepath.Clean. The later write
// uses the raw path, and Clean would erase a ".." that the write still walks.
func absWithoutClean(path string) (string, error) {
	if filepath.IsAbs(path) {
		return path, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return wd + string(filepath.Separator) + path, nil
}

// resolveExistingAncestor follows symlinks in the deepest existing ancestor of
// path and re-appends the components that do not exist yet, so a caller can see
// where a later MkdirAll/WriteFile would land. ".." is applied only after a
// symlink is followed: cleaning it first would hide a write that walks out of
// the link and back into the session.
func resolveExistingAncestor(path string) (string, error) {
	return resolvePathComponents(path, 0)
}

func resolvePathComponents(path string, depth int) (string, error) {
	const maxSymlinkDepth = 64
	if depth > maxSymlinkDepth {
		return "", fmt.Errorf("resolve %s: too many symlinks", path)
	}
	vol := filepath.VolumeName(path)
	rest := strings.TrimPrefix(path, vol)
	parts := strings.Split(rest, string(filepath.Separator))
	resolved := vol
	if strings.HasPrefix(rest, string(filepath.Separator)) {
		resolved = vol + string(filepath.Separator)
	}
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			resolved = parentPath(resolved)
			continue
		}
		next := joinPathComponent(resolved, part)
		info, err := os.Lstat(next)
		if err != nil {
			if os.IsNotExist(err) {
				resolved = next
				continue
			}
			return "", fmt.Errorf("resolve %s: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			resolved = next
			continue
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", fmt.Errorf("resolve %s: %w", path, err)
		}
		if !filepath.IsAbs(target) {
			target = joinPathComponent(resolved, target)
		}
		followed, err := resolvePathComponents(target, depth+1)
		if err != nil {
			return "", err
		}
		resolved = followed
	}
	return resolved, nil
}

func parentPath(path string) string {
	vol := filepath.VolumeName(path)
	rest := strings.TrimPrefix(path, vol)
	rest = strings.TrimRight(rest, string(filepath.Separator))
	i := strings.LastIndex(rest, string(filepath.Separator))
	if i < 0 {
		return vol
	}
	if i == 0 {
		return vol + string(filepath.Separator)
	}
	return vol + rest[:i]
}

func joinPathComponent(parent, child string) string {
	if parent == "" {
		return child
	}
	if strings.HasSuffix(parent, string(filepath.Separator)) {
		return parent + child
	}
	return parent + string(filepath.Separator) + child
}
