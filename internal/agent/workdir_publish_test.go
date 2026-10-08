package agent

import (
	"context"
	"testing"
)

func workDirChangedEvents(events []AgentEvent) []WorkDirChangedEvent {
	var found []WorkDirChangedEvent
	for _, evt := range events {
		if e, ok := evt.(WorkDirChangedEvent); ok {
			found = append(found, e)
		}
	}
	return found
}

// A committed checkout switch notifies surfaces immediately. Without the
// notification the status bar keeps rendering the previous directory until some
// unrelated restore event lands, which is what made the path look like it moved
// at compaction time.
func TestWorkDirSwitchPublishesWorkDirChangedEvent(t *testing.T) {
	a, _ := newWorktreeTestAgent(t, "session-notify")
	drainAgentEvents(a.outputCh)

	first := installTestCheckout(t, a, "feat-notify")
	events := workDirChangedEvents(drainAgentEvents(a.outputCh))
	if len(events) != 1 {
		t.Fatalf("installing the checkout published %d invalidations, want 1", len(events))
	}
	if want := a.workDirState.load().Generation; events[0].Generation != want {
		t.Fatalf("install event generation = %d, want the published generation %d", events[0].Generation, want)
	}

	second := adoptTestCheckout(t, a, "feat-notify-two")
	if second.Generation != first.Generation+1 {
		t.Fatalf("second generation = %d, want %d", second.Generation, first.Generation+1)
	}
	events = workDirChangedEvents(drainAgentEvents(a.outputCh))
	if len(events) != 1 {
		t.Fatalf("the switch published %d invalidations, want 1", len(events))
	}
	if want := a.workDirState.load().Generation; events[0].Generation != want {
		t.Fatalf("switch event generation = %d, want the published generation %d", events[0].Generation, want)
	}
}

// A store that leaves the binding untouched is not a publication.
func TestPublishWorkDirChangeSkipsUnchangedBinding(t *testing.T) {
	a := &MainAgent{parentCtx: context.Background(), outputCh: make(chan AgentEvent, 4)}
	state := WorkDirState{Path: "/checkouts/feat", WorktreeID: "feat", Generation: 1}
	a.publishWorkDirChange(state, state)
	if events := drainAgentEvents(a.outputCh); len(events) != 0 {
		t.Fatalf("unchanged binding published %d events, want none", len(events))
	}

	next := WorkDirState{Path: "/checkouts/other", WorktreeID: "other", Generation: 2}
	a.publishWorkDirChange(state, next)
	events := workDirChangedEvents(drainAgentEvents(a.outputCh))
	if len(events) != 1 || events[0].Generation != 2 {
		t.Fatalf("changed binding published %v, want one invalidation with generation 2", events)
	}
}

// The snapshot reports the path, the worktree identity and the generation of
// one release, so a consumer never combines a path from one switch with an
// identity from another.
func TestWorkDirSnapshotCarriesOneRelease(t *testing.T) {
	a, repo := newWorktreeTestAgent(t, "session-snapshot")
	initial := a.WorkDirSnapshot()
	if initial.Path != repo || initial.WorktreeID != "" {
		t.Fatalf("initial snapshot = %#v, want the startup directory with no worktree identity", initial)
	}

	installed := installTestCheckout(t, a, "feat-snap")
	after := a.WorkDirSnapshot()
	if after.Path != installed.Path || after.WorktreeID != "feat-snap" || after.Generation != installed.Generation {
		t.Fatalf("snapshot after the install = %#v, want path %q, identity feat-snap, generation %d", after, installed.Path, installed.Generation)
	}

	second := adoptTestCheckout(t, a, "feat-snap-two")
	switched := a.WorkDirSnapshot()
	if switched.Path != second.Path || switched.WorktreeID != "feat-snap-two" || switched.Generation != second.Generation {
		t.Fatalf("snapshot after the switch = %#v, want path %q, identity feat-snap-two, generation %d", switched, second.Path, second.Generation)
	}
}
