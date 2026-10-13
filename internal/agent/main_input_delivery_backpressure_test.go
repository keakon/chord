package agent

import (
	"context"
	"testing"
	"time"

	"github.com/keakon/chord/internal/recovery"
)

func TestInputDeliveryContinuationWithFullEventQueue(t *testing.T) {
	for _, settle := range []bool{false, true} {
		name := "resume"
		if settle {
			name = "settle"
		}
		t.Run(name, func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			a.started.Store(true)
			defer a.started.Store(false)
			a.eventCh = make(chan Event, 1)
			a.eventOverflowLimit = 1
			a.sendEvent(Event{Type: "first"})
			a.sendEvent(Event{Type: "second"})
			continuation := a.resumeDeliveryRequest
			if settle {
				// Retain the operation behind an in-flight write so its completion
				// can be exercised with deterministic queue saturation.
				a.delivery.inflight = &deliveryOperation{}
				a.settleDeliveryWork("work", recovery.DeliveryWorkCompleted)
				continuation = a.delivery.pending[0].finish
			}
			done := make(chan struct{})
			go func() { continuation(); close(done) }()
			select {
			case <-done:
			case <-time.After(time.Second):
				a.signalStopping()
				<-done
				t.Fatal("owner-loop continuation waited for event capacity")
			}
			for _, want := range []string{"first", "second", eventInputDeliveryAdvance} {
				evt, err := a.nextEvent(context.Background())
				if err != nil || evt.Type != want {
					t.Fatalf("event = %+v, error = %v; want %s", evt, err, want)
				}
			}
			a.delivery.inflight, a.delivery.pending = nil, nil
		})
	}
}
