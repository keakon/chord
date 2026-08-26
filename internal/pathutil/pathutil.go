// Package pathutil provides shared path resolution helpers. Tool execution
// and permission matching both use it so the two sides agree on how a
// relative path maps onto the session working directory: a rule that allows
// "src/**" must resolve against the same base directory the tool would run in.
package pathutil

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrHomeDirUnavailable is returned when a path starting with "~" cannot be
// expanded because the home directory is unknown.
var ErrHomeDirUnavailable = errors.New("home directory is unavailable")

// ExpandTilde expands a leading "~", "~/", or (on Windows) "~\" to the user's
// home directory. A bare "~" expands to the home directory itself; other
// paths are returned unchanged (the tilde inside a path component is a
// literal filename).
func ExpandTilde(path string) (string, error) {
	return expandTilde(path, runtime.GOOS == "windows")
}

func expandTilde(path string, isWindows bool) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return trimmed, nil
	}
	if trimmed == "~" {
		return expandHome("")
	}
	if strings.HasPrefix(trimmed, "~/") || (isWindows && strings.HasPrefix(trimmed, `~\`)) {
		// "~/" is the conventional spelling; Windows additionally accepts "~\"
		// as a separator. Both were historically expanded, so keep both working.
		return expandHome(trimmed[2:])
	}
	return trimmed, nil
}

func expandHome(rel string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ErrHomeDirUnavailable
	}
	if rel == "" {
		return home, nil
	}
	return filepath.Join(home, rel), nil
}

// ResolveInDir cleans path and, when baseDir is non-empty and path is
// relative, resolves it against baseDir. Absolute paths are returned as-is;
// an empty baseDir keeps relative paths relative. The result is lexically
// clean (redundant "./" and inner ".." hops are removed) but is not
// guaranteed to exist.
func ResolveInDir(path, baseDir string) (string, error) {
	resolved, err := ExpandTilde(path)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(resolved) || strings.TrimSpace(baseDir) == "" || strings.TrimSpace(resolved) == "" {
		return filepath.Clean(resolved), nil
	}
	base, err := ExpandTilde(baseDir)
	if err != nil {
		return "", err
	}
	return filepath.Clean(filepath.Join(base, resolved)), nil
}

// NormalizeWithinBase returns path spelled consistently for permission rule
// matching and rule suggestions: when the resolved path lies inside baseDir it
// is returned relative to baseDir, otherwise in absolute form. The result
// always uses "/" separators so rules are portable across platforms.
func NormalizeWithinBase(path, baseDir string) (string, error) {
	resolved, err := ResolveInDir(path, baseDir)
	if err != nil {
		return "", err
	}
	if rel, ok := RelToBase(resolved, baseDir); ok {
		return filepath.ToSlash(rel), nil
	}
	return filepath.ToSlash(resolved), nil
}

// RelToBase returns path relative to baseDir when path lies inside baseDir
// (the directory itself or a descendant); ok is false when path escapes
// baseDir or the two live on different volumes.
func RelToBase(path, baseDir string) (rel string, ok bool) {
	rel, err := filepath.Rel(filepath.Clean(baseDir), filepath.Clean(path))
	if err != nil {
		return "", false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}
