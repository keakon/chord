package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnmergedCommitsTracksCommitsOnlyOnBranch(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()

	info, err := Create(ctx, CreateOptions{Name: "unique", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if n, err := UnmergedCommits(ctx, repo, info.Branch); err != nil || n != 0 {
		t.Fatalf("fresh branch unmerged = %d, err = %v, want 0", n, err)
	}
	if err := os.WriteFile(filepath.Join(info.Path, "work.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runTestGit(t, info.Path, "add", "work.txt")
	runTestGit(t, info.Path, "commit", "-q", "-m", "work")
	if n, err := UnmergedCommits(ctx, repo, info.Branch); err != nil || n != 1 {
		t.Fatalf("after commit unmerged = %d, err = %v, want 1", n, err)
	}
	// Merging the branch into the main checkout makes its tip reachable from
	// another ref, so the branch no longer holds unique commits.
	runTestGit(t, repo, "merge", "--ff-only", info.Branch)
	if n, err := UnmergedCommits(ctx, repo, info.Branch); err != nil || n != 0 {
		t.Fatalf("after merge unmerged = %d, err = %v, want 0", n, err)
	}
}

func TestCreateRespectsExplicitBase(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	first := gitRevParse(t, repo, "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "second.txt"), []byte("second\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runTestGit(t, repo, "add", "second.txt")
	runTestGit(t, repo, "commit", "-q", "-m", "second")
	if second := gitRevParse(t, repo, "HEAD"); second == first {
		t.Fatal("expected a new commit on main")
	}

	info, err := Create(ctx, CreateOptions{Name: "based", RepoRoot: repo, PathLocator: pl, Base: first})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := gitRevParse(t, info.Path, "HEAD"); got != first {
		t.Fatalf("worktree HEAD = %s, want explicit base %s", got, first)
	}
}

func TestCreateNestedRequiresAllowNested(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	parent, err := Create(ctx, CreateOptions{Name: "parent", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatalf("Create parent: %v", err)
	}

	if _, err := Create(ctx, CreateOptions{Name: "child", RepoRoot: parent.Path, PathLocator: pl}); err == nil {
		t.Fatal("nested creation without AllowNested should be refused")
	}
	child, err := Create(ctx, CreateOptions{
		Name:        "child",
		RepoRoot:    parent.Path,
		PathLocator: pl,
		AllowNested: true,
		Base:        gitRevParse(t, parent.Path, "HEAD"),
	})
	if err != nil {
		t.Fatalf("nested Create with AllowNested: %v", err)
	}
	if got, want := gitRevParse(t, child.Path, "HEAD"), gitRevParse(t, parent.Path, "HEAD"); got != want {
		t.Fatalf("child base = %s, want the parent checkout HEAD %s", got, want)
	}
}

func TestCreateChecksOutExistingChordBranch(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	runTestGit(t, repo, "branch", "chord/feature")

	info, err := Create(ctx, CreateOptions{Name: "feature", RepoRoot: repo, PathLocator: pl, Branch: "chord/feature"})
	if err != nil {
		t.Fatalf("Create with existing branch: %v", err)
	}
	if info.Branch != "chord/feature" {
		t.Fatalf("branch = %q, want chord/feature", info.Branch)
	}
	if out := strings.TrimSpace(string(mustRunGit(t, info.Path, "rev-parse", "--abbrev-ref", "HEAD"))); out != "chord/feature" {
		t.Fatalf("worktree branch = %q, want chord/feature", out)
	}

	runTestGit(t, repo, "branch", "plain-feature")
	if _, err := Create(ctx, CreateOptions{Name: "plain", RepoRoot: repo, PathLocator: pl, Branch: "plain-feature"}); err == nil {
		t.Fatal("non chord-managed branch should be refused")
	}
	runTestGit(t, repo, "branch", "chord/other")
	if _, err := Create(ctx, CreateOptions{Name: "other", RepoRoot: repo, PathLocator: pl, Branch: "chord/other", Base: "HEAD"}); err == nil {
		t.Fatal("branch and base are mutually exclusive")
	}
	if _, err := Create(ctx, CreateOptions{Name: "other", RepoRoot: repo, PathLocator: pl, Branch: "chord/other", ResetBranch: true}); err == nil {
		t.Fatal("branch and reset_branch are mutually exclusive")
	}
	if _, err := Create(ctx, CreateOptions{Name: "missing", RepoRoot: repo, PathLocator: pl, Branch: "chord/missing"}); err == nil {
		t.Fatal("checking out a missing branch should be refused")
	}
}

func TestCreateHonorsExplicitPath(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	custom := filepath.Join(t.TempDir(), "custom-checkout")
	info, err := Create(context.Background(), CreateOptions{Name: "custom", RepoRoot: repo, PathLocator: pl, Path: custom})
	if err != nil {
		t.Fatalf("Create with path: %v", err)
	}
	want, err := filepath.EvalSymlinks(custom)
	if err != nil {
		want = custom
	}
	if info.Path != filepath.Clean(want) {
		t.Fatalf("path = %s, want %s", info.Path, want)
	}
	if _, err := os.Stat(filepath.Join(info.Path, ".git")); err != nil {
		t.Fatalf("expected a linked worktree at the explicit path: %v", err)
	}
}

func TestRemoveDiscardChangesKeepsBranch(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	info, err := Create(ctx, CreateOptions{Name: "dirty", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := os.WriteFile(filepath.Join(info.Path, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := Remove(ctx, repo, "dirty", RemoveOptions{}, pl); err == nil {
		t.Fatal("dirty worktree should be refused without DiscardChanges")
	}
	if err := Remove(ctx, repo, "dirty", RemoveOptions{DiscardChanges: true}, pl); err != nil {
		t.Fatalf("Remove with DiscardChanges: %v", err)
	}
	if _, err := os.Stat(info.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree directory should be gone, stat err = %v", err)
	}
	exists, err := BranchRefExists(ctx, repo, info.Branch)
	if err != nil {
		t.Fatalf("BranchRefExists: %v", err)
	}
	if !exists {
		t.Fatal("DiscardChanges must keep the branch")
	}
}

func TestHeadCommitResolvesCheckoutHead(t *testing.T) {
	repo := setupTestRepo(t)
	head, err := HeadCommit(context.Background(), repo)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	if head != gitRevParse(t, repo, "HEAD") {
		t.Fatalf("HeadCommit = %s, want %s", head, gitRevParse(t, repo, "HEAD"))
	}
}
