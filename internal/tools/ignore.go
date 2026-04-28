package tools

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// skipDirNames lists directory names that are always excluded from Grep/Glob
// searches (unless the user explicitly specifies one as the search path).
// These are VCS internal data, tool-specific runtime directories, and
// dependency/install directories that typically contain many non-source files.
var skipDirNames = map[string]bool{
	// VCS internal data directories
	".git": true,
	".svn": true,
	".hg":  true,
	".bzr": true,
	".jj":  true, // Jujutsu
	".sl":  true, // Sapling

	// Python
	"__pycache__":   true, // Python bytecode cache
	".mypy_cache":   true,
	".pytest_cache": true,
	".ruff_cache":   true,
	".pyre":         true,
	".pytype":       true,

	// Python virtual environments
	".venv": true,
	"venv":  true,
	"env":   true,

	// Node.js
	"node_modules": true,

	// Build artifacts
	"dist":   true,
	"build":  true,
	"target": true, // Rust, Java, etc.
	"out":    true, // Common output directory

	// Cache directories
	".cache": true,

	// IDE/Editor directories
	".idea": true, // JetBrains IDEs
}

// gitIgnoreMatcher determines whether a file path should be excluded based on
// .gitignore rules found in the repository root.
type gitIgnoreMatcher struct {
	patterns []gitIgnorePattern
	root     string // absolute path of the repo root
}

// GitIgnoreMatcher is the exported type used by TUI @-mention scanning.
type GitIgnoreMatcher = gitIgnoreMatcher

// NewGitIgnoreMatcher loads .gitignore patterns from dir. Returns nil when no
// .gitignore exists or it cannot be read. Safe to call on a nil result: the
// Match method guards against a nil receiver.
func NewGitIgnoreMatcher(dir string) *GitIgnoreMatcher {
	return newGitIgnoreMatcher(dir)
}

// IsSkippedDirName reports whether Chord always excludes a directory with
// this basename from Grep/Glob/@-mention walks (e.g. VCS metadata, tool
// caches, compiled-bytecode dirs).
func IsSkippedDirName(name string) bool {
	return skipDirNames[name]
}

type gitIgnorePattern struct {
	pattern  string // raw pattern from .gitignore
	negate   bool   // pattern prefixed with !
	dirOnly  bool   // pattern suffixed with /
	matchDir bool   // pattern contains a slash (not just a filename)
}

// newGitIgnoreMatcher loads .gitignore patterns from the given directory (if it
// contains a .gitignore file). Returns nil if no .gitignore exists or it
// cannot be read.
func newGitIgnoreMatcher(dir string) *gitIgnoreMatcher {
	gitignorePath := filepath.Join(dir, ".gitignore")
	f, err := os.Open(gitignorePath)
	if err != nil {
		return nil
	}
	defer f.Close()

	m := &gitIgnoreMatcher{root: dir}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		line = strings.TrimSpace(line)
		// Skip empty lines and comments.
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p := parseGitIgnorePattern(line)
		m.patterns = append(m.patterns, p)
	}
	if len(m.patterns) == 0 {
		return nil
	}
	return m
}

func parseGitIgnorePattern(line string) gitIgnorePattern {
	p := gitIgnorePattern{pattern: line}

	// Negation.
	if strings.HasPrefix(line, "!") {
		p.negate = true
		line = line[1:]
	}

	// Directory-only indicator (trailing slash).
	if strings.HasSuffix(line, "/") {
		p.dirOnly = true
		line = strings.TrimSuffix(line, "/")
	}

	// If the pattern contains a slash (after removing trailing /), it's
	// anchored to the .gitignore location.
	p.matchDir = strings.Contains(line, "/")
	p.pattern = line

	return p
}

// Match reports whether the given relative path (forward-slash separated) should
// be ignored according to the loaded .gitignore rules.
// isDir indicates whether the path refers to a directory.
func (m *gitIgnoreMatcher) Match(relPath string, isDir bool) bool {
	if m == nil {
		return false
	}

	// Walk through patterns in order; last matching pattern wins.
	result := false
	for _, p := range m.patterns {
		if p.dirOnly && !isDir {
			// dirOnly patterns only match directories.
			// For a non-directory path, check if any *strict* parent
			// directory component matches the pattern. The path itself
			// (the last component) is a file, so it cannot match a
			// dirOnly pattern directly.
			parts := strings.Split(relPath, "/")
			if len(parts) < 2 {
				// No parent directory to check.
				continue
			}
			parentMatched := false
			for i := 1; i < len(parts); i++ {
				prefix := strings.Join(parts[:i], "/")
				if matchGitIgnorePattern(p, prefix) {
					parentMatched = true
					break
				}
			}
			if !parentMatched {
				continue
			}
			result = !p.negate
			continue
		}
		if matchGitIgnorePattern(p, relPath) {
			result = !p.negate
		}
	}
	return result
}

func matchGitIgnorePattern(p gitIgnorePattern, relPath string) bool {
	pattern := p.pattern

	// Leading slash anchors pattern to the .gitignore directory.
	if strings.HasPrefix(pattern, "/") {
		pattern = pattern[1:]
		return matchGitIgnoreSegment(pattern, relPath)
	}

	// Patterns without slashes can match at any depth.
	if !p.matchDir {
		// Try matching the basename.
		base := filepath.Base(relPath)
		if matchGitIgnoreSegment(pattern, base) {
			return true
		}
		// Also try matching at any directory level.
		parts := strings.Split(relPath, "/")
		for i := 0; i < len(parts); i++ {
			if matchGitIgnoreSegment(pattern, strings.Join(parts[i:], "/")) {
				return true
			}
		}
		return false
	}

	// Patterns with slashes match relative to the .gitignore directory,
	// but also at any subdirectory level.
	if matchGitIgnoreSegment(pattern, relPath) {
		return true
	}
	parts := strings.Split(relPath, "/")
	for i := 1; i < len(parts); i++ {
		if matchGitIgnoreSegment(pattern, strings.Join(parts[i:], "/")) {
			return true
		}
	}
	return false
}

// matchGitIgnoreSegment matches a single gitignore pattern segment against a
// path string using filepath.Match (glob semantics).
func matchGitIgnoreSegment(pattern, path string) bool {
	// Handle "**" (match any number of directories).
	if strings.Contains(pattern, "**") {
		return matchDoublestar(pattern, path)
	}

	// If pattern ends with a component, match exactly.
	matched, err := filepath.Match(pattern, path)
	if err != nil {
		return false
	}
	if matched {
		return true
	}

	// If the pattern doesn't contain a slash, it can match the basename.
	// (Already handled by callers for non-matchDir patterns.)
	return false
}

// matchDoublestar handles gitignore patterns with ** by expanding them into
// possible matches. This is a simplified implementation that covers the common
// cases: "a/**/b" matches "a/b", "a/x/b", "a/x/y/b", etc.
func matchDoublestar(pattern, path string) bool {
	// Split on "**".
	parts := strings.SplitN(pattern, "**", 2)
	if len(parts) != 2 {
		return false
	}
	prefix := parts[0]
	suffix := parts[1]

	// Prefix must match the beginning of the path.
	if prefix != "" {
		prefix = strings.TrimSuffix(prefix, "/")
		if !strings.HasPrefix(path, prefix+"/") && path != prefix {
			return false
		}
		if strings.HasPrefix(path, prefix+"/") {
			path = path[len(prefix)+1:]
		} else if path == prefix {
			path = ""
		}
	}

	suffix = strings.TrimPrefix(suffix, "/")

	if suffix == "" {
		return true
	}

	// Try matching suffix at every level.
	for {
		matched, err := filepath.Match(suffix, path)
		if err == nil && matched {
			return true
		}
		idx := strings.Index(path, "/")
		if idx < 0 {
			break
		}
		path = path[idx+1:]
	}
	return false
}
