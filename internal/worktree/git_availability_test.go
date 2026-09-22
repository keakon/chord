package worktree

import (
	"context"
	"errors"
	"os/exec"
	"testing"
)

// requireGit skips a test that needs a working git on PATH.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

// nonRepoDir returns a directory that is not inside a git repository, skipping
// the test when the machine's temp directory happens to be one.
func nonRepoDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := runGitText(context.Background(), dir, "rev-parse", "--git-dir"); err == nil {
		t.Skip("temp dir is inside a git repository")
	}
	return dir
}

func TestGitAvailableFollowsPath(t *testing.T) {
	requireGit(t)
	if !GitAvailable() {
		t.Fatal("GitAvailable() = false with git on PATH")
	}
	t.Setenv("PATH", t.TempDir())
	if GitAvailable() {
		t.Fatal("GitAvailable() = true without git on PATH")
	}
}

// TestGitProbesReportMissingBinary pins the distinction between a machine
// without git and a directory that is not a repository: both make a probe
// fail, but only the latter may be reported as "not a git repository".
func TestGitProbesReportMissingBinary(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	ctx := context.Background()

	if _, err := GitMainRoot(ctx, dir); !errors.Is(err, ErrGitUnavailable) {
		t.Errorf("GitMainRoot error = %v, want ErrGitUnavailable", err)
	}
	if _, err := GitCommonDir(ctx, dir); !errors.Is(err, ErrGitUnavailable) {
		t.Errorf("GitCommonDir error = %v, want ErrGitUnavailable", err)
	}
	if _, err := IsBareRepository(ctx, dir); !errors.Is(err, ErrGitUnavailable) {
		t.Errorf("IsBareRepository error = %v, want ErrGitUnavailable", err)
	}
	if _, err := IsInsideLinkedWorktree(ctx, dir); !errors.Is(err, ErrGitUnavailable) {
		t.Errorf("IsInsideLinkedWorktree error = %v, want ErrGitUnavailable", err)
	}
	if _, err := runGitText(ctx, dir, "rev-parse", "HEAD"); !errors.Is(err, ErrGitUnavailable) {
		t.Errorf("runGitText error = %v, want ErrGitUnavailable", err)
	}
	if errors.Is(ErrGitUnavailable, ErrNotGitRepository) {
		t.Error("a missing binary must not be classified as a repository error")
	}
}

func TestGitProbesReportNonRepository(t *testing.T) {
	requireGit(t)
	dir := nonRepoDir(t)
	ctx := context.Background()

	if _, err := GitMainRoot(ctx, dir); !errors.Is(err, ErrNotGitRepository) {
		t.Errorf("GitMainRoot error = %v, want ErrNotGitRepository", err)
	}
	if _, err := IsBareRepository(ctx, dir); !errors.Is(err, ErrNotGitRepository) {
		t.Errorf("IsBareRepository error = %v, want ErrNotGitRepository", err)
	}
	if _, err := IsInsideLinkedWorktree(ctx, dir); !errors.Is(err, ErrNotGitRepository) {
		t.Errorf("IsInsideLinkedWorktree error = %v, want ErrNotGitRepository", err)
	}
}
