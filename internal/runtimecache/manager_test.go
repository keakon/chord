package runtimecache

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestOpenSessionUsesStableProjectAndSessionPath(t *testing.T) {
	chordHome := filepath.Join(t.TempDir(), ".chord")
	mgr, err := NewManager(chordHome)
	if err != nil {
		t.Fatalf("NewManager(): %v", err)
	}

	projectRoot := filepath.Join(t.TempDir(), "project-a")
	handle, err := mgr.OpenSession(projectRoot, "session-1")
	if err != nil {
		t.Fatalf("OpenSession(): %v", err)
	}
	defer func() { _ = handle.Remove() }()

	if got := handle.ViewportSpillPath(); filepath.Dir(got) != handle.ViewportDir() {
		t.Fatalf("ViewportSpillPath dir = %q, want %q", filepath.Dir(got), handle.ViewportDir())
	}
	if got := handle.Dir(); filepath.Base(got) != "session-1" {
		t.Fatalf("cache dir base = %q, want session-1", filepath.Base(got))
	}
	if _, err := os.Stat(filepath.Join(handle.Dir(), metaFileName)); err != nil {
		t.Fatalf("meta file stat: %v", err)
	}
}

func TestCleanupStaleSessionsRemovesUnlockedDirsAndKeepsLocked(t *testing.T) {
	chordHome := filepath.Join(t.TempDir(), ".chord")
	mgr, err := NewManager(chordHome)
	if err != nil {
		t.Fatalf("NewManager(): %v", err)
	}
	projectRoot := filepath.Join(t.TempDir(), "project-a")

	live, err := mgr.OpenSession(projectRoot, "live")
	if err != nil {
		t.Fatalf("OpenSession(live): %v", err)
	}
	defer func() { _ = live.Remove() }()

	stale, err := mgr.OpenSession(projectRoot, "stale")
	if err != nil {
		t.Fatalf("OpenSession(stale): %v", err)
	}
	staleDir := stale.Dir()
	if err := stale.Close(); err != nil {
		t.Fatalf("Close(stale): %v", err)
	}

	if err := mgr.CleanupStaleSessions(context.Background()); err != nil {
		t.Fatalf("CleanupStaleSessions(): %v", err)
	}
	if _, err := os.Stat(live.Dir()); err != nil {
		t.Fatalf("live dir stat: %v", err)
	}
	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Fatalf("stale dir err = %v, want not exist", err)
	}
}

func TestOpenSessionRemovesInactiveStaleDirBeforeReopen(t *testing.T) {
	chordHome := filepath.Join(t.TempDir(), ".chord")
	mgr, err := NewManager(chordHome)
	if err != nil {
		t.Fatalf("NewManager(): %v", err)
	}
	projectRoot := filepath.Join(t.TempDir(), "project-a")

	handle, err := mgr.OpenSession(projectRoot, "session-1")
	if err != nil {
		t.Fatalf("OpenSession(): %v", err)
	}
	dir := handle.Dir()
	staleMarker := filepath.Join(dir, "viewport", "stale.txt")
	if err := os.WriteFile(staleMarker, []byte("old"), 0o600); err != nil {
		t.Fatalf("WriteFile(stale): %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	reopened, err := mgr.OpenSession(projectRoot, "session-1")
	if err != nil {
		t.Fatalf("OpenSession(reopen): %v", err)
	}
	defer func() { _ = reopened.Remove() }()

	if _, err := os.Stat(staleMarker); !os.IsNotExist(err) {
		t.Fatalf("stale marker err = %v, want not exist", err)
	}
}

func TestCleanupCandidatesKeepsOnlyMostRecentSessions(t *testing.T) {
	chordHome := filepath.Join(t.TempDir(), ".chord")
	mgr, err := NewManager(chordHome)
	if err != nil {
		t.Fatalf("NewManager(): %v", err)
	}
	projectRoot := filepath.Join(t.TempDir(), "project-a")

	oldestDir := ""
	now := time.Now()
	for i := 0; i < startupCleanupLimit+1; i++ {
		sessionID := "session-" + strconv.Itoa(i)
		handle, err := mgr.OpenSession(projectRoot, sessionID)
		if err != nil {
			t.Fatalf("OpenSession(%s): %v", sessionID, err)
		}
		dir := handle.Dir()
		if err := handle.Close(); err != nil {
			t.Fatalf("Close(%s): %v", sessionID, err)
		}
		modTime := now.Add(-time.Duration(i) * time.Minute)
		if err := os.Chtimes(dir, modTime, modTime); err != nil {
			t.Fatalf("Chtimes(%s): %v", sessionID, err)
		}
		if i == startupCleanupLimit {
			oldestDir = dir
		}
	}

	if err := mgr.CleanupStaleSessions(context.Background()); err != nil {
		t.Fatalf("CleanupStaleSessions(): %v", err)
	}
	if _, err := os.Stat(oldestDir); err != nil {
		t.Fatalf("oldest dir should remain because cleanup only inspects recent %d sessions: %v", startupCleanupLimit, err)
	}
}

func TestCompareCleanupCandidateFallsBackToSessionIDDescending(t *testing.T) {
	a := cleanupCandidate{sessionID: "100"}
	b := cleanupCandidate{sessionID: "200"}
	if got := compareCleanupCandidate(a, b); got <= 0 {
		t.Fatalf("compareCleanupCandidate(%q,%q) = %d, want > 0 because 200 should sort first", a.sessionID, b.sessionID, got)
	}
	if got := compareCleanupCandidate(b, a); got >= 0 {
		t.Fatalf("compareCleanupCandidate(%q,%q) = %d, want < 0 because 200 should sort first", b.sessionID, a.sessionID, got)
	}
}
