package worktree

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/keakon/chord/internal/config"
)

// FinishOptions controls Finish. Finish is the "happy path" workflow for
// bringing a chord-managed worktree branch back onto the main line and then
// reclaiming the worktree.
//
// Finish is intentionally conservative: it does not fetch from the network
// and relies solely on local refs.
type FinishOptions struct {
	// Onto is the target branch to rebase onto AND the branch that will be
	// fast-forwarded in the main worktree. When empty, Finish uses the main
	// worktree's current branch (git branch --show-current).
	//
	// Onto must be a local branch that can be checked out (e.g. "main").
	Onto string
	// Force relaxes the "clean tree" checks and will force-delete the
	// worktree branch when reclaiming it.
	Force bool
	// BranchPrefix scopes the lookup of the worktree's name to a specific
	// prefix. Empty falls back to DefaultBranchPrefix. Must match the prefix
	// Create used.
	BranchPrefix string
}

// Finish rebases the given worktree branch onto the chosen main branch,
// fast-forwards that main branch to include the worktree commits, then
// removes the worktree and deletes its branch.
//
// The sequence is:
//  1. (worktree) git rebase <onto>
//  2. (main)     git checkout <onto> (when needed)
//  3. (main)     git merge --ff-only <worktree-branch>
//  4. Remove(worktree, --delete-branch)
//
// If any step fails, Finish aborts without removing the worktree/branch.
func Finish(ctx context.Context, repoRoot, name string, opts FinishOptions, pathLocator *config.PathLocator) error {
	if err := ValidateSlug(name); err != nil {
		return err
	}
	if pathLocator == nil {
		return fmt.Errorf("finish worktree: nil PathLocator")
	}
	mainRoot, err := GitMainRoot(ctx, repoRoot)
	if err != nil {
		return err
	}
	info, err := ResolveByName(ctx, mainRoot, name, opts.BranchPrefix)
	if err != nil {
		return err
	}

	onto := strings.TrimSpace(opts.Onto)
	if onto == "" {
		onto, err = runGitText(ctx, mainRoot, "branch", "--show-current")
		if err != nil {
			return err
		}
		if onto == "" {
			return fmt.Errorf("cannot determine main branch (detached HEAD in %s); pass --onto", mainRoot)
		}
	}
	if onto == info.Branch {
		return fmt.Errorf("target branch %q equals worktree branch; pass --onto to choose the main branch", onto)
	}

	if !opts.Force {
		if dirty, ok := IsDirty(ctx, info.Path); ok && dirty {
			return fmt.Errorf("worktree %q has uncommitted changes; commit/stash them or pass --force", name)
		}
		if dirty, ok := IsDirty(ctx, mainRoot); ok && dirty {
			return fmt.Errorf("main repository has uncommitted changes; clean it or pass --force")
		}
	}

	// 1) Rebase the chord-managed worktree branch onto the target branch.
	// Be explicit about the branch: users might have checked out something else
	// inside the worktree, but `finish <name>` is defined in terms of the
	// chord-managed branch for that worktree.
	wtBranch, err := runGitText(ctx, info.Path, "branch", "--show-current")
	if err != nil {
		return err
	}
	if wtBranch == "" {
		return fmt.Errorf("worktree %q is in detached HEAD state; check out %q (or recreate the worktree) before finishing", name, info.Branch)
	}
	if wtBranch != info.Branch {
		return fmt.Errorf("worktree %q is currently on branch %q (expected %q); check out %q (or pass the correct worktree name)", name, wtBranch, info.Branch, info.Branch)
	}

	// Avoid starting a nested rebase. A failed rebase is intentionally left for
	// the user to resolve; if one is already in progress, we should not attempt
	// to run another `git rebase` from Finish.
	if dir, ok, derr := detectRebaseInProgress(ctx, info.Path); derr != nil {
		return derr
	} else if ok {
		return fmt.Errorf("worktree %q already has a rebase in progress (%s); resolve it (git rebase --continue/--skip/--abort) and then re-run `chord worktree finish %s`", name, dir, name)
	}

	// Best-effort preflight: if the worktree branch contains commits that are
	// patch-equivalent to commits already in the target branch, rebase is more
	// likely to hit add/add conflicts. We do not auto-skip anything here; the
	// warning is carried via the eventual error message when rebase fails.
	var redundant []string
	if lines, lerr := listRedundantCherryCommits(ctx, mainRoot, onto, info.Branch); lerr == nil {
		redundant = lines
	}

	rebaseArgs := []string{"rebase"}
	if opts.Force {
		// Best-effort convenience. Still safe because git keeps the stash/ORIG_HEAD
		// if it cannot complete.
		rebaseArgs = append(rebaseArgs, "--autostash")
	}
	rebaseArgs = append(rebaseArgs, onto)
	if _, err := runGit(ctx, info.Path, rebaseArgs...); err != nil {
		return fmt.Errorf("rebase worktree %q onto %q: %w\n\n%s", name, onto, err, finishRebaseHelp(name, onto, info.Path, redundant))
	}

	// 2) Ensure the main worktree is on the target branch.
	mainBranch, err := runGitText(ctx, mainRoot, "branch", "--show-current")
	if err != nil {
		return err
	}
	if mainBranch != onto {
		if _, err := runGit(ctx, mainRoot, "checkout", onto); err != nil {
			return fmt.Errorf("checkout %q in main repository: %w", onto, err)
		}
	}

	// 3) Fast-forward main onto the rebased worktree branch.
	if _, err := runGit(ctx, mainRoot, "merge", "--ff-only", info.Branch); err != nil {
		return fmt.Errorf("fast-forward %q to %q: %w", onto, info.Branch, err)
	}

	// 4) Reclaim worktree + branch.
	removeOpts := RemoveOptions{Force: opts.Force, DeleteBranch: true, BranchPrefix: opts.BranchPrefix}
	if err := Remove(ctx, mainRoot, name, removeOpts, pathLocator); err != nil {
		return fmt.Errorf("remove worktree %q after merge: %w", name, err)
	}
	return nil
}

func detectRebaseInProgress(ctx context.Context, cwd string) (string, bool, error) {
	// In both apply and merge backend, `git rebase` writes state under
	// rebase-apply or rebase-merge.
	for _, p := range []string{"rebase-merge", "rebase-apply"} {
		dir, err := runGitText(ctx, cwd, "rev-parse", "--git-path", p)
		if err != nil {
			return "", false, err
		}
		if dir == "" {
			continue
		}
		if _, err := os.Stat(dir); err == nil {
			return dir, true, nil
		}
	}
	return "", false, nil
}

func finishRebaseHelp(name, onto, worktreePath string, redundantCommits []string) string {
	// Keep this message copy-pastable; users are already in an error path.
	extra := ""
	if len(redundantCommits) > 0 {
		extra = "Preflight: some commits on the worktree branch look redundant vs the target branch (git cherry reports patch-equivalent commits):\n"
		for _, line := range redundantCommits {
			extra += "  " + line + "\n"
		}
		extra += "\n"
	}
	return fmt.Sprintf(
		"The worktree and branch were kept.\n\n"+
			"%s"+
			"To resolve:\n"+
			"  cd %s\n"+
			"  git status\n"+
			"  git rebase --show-current-patch\n\n"+
			"Then choose one:\n"+
			"  1) If the current patch is redundant (e.g. add/add \"both added\" conflicts because %q already has the files), skip it:\n"+
			"     git rebase --skip\n"+
			"  2) Otherwise resolve conflicts, then:\n"+
			"     git add/rm <conflicted_files>\n"+
			"     git rebase --continue\n"+
			"  3) To abort and restore the pre-rebase state:\n"+
			"     git rebase --abort\n\n"+
			"After the rebase finishes, re-run:\n"+
			"  chord worktree finish %s\n",
		extra, worktreePath, onto, name,
	)
}

func listRedundantCherryCommits(ctx context.Context, cwd, onto, branch string) ([]string, error) {
	out, err := runGitText(ctx, cwd, "cherry", "-v", onto, branch)
	if err != nil {
		return nil, err
	}
	var redundant []string
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "- ") {
			redundant = append(redundant, line)
			if len(redundant) >= 5 {
				// Keep the hint short; the full history is in `git cherry -v`.
				break
			}
		}
	}
	return redundant, nil
}
