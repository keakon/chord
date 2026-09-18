package main

import (
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
)

// Push envelopes from the event loop and the command path must reach the wire
// in seq order: a client that applies only newer versions drops a push stamped
// earlier but emitted later, and role_change is never re-announced (its dedupe
// marker advanced at stamp time). This drives both paths through their real
// ordered sections concurrently.
func TestHeadlessStampedPushesReachWireInSeqOrderAcrossPaths(t *testing.T) {
	backend := &mockBackend{availableRoles: []string{"builder", "planner", "reviewer"}, currentRole: "builder"}
	state := &headlessState{
		role:          "builder",
		subscriptions: map[string]bool{"role_change": true, "activity": true},
	}
	to := newTestOut()
	writer := to.writer()

	const roleSwitches = 40
	const eventsPerRole = 12
	roles := []string{"planner", "reviewer", "builder"}

	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range roleSwitches {
			next := roles[i%len(roles)]
			handleHeadlessCommand(headlessCommand{Type: "role", Action: "set", Role: next}, backend, state, writer)
		}
	})
	wg.Go(func() {
		for range roleSwitches {
			for range eventsPerRole {
				writer.ordered(func() {
					envs := filterHeadlessEvent(agent.AgentActivityEvent{Type: agent.ActivityStreaming, Detail: "tick"}, state, backend)
					for _, env := range envs {
						writer.emit(env)
					}
				})
			}
		}
	})
	wg.Wait()

	var lastSeq uint64
	sawPush := false
	for _, env := range to.drain() {
		switch env.Type {
		case "role_change", "activity":
			// Pushes only: status snapshots (bump=false) may legally share or
			// trail the latest version they were copied against.
			sawPush = true
			if env.Seq == 0 {
				t.Fatalf("%s push on the wire without a seq", env.Type)
			}
			if env.Seq <= lastSeq {
				t.Fatalf("wire order inverted: %s seq=%d followed seq=%d", env.Type, env.Seq, lastSeq)
			}
			lastSeq = env.Seq
		}
	}
	if !sawPush {
		t.Fatal("expected role_change and activity pushes on the wire")
	}
}

// ordered serializes whole stamp+enqueue sections: while one section runs, a
// command-path role_change must be parked before its stamp, or its seq could
// interleave onto the wire out of order.
func TestHeadlessOrderedSectionsExcludeConcurrentStamps(t *testing.T) {
	backend := &mockBackend{availableRoles: []string{"builder", "planner"}, currentRole: "builder"}
	state := &headlessState{role: "builder", subscriptions: map[string]bool{"role_change": true}}
	to := newTestOut()
	writer := to.writer()

	entered := make(chan struct{})
	release := make(chan struct{})
	sectionDone := make(chan struct{})
	go func() {
		defer close(sectionDone)
		writer.ordered(func() {
			close(entered)
			<-release
		})
	}()
	<-entered

	switchDone := make(chan struct{})
	go func() {
		defer close(switchDone)
		handleHeadlessCommand(headlessCommand{Type: "role", Action: "set", Role: "planner"}, backend, state, writer)
	}()

	// While the ordering lock is held the role set must be parked before its
	// stamp: no cache write, no seq bump, no role_change. The unstamped
	// role_response reply is allowed to precede the parked section.
	select {
	case <-switchDone:
		t.Fatal("role set completed while an ordered section held the push lock")
	case <-time.After(150 * time.Millisecond):
	}

	close(release)
	<-sectionDone
	<-switchDone

	for _, env := range to.drain() {
		if env.Type == "role_change" {
			if env.Seq == 0 {
				t.Fatalf("role_change emitted without seq: %#v", env)
			}
			return
		}
	}
	t.Fatal("role_change not emitted after the ordered section released")
}
