package agent

import "testing"

func TestInputConsumptionReceipts(t *testing.T) {
	for _, tc := range []struct {
		name, content, status string
		busy, transition      bool
	}{
		{"local", "/models status", InputHandled, false, false},
		{"busy-local", "/models status", InputHandled, true, false},
		{"busy-message", "next", InputQueued, true, false},
		{"transition-message", "next", InputQueued, false, true},
		{"transition-command", "/mcp enable sample", InputHandled, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newReadyTestMainAgent(t)
			if tc.busy {
				a.newTurn()
			}
			a.mcpTransitionActive.Store(tc.transition)
			a.handleUserMessage(Event{Type: EventUserMessage, Payload: acceptedUserMessage{Content: tc.content, RequestID: "input-1"}})
			events := collectAgentEventsUntil(t, a.Events(), func(events []AgentEvent) bool {
				for _, e := range events {
					if _, ok := e.(InputResultEvent); ok {
						return true
					}
				}
				return false
			})
			got := events[len(events)-1].(InputResultEvent)
			if got.RequestID != "input-1" || got.Status != tc.status {
				t.Fatalf("receipt = %#v", got)
			}
		})
	}
}

func TestInputReceiptCancelAndRecovery(t *testing.T) {
	provider := &keyRecordingProvider{}
	a := newCancelWindowTestAgent(t, provider)
	if !a.SendUserMessageWithReceipt("first", "input-1") {
		t.Fatal("admission failed")
	}
	if !a.CancelCurrentTurn() {
		t.Fatal("accepted input was not cancelled")
	}
	startMainAgentLoopForTest(t, a)
	collectAgentEventsUntil(t, a.Events(), func(events []AgentEvent) bool {
		rejected := false
		for _, e := range events {
			if r, ok := e.(InputResultEvent); ok && r.RequestID == "input-1" && r.Status == InputRejected {
				rejected = true
			}
			if _, ok := e.(GlobalIdleEvent); ok && rejected {
				return true
			}
		}
		return false
	})
	// A new input wakes the parked drain; its admission is queued, and actual
	// work is announced by activity rather than a second consumption reply.
	if !a.SendUserMessageWithReceipt("second", "input-2") {
		t.Fatal("admission failed")
	}
	receipts := map[string]InputResultEvent{}
	collectAgentEventsUntil(t, a.Events(), func(events []AgentEvent) bool {
		for _, e := range events {
			if r, ok := e.(InputResultEvent); ok {
				receipts[r.RequestID] = r
			}
		}
		return len(receipts) == 1
	})
	if receipts["input-2"].Status != InputQueued {
		t.Fatalf("receipts = %#v", receipts)
	}
	waitForAgentState(t, "recovered input answered", func() bool { return len(a.GetMessages()) == 3 })
	if len(provider.recorded()) != 1 {
		t.Fatal("cancelled input called provider")
	}
	a.signalStopping()
	if a.SendUserMessageWithReceipt("third", "input-3") {
		t.Fatal("shutdown admitted input")
	}
}
