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

// AbbreviateHome returns path with a leading home-directory prefix replaced
// by "~" and "/" separators throughout (for example "/Users/me/a/b" becomes
// "~/a/b", "C:\Users\me\a\b" becomes "~/a/b"). Paths not under the home
// directory are returned with separators normalized but otherwise unchanged.
// The result is the portable spelling also understood by ExpandTilde, so
// paths embedded in prompts or pipes round-trip through tools that expand
// "~" back to the home directory.
func AbbreviateHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.ToSlash(AbbreviateHomeIn(path, home))
}

// AbbreviateHomeIn abbreviates path when it lies under home, keeping the
// platform separator: "~" plus the remaining path. Matching follows the
// platform spelling rules: when home is a Windows volume path (for example
// "C:\Users\me") the prefix comparison is case-insensitive and accepts either
// separator, so "C:/Users/me/x" and "c:\users\me\y" both abbreviate.
func AbbreviateHomeIn(path, home string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	home = strings.TrimSpace(home)
	if home == "" {
		return path
	}
	if isWindowsVolumePath(home) {
		return abbreviateHomeInWindows(path, home)
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+string(os.PathSeparator)) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

// isWindowsVolumePath reports whether p uses Windows volume syntax such as
// "C:\..." or "C:/...".
func isWindowsVolumePath(p string) bool {
	return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}

func abbreviateHomeInWindows(path, home string) string {
	home = strings.TrimRight(home, `/\`)
	homeNorm := strings.ReplaceAll(home, "/", `\`)
	if len(path) < len(homeNorm) {
		return path
	}
	prefixNorm := strings.ReplaceAll(path[:len(homeNorm)], "/", `\`)
	if !strings.EqualFold(prefixNorm, homeNorm) {
		return path
	}
	rest := path[len(homeNorm):]
	if rest == "" {
		return "~"
	}
	if rest[0] == '\\' || rest[0] == '/' {
		return "~" + rest
	}
	return path
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

// ResolveSymlinksBestEffort re-spells path through the EvalSymlinks result of
// its nearest existing ancestor, so a path that differs from a known root only
// by a symlinked prefix still matches that root. It returns false when no
// ancestor resolves, leaving the caller to keep its own spelling.
//
// Permission matching and tool execution both need this, and they must agree:
// a path the rule check resolved one way and the tool resolved another is a
// rule that silently stops applying to the call it was meant to govern.
//
// The nearest existing ancestor rather than the full path is deliberate: a
// path being created does not exist yet, but its parent directory does.
func ResolveSymlinksBestEffort(path string) (string, bool) {
	cur := filepath.Clean(path)
	remainder := ""
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			if remainder == "" {
				return resolved, true
			}
			return filepath.Join(resolved, remainder), true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", false
		}
		remainder = filepath.Join(filepath.Base(cur), remainder)
		cur = parent
	}
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

// CheckoutRoot returns the root directory of the git checkout dir belongs to:
// the nearest directory at or above dir that carries a .git entry. A linked
// worktree or submodule nested inside a repository is its own checkout, so its
// root wins over the outer repository.
//
// When dir lies inside limit the walk never goes above limit and limit is
// returned as a fallback root when no .git entry is found; this keeps callers
// anchored to a repository boundary they already resolved. When dir lies
// outside limit the walk is unbounded, and dir itself is the fallback.
func CheckoutRoot(dir, limit string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	absDir = filepath.Clean(absDir)

	bounded := false
	absLimit := ""
	if l := strings.TrimSpace(limit); l != "" {
		if absL, lerr := filepath.Abs(l); lerr == nil {
			absLimit = filepath.Clean(absL)
			_, bounded = RelToBase(absDir, absLimit)
		}
	}

	for cur := absDir; ; {
		if _, serr := os.Lstat(filepath.Join(cur, ".git")); serr == nil {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur || (bounded && cur == absLimit) {
			break
		}
		cur = parent
	}
	if bounded {
		return absLimit
	}
	return absDir
}
