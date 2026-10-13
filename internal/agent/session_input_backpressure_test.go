package agent

import (
	"context"
	"testing"
	"time"
)

func TestSessionInputBackpressureReleasesAdmission(t *testing.T) {
	for _, name := range []string{"resume", "cutoff", "switch"} {
		closeAdmission := name != "resume"
		t.Run(name, func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			a.eventCh = make(chan Event, 1)
			a.eventOverflowLimit = 1
			a.sendEvent(Event{Type: "first"})
			a.sendEvent(Event{Type: "second"})
			done := make(chan bool, 1)
			go func() { done <- a.sendSessionInput("sample", nil, "request") }()
			t.Cleanup(func() { a.signalStopping() })
			deadline := time.Now().Add(time.Second)
			for a.eventBackpressure.Load() == 0 {
				if time.Now().After(deadline) {
					t.Fatal("input did not wait for queue capacity")
				}
				time.Sleep(time.Millisecond)
			}
			if !a.pause.inputMu.TryLock() {
				t.Fatal("input retained admission lock while waiting for capacity")
			}
			a.pause.inputClosed = name == "cutoff"
			a.pause.inputMu.Unlock()
			if name == "switch" {
				a.installPausedSession(&loadedSessionState{})
			}
			if a.acceptedRawMessages.Load() != 0 {
				t.Fatal("input was accepted before obtaining queue capacity")
			}
			for _, want := range []string{"first", "second"} {
				evt, err := a.nextEvent(context.Background())
				if err != nil || evt.Type != want {
					t.Fatalf("event = %+v, error = %v; want %s", evt, err, want)
				}
			}
			select {
			case accepted := <-done:
				if accepted == closeAdmission {
					t.Fatalf("accepted = %v with closed admission = %v", accepted, closeAdmission)
				}
			case <-time.After(time.Second):
				t.Fatal("input did not finish after capacity was released")
			}
			if closeAdmission {
				if a.acceptedRawMessages.Load() != 0 || a.hasDeferredEvents() || len(a.eventCh) != 0 {
					t.Fatal("rejected input entered the queue")
				}
				return
			}
			evt, err := a.nextEvent(context.Background())
			if err != nil || evt.Type != EventUserMessage {
				t.Fatalf("input event = %+v, error = %v", evt, err)
			}
			p := evt.Payload.(acceptedUserMessage)
			if p.Content != "sample" || p.AcceptedOrder != 1 || p.InputID == "" {
				t.Fatalf("accepted input = %+v", p)
			}
		})
	}
}
