package config

// WorktreeConfig controls chord's git worktree integration. Used by the
// startup-level `--worktree` flag and the `chord worktree …` subcommand.
//
// Fields are intentionally minimal in v1: directory layout
// (<stateDir>/worktrees/<repoID>/<slug>) and base ref (HEAD) are not
// configurable. Symlink/sparse/post-create-hook are deferred to v2.
type WorktreeConfig struct {
	// BranchPrefix overrides the default "chord/" prefix used for
	// branch names and porcelain filtering. Trailing "/" is allowed but
	// not required.
	BranchPrefix string `json:"branch_prefix,omitempty" yaml:"branch_prefix,omitempty"`

	// RequireClean refuses worktree creation when the main repository
	// has uncommitted changes. Default false: chord follows
	// `git worktree add` semantics and warns instead of failing, since
	// the worktree starts from HEAD anyway.
	RequireClean bool `json:"require_clean,omitempty" yaml:"require_clean,omitempty"`
}
