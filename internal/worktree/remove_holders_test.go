package worktree

import (
	"context"
	"strings"
	"testing"
)

// TestRemoveRefusesWhileAHolderIsReported pins the guard every removal path
// shares: a checkout something still works in must not be deleted, and the
// resolver is consulted on the removal path itself rather than only by the
// caller's earlier check.
func TestRemoveRefusesWhileAHolderIsReported(t *testing.T) {
	ctx := context.Background()
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	info := newResolveWorktree(t, repo, pl, "feat-held")

	called := 0
	err := Remove(ctx, repo, info.Name, RemoveOptions{
		BranchPrefix: DefaultBranchPrefix,
		Holders: func(*Info) []string {
			called++
			return []string{"agent sub-1 is still bound to it (running)"}
		},
	}, pl)
	if err == nil {
		t.Fatal("Remove succeeded while a holder was reported")
	}
	if !strings.Contains(err.Error(), "still in use") {
		t.Fatalf("Remove error = %v, want it to name the in-use reason", err)
	}
	if called != 1 {
		t.Fatalf("holder resolver called %d times, want 1", called)
	}
	// The checkout must survive the refusal.
	if _, err := ResolveByName(ctx, repo, info.Name, DefaultBranchPrefix); err != nil {
		t.Fatalf("worktree should still exist after a refused removal: %v", err)
	}
}

// TestRemoveHonorsHoldersEvenWhenForced pins that force overrides "this
// checkout has uncommitted changes", not "this checkout is still in use".
// Losing the tree's contents and pulling the directory out from under running
// work are different consents.
func TestRemoveHonorsHoldersEvenWhenForced(t *testing.T) {
	ctx := context.Background()
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	info := newResolveWorktree(t, repo, pl, "feat-held-force")

	err := Remove(ctx, repo, info.Name, RemoveOptions{
		BranchPrefix: DefaultBranchPrefix,
		Force:        true,
		Holders:      func(*Info) []string { return []string{"background job job-1 is still running there"} },
	}, pl)
	if err == nil || !strings.Contains(err.Error(), "still in use") {
		t.Fatalf("Remove(force) error = %v, want an in-use refusal", err)
	}
	if _, err := ResolveByName(ctx, repo, info.Name, DefaultBranchPrefix); err != nil {
		t.Fatalf("worktree should still exist after a refused removal: %v", err)
	}
}

// TestRemoveProceedsWhenNothingHoldsTheCheckout is the counterpart: an empty
// answer is a real "nothing is using it", and the removal goes through.
func TestRemoveProceedsWhenNothingHoldsTheCheckout(t *testing.T) {
	ctx := context.Background()
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	info := newResolveWorktree(t, repo, pl, "feat-free")

	called := 0
	err := Remove(ctx, repo, info.Name, RemoveOptions{
		BranchPrefix: DefaultBranchPrefix,
		Holders: func(*Info) []string {
			called++
			return nil
		},
	}, pl)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if called != 1 {
		t.Fatalf("holder resolver called %d times, want 1", called)
	}
	if _, err := ResolveByName(ctx, repo, info.Name, DefaultBranchPrefix); err == nil {
		t.Fatal("worktree should be gone after Remove")
	}
}

// TestFinishReclaimRefusesWhileAHolderIsReported proves the command line's
// finish path is wired to the same guard: FinishOptions.Holders reaches
// Remove, so a finish does not delete a checkout that is still in use.
func TestFinishReclaimRefusesWhileAHolderIsReported(t *testing.T) {
	ctx := context.Background()
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	info := newResolveWorktree(t, repo, pl, "feat-finish-held")

	err := reclaimFinishedWorktree(ctx, repo, info, info.Name, FinishOptions{
		BranchPrefix: DefaultBranchPrefix,
		Holders: func(*Info) []string {
			return []string{"chord session 20260101000000000 is still open in this checkout"}
		},
	}, pl)
	if err == nil || !strings.Contains(err.Error(), "still in use") {
		t.Fatalf("reclaimFinishedWorktree error = %v, want an in-use refusal", err)
	}
	if _, err := ResolveByName(ctx, repo, info.Name, DefaultBranchPrefix); err != nil {
		t.Fatalf("worktree should still exist after a refused finish: %v", err)
	}
}
