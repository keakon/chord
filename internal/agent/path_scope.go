package agent

import (
	"github.com/keakon/chord/internal/permission"
)

// PathRootsResolver returns the canonical roots of every checkout of the
// repository plus the container directories whose immediate children are
// checkout roots (the configured worktree root). cmd/chord injects it because
// it is the layer that resolves the repository's git topology and configured
// worktree root; the worktree tools in this package only switch the agent's
// own active checkout, they never extend the policy roots.
type PathRootsResolver func() (roots, containers []string)

// pathRootsSnapshot is the immutable result of a PathRootsResolver for one
// turn. It is swapped whole rather than mutated so tool goroutines can read it
// without locking.
type pathRootsSnapshot struct {
	roots      []string
	containers []string
}

// SetPathRootsResolver installs the repository checkout resolver. Call it
// before the agent starts running turns; the snapshot is refreshed at each
// turn start, and by worktree-changing tools once they exist.
func (a *MainAgent) SetPathRootsResolver(fn PathRootsResolver) {
	if a == nil {
		return
	}
	a.pathRootsResolver = fn
}

// SetPathRootsInvalidator installs the mutation hook for the resolver cache.
// Worktree-changing operations call it before refreshing their next scope;
// ordinary turns can reuse the last topology snapshot.
func (a *MainAgent) SetPathRootsInvalidator(fn func()) {
	if a == nil {
		return
	}
	a.pathRootsInvalidator = fn
}

// refreshPathRoots re-resolves the checkout snapshot for the turn about to
// start, so a worktree created since the previous turn is already in scope.
func (a *MainAgent) refreshPathRoots() {
	if a == nil || a.pathRootsResolver == nil {
		return
	}
	roots, containers := a.pathRootsResolver()
	a.pathRoots.Store(&pathRootsSnapshot{roots: roots, containers: containers})
}

func (a *MainAgent) invalidatePathRoots() {
	if a == nil {
		return
	}
	if a.pathRootsInvalidator != nil {
		a.pathRootsInvalidator()
	}
	a.refreshPathRoots()
}

// effectivePathScope returns the path evaluation scope for the agent's next
// tool call: the tool base dir plus the current checkout snapshot.
func (a *MainAgent) effectivePathScope() permission.PathScope {
	if a == nil {
		return permission.PathScope{}
	}
	scope := permission.PathScope{Cwd: a.effectiveToolBaseDir()}
	if snap := a.pathRoots.Load(); snap != nil {
		scope.Roots = snap.roots
		scope.Containers = snap.containers
	}
	return scope
}

// effectivePathScope mirrors MainAgent's scope for a SubAgent: its own workDir
// with the parent's checkout snapshot, since sub-agents share the repository's
// policy roots. A parentless sub-agent carries no roots and keeps cwd-only
// path rules.
func (s *SubAgent) effectivePathScope() permission.PathScope {
	if s == nil {
		return permission.PathScope{}
	}
	scope := permission.PathScope{Cwd: s.effectiveToolBaseDir()}
	if s.parent != nil {
		if snap := s.parent.pathRoots.Load(); snap != nil {
			scope.Roots = snap.roots
			scope.Containers = snap.containers
		}
	}
	return scope
}
