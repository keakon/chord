package worktree

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// CurrentBranch returns the current checked-out branch name in dir.
//
// It is best-effort and returns an empty string when dir is in detached HEAD
// state.
func CurrentBranch(ctx context.Context, dir string) (string, error) {
	out, err := runGitText(ctx, dir, "branch", "--show-current")
	return out, err
}

// BranchRefExists reports whether refs/heads/<branch> exists in the
// repository at dir. Branch names are matched exactly; patterns are not
// expanded.
func BranchRefExists(ctx context.Context, dir, branch string) (bool, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return false, nil
	}
	out, err := runGitText(ctx, dir, "for-each-ref", "--format=%(refname)", "refs/heads/"+branch)
	if err != nil {
		return false, err
	}
	return out != "", nil
}

// HeadCommit returns the commit dir's HEAD points at. Callers use it as the
// base for a new worktree created from the checkout they are standing in.
func HeadCommit(ctx context.Context, dir string) (string, error) {
	out, err := runGitText(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if out == "" {
		return "", fmt.Errorf("resolve HEAD in %s: empty revision", dir)
	}
	return out, nil
}

// DiffStat returns the compact change summary a worker reports to its owner:
// `git diff --stat` of dir against base. Committed and uncommitted changes are
// both included, because a worktree may hold commits and uncommitted edits at
// the same time. The output is capped so a worktree with thousands of changed
// files cannot flood the completion message.
func DiffStat(ctx context.Context, dir, base string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", fmt.Errorf("diff stat: empty directory")
	}
	args := []string{"diff", "--stat"}
	if base = strings.TrimSpace(base); base != "" {
		args = append(args, base)
	}
	out, err := runGitText(ctx, dir, args...)
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "(no changes)", nil
	}
	const maxDiffStatBytes = 4000
	if len(out) > maxDiffStatBytes {
		out = out[:maxDiffStatBytes] + "\n... (diff stat truncated)"
	}
	return out, nil
}

// BranchFullyMerged reports whether `git branch -d` would delete branch, and
// names the ref it would test: the branch's upstream when one is configured,
// HEAD otherwise — the same criterion git itself applies. Callers use it to
// refuse a branch deletion before taking a checkout apart, so a refusal leaves
// the worktree untouched instead of half-removed.
func BranchFullyMerged(ctx context.Context, dir, branch string) (bool, string, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return false, "", fmt.Errorf("check branch merged: empty branch")
	}
	target := "HEAD"
	if upstream, err := runGitText(ctx, dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", branch+"@{upstream}"); err == nil {
		if upstream = strings.TrimSpace(upstream); upstream != "" {
			target = upstream
		}
	}
	out, err := runGitText(ctx, dir, "rev-list", "--count", branch, "--not", target)
	if err != nil {
		return false, target, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return false, target, fmt.Errorf("parse rev-list count %q: %w", out, err)
	}
	return n == 0, target, nil
}

// UnmergedCommits reports how many commits reachable from branch are not
// reachable from any other ref: commits that would only exist on that branch.
// The branch under test is excluded from the comparison, so a branch whose tip
// is also reachable from the main checkout counts as merged. The comparison
// covers all local and remote branches and tags.
func UnmergedCommits(ctx context.Context, dir, branch string) (int, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return 0, fmt.Errorf("count unmerged commits: empty branch")
	}
	out, err := runGitText(ctx, dir, "rev-list", "--count", branch, "--not", "--exclude="+branch, "--branches", "--remotes", "--tags")
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("parse rev-list count %q: %w", out, err)
	}
	return n, nil
}
