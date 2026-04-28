package filelock

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestTrackRead_ConcurrentReads(t *testing.T) {
	ft := NewFileTracker()

	// Multiple agents can read the same file without conflict.
	ft.TrackRead("main.go", "agent-1", "hash-abc")
	ft.TrackRead("main.go", "agent-2", "hash-abc")
	ft.TrackRead("main.go", "agent-3", "hash-abc")

	// Same agent can acquire and release without conflict.
	if err := ft.AcquireWrite("main.go", "agent-1", "hash-abc"); err != nil {
		t.Fatalf("agent-1 should acquire write: %v", err)
	}
	ft.ReleaseWrite("main.go", "agent-1", "hash-new")
}

func TestAcquireWrite_WriteWriteConflict(t *testing.T) {
	ft := NewFileTracker()

	// agent-1 acquires write.
	if err := ft.AcquireWrite("main.go", "agent-1", "hash-abc"); err != nil {
		t.Fatalf("agent-1 should acquire write: %v", err)
	}

	// agent-2 tries to write the same file → conflict.
	err := ft.AcquireWrite("main.go", "agent-2", "hash-abc")
	if err == nil {
		t.Fatal("expected write-write conflict error")
	}

	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConflictError, got %T: %v", err, err)
	}
	if ce.Path != "main.go" {
		t.Errorf("expected path main.go, got %s", ce.Path)
	}
	if ce.ModifiedBy != "agent-1" {
		t.Errorf("expected ModifiedBy agent-1, got %s", ce.ModifiedBy)
	}
	if ce.Message == "" {
		t.Fatal("expected non-empty conflict message")
	}
}

func TestAcquireWrite_SameAgentConcurrentWriteConflict(t *testing.T) {
	ft := NewFileTracker()
	if err := ft.AcquireWrite("main.go", "agent-1", "hash-abc"); err != nil {
		t.Fatalf("agent-1 first acquire: %v", err)
	}

	err := ft.AcquireWrite("main.go", "agent-1", "hash-abc")
	if err == nil {
		t.Fatal("expected same-agent concurrent write conflict")
	}
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConflictError, got %T: %v", err, err)
	}
	if ce.ModifiedBy != "agent-1" {
		t.Fatalf("ModifiedBy = %q, want agent-1", ce.ModifiedBy)
	}
}

func TestAcquireWrite_ReadModifyWriteDetection(t *testing.T) {
	ft := NewFileTracker()

	// agent-1 reads the file.
	ft.TrackRead("main.go", "agent-1", "hash-v1")

	// agent-2 writes and changes the file content.
	if err := ft.AcquireWrite("main.go", "agent-2", "hash-v1"); err != nil {
		t.Fatalf("agent-2 should acquire write: %v", err)
	}
	ft.ReleaseWrite("main.go", "agent-2", "hash-v2")

	// agent-1 now tries to write — should detect stale read.
	// The currentHash is "hash-v2" (what's on disk), but agent-1's read hash
	// was invalidated by ReleaseWrite, so the readHashes entry was deleted.
	// agent-1 needs to re-read the file to get a fresh hash.

	// Re-read scenario: agent-1 reads again with old hash → trackRead updates.
	ft.TrackRead("main.go", "agent-1", "hash-v2")
	if err := ft.AcquireWrite("main.go", "agent-1", "hash-v2"); err != nil {
		t.Fatalf("agent-1 with fresh read should succeed: %v", err)
	}
	ft.ReleaseWrite("main.go", "agent-1", "hash-v3")
}

func TestAcquireWrite_StaleReadDetection(t *testing.T) {
	ft := NewFileTracker()

	// agent-1 and agent-2 both read the file.
	ft.TrackRead("main.go", "agent-1", "hash-v1")
	ft.TrackRead("main.go", "agent-2", "hash-v1")

	// agent-2 writes, changing the content.
	if err := ft.AcquireWrite("main.go", "agent-2", "hash-v1"); err != nil {
		t.Fatalf("agent-2 should acquire write: %v", err)
	}
	ft.ReleaseWrite("main.go", "agent-2", "hash-v2")

	// agent-1's read hash was invalidated by ReleaseWrite (empty sentinel).
	if err := ft.AcquireWrite("main.go", "agent-1", "hash-v2"); err == nil {
		t.Fatal("expected stale-read conflict error")
	}

	ft2 := NewFileTracker()
	ft2.TrackRead("f.go", "a1", "h1")
	err := ft2.AcquireWrite("f.go", "a1", "h2")
	if err == nil {
		t.Fatal("expected external-modification error")
	}
	var ext *ExternalModificationError
	if !errors.As(err, &ext) {
		t.Fatalf("expected ExternalModificationError, got %T: %v", err, err)
	}
}

func TestReleaseAll_Cleanup(t *testing.T) {
	ft := NewFileTracker()

	// agent-1 reads and writes multiple files.
	ft.TrackRead("a.go", "agent-1", "ha")
	ft.TrackRead("b.go", "agent-1", "hb")
	if err := ft.AcquireWrite("a.go", "agent-1", "ha"); err != nil {
		t.Fatalf("acquire a.go: %v", err)
	}
	if err := ft.AcquireWrite("b.go", "agent-1", "hb"); err != nil {
		t.Fatalf("acquire b.go: %v", err)
	}

	// agent-2 also has a read on a.go.
	ft.TrackRead("a.go", "agent-2", "ha")

	// Release all for agent-1.
	ft.ReleaseAll("agent-1")

	// agent-2 should now be able to write a.go.
	if err := ft.AcquireWrite("a.go", "agent-2", "ha"); err != nil {
		t.Fatalf("agent-2 should acquire after ReleaseAll: %v", err)
	}

	// agent-2 should also be able to write b.go (agent-1's lock released).
	if err := ft.AcquireWrite("b.go", "agent-2", "hb"); err != nil {
		t.Fatalf("agent-2 should acquire b.go after ReleaseAll: %v", err)
	}
}

func TestReleaseAll_CleansUpEmptyReadHashes(t *testing.T) {
	ft := NewFileTracker()

	ft.TrackRead("solo.go", "agent-1", "h1")
	ft.ReleaseAll("agent-1")

	// Internal state: readHashes["solo.go"] should be fully removed.
	// Verify by having agent-2 track and write — should not see stale entries.
	ft.TrackRead("solo.go", "agent-2", "h1")
	if err := ft.AcquireWrite("solo.go", "agent-2", "h1"); err != nil {
		t.Fatalf("agent-2 should write after full cleanup: %v", err)
	}
}

func TestReleaseWrite_InvalidatesOtherReadHashes(t *testing.T) {
	ft := NewFileTracker()

	ft.TrackRead("f.go", "agent-1", "v1")
	ft.TrackRead("f.go", "agent-2", "v1")

	// agent-1 writes.
	if err := ft.AcquireWrite("f.go", "agent-1", "v1"); err != nil {
		t.Fatalf("agent-1 acquire: %v", err)
	}
	ft.ReleaseWrite("f.go", "agent-1", "v2")

	// agent-1's own read hash should be updated to v2.
	if err := ft.AcquireWrite("f.go", "agent-1", "v2"); err != nil {
		t.Fatalf("agent-1 should re-acquire with v2: %v", err)
	}
	ft.ReleaseWrite("f.go", "agent-1", "v2")

	// agent-2's read hash was set to "" sentinel by ReleaseWrite.
	// They CANNOT acquire write with v2 because "" != "v2" (stale read detected).
	if err := ft.AcquireWrite("f.go", "agent-2", "v2"); err == nil {
		t.Fatal("agent-2 should NOT be able to write without re-reading (stale read sentinel)")
	}

	// agent-2 must re-read the file first, then can write.
	ft.TrackRead("f.go", "agent-2", "v2")
	if err := ft.AcquireWrite("f.go", "agent-2", "v2"); err != nil {
		t.Fatalf("agent-2 should be able to write after re-reading: %v", err)
	}
}

func TestConcurrentAccess(t *testing.T) {
	ft := NewFileTracker()
	const goroutines = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			agentID := fmt.Sprintf("agent-%d", id)
			ft.TrackRead("shared.go", agentID, "h1")
		}(i)
	}

	wg.Wait()

	// No panic = concurrent reads are safe.
	// Verify one write succeeds.
	if err := ft.AcquireWrite("shared.go", "agent-0", "h1"); err != nil {
		t.Fatalf("write after concurrent reads: %v", err)
	}
}

func TestDifferentFiles_NoConflict(t *testing.T) {
	ft := NewFileTracker()

	// Two agents writing different files should never conflict.
	if err := ft.AcquireWrite("a.go", "agent-1", "ha"); err != nil {
		t.Fatalf("agent-1 a.go: %v", err)
	}
	if err := ft.AcquireWrite("b.go", "agent-2", "hb"); err != nil {
		t.Fatalf("agent-2 b.go: %v", err)
	}

	ft.ReleaseWrite("a.go", "agent-1", "ha2")
	ft.ReleaseWrite("b.go", "agent-2", "hb2")
}
