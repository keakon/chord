package agent

import (
	"context"
	"testing"
)

func TestEventOverflowPreservesOrderAndRemainsRunnable(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.eventCh = make(chan Event, 1)

	a.sendEvent(Event{Type: "first"})
	a.sendEvent(Event{Type: "second"})
	a.sendEvent(Event{Type: "third"})

	if !a.hasDeferredEvents() {
		t.Fatal("overflow events were not retained")
	}
	if !a.hasQueuedAutomaticWork() {
		t.Fatal("overflow events were not reported as queued work")
	}

	for i, want := range []string{"first", "second", "third"} {
		evt, err := a.nextEvent(context.Background())
		if err != nil {
			t.Fatalf("nextEvent(%d): %v", i, err)
		}
		if evt.Type != want {
			t.Fatalf("nextEvent(%d).Type = %q, want %q", i, evt.Type, want)
		}
		if evt.Seq != uint64(i+1) {
			t.Fatalf("nextEvent(%d).Seq = %d, want %d", i, evt.Seq, i+1)
		}
	}
	if a.hasDeferredEvents() {
		t.Fatal("overflow queue was not fully drained")
	}
}

func TestConcurrentEventOverflowDeliversEveryEvent(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.eventCh = make(chan Event, 1)

	const producers = 32
	start := make(chan struct{})
	done := make(chan struct{}, producers)
	for i := range producers {
		go func() {
			<-start
			a.sendEvent(Event{Type: "concurrent", Payload: i})
			done <- struct{}{}
		}()
	}
	close(start)
	for range producers {
		<-done
	}

	seen := make(map[int]bool, producers)
	var lastSeq uint64
	for range producers {
		evt, err := a.nextEvent(context.Background())
		if err != nil {
			t.Fatalf("nextEvent: %v", err)
		}
		value, ok := evt.Payload.(int)
		if !ok {
			t.Fatalf("payload type = %T, want int", evt.Payload)
		}
		if seen[value] {
			t.Fatalf("duplicate event payload %d", value)
		}
		seen[value] = true
		if evt.Seq <= lastSeq {
			t.Fatalf("event sequence regressed: previous=%d current=%d", lastSeq, evt.Seq)
		}
		lastSeq = evt.Seq
	}
	if len(seen) != producers {
		t.Fatalf("delivered %d events, want %d", len(seen), producers)
	}
}
