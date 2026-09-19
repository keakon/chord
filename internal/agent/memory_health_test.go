package agent

import (
	"errors"
	"testing"

	"github.com/keakon/chord/internal/memory"
)

// The status bar's MEMORY pill has to warn once memory stops working, so a
// permanent failure marks the region degraded and a later success clears it.
func TestNoteMemoryOutcomeTracksPermanentFailures(t *testing.T) {
	a := &MainAgent{}
	if a.MemoryDegraded() {
		t.Fatal("fresh agent reported degraded memory")
	}
	a.noteMemoryOutcome(errors.New("transient model failure"))
	if a.MemoryDegraded() {
		t.Fatal("a transient failure must not mark memory degraded")
	}
	a.noteMemoryOutcome(memory.ErrManagedMarkers)
	if !a.MemoryDegraded() {
		t.Fatal("a permanent failure must mark memory degraded")
	}
	a.noteMemoryOutcome(nil)
	if a.MemoryDegraded() {
		t.Fatal("a successful commit must clear the degraded mark")
	}
}

func TestMemoryDegradedNilReceiver(t *testing.T) {
	var a *MainAgent
	if a.MemoryDegraded() {
		t.Fatal("nil receiver reported degraded memory")
	}
}

// A permanent failure (or its recovery) flips the health flag and announces it,
// so a running TUI repaints instead of keeping the pill it happened to render.
func TestNoteMemoryOutcomeEmitsHealthFlip(t *testing.T) {
	a := &MainAgent{outputCh: make(chan AgentEvent, 1)}

	a.noteMemoryOutcome(memory.ErrManagedMarkers)
	select {
	case evt := <-a.outputCh:
		health, ok := evt.(MemoryHealthEvent)
		if !ok || !health.Degraded {
			t.Fatalf("event = %#v, want a degraded MemoryHealthEvent", evt)
		}
	default:
		t.Fatal("a permanent failure did not announce the health change")
	}

	a.noteMemoryOutcome(memory.ErrManagedMarkers)
	select {
	case evt := <-a.outputCh:
		t.Fatalf("an unchanged degraded state re-announced health: %#v", evt)
	default:
	}

	a.noteMemoryOutcome(nil)
	select {
	case evt := <-a.outputCh:
		health, ok := evt.(MemoryHealthEvent)
		if !ok || health.Degraded {
			t.Fatalf("event = %#v, want a healthy MemoryHealthEvent", evt)
		}
	default:
		t.Fatal("recovery did not announce the health change")
	}
}
