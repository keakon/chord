package config

// WorktreeConfig controls chord's git worktree integration. It is used by the
// startup `--worktree` flag, the `chord worktree …` subcommand, and the
// WorktreeEnter/WorktreeExit tools.
//
// Two knobs decide what chord owns and where it puts checkouts: the branch
// prefix, and the directory worktrees are created under. Which gitignored
// files a fresh worktree receives is declared by the repository's
// `.worktreeinclude` file, not configured here.
type WorktreeConfig struct {
	// BranchPrefix overrides the default "chord/" prefix used for branch
	// names (`<prefix><slug>`) and for filtering chord-managed worktrees
	// out of `git worktree list --porcelain`. Trailing "/" is appended
	// automatically when omitted; an empty value falls back to "chord/".
	BranchPrefix string `json:"branch_prefix,omitempty" yaml:"branch_prefix,omitempty"`
	// Root overrides where chord creates worktrees. Empty keeps the
	// historical state-dir layout (<stateDir>/worktrees/<repoID>/<slug>),
	// which lives outside the repository. A relative path resolves against
	// the main repository root, so `.chord/worktrees` places checkouts at
	// <repo>/.chord/worktrees/<slug>; an absolute path is used as-is.
	//
	// When Root resolves inside the repository, chord also keeps a
	// self-ignoring `.gitignore` in that directory so the checkout does not
	// show the worktrees as untracked. That file only keeps `git status`
	// clean; it is not a protection mechanism.
	Root string `json:"root,omitempty" yaml:"root,omitempty"`
}
