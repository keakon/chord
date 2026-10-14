package tools

import (
	"path/filepath"
	"strings"
)

// worktreeSkipRel returns the chord worktree container relative to the walk
// root when that walk must prune it, and ok=false otherwise. The container is
// pruned only when it lies strictly below the walk root, so explicitly
// searching the worktree root itself (or a path inside it) still works. Both
// paths are expected to be absolute and clean; a mismatch between a canonical
// and a lexical spelling simply disables the prune.
func worktreeSkipRel(searchRoot, worktreeRoot string) (string, bool) {
	searchRoot = strings.TrimSpace(searchRoot)
	worktreeRoot = strings.TrimSpace(worktreeRoot)
	if searchRoot == "" || worktreeRoot == "" {
		return "", false
	}
	rel, err := filepath.Rel(filepath.Clean(searchRoot), filepath.Clean(worktreeRoot))
	if err != nil {
		return "", false
	}
	if rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}
