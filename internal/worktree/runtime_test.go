package worktree

import (
	"context"
	"testing"
)

// Create refuses to nest a checkout inside a linked worktree: a new worktree is
// always created from the main repo, never from another checkout.
func TestCreateRefusesNestedCreation(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	parent, err := Create(ctx, CreateOptions{Name: "parent", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatalf("Create parent: %v", err)
	}

	if _, err := Create(ctx, CreateOptions{Name: "child", RepoRoot: parent.Path, PathLocator: pl}); err == nil {
		t.Fatal("nested creation should be refused")
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
