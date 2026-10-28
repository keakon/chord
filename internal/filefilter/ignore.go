// Package filefilter owns default discovery exclusions and root-local Git ignore rules.
package filefilter

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"github.com/keakon/golog/log"
)

// GitIgnoreMatcher is a read-only snapshot of rules loaded from one directory.
// It is safe to share after construction; each new scan loads its own snapshot.
type GitIgnoreMatcher struct {
	patterns []gitIgnorePattern
}

type gitIgnorePattern struct {
	pattern  string // raw pattern from .gitignore
	negate   bool   // pattern prefixed with !
	dirOnly  bool   // pattern suffixed with /
	matchDir bool   // pattern contains a slash (not just a filename)
	literal  bool   // pattern has no glob metacharacters
}

// NewGitIgnoreMatcher reads only dir/.gitignore, without searching parent or
// nested directories. It does not cache rules: construct a new matcher to observe
// changes. Returns nil when the file cannot be read or contains no rules.
// Match is safe to call on a nil result.
func NewGitIgnoreMatcher(dir string) *GitIgnoreMatcher {
	gitignorePath := filepath.Join(dir, ".gitignore")
	f, err := os.Open(gitignorePath)
	if err != nil {
		return nil
	}
	defer f.Close()

	m := &GitIgnoreMatcher{}
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
	if err := scanner.Err(); err != nil {
		// A line longer than bufio.MaxScanTokenSize aborts the scan, but every
		// rule parsed before it is still valid. Dropping them all would silently
		// un-ignore the whole directory for Grep/Glob, so keep what we have and
		// leave a trace explaining why the tail of the file was not applied.
		log.Warnf("gitignore scan aborted dir=%v error=%v; keeping %d parsed pattern(s)", dir, err, len(m.patterns))
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
	p.literal = !strings.ContainsAny(line, `*?[\\`)

	return p
}

// Match reports whether the given relative path (forward-slash separated) should
// be ignored according to the loaded .gitignore rules.
// isDir indicates whether the path refers to a directory.
func (m *GitIgnoreMatcher) Match(relPath string, isDir bool) bool {
	if m == nil {
		return false
	}
	// The search root itself ("." relative to the .gitignore location) is never
	// subject to its own ignore rules, mirroring git semantics. Without this a
	// common pattern like ".*" matches the literal "." and silently skips the
	// entire walk.
	if relPath == "." || relPath == "" {
		return false
	}

	// Walk through patterns in order; last matching pattern wins. Segment
	// offsets are computed once per path: splitting and re-joining the path for
	// every rule is O(rules x depth) allocations on a full-tree Grep/Glob scan.
	var startBuf [16]int
	starts := appendPathSegmentStarts(startBuf[:0], relPath)
	result := false
	for i := range m.patterns {
		p := &m.patterns[i]
		if p.dirOnly && !isDir {
			// dirOnly patterns only match directories.
			// For a non-directory path, check if any *strict* parent
			// directory component matches the pattern. The path itself
			// (the last component) is a file, so it cannot match a
			// dirOnly pattern directly.
			if len(starts) < 2 {
				// No parent directory to check.
				continue
			}
			parentMatched := false
			for i := 1; i < len(starts); i++ {
				// Prefix path relPath[:starts[i]-1] drops the trailing slash;
				// its segments are the first i entries of starts.
				if matchGitIgnorePattern(*p, relPath[:starts[i]-1], starts[:i]) {
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
		if matchGitIgnorePattern(*p, relPath, starts) {
			result = !p.negate
		}
	}
	return result
}

// appendPathSegmentStarts appends the start offset of every slash-separated
// segment of path. The returned slice is reused by the caller, and the segment
// strings are zero-copy substrings of path.
func appendPathSegmentStarts(starts []int, path string) []int {
	starts = append(starts, 0)
	for i := 0; i < len(path); i++ {
		if path[i] == '/' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

func matchGitIgnorePattern(p gitIgnorePattern, relPath string, starts []int) bool {
	pattern := p.pattern

	// Leading slash anchors pattern to the .gitignore directory.
	if strings.HasPrefix(pattern, "/") {
		pattern = pattern[1:]
		if p.literal {
			return pattern == relPath
		}
		return matchGitIgnoreSegment(pattern, relPath)
	}

	// Patterns without slashes can match at any depth.
	if !p.matchDir {
		base := relPath[starts[len(starts)-1]:]
		if p.literal {
			// A slash-free literal can only equal the basename: every other
			// suffix contains a separator, which the pattern cannot contain.
			return pattern == base
		}
		// Try matching the basename.
		if matchGitIgnoreSegment(pattern, base) {
			return true
		}
		// Also try matching at any directory level.
		for _, start := range starts {
			if matchGitIgnoreSegment(pattern, relPath[start:]) {
				return true
			}
		}
		return false
	}

	// Patterns with slashes match relative to the .gitignore directory,
	// but also at any subdirectory level.
	if p.literal {
		if pattern == relPath {
			return true
		}
		for i := 1; i < len(starts); i++ {
			if pattern == relPath[starts[i]:] {
				return true
			}
		}
		return false
	}
	if matchGitIgnoreSegment(pattern, relPath) {
		return true
	}
	for i := 1; i < len(starts); i++ {
		if matchGitIgnoreSegment(pattern, relPath[starts[i]:]) {
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
