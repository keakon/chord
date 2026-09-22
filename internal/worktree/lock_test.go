package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWithCheckoutLockSerializesWriters pins that the checkout mutation lock is
// exclusive: a second writer waits for the first to release. That ordering is
// what makes "record this session's checkout" and "remove the checkout"
// sequential instead of interleaved.
func TestWithCheckoutLockSerializesWriters(t *testing.T) {
	stateDir := t.TempDir()
	checkout := t.TempDir()

	held := make(chan struct{})
	release := make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- WithCheckoutLock(stateDir, checkout, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	entered := make(chan struct{})
	second := make(chan error, 1)
	go func() {
		second <- WithCheckoutLock(stateDir, checkout, func() error {
			close(entered)
			return nil
		})
	}()

	select {
	case <-entered:
		t.Fatal("second writer entered while the first still held the checkout lock")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first WithCheckoutLock: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("second writer never ran after the first released the lock")
	}
	if err := <-second; err != nil {
		t.Fatalf("second WithCheckoutLock: %v", err)
	}
}

// TestClaimCheckoutSkipsAGoneCheckout is the write-side half of the pair under
// test: a writer never records a checkout a removal already deleted, and it
// learns that from ErrCheckoutGone instead of running its claim.
func TestClaimCheckoutSkipsAGoneCheckout(t *testing.T) {
	stateDir := t.TempDir()
	checkout := filepath.Join(t.TempDir(), "removed")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatalf("mkdir checkout: %v", err)
	}
	if err := os.RemoveAll(checkout); err != nil {
		t.Fatalf("remove checkout: %v", err)
	}

	ran := false
	err := ClaimCheckout(stateDir, checkout, func() error {
		ran = true
		return nil
	})
	if !errors.Is(err, ErrCheckoutGone) {
		t.Fatalf("ClaimCheckout error = %v, want ErrCheckoutGone", err)
	}
	if ran {
		t.Fatal("ClaimCheckout ran the claim for a removed checkout")
	}
}

// TestClaimCheckoutRunsForAPresentCheckout is the counterpart: an existing
// directory is claimed, and a failure the claim itself reports is returned
// unchanged.
func TestClaimCheckoutRunsForAPresentCheckout(t *testing.T) {
	stateDir := t.TempDir()
	checkout := t.TempDir()

	ran := false
	if err := ClaimCheckout(stateDir, checkout, func() error {
		ran = true
		return nil
	}); err != nil {
		t.Fatalf("ClaimCheckout: %v", err)
	}
	if !ran {
		t.Fatal("ClaimCheckout did not run the claim for an existing checkout")
	}

	claimErr := errors.New("claim write failed")
	if err := ClaimCheckout(stateDir, checkout, func() error { return claimErr }); !errors.Is(err, claimErr) {
		t.Fatalf("ClaimCheckout error = %v, want the claim's own error", err)
	}
}

// TestRemoveScansHoldersUnderTheMutationLock pins the ordering the pair relies
// on: the holder scan runs after the removal acquired the lock, so a claim
// written under that lock is visible to it. The resolver reports a holder from
// the claim file this test writes while holding the lock, which a scan taken
// outside the lock would not see — and it would then delete a checkout that was
// just claimed.
func TestRemoveScansHoldersUnderTheMutationLock(t *testing.T) {
	ctx := context.Background()
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	info := newResolveWorktree(t, repo, pl, "feat-claim-lock")
	claim := filepath.Join(t.TempDir(), "claim")

	held := make(chan struct{})
	release := make(chan struct{})
	claimed := make(chan error, 1)
	go func() {
		claimed <- WithCheckoutLock(pl.StateDir, info.Path, func() error {
			close(held)
			<-release
			return os.WriteFile(claim, []byte("claimed"), 0o600)
		})
	}()
	<-held

	removed := make(chan error, 1)
	go func() {
		removed <- Remove(ctx, repo, info.Name, RemoveOptions{
			BranchPrefix: DefaultBranchPrefix,
			Holders: func(*Info) []string {
				if _, err := os.Stat(claim); err == nil {
					return []string{"session 20260101000000000 is still bound to it"}
				}
				return nil
			},
		}, pl)
	}()

	// The removal must wait for the claim writer. Finishing here means the
	// holder scan ran before the lock, so it could not have seen this claim.
	select {
	case err := <-removed:
		t.Fatalf("Remove finished while the checkout claim was being written: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	if err := <-claimed; err != nil {
		t.Fatalf("claim writer: %v", err)
	}
	if err := <-removed; err == nil || !strings.Contains(err.Error(), "still in use") {
		t.Fatalf("Remove error = %v, want an in-use refusal for the claim just written", err)
	}
	if _, err := ResolveByName(ctx, repo, info.Name, DefaultBranchPrefix); err != nil {
		t.Fatalf("checkout should survive a refused removal: %v", err)
	}
}
