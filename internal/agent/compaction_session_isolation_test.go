package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCompactionHistoryAllocatorUsesCapturedSessionDirectory(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	oldSessionDir := a.sessionDir
	newSessionDir := filepath.Join(t.TempDir(), "new-session")
	if err := os.MkdirAll(newSessionDir, 0o755); err != nil {
		t.Fatalf("create new session dir: %v", err)
	}

	first, err := a.nextCompactionIndexForAgent(oldSessionDir)
	if err != nil {
		t.Fatalf("first old-session allocation: %v", err)
	}
	if first != 1 {
		t.Fatalf("first old-session allocation = %d, want 1", first)
	}
	if err := os.WriteFile(filepath.Join(newSessionDir, "history-9.md"), []byte("# history 9\n"), 0o644); err != nil {
		t.Fatalf("seed new-session archive: %v", err)
	}

	a.installSessionTarget(newSessionDir)

	lateOld, err := a.nextCompactionIndexForAgent(oldSessionDir)
	if err != nil {
		t.Fatalf("late old-session allocation: %v", err)
	}
	if lateOld != 2 {
		t.Fatalf("late old-session allocation = %d, want 2", lateOld)
	}
	currentNew, err := a.nextCompactionIndexForAgent(newSessionDir)
	if err != nil {
		t.Fatalf("new-session allocation: %v", err)
	}
	if currentNew != 10 {
		t.Fatalf("new-session allocation = %d, want 10", currentNew)
	}
}

// TestInstallSessionTargetReseedsAllocatorAgainstDiskGrowth pins the
// return-to-session path: while a session was inactive, another process may
// have compacted it and grown its history-*.md files. Re-activating the
// session must raise that directory's allocator floor above the new on-disk
// maximum so the next allocation cannot truncate an archive this process never
// saw.
func TestInstallSessionTargetReseedsAllocatorAgainstDiskGrowth(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	session := filepath.Join(t.TempDir(), "session")
	if err := os.MkdirAll(session, 0o755); err != nil {
		t.Fatalf("create session dir: %v", err)
	}
	a.installSessionTarget(session)
	first, err := a.nextCompactionIndexForAgent(session)
	if err != nil {
		t.Fatalf("first allocation: %v", err)
	}
	if first != 1 {
		t.Fatalf("first allocation = %d, want 1", first)
	}

	// Leave the session; while away, another process compacts it.
	other := filepath.Join(t.TempDir(), "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatalf("create other session dir: %v", err)
	}
	a.installSessionTarget(other)
	if err := os.WriteFile(filepath.Join(session, "history-9.md"), []byte("# history 9\n"), 0o644); err != nil {
		t.Fatalf("seed external archive: %v", err)
	}

	// Returning must allocate strictly above the archive written while away.
	a.installSessionTarget(session)
	got, err := a.nextCompactionIndexForAgent(session)
	if err != nil {
		t.Fatalf("post-return allocation: %v", err)
	}
	if got != 10 {
		t.Fatalf("post-return allocation = %d, want 10 (allocator floor raised to the external on-disk maximum)", got)
	}
}
