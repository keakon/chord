package config

// WorktreeConfig controls chord's git worktree integration. Used by the
// startup-level `--worktree` flag and the `chord worktree …` subcommand.
//
// Fields are intentionally minimal in v1: directory layout
// (<stateDir>/worktrees/<repoID>/<slug>) and base ref (HEAD) are not
// configurable. Symlink/sparse/post-create-hook are deferred to v2.
type WorktreeConfig struct {
	// BranchPrefix overrides the default "chord/" prefix used for branch
	// names (`<prefix><slug>`) and for filtering chord-managed worktrees
	// out of `git worktree list --porcelain`. Trailing "/" is appended
	// automatically when omitted; an empty value falls back to "chord/".
	BranchPrefix string `json:"branch_prefix,omitempty" yaml:"branch_prefix,omitempty"`
}
