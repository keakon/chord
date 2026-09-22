package main

import (
	"context"
	"slices"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/worktree"
)

// resolveWorktreeBranchPrefix normalizes the configured branch prefix once, so
// the policy-root resolver and the worktree tools agree on which worktrees are
// chord-managed. Invalid values are logged and fall back to the default.
func resolveWorktreeBranchPrefix(cfg *config.Config) string {
	if cfg == nil {
		return worktree.DefaultBranchPrefix
	}
	normalized, err := worktree.NormalizeBranchPrefix(cfg.Worktree.BranchPrefix)
	if err != nil {
		log.Warnf("failed to normalize worktree branch prefix prefix=%v error=%v", cfg.Worktree.BranchPrefix, err)
		return worktree.DefaultBranchPrefix
	}
	return normalized
}

// newPathRootsResolver returns the checkout-roots resolver injected into the
// agent for policy-root path evaluation. Storage paths and the worktree
// location are pinned at startup; only the checkout set itself is re-listed,
// once per turn.
func newPathRootsResolver(ctx context.Context, contentRoot string, pl *config.PathLocator, worktreeRoot string) agent.PathRootsResolver {
	return func() (roots, containers []string) {
		return resolvePathRoots(ctx, contentRoot, pl, worktreeRoot)
	}
}

// resolvePathRoots returns the canonical roots of every checkout of the
// repository anchored at contentRoot, plus the container directory whose
// immediate children are chord-managed worktrees. The container is what lets
// path evaluation derive a root for a worktree created after the snapshot was
// taken, so a fresh checkout cannot silently fall back to the main
// checkout's spelling.
//
// The checkout set is deliberately "every worktree of this repository", not
// "every worktree chord manages": every checkout of the repository contributes
// to one set of relative roots, and scoping the set by chord's own branch prefix
// would make a repository-relative rule match in some checkouts and silently not
// match in others.
func resolvePathRoots(ctx context.Context, contentRoot string, pl *config.PathLocator, configuredRoot string) (roots, containers []string) {
	if ctx == nil {
		ctx = context.Background()
	}
	contentRoot = strings.TrimSpace(contentRoot)
	if contentRoot == "" || pl == nil {
		return nil, nil
	}
	if bare, berr := worktree.IsBareRepository(ctx, contentRoot); berr == nil && bare {
		log.Warnf("bare repository has no working tree; policy roots fall back to the repository directory workdir=%v", contentRoot)
		return []string{contentRoot}, nil
	}
	mainRoot, err := worktree.GitMainRoot(ctx, contentRoot)
	if err != nil || strings.TrimSpace(mainRoot) == "" {
		return nil, nil
	}
	// The main checkout is seeded explicitly so a failed listing still leaves
	// the repository itself covered.
	roots = append(roots, mainRoot)
	if paths, perr := worktree.CheckoutPaths(ctx, mainRoot); perr == nil {
		roots = append(roots, paths...)
	} else {
		log.Warnf("failed to list repository checkouts; policy roots keep the main checkout only main_root=%v error=%v", mainRoot, perr)
	}
	if container, cerr := worktree.WorktreeRoot(pl, mainRoot, configuredRoot); cerr == nil && container != "" {
		containers = append(containers, container)
	}
	return dedupeRootsLongestFirst(roots), containers
}

// dedupeRootsLongestFirst drops duplicate/empty roots and orders the rest by
// descending length, so the intended precedence (a worktree nested inside the
// main checkout) is visible; path evaluation still computes the longest match
// on its own.
func dedupeRootsLongestFirst(roots []string) []string {
	seen := make(map[string]struct{}, len(roots))
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		out = append(out, root)
	}
	slices.SortFunc(out, func(a, b string) int { return len(b) - len(a) })
	return out
}

// resolveContentRoot returns where project content and machine state are
// anchored. Inside a linked worktree that is the main worktree root, so every
// checkout of one repository shares the same project configuration, agent
// definitions, skills, memory, and session storage. Everywhere else — a main
// worktree, a bare repository, a directory outside git — it is workDir itself.
func resolveContentRoot(ctx context.Context, workDir string) string {
	if ctx == nil {
		ctx = context.Background()
	}
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return workDir
	}
	if bare, berr := worktree.IsBareRepository(ctx, workDir); berr == nil && bare {
		log.Warnf("bare repository has no working tree; staying in repository directory workdir=%v", workDir)
		return workDir
	}
	inside, err := worktree.IsInsideLinkedWorktree(ctx, workDir)
	if err != nil {
		log.Warnf("failed to detect linked worktree workdir=%v error=%v", workDir, err)
		return workDir
	}
	if !inside {
		return workDir
	}
	mainRoot, err := worktree.GitMainRoot(ctx, workDir)
	if err != nil || strings.TrimSpace(mainRoot) == "" {
		log.Warnf("failed to resolve git main root workdir=%v error=%v", workDir, err)
		return workDir
	}
	return mainRoot
}
