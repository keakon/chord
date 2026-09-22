package permission

import (
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/pathutil"
)

// PathScope describes the filesystem roots a path-taking tool call is
// evaluated against. A session working in a linked worktree still sees the
// whole repository, so the same repository-relative spelling must resolve to
// the same rule action no matter which checkout the agent currently stands in.
//
// The zero value carries no roots and degrades path evaluation to the plain
// lexical Evaluate, matching an unknown working directory.
type PathScope struct {
	// Cwd is the tool base dir: the checkout the tool would execute in. It is
	// always a root candidate, so a session whose repository root is unknown
	// keeps the cwd-only behavior.
	Cwd string
	// Roots are the canonical roots of every checkout of one repository (the
	// main checkout plus each linked worktree). Roots are deduplicated and the
	// longest prefix wins, because a worktree normally nests inside the main
	// checkout.
	Roots []string
	// Containers are directories whose immediate children are checkout roots
	// (the configured worktree root). A path under <container>/<slug>/ is
	// treated as relative to that checkout even when the slug is missing from
	// Roots, which closes the window between a checkout being created and the
	// next roots refresh. The rule is purely lexical and does no IO.
	Containers []string
}

// degenerate reports whether the scope carries no roots at all, so path
// evaluation falls back to the plain lexical Evaluate.
func (s PathScope) degenerate() bool {
	return strings.TrimSpace(s.Cwd) == "" && len(s.Roots) == 0 && len(s.Containers) == 0
}

// normalizePathInput converts a tool-supplied path into the spelling rules are
// matched against, using "/" separators so rules are portable across
// platforms:
//
//  1. Lexical cleanup: the path is tilde-expanded and, when relative, joined
//     onto Cwd, then cleaned.
//  2. The longest matching scope root wins (Cwd, the snapshot roots, and any
//     root derived from a container).
//  3. A hit becomes the root-relative spelling, so relative rules match the
//     same repository-relative path in every checkout.
//  4. An absolute path that hits no root is retried once through the nearest
//     existing ancestor's EvalSymlinks, because a file being created does not
//     exist yet but its parent does.
//  5. Otherwise the absolute spelling is kept, which only absolute rules and
//     "*" can match.
func normalizePathInput(input string, scope PathScope) string {
	p := strings.TrimSpace(input)
	if p == "" {
		return p
	}
	resolved, err := pathutil.ResolveInDir(p, strings.TrimSpace(scope.Cwd))
	if err != nil {
		resolved = filepath.Clean(p)
	}
	if rel, ok := scope.relativize(resolved); ok {
		return rel
	}
	if filepath.IsAbs(resolved) {
		if real, ok := resolveSymlinksBestEffort(resolved); ok {
			if rel, ok := scope.relativize(real); ok {
				return rel
			}
		}
	}
	return filepath.ToSlash(resolved)
}

// relativize maps an absolute path onto the longest matching scope root and
// returns it in "/"-separated relative form. The bool is false when no root
// contains the path.
func (s PathScope) relativize(path string) (string, bool) {
	best := ""
	bestRel := ""
	consider := func(root string) {
		root = strings.TrimSpace(root)
		if root == "" || !filepath.IsAbs(root) {
			return
		}
		root = filepath.Clean(root)
		rel, ok := pathutil.RelToBase(path, root)
		if !ok {
			return
		}
		if best == "" || len(root) > len(best) {
			best, bestRel = root, rel
		}
	}
	consider(s.Cwd)
	for _, root := range s.Roots {
		consider(root)
	}
	for _, container := range s.Containers {
		if root, ok := containerRoot(container, path); ok {
			consider(root)
		}
	}
	if best == "" {
		return "", false
	}
	return filepath.ToSlash(bestRel), true
}

// containerRoot derives the checkout root for path when path sits below a
// container whose immediate children are checkouts, i.e.
// <container>/<slug>/... projects onto the root <container>/<slug>.
func containerRoot(container, path string) (string, bool) {
	container = strings.TrimSpace(container)
	if container == "" || !filepath.IsAbs(container) {
		return "", false
	}
	rel, ok := pathutil.RelToBase(path, container)
	if !ok || rel == "." {
		return "", false
	}
	seg := rel
	if i := strings.IndexByte(rel, filepath.Separator); i >= 0 {
		seg = rel[:i]
	}
	if seg == "" || seg == "." || seg == ".." {
		return "", false
	}
	return filepath.Join(container, seg), true
}

// resolveSymlinksBestEffort re-spells path through the nearest existing
// ancestor's EvalSymlinks result, so a path that only differs from a root by a
// symlinked prefix still matches. Returns false when nothing resolves.
func resolveSymlinksBestEffort(path string) (string, bool) {
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
