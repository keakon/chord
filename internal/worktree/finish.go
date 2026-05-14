package worktree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/keakon/chord/internal/config"
)

// FinishOptions controls Finish. Finish is the "happy path" workflow for
// bringing a chord-managed worktree branch back onto the main line and then
// reclaiming the worktree.
//
// Finish is intentionally conservative: it does not fetch from the network
// and relies solely on local refs.
type FinishOptions struct {
	// Onto is the target branch that receives a single squashed commit built
	// from the finished worktree branch's final tree state. When empty, Finish
	// uses the main worktree's current branch (git branch --show-current).
	//
	// Onto must be a local branch that can be checked out (e.g. "main").
	Onto string
	// Check previews whether the target branch can merge cleanly into the
	// worktree branch without mutating the real worktree branch or the target
	// branch.
	Check bool
	// Message overrides the generated squash commit message. Empty falls back
	// to the default squash message handling.
	Message string
	// BranchPrefix scopes the lookup of the worktree's name to a specific
	// prefix. Empty falls back to DefaultBranchPrefix. Must match the prefix
	// Create used.
	BranchPrefix string
}

// Finish merges the target branch into the real worktree branch, then squashes
// the finished worktree state back onto that target branch as a single commit,
// fast-forwards the target branch to include the squashed result, and finally
// removes the worktree and deletes its branch.
//
// The sequence is:
//  1. (real wt)  git merge <onto>
//  2. (temp)     git worktree add --detach <tmp> <onto>
//  3. (temp)     git checkout -b <tmp-branch>
//  4. (temp)     git merge --squash <worktree-branch>
//  5. (temp)     git commit (using the generated squash message)
//  6. (main)     git checkout <onto> (when needed)
//  7. (main)     git merge --ff-only <tmp-branch>
//  8. Remove(worktree) + delete the original worktree branch
//
// If the finished worktree branch has no net diff relative to the target
// branch, Finish skips steps 2-7 and simply reclaims the worktree/branch after
// the target branch has been merged into the real worktree branch.
func Finish(ctx context.Context, repoRoot, name string, opts FinishOptions, pathLocator *config.PathLocator) error {
	if !opts.Check && pathLocator == nil {
		return fmt.Errorf("finish worktree: nil PathLocator")
	}
	mainRoot, info, onto, err := prepareFinish(ctx, repoRoot, name, opts)
	if err != nil {
		return err
	}
	if opts.Check {
		return checkFinish(ctx, mainRoot, info, name, onto)
	}

	hadPreMergeDiff, err := branchesDiffer(ctx, mainRoot, onto, info.Branch)
	if err != nil {
		return err
	}
	if hadPreMergeDiff {
		if err := ensureCommitIdentity(ctx, mainRoot); err != nil {
			return err
		}
	}

	if err := mergeOnto(ctx, info.Path, onto); err != nil {
		return formatConflictError(err, info.Path, name, onto, finishConflictHelp(name, info.Path), "finish worktree")
	}

	hasPostMergeDiff, err := branchesDiffer(ctx, mainRoot, onto, info.Branch)
	if err != nil {
		return err
	}
	if hasPostMergeDiff {
		tmpPath, tmpBranch, cleanup, err := createFinishScratch(ctx, mainRoot, name, onto)
		if err != nil {
			return err
		}
		defer cleanup()

		if err := applySquash(ctx, tmpPath, info.Branch); err != nil {
			return fmt.Errorf("squash finished worktree %q onto %q: %w", name, onto, err)
		}
		if err := commitSquash(ctx, tmpPath, name, opts.Message); err != nil {
			return fmt.Errorf("commit squashed worktree %q into %q: %w", name, onto, err)
		}

		mainBranch, err := runGitText(ctx, mainRoot, "branch", "--show-current")
		if err != nil {
			return err
		}
		if mainBranch != onto {
			if _, err := runGit(ctx, mainRoot, "checkout", onto); err != nil {
				return fmt.Errorf("checkout %q in main repository: %w", onto, err)
			}
		}
		if _, err := runGit(ctx, mainRoot, "merge", "--ff-only", tmpBranch); err != nil {
			return fmt.Errorf("fast-forward %q to the squashed worktree result: %w", onto, err)
		}
	}

	removeOpts := RemoveOptions{DeleteBranch: false, BranchPrefix: opts.BranchPrefix}
	if err := Remove(ctx, mainRoot, name, removeOpts, pathLocator); err != nil {
		return fmt.Errorf("remove worktree %q after squash finish: %w", name, err)
	}
	if _, err := runGit(ctx, mainRoot, "branch", "-D", info.Branch); err != nil {
		return fmt.Errorf("delete squashed worktree branch %q: %w", info.Branch, err)
	}
	return nil
}

func prepareFinish(ctx context.Context, repoRoot, name string, opts FinishOptions) (mainRoot string, info *Info, onto string, err error) {
	if err := ValidateSlug(name); err != nil {
		return "", nil, "", err
	}
	mainRoot, err = GitMainRoot(ctx, repoRoot)
	if err != nil {
		return "", nil, "", err
	}
	info, err = ResolveByName(ctx, mainRoot, name, opts.BranchPrefix)
	if err != nil {
		return "", nil, "", err
	}

	onto = strings.TrimSpace(opts.Onto)
	if onto == "" {
		onto, err = CurrentBranch(ctx, mainRoot)
		if err != nil {
			return "", nil, "", err
		}
		if onto == "" {
			return "", nil, "", fmt.Errorf("cannot determine main branch (detached HEAD in %s); pass --onto", mainRoot)
		}
	}
	if onto == info.Branch {
		return "", nil, "", fmt.Errorf("target branch %q equals worktree branch; pass --onto to choose the main branch", onto)
	}

	if dirty, ok := IsDirty(ctx, info.Path); ok && dirty {
		return "", nil, "", fmt.Errorf("worktree %q has uncommitted changes; commit/stash them before finishing", name)
	}
	if dirty, ok := IsDirty(ctx, mainRoot); ok && dirty {
		return "", nil, "", fmt.Errorf("main repository has uncommitted changes; clean it before finishing")
	}

	wtBranch, err := CurrentBranch(ctx, info.Path)
	if err != nil {
		return "", nil, "", err
	}
	if wtBranch == "" {
		return "", nil, "", fmt.Errorf("worktree %q is in detached HEAD state; check out %q (or recreate the worktree) before finishing", name, info.Branch)
	}
	if wtBranch != info.Branch {
		return "", nil, "", fmt.Errorf("worktree %q is currently on branch %q (expected %q); check out %q (or pass the correct worktree name)", name, wtBranch, info.Branch, info.Branch)
	}

	if dir, ok, derr := detectRebaseInProgress(ctx, info.Path); derr != nil {
		return "", nil, "", derr
	} else if ok {
		return "", nil, "", fmt.Errorf("worktree %q already has a rebase in progress (%s); resolve it (git rebase --continue/--skip/--abort) before finishing", name, dir)
	}
	if file, ok, merr := detectMergeInProgress(ctx, info.Path); merr != nil {
		return "", nil, "", merr
	} else if ok {
		return "", nil, "", fmt.Errorf("worktree %q already has a merge in progress (%s); resolve it (git status; then git commit or git merge --abort) before finishing", name, file)
	}
	return mainRoot, info, onto, nil
}

func checkFinish(ctx context.Context, mainRoot string, info *Info, name, onto string) error {
	tmpPath, _, cleanup, err := createFinishScratch(ctx, mainRoot, name, info.Branch)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := mergeOnto(ctx, tmpPath, onto); err != nil {
		return formatConflictError(err, tmpPath, name, onto, finishCheckHelp(name, onto, info.Path), "finish check for worktree")
	}
	return nil
}

func branchesDiffer(ctx context.Context, cwd, onto, branch string) (bool, error) {
	baseTree, err := runGitText(ctx, cwd, "rev-parse", onto+"^{tree}")
	if err != nil {
		return false, fmt.Errorf("resolve tree for %q: %w", onto, err)
	}
	branchTree, err := runGitText(ctx, cwd, "rev-parse", branch+"^{tree}")
	if err != nil {
		return false, fmt.Errorf("resolve tree for %q: %w", branch, err)
	}
	return baseTree != branchTree, nil
}

func createFinishScratch(ctx context.Context, mainRoot, name, startRef string) (tmpPath, tmpBranch string, cleanup func(), err error) {
	tmpParent, err := os.MkdirTemp("", "chord-finish-*")
	if err != nil {
		return "", "", nil, fmt.Errorf("create temporary finish directory: %w", err)
	}
	cleanup = func() {
		_, _ = runGit(context.Background(), mainRoot, "worktree", "remove", "--force", tmpPath)
		if tmpBranch != "" {
			_, _ = runGit(context.Background(), mainRoot, "branch", "-D", tmpBranch)
		}
		_ = os.RemoveAll(tmpParent)
	}

	tmpPath = filepath.Join(tmpParent, name)
	if _, err := runGit(ctx, mainRoot, "worktree", "add", "--detach", tmpPath, startRef); err != nil {
		cleanup()
		return "", "", nil, fmt.Errorf("create temporary finish worktree for %q: %w", name, err)
	}
	// Use an internal branch so the temporary worktree has a writable branch
	// and, for the squash phase, a stable ref that the main worktree can fast-
	// forward to.
	tmpBranch = fmt.Sprintf("chord-tmp/finish-%s-%d", name, time.Now().UnixNano())
	if _, err := runGit(ctx, tmpPath, "checkout", "-q", "-b", tmpBranch); err != nil {
		cleanup()
		return "", "", nil, fmt.Errorf("create temporary finish branch for %q: %w", name, err)
	}
	return tmpPath, tmpBranch, cleanup, nil
}

func mergeOnto(ctx context.Context, cwd, onto string) error {
	_, err := runGit(ctx, cwd, "merge", "--no-edit", onto)
	return err
}

func applySquash(ctx context.Context, cwd, branch string) error {
	_, err := runGit(ctx, cwd, "merge", "--squash", branch)
	return err
}

func ensureCommitIdentity(ctx context.Context, cwd string) error {
	if hasCommitIdentityEnv() {
		return nil
	}
	if _, err := runGitText(ctx, cwd, "var", "GIT_AUTHOR_IDENT"); err != nil {
		return fmt.Errorf("finish worktree requires git author/committer identity for the squash commit; configure git user.name and user.email (or set GIT_AUTHOR_* / GIT_COMMITTER_*): %w", err)
	}
	if _, err := runGitText(ctx, cwd, "var", "GIT_COMMITTER_IDENT"); err != nil {
		return fmt.Errorf("finish worktree requires git author/committer identity for the squash commit; configure git user.name and user.email (or set GIT_AUTHOR_* / GIT_COMMITTER_*): %w", err)
	}
	return nil
}

func hasCommitIdentityEnv() bool {
	return strings.TrimSpace(os.Getenv("GIT_AUTHOR_NAME")) != "" &&
		strings.TrimSpace(os.Getenv("GIT_AUTHOR_EMAIL")) != "" &&
		strings.TrimSpace(os.Getenv("GIT_COMMITTER_NAME")) != "" &&
		strings.TrimSpace(os.Getenv("GIT_COMMITTER_EMAIL")) != ""
}

func commitSquash(ctx context.Context, cwd, name, message string) error {
	if strings.TrimSpace(message) != "" {
		_, err := runGit(ctx, cwd, "commit", "-q", "-m", message)
		return err
	}
	msgPath, err := runGitText(ctx, cwd, "rev-parse", "--git-path", "SQUASH_MSG")
	if err == nil && msgPath != "" {
		checkPath := resolveGitPath(cwd, msgPath)
		if _, statErr := os.Stat(checkPath); statErr == nil {
			if _, err := runGit(ctx, cwd, "commit", "-q", "-F", msgPath); err == nil {
				return nil
			}
		}
	}
	_, err = runGit(ctx, cwd, "commit", "-q", "-m", fmt.Sprintf("Finish worktree %s", name))
	return err
}

func conflictedFiles(ctx context.Context, cwd string) ([]string, error) {
	out, err := runGitText(ctx, cwd, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	var files []string
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

func resolveGitPath(cwd, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(cwd, path)
}

func finishCheckHelp(name, onto, worktreePath string) string {
	return fmt.Sprintf(
		"No changes were made to the real worktree, branch, or target branch.\n\n"+
			"To reduce finish-time conflicts before finishing:\n"+
			"  cd %s\n"+
			"  git status\n"+
			"  git merge %s\n\n"+
			"Resolve any conflicts, complete the merge commit, then re-run:\n"+
			"  chord worktree finish %s\n",
		worktreePath, onto, name,
	)
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
		if _, err := os.Stat(resolveGitPath(cwd, dir)); err == nil {
			return resolveGitPath(cwd, dir), true, nil
		}
	}
	return "", false, nil
}

func detectMergeInProgress(ctx context.Context, cwd string) (string, bool, error) {
	file, err := runGitText(ctx, cwd, "rev-parse", "--git-path", "MERGE_HEAD")
	if err != nil {
		return "", false, err
	}
	if file == "" {
		return "", false, nil
	}
	resolved := resolveGitPath(cwd, file)
	if _, err := os.Stat(resolved); err == nil {
		return resolved, true, nil
	}
	return "", false, nil
}

func formatConflictError(cause error, conflictPath, name, onto, help, prefix string) error {
	conflicts, err := conflictedFiles(context.Background(), conflictPath)
	if err == nil && len(conflicts) > 0 {
		return fmt.Errorf("%s %q with %q would conflict: %w\n\nConflicted files:\n  %s\n\n%s", prefix, name, onto, cause, strings.Join(conflicts, "\n  "), help)
	}
	return fmt.Errorf("%s %q with %q would fail: %w\n\n%s", prefix, name, onto, cause, help)
}

func finishConflictHelp(name, worktreePath string) string {
	return fmt.Sprintf(
		"The target branch was left unchanged.\n"+
			"The real worktree branch now has a merge in progress.\n\n"+
			"To resolve and finish:\n"+
			"  cd %s\n"+
			"  git status\n\n"+
			"Resolve any conflicts, complete the merge commit, then re-run:\n"+
			"  chord worktree finish %s\n",
		worktreePath, name,
	)
}
