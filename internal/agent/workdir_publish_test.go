package agent

import (
	"context"
	"testing"

	"github.com/keakon/chord/internal/tools"
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
func TestWorktreeSwitchPublishesWorkDirChangedEvent(t *testing.T) {
	ctx := context.Background()
	a, repo := newWorktreeTestAgent(t, "session-notify")
	drainAgentEvents(a.outputCh)

	entered, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-notify"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}
	if entered.Path == "" {
		t.Fatal("WorktreeEnter returned no checkout path")
	}
	events := workDirChangedEvents(drainAgentEvents(a.outputCh))
	if len(events) != 1 {
		t.Fatalf("enter published %d invalidations, want 1", len(events))
	}
	if want := a.workDirState.load().Generation; events[0].Generation != want {
		t.Fatalf("enter event generation = %d, want the published generation %d", events[0].Generation, want)
	}

	exit, err := a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-notify"})
	if err != nil {
		t.Fatalf("WorktreeExit: %v", err)
	}
	if exit.WorkDir != repo {
		t.Fatalf("exit WorkDir = %q, want %q", exit.WorkDir, repo)
	}
	events = workDirChangedEvents(drainAgentEvents(a.outputCh))
	if len(events) != 1 {
		t.Fatalf("exit published %d invalidations, want 1", len(events))
	}
	if want := a.workDirState.load().Generation; events[0].Generation != want {
		t.Fatalf("exit event generation = %d, want the published generation %d", events[0].Generation, want)
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
	ctx := context.Background()
	a, repo := newWorktreeTestAgent(t, "session-snapshot")
	initial := a.WorkDirSnapshot()
	if initial.Path != repo || initial.WorktreeID != "" {
		t.Fatalf("initial snapshot = %#v, want the startup directory with no worktree identity", initial)
	}

	entered, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-snap"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}
	after := a.WorkDirSnapshot()
	if after.Path != entered.Path || after.WorktreeID != "feat-snap" || after.Generation != 1 {
		t.Fatalf("snapshot after enter = %#v, want path %q, identity feat-snap, generation 1", after, entered.Path)
	}
}
