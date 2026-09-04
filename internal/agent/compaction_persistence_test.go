package agent

import (
	"fmt"
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

// TestPruneCompactionIndexAllocatorsDropsSettledDirs pins the map's bound: a
// session switch drops allocators for directories whose handed-out indexes are
// all on disk, keeps the active one, and keeps an entry whose allocation has
// not landed yet (a late worker must not be handed the same index twice).
func TestPruneCompactionIndexAllocatorsDropsSettledDirs(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	settled := t.TempDir()
	inflight := t.TempDir()
	active := t.TempDir()

	// A settled directory: the index it handed out is visible on disk.
	idx, err := a.nextCompactionIndexForAgent(settled)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settled, fmt.Sprintf("history-%d.md", idx)), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// An in-flight directory: the index was handed out but no file exists yet.
	if _, err := a.nextCompactionIndexForAgent(inflight); err != nil {
		t.Fatal(err)
	}
	if _, err := a.nextCompactionIndexForAgent(active); err != nil {
		t.Fatal(err)
	}

	a.pruneCompactionIndexAllocators(active)

	a.compactionIndexAllocsMu.Lock()
	_, keptSettled := a.compactionIndexAllocs[filepath.Clean(settled)]
	_, keptInflight := a.compactionIndexAllocs[filepath.Clean(inflight)]
	_, keptActive := a.compactionIndexAllocs[filepath.Clean(active)]
	total := len(a.compactionIndexAllocs)
	a.compactionIndexAllocsMu.Unlock()

	if keptSettled {
		t.Fatal("an allocator whose indexes are all on disk must be pruned")
	}
	if !keptInflight {
		t.Fatal("an allocator with an index not yet written must survive so it cannot re-hand it out")
	}
	if !keptActive {
		t.Fatal("the active session's allocator must never be pruned")
	}
	if total != 2 {
		t.Fatalf("allocator map size = %d, want 2", total)
	}

	// The pruned directory re-seeds from disk and never reuses its index.
	reallocated, err := a.nextCompactionIndexForAgent(settled)
	if err != nil {
		t.Fatal(err)
	}
	if reallocated <= idx {
		t.Fatalf("re-created allocator handed out index %d again (previous %d)", reallocated, idx)
	}
}
