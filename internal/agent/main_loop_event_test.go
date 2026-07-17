package agent

import (
	"context"
	"testing"
	"time"
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

func TestEventOverflowBackpressuresUntilCapacityIsReleased(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.eventCh = make(chan Event, 1)
	a.eventOverflowLimit = 1
	a.sendEvent(Event{Type: "channel"})
	a.sendEvent(Event{Type: "overflow"})

	done := make(chan struct{})
	go func() {
		a.sendEvent(Event{Type: "blocked"})
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("producer completed before overflow capacity was released")
	case <-time.After(50 * time.Millisecond):
	}

	if _, err := a.nextEvent(context.Background()); err != nil {
		t.Fatalf("nextEvent: %v", err)
	}
	if _, err := a.nextEvent(context.Background()); err != nil {
		t.Fatalf("nextEvent: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("producer did not resume after capacity was released")
	}
	stats := a.EventQueueStats()
	if stats.Backpressure == 0 || stats.OverflowPeak != 1 {
		t.Fatalf("event queue stats = %+v, want backpressure and peak 1", stats)
	}
}

func TestEventOverflowCoalescesProgressBySource(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.eventCh = make(chan Event, 1)
	a.eventOverflowLimit = 1
	a.sendEvent(Event{Type: "channel"})
	a.sendEvent(Event{Type: EventSubAgentProgressUpdated, SourceID: "worker-1", Payload: &SubAgentProgressUpdatedPayload{Summary: "old"}})
	a.sendEvent(Event{Type: EventSubAgentProgressUpdated, SourceID: "worker-1", Payload: &SubAgentProgressUpdatedPayload{Summary: "new"}})

	if _, err := a.nextEvent(context.Background()); err != nil {
		t.Fatalf("nextEvent: %v", err)
	}
	evt, err := a.nextEvent(context.Background())
	if err != nil {
		t.Fatalf("nextEvent: %v", err)
	}
	payload, _ := evt.Payload.(*SubAgentProgressUpdatedPayload)
	if payload == nil || payload.Summary != "new" {
		t.Fatalf("coalesced payload = %#v, want latest progress", evt.Payload)
	}
	if stats := a.EventQueueStats(); stats.Coalesced != 1 {
		t.Fatalf("coalesced count = %d, want 1", stats.Coalesced)
	}
}

func TestEventOverflowPreservesDistinctAgentLogs(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.eventCh = make(chan Event, 1)
	a.sendEvent(Event{Type: "channel"})
	a.sendEvent(Event{Type: EventAgentLog, SourceID: "worker-1", Payload: "first diagnostic"})
	a.sendEvent(Event{Type: EventAgentLog, SourceID: "worker-1", Payload: "second diagnostic"})

	wants := []string{"channel", "first diagnostic", "second diagnostic"}
	for i, want := range wants {
		evt, err := a.nextEvent(context.Background())
		if err != nil {
			t.Fatalf("nextEvent(%d): %v", i, err)
		}
		if i == 0 {
			if evt.Type != want {
				t.Fatalf("nextEvent(%d).Type = %q, want %q", i, evt.Type, want)
			}
			continue
		}
		if evt.Type != EventAgentLog || evt.Payload != want {
			t.Fatalf("nextEvent(%d) = %#v, want AgentLog %q", i, evt, want)
		}
	}
	if stats := a.EventQueueStats(); stats.Coalesced != 0 {
		t.Fatalf("agent logs were coalesced: %+v", stats)
	}
}

func TestLoopFollowUpReservePreservesCausalEventsPastSoftLimit(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.started.Store(true)
	a.loopEventLimit = 1
	a.queueLoopEvent(Event{Type: "first"})
	a.queueLoopEvent(Event{Type: "second"})
	for i, want := range []string{"first", "second"} {
		evt, err := a.nextEvent(context.Background())
		if err != nil {
			t.Fatalf("nextEvent(%d): %v", i, err)
		}
		if evt.Type != want {
			t.Fatalf("nextEvent(%d).Type = %q, want %q", i, evt.Type, want)
		}
	}
}

func TestHandleEscalateQueuesFollowUpWithoutBlockingOnFullExternalQueue(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.started.Store(true)
	a.eventCh = make(chan Event, 1)
	a.eventOverflowLimit = 1
	a.eventCh <- Event{Type: "channel", Seq: 1}
	a.deferredEvents = append(a.deferredEvents, Event{Type: "overflow", Seq: 2})
	sub := newControllableTestSubAgent(t, a, "task-escalate")
	done := make(chan struct{})
	go func() {
		a.handleEscalate(Event{SourceID: sub.instanceID, Payload: "need decision"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handleEscalate blocked on the full external event queue")
	}
	a.eventMu.Lock()
	defer a.eventMu.Unlock()
	if len(a.loopEvents) != 1 || a.loopEvents[0].Type != EventSubAgentMailbox {
		t.Fatalf("loop follow-ups = %#v, want one mailbox event", a.loopEvents)
	}
}

func TestEventOverflowCoalescingPreservesInterveningEventOrder(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.eventCh = make(chan Event, 1)
	a.sendEvent(Event{Type: "channel"})
	a.sendEvent(Event{Type: EventSubAgentProgressUpdated, SourceID: "worker-1", Payload: &SubAgentProgressUpdatedPayload{Summary: "old"}})
	a.sendEvent(Event{Type: EventSubAgentStateChanged, SourceID: "worker-1"})
	a.sendEvent(Event{Type: EventSubAgentProgressUpdated, SourceID: "worker-1", Payload: &SubAgentProgressUpdatedPayload{Summary: "new"}})

	wants := []string{"channel", EventSubAgentStateChanged, EventSubAgentProgressUpdated}
	for i, want := range wants {
		evt, err := a.nextEvent(context.Background())
		if err != nil {
			t.Fatalf("nextEvent(%d): %v", i, err)
		}
		if evt.Type != want {
			t.Fatalf("nextEvent(%d).Type = %q, want %q", i, evt.Type, want)
		}
		if evt.Type == EventSubAgentProgressUpdated {
			payload, _ := evt.Payload.(*SubAgentProgressUpdatedPayload)
			if payload == nil || payload.Summary != "new" {
				t.Fatalf("progress payload = %#v, want latest progress", evt.Payload)
			}
		}
	}
}

func TestEventOverflowDoesNotCoalesceResetNudgeAcrossIdle(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.eventCh = make(chan Event, 1)
	a.sendEvent(Event{Type: "channel"})
	a.sendEvent(Event{Type: EventResetNudge, SourceID: "worker-1"})
	a.sendEvent(Event{Type: EventAgentIdle, SourceID: "worker-1"})
	a.sendEvent(Event{Type: EventResetNudge, SourceID: "worker-1"})

	wants := []string{"channel", EventResetNudge, EventAgentIdle, EventResetNudge}
	for i, want := range wants {
		evt, err := a.nextEvent(context.Background())
		if err != nil {
			t.Fatalf("nextEvent(%d): %v", i, err)
		}
		if evt.Type != want {
			t.Fatalf("nextEvent(%d).Type = %q, want %q", i, evt.Type, want)
		}
	}
}

func TestEventOverflowBackpressureStopsOnShutdown(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.eventCh = make(chan Event, 1)
	a.eventOverflowLimit = 1
	a.sendEvent(Event{Type: "channel"})
	a.sendEvent(Event{Type: "overflow"})
	done := make(chan struct{})
	go func() {
		a.sendEvent(Event{Type: "blocked"})
		close(done)
	}()
	close(a.stoppingCh)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("producer remained blocked after shutdown")
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
