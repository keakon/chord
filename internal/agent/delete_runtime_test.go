package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/filelock"
)

func TestAcquireDeleteLocksNoopsForInvalidOrAbsentInput(t *testing.T) {
	tracker := filelock.NewFileTracker()
	if locks, err := acquireDeleteLocks(nil, "main", json.RawMessage(`{"paths":["file.txt"],"reason":"cleanup"}`)); err != nil || locks != nil {
		t.Fatalf("nil tracker acquire = (%#v, %v), want nil nil", locks, err)
	}
	if locks, err := acquireDeleteLocks(tracker, "main", json.RawMessage(`{`)); err != nil || locks != nil {
		t.Fatalf("invalid args acquire = (%#v, %v), want nil nil", locks, err)
	}
	absent := filepath.Join(t.TempDir(), "absent.txt")
	if locks, err := acquireDeleteLocks(tracker, "main", deleteArgs(t, absent)); err != nil || locks != nil {
		t.Fatalf("absent path acquire = (%#v, %v), want nil nil", locks, err)
	}
}

func TestAcquireDeleteLocksLocksExistingPathsAndReleaseAborts(t *testing.T) {
	tracker := filelock.NewFileTracker()
	path := writeDeleteLockFile(t, "target.txt", "before")

	locks, err := acquireDeleteLocks(tracker, "main", deleteArgs(t, path))
	if err != nil {
		t.Fatalf("acquireDeleteLocks: %v", err)
	}
	if locks == nil || len(locks.paths) != 1 || locks.paths[0] != path {
		t.Fatalf("locks = %#v, want one locked path", locks)
	}
	if err := tracker.AcquireWrite(path, "other", computeFileHash(path)); err == nil {
		t.Fatal("other agent acquired write while delete lock was held")
	}
	locks.Release()
	if err := tracker.AcquireWrite(path, "other", computeFileHash(path)); err != nil {
		t.Fatalf("other agent should acquire after abort release: %v", err)
	}
	tracker.AbortWrite(path, "other")
}

func TestAcquireDeleteLocksReportsStaleRead(t *testing.T) {
	tracker := filelock.NewFileTracker()
	path := writeDeleteLockFile(t, "stale.txt", "before")
	tracker.TrackRead(path, "main", computeFileHash(path))
	if err := os.WriteFile(path, []byte("external"), 0o644); err != nil {
		t.Fatalf("WriteFile external: %v", err)
	}

	locks, err := acquireDeleteLocks(tracker, "main", deleteArgs(t, path))
	if err != nil {
		t.Fatalf("acquireDeleteLocks: %v", err)
	}
	if locks == nil || !locks.stale {
		t.Fatalf("locks = %#v, want stale lock set", locks)
	}
	locks.Release()
}

func TestAcquireDeleteLocksRollsBackEarlierLocksOnConflict(t *testing.T) {
	tracker := filelock.NewFileTracker()
	dir := t.TempDir()
	first := writeDeleteLockFileInDir(t, dir, "a-first.txt", "one")
	second := writeDeleteLockFileInDir(t, dir, "b-second.txt", "two")
	if err := tracker.AcquireWrite(second, "other", computeFileHash(second)); err != nil {
		t.Fatalf("AcquireWrite second: %v", err)
	}
	defer tracker.AbortWrite(second, "other")

	locks, err := acquireDeleteLocks(tracker, "main", deleteArgs(t, first, second))
	if err == nil || locks != nil {
		t.Fatalf("acquireDeleteLocks = (%#v, %v), want conflict error", locks, err)
	}
	if !strings.Contains(err.Error(), "being written by other") {
		t.Fatalf("unexpected conflict error: %v", err)
	}
	if err := tracker.AcquireWrite(first, "other", computeFileHash(first)); err != nil {
		t.Fatalf("first lock was not rolled back after second conflict: %v", err)
	}
	tracker.AbortWrite(first, "other")
}

func TestDeleteLockCommitInvalidatesOtherReaders(t *testing.T) {
	tracker := filelock.NewFileTracker()
	path := writeDeleteLockFile(t, "deleted.txt", "before")
	otherHash := computeFileHash(path)
	tracker.TrackRead(path, "other", otherHash)

	locks, err := acquireDeleteLocks(tracker, "main", deleteArgs(t, path))
	if err != nil {
		t.Fatalf("acquireDeleteLocks: %v", err)
	}
	locks.Commit("Deleted (1):\n- " + path)
	locks.Release()

	status, err := tracker.AcquireWriteStatus(path, "other", otherHash)
	if err != nil {
		t.Fatalf("AcquireWriteStatus other: %v", err)
	}
	defer tracker.AbortWrite(path, "other")
	if !status.ExternalChanged {
		t.Fatal("committed delete release should invalidate another agent's read hash")
	}
}

func TestContainsDeleteResultPath(t *testing.T) {
	if !containsDeleteResultPath([]string{"a.txt", "b.txt"}, "b.txt") {
		t.Fatal("containsDeleteResultPath did not find existing path")
	}
	if containsDeleteResultPath([]string{"a.txt"}, "b.txt") {
		t.Fatal("containsDeleteResultPath found missing path")
	}
}

func writeDeleteLockFile(t *testing.T, name, content string) string {
	t.Helper()
	return writeDeleteLockFileInDir(t, t.TempDir(), name, content)
}

func writeDeleteLockFileInDir(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
	return path
}

func deleteArgs(t *testing.T, paths ...string) json.RawMessage {
	t.Helper()
	raw := `{"paths":[`
	for i, path := range paths {
		if i > 0 {
			raw += ","
		}
		encoded, err := json.Marshal(path)
		if err != nil {
			t.Fatalf("Marshal path: %v", err)
		}
		raw += string(encoded)
	}
	raw += `],"reason":"cleanup"}`
	return json.RawMessage(raw)
}
