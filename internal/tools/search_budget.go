package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	broadSearchVisitedEntriesLimit = int64(100_000)
	broadSearchMinCheckEntries     = int64(1_000)
)

// broadSearchLimits holds the thresholds used by broadSearchGuard. It is a var
// so tests can tighten the thresholds without creating hundreds of thousands of
// filesystem entries. Do not mutate outside tests.
var broadSearchLimits = struct {
	visitedEntries int64
	minCandidates  int64
}{
	visitedEntries: broadSearchVisitedEntriesLimit,
	minCandidates:  broadSearchMinCheckEntries,
}

// errGuardAbort is returned by walk callbacks to stop traversal when the
// broad-search guard decides the call is likely an accidental scan of a huge
// root with very few candidate files. Callers translate it into a user-facing
// error via broadSearchGuard.abortError().
var errGuardAbort = errors.New("broad search guard abort")

// resolveExactIncludeFiles returns direct file paths under root for includes
// that are relative paths without glob metacharacters. When every include is a
// plain relative path, the second result is true and the caller should use only
// these paths instead of walking the root. If any include contains glob meta
// characters or is absolute, the second result is false (no fast path).
func resolveExactIncludeFiles(root string, includes []string) ([]string, bool) {
	if len(includes) == 0 {
		return nil, false
	}
	files := make([]string, 0, len(includes))
	for _, include := range includes {
		rel, ok := cleanRelativePathFilter(include)
		if !ok {
			return nil, false
		}
		files = append(files, filepath.Join(root, filepath.FromSlash(rel)))
	}
	return files, true
}

// broadSearchGuard aborts searches that look like an accidental recursive scan
// of a huge root while very few files are actually being searched.
type broadSearchGuard struct {
	toolName   string
	root       string
	filterKind string
	filters    []string
	visited    int64
	candidates int64
}

func newBroadSearchGuard(toolName, root, filterKind string, filters []string) broadSearchGuard {
	return broadSearchGuard{toolName: toolName, root: root, filterKind: filterKind, filters: filters}
}

func (g *broadSearchGuard) visit() {
	g.visited++
}

func (g *broadSearchGuard) candidate() {
	g.candidates++
}

func (g *broadSearchGuard) shouldAbort() bool {
	if g.visited < broadSearchLimits.visitedEntries {
		return false
	}
	if len(g.filters) == 0 {
		return false
	}
	if !isBroadSearchRoot(g.root) && !allExactRelativePaths(g.filters) {
		return false
	}
	if g.candidates == 0 {
		return true
	}
	return g.visited/g.candidates >= broadSearchLimits.minCandidates
}

func (g broadSearchGuard) abortError() error {
	filters := strings.Join(g.filters, ", ")
	return fmt.Errorf("search aborted: %s visited %d filesystem entries under %s but only %d candidate files matched %s %q. The filter is applied during directory traversal; it does not avoid walking the search root. If the target file path is known, pass the full file path as the search path, or narrow the search path before retrying", g.toolName, g.visited, g.root, g.candidates, g.filterKind, filters)
}

func hasGlobMeta(pattern string) bool {
	escaped := false
	for _, r := range pattern {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		switch r {
		case '*', '?', '[', '{':
			return true
		}
	}
	return false
}

func cleanRelativePathFilter(filter string) (string, bool) {
	filter = strings.TrimSpace(filter)
	if filter == "" || filepath.IsAbs(filter) || hasGlobMeta(filter) {
		return "", false
	}
	cleaned := filepath.Clean(filepath.FromSlash(filter))
	if cleaned == "." || cleaned == string(filepath.Separator) || cleaned == "" {
		return "", false
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", false
	}
	return cleaned, true
}

func allExactRelativePaths(filters []string) bool {
	if len(filters) == 0 {
		return false
	}
	for _, filter := range filters {
		if _, ok := cleanRelativePathFilter(filter); !ok {
			return false
		}
	}
	return true
}

func isBroadSearchRoot(root string) bool {
	if root == "" {
		return false
	}
	abs, err := filepath.Abs(root)
	if err == nil {
		root = abs
	}
	root = filepath.Clean(root)
	if runtime.GOOS == "windows" {
		volume := filepath.VolumeName(root)
		if root == volume+string(filepath.Separator) {
			return true
		}
	} else if root == string(filepath.Separator) {
		return true
	}
	if cwd, err := os.Getwd(); err == nil && filepath.Clean(cwd) == root {
		return false
	}
	if home, err := os.UserHomeDir(); err == nil && filepath.Clean(home) == root {
		return true
	}
	if temp := os.TempDir(); temp != "" {
		temp = filepath.Clean(temp)
		if root == temp || isPathWithin(root, temp) {
			return true
		}
	}
	if runtime.GOOS == "darwin" && (root == "/private/var/folders" || strings.HasPrefix(root, "/private/var/folders/")) {
		return true
	}
	return false
}

func isPathWithin(path, parent string) bool {
	path = filepath.Clean(path)
	parent = filepath.Clean(parent)
	if path == parent {
		return true
	}
	rel, err := filepath.Rel(parent, path)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
