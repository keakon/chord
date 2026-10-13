package executiongate

import (
	"sync"
	"testing"
)

func TestFreezePreservesExistingExecution(t *testing.T) {
	var g Gate
	l, err := g.TryBegin(Tool, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	frozen := g.Frozen()
	state := g.Freeze()
	if state.Active != 1 {
		t.Fatalf("active = %d", state.Active)
	}
	select {
	case <-frozen:
	default:
		t.Fatal("freeze signal not closed")
	}
	if _, err := g.TryBegin(LLM, "task-1"); !IsPaused(err) {
		t.Fatalf("admission = %v", err)
	}
	if err := g.Resume(); err == nil {
		t.Fatal("resumed with active lease")
	}
	l.Finish()
	l.Finish()
	if err := g.Resume(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-g.Frozen():
		t.Fatal("new generation is frozen")
	default:
	}
	if _, err := g.TryBegin(LLM, "task-2"); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentFreezeAdmission(t *testing.T) {
	var g Gate
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			l, err := g.TryBegin(Tool, "task")
			if err == nil {
				l.Finish()
			} else if !IsPaused(err) {
				t.Error(err)
			}
		})
	}
	g.Freeze()
	wg.Wait()
	if len(g.Snapshot().Activities) != 0 {
		t.Fatal("lease leaked")
	}
	for range 100 {
		if _, err := g.TryBegin(LLM, "task"); !IsPaused(err) {
			t.Fatal(err)
		}
	}
}
