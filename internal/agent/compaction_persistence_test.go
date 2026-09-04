package agent

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestNextCompactionIndexForAgentNeverReusesIndexes(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())

	first, err := a.nextCompactionIndexForAgent(a.sessionDir)
	if err != nil {
		t.Fatalf("first allocation: %v", err)
	}
	// Simulate the discarded worker's deferred cleanup removing the file the
	// first index was written to: the allocator must hand out a strictly
	// greater index instead of re-scanning to the same number.
	second, err := a.nextCompactionIndexForAgent(a.sessionDir)
	if err != nil {
		t.Fatalf("second allocation: %v", err)
	}
	if second != first+1 {
		t.Fatalf("second allocation = %d, want %d (never reuse a handed-out index)", second, first+1)
	}
}

func TestNextCompactionIndexForAgentSeedsFromDisk(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())

	// A pre-existing archive on disk must seed the allocator: the first
	// allocation continues after the on-disk maximum, never collides with it.
	existing := filepath.Join(a.sessionDir, "history-4.md")
	if err := os.WriteFile(existing, []byte("# history 4\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	got, err := a.nextCompactionIndexForAgent(a.sessionDir)
	if err != nil {
		t.Fatalf("allocation: %v", err)
	}
	if got != 5 {
		t.Fatalf("first allocation = %d, want 5 (seeded from the on-disk maximum 4)", got)
	}
}

func TestNextCompactionIndexForAgentReseedsAfterSessionSwitch(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	a := newTestMainAgent(t, first)

	if _, err := a.nextCompactionIndexForAgent(a.sessionDir); err != nil {
		t.Fatalf("first-session allocation: %v", err)
	}

	// Switch the session dir with existing higher archives: the allocator
	// must reseed against the new dir's on-disk maximum.
	if err := os.WriteFile(filepath.Join(second, "history-9.md"), []byte("# history 9\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	a.installSessionTarget(second)

	got, err := a.nextCompactionIndexForAgent(a.sessionDir)
	if err != nil {
		t.Fatalf("post-switch allocation: %v", err)
	}
	if got != 10 {
		t.Fatalf("post-switch allocation = %d, want 10 (reseeded from the new dir maximum 9)", got)
	}
}

func TestNextCompactionIndexForAgentConcurrentAllocationsAreUnique(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())

	const workers = 4
	const perWorker = 25
	seen := make(map[int]struct{})
	var mu sync.Mutex
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				idx, err := a.nextCompactionIndexForAgent(a.sessionDir)
				if err != nil {
					t.Errorf("allocation: %v", err)
					return
				}
				mu.Lock()
				if _, dup := seen[idx]; dup {
					t.Errorf("index %d handed out twice", idx)
				}
				seen[idx] = struct{}{}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != workers*perWorker {
		t.Fatalf("unique indexes = %d, want %d", len(seen), workers*perWorker)
	}
}

// TestNextCompactionIndexForAgentReseedRaisesFloorWithoutLowering pins the
// reseed semantics used by session activation: the allocator floor only moves
// up. After an external archive grows the on-disk maximum the next allocation
// continues above it, and removing that archive later must not re-lower a
// floor that indexes were already handed out from.
func TestNextCompactionIndexForAgentReseedRaisesFloorWithoutLowering(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())

	first, err := a.nextCompactionIndexForAgent(a.sessionDir)
	if err != nil {
		t.Fatalf("first allocation: %v", err)
	}
	second, err := a.nextCompactionIndexForAgent(a.sessionDir)
	if err != nil {
		t.Fatalf("second allocation: %v", err)
	}
	if first != 1 || second != 2 {
		t.Fatalf("allocations = %d, %d, want 1, 2", first, second)
	}

	external := filepath.Join(a.sessionDir, "history-9.md")
	if err := os.WriteFile(external, []byte("# history 9\n"), 0o644); err != nil {
		t.Fatalf("seed external archive: %v", err)
	}
	if err := a.reseedCompactionIndexAllocator(a.sessionDir); err != nil {
		t.Fatalf("reseed: %v", err)
	}
	got, err := a.nextCompactionIndexForAgent(a.sessionDir)
	if err != nil {
		t.Fatalf("post-reseed allocation: %v", err)
	}
	if got != 10 {
		t.Fatalf("post-reseed allocation = %d, want 10", got)
	}

	// The external archive disappears; reseeding must not lower the floor.
	if err := os.Remove(external); err != nil {
		t.Fatalf("remove external archive: %v", err)
	}
	if err := a.reseedCompactionIndexAllocator(a.sessionDir); err != nil {
		t.Fatalf("second reseed: %v", err)
	}
	got, err = a.nextCompactionIndexForAgent(a.sessionDir)
	if err != nil {
		t.Fatalf("post-second-reseed allocation: %v", err)
	}
	if got != 11 {
		t.Fatalf("post-second-reseed allocation = %d, want 11 (floor must never drop)", got)
	}
}
