package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

// An ordinary idle send on a session that never used the queue dispatches
// directly and must not create input-delivery.json — the absent-queue
// re-dispatch keeps first-use sends out of the durable pipeline.
func TestAbsentQueueOrdinarySendCreatesNoQueueFile(t *testing.T) {
	t.Parallel()
	provider := &keyRecordingProvider{}
	a := newCancelWindowTestAgent(t, provider)
	if !a.SendUserMessageWithReceipt("first", "input-1") {
		t.Fatal("admission failed")
	}
	startMainAgentLoopForTest(t, a)
	waitForAgentState(t, "the ordinary send to be answered", func() bool {
		return len(provider.recorded()) == 1
	})
	if _, err := os.Stat(filepath.Join(a.sessionDir, recovery.InputDeliveryFile)); !os.IsNotExist(err) {
		t.Fatalf("ordinary idle send created a queue file: %v", err)
	}
}

// The other exit of the re-dispatch: a cancel-covered message in the same
// situation stays out of the immediate path and lands in the durable queue,
// whose file the retirement then consumes.
func TestAbsentQueueCoveredCancelCreatesDurableRow(t *testing.T) {
	t.Parallel()
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
		consumed := false
		for _, e := range events {
			if r, ok := e.(InputResultEvent); ok && r.RequestID == "input-1" && r.Status != InputQueued {
				t.Fatalf("covered receipt: %#v", r)
			}
			if _, ok := e.(PendingDraftConsumedEvent); ok {
				consumed = true
			}
			if _, ok := e.(GlobalIdleEvent); ok && consumed {
				return true
			}
		}
		return false
	})
	if requests := provider.recorded(); len(requests) != 0 {
		t.Fatalf("provider requests = %d, want 0 for a covered message", len(requests))
	}
	msgs := a.GetMessages()
	if len(msgs) != 1 || msgs[0].Role != message.RoleUser {
		t.Fatalf("messages = %#v, want the retained user message", msgs)
	}
	disk, err := a.recoveryManager().LoadInputDelivery()
	if err != nil {
		t.Fatal(err)
	}
	if disk == nil || len(disk.Inputs) != 0 {
		t.Fatalf("covered retirement left durable state: %+v", disk)
	}
}
