package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/worktree"
)

// newHoldersTestLocator returns a PathLocator whose session store is isolated
// per test.
func newHoldersTestLocator(t *testing.T) *config.PathLocator {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), "state")
	sessionsDir := filepath.Join(stateDir, "sessions")
	for _, p := range []string{stateDir, sessionsDir, filepath.Join(t.TempDir(), "cache")} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
	}
	return &config.PathLocator{
		ConfigHome:   t.TempDir(),
		StateDir:     stateDir,
		CacheDir:     filepath.Join(t.TempDir(), "cache"),
		SessionsRoot: sessionsDir,
		LogsDir:      t.TempDir(),
		ExportsDir:   filepath.Join(stateDir, "exports"),
	}
}

// seedSessionInCheckout writes a session recorded as working in checkoutPath
// and returns its session directory.
func seedSessionInCheckout(t *testing.T, pl *config.PathLocator, contentRoot, sessionID, checkoutPath string) string {
	t.Helper()
	pj, err := pl.LocateProject(contentRoot)
	if err != nil {
		t.Fatalf("LocateProject: %v", err)
	}
	sessionDir := filepath.Join(pj.ProjectSessionsDir, sessionID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	if err := recovery.RecordWorktreeBoundary(sessionDir,
		recovery.WorktreeBinding{Path: checkoutPath},
		recovery.WorktreeTimelineEntry{Reason: recovery.WorktreeSwitchEnter, At: time.Now().UTC()}); err != nil {
		t.Fatalf("RecordWorktreeBoundary: %v", err)
	}
	return sessionDir
}

// TestWorktreeSessionHoldersDetectsLiveSession is the case the guard exists
// for: another chord process is open in the checkout, so deleting it would
// pull the directory out from under a running session.
func TestWorktreeSessionHoldersDetectsLiveSession(t *testing.T) {
	pl := newHoldersTestLocator(t)
	contentRoot := t.TempDir()
	checkout := filepath.Join(t.TempDir(), "wt-alpha")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatalf("mkdir checkout: %v", err)
	}
	sessionDir := seedSessionInCheckout(t, pl, contentRoot, "20260101000000000", checkout)

	lock, err := recovery.AcquireSessionLock(sessionDir)
	if err != nil {
		t.Fatalf("AcquireSessionLock: %v", err)
	}
	defer func() { _ = lock.Release() }()

	holders := newWorktreeSessionHolders(pl, contentRoot)(&worktree.Info{Name: "alpha", Path: checkout})
	if len(holders) != 1 {
		t.Fatalf("holders = %v, want exactly one", holders)
	}
	if !strings.Contains(holders[0], "20260101000000000") {
		t.Fatalf("holder = %q, want it to name the session", holders[0])
	}
}

// TestWorktreeSessionHoldersIgnoresAbandonedSession pins the other half of the
// contract: the binding record survives a crash, so it alone must not count as
// a holder — otherwise every abandoned session would block removal forever.
// Only a session whose lock is still held by a live process counts.
func TestWorktreeSessionHoldersIgnoresAbandonedSession(t *testing.T) {
	pl := newHoldersTestLocator(t)
	contentRoot := t.TempDir()
	checkout := filepath.Join(t.TempDir(), "wt-beta")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatalf("mkdir checkout: %v", err)
	}
	seedSessionInCheckout(t, pl, contentRoot, "20260102000000000", checkout)
	// Deliberately no AcquireSessionLock: the session crashed or exited.

	if holders := newWorktreeSessionHolders(pl, contentRoot)(&worktree.Info{Name: "beta", Path: checkout}); len(holders) != 0 {
		t.Fatalf("holders = %v, want none for an abandoned session", holders)
	}
}

// TestWorktreeSessionHoldersIgnoresOtherCheckout keeps a session in one
// checkout from blocking removal of a sibling.
func TestWorktreeSessionHoldersIgnoresOtherCheckout(t *testing.T) {
	pl := newHoldersTestLocator(t)
	contentRoot := t.TempDir()
	other := filepath.Join(t.TempDir(), "wt-gamma")
	target := filepath.Join(t.TempDir(), "wt-delta")
	for _, p := range []string{other, target} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
	}
	sessionDir := seedSessionInCheckout(t, pl, contentRoot, "20260103000000000", other)
	lock, err := recovery.AcquireSessionLock(sessionDir)
	if err != nil {
		t.Fatalf("AcquireSessionLock: %v", err)
	}
	defer func() { _ = lock.Release() }()

	if holders := newWorktreeSessionHolders(pl, contentRoot)(&worktree.Info{Name: "delta", Path: target}); len(holders) != 0 {
		t.Fatalf("holders = %v, want none: the live session is in another checkout", holders)
	}
}

// TestWorktreeSessionHoldersFailsClosedWhenUnverifiable pins that an unknown
// answer refuses the removal rather than allowing it, matching the agent-side
// guard's treatment of an unverifiable checkout.
func TestWorktreeSessionHoldersFailsClosedWhenUnverifiable(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "wt-eps")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatalf("mkdir checkout: %v", err)
	}
	holders := newWorktreeSessionHolders(nil, "")(&worktree.Info{Name: "eps", Path: checkout})
	if len(holders) != 1 {
		t.Fatalf("holders = %v, want one unverifiable-holder reason", holders)
	}
	if !strings.Contains(holders[0], "could not be checked") {
		t.Fatalf("holder = %q, want it to say the check could not be made", holders[0])
	}
}
