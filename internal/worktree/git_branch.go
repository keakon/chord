package worktree

import "context"

// CurrentBranch returns the current checked-out branch name in dir.
//
// It is best-effort and returns an empty string when dir is in detached HEAD
// state.
func CurrentBranch(ctx context.Context, dir string) (string, error) {
	out, err := runGitText(ctx, dir, "branch", "--show-current")
	return out, err
}
