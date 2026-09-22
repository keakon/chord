package worktree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
)

func newResolveWorktree(t *testing.T, repo string, pl *config.PathLocator, name string) *Info {
	t.Helper()
	info, err := Create(context.Background(), CreateOptions{Name: name, RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatalf("Create(%s): %v", name, err)
	}
	return info
}

func TestResolveByPathMatchesCanonicalSpelling(t *testing.T) {
	ctx := context.Background()
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	info := newResolveWorktree(t, repo, pl, "feat-resolve")

	got, err := ResolveByPath(ctx, repo, info.Path, "")
	if err != nil {
		t.Fatalf("ResolveByPath: %v", err)
	}
	if got.Name != info.Name || got.Path != info.Path || got.Branch != info.Branch || got.RepoRoot != repo {
		t.Fatalf("resolved = %#v, want %#v", got, info)
	}

	// A non-canonical spelling of the same checkout still resolves: the
	// recorded path may carry redundant separators or a trailing dot.
	noisy := filepath.Join(info.Path, ".") + string(filepath.Separator)
	if got, err := ResolveByPath(ctx, repo, noisy, ""); err != nil || got.Path != info.Path {
		t.Fatalf("ResolveByPath(%q) = %#v, %v; want path %s", noisy, got, err, info.Path)
	}

	// A subdirectory of the worktree is not its root.
	if _, err := ResolveByPath(ctx, repo, filepath.Join(info.Path, "sub"), ""); err == nil {
		t.Fatal("expected a subdirectory to be rejected as a worktree root")
	}

	// A symlink to the checkout resolves to the same worktree: the recorded
	// path may have been reached through a link.
	link := filepath.Join(t.TempDir(), "wt-link")
	if err := os.Symlink(info.Path, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if got, err := ResolveByPath(ctx, repo, link, ""); err != nil || got.Path != info.Path {
		t.Fatalf("ResolveByPath(symlink) = %#v, %v; want path %s", got, err, info.Path)
	}
}

func TestResolveByPathRejectsNonWorktree(t *testing.T) {
	ctx := context.Background()
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	newResolveWorktree(t, repo, pl, "feat-resolve")

	if _, err := ResolveByPath(ctx, repo, "  ", ""); err == nil || !strings.Contains(err.Error(), "empty worktree path") {
		t.Fatalf("empty path err = %v, want an empty-path error", err)
	}
	// The main checkout is a checkout of the repository, but not one of its
	// linked worktrees.
	if _, err := ResolveByPath(ctx, repo, repo, ""); err == nil {
		t.Fatal("expected the main checkout to be rejected")
	}
	// A directory that is not a checkout of this repository at all.
	if _, err := ResolveByPath(ctx, repo, t.TempDir(), ""); err == nil {
		t.Fatal("expected a foreign directory to be rejected")
	}
}

func TestIsWorktreeOfRequiresLinkedCheckoutOfMain(t *testing.T) {
	ctx := context.Background()
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	info := newResolveWorktree(t, repo, pl, "feat-of")

	if !IsWorktreeOf(ctx, info.Path, repo) {
		t.Fatalf("own worktree %s not recognized", info.Path)
	}
	if IsWorktreeOf(ctx, repo, repo) {
		t.Fatal("the main checkout must not be reported as a linked worktree")
	}
	if IsWorktreeOf(ctx, filepath.Join(info.Path, "missing"), repo) {
		t.Fatal("a missing directory must not be recognized")
	}
	if IsWorktreeOf(ctx, info.Path, setupTestRepo(t)) {
		t.Fatal("a worktree of another repository must not match")
	}
	if IsWorktreeOf(ctx, info.Path, "") || IsWorktreeOf(ctx, "", repo) {
		t.Fatal("empty arguments must report false")
	}
}

func TestDiffStatCoversCommittedAndUncommittedChanges(t *testing.T) {
	ctx := context.Background()
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	info := newResolveWorktree(t, repo, pl, "feat-diff")
	base := gitRevParse(t, repo, "HEAD")

	if got, err := DiffStat(ctx, info.Path, base); err != nil || got != "(no changes)" {
		t.Fatalf("clean stat = %q, %v; want (no changes)", got, err)
	}

	// One committed file plus one uncommitted edit to a tracked file: the stat
	// must cover both, because a worktree may hold commits and live edits at the
	// same time. Untracked files are outside `git diff --stat`; they reach the
	// owner through the worker's reported files instead.
	if err := os.WriteFile(filepath.Join(info.Path, "committed.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, info.Path, "add", "committed.txt")
	runTestGit(t, info.Path, "commit", "-q", "-m", "add committed")
	if err := os.WriteFile(filepath.Join(info.Path, "README.md"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := DiffStat(ctx, info.Path, base)
	if err != nil {
		t.Fatalf("DiffStat: %v", err)
	}
	for _, want := range []string{"committed.txt", "README.md"} {
		if !strings.Contains(got, want) {
			t.Fatalf("stat %q missing %q", got, want)
		}
	}

	if _, err := DiffStat(ctx, "  ", base); err == nil {
		t.Fatal("empty directory must be rejected")
	}
}
