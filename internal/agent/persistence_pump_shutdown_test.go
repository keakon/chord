package agent

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func TestPersistencePumpCloseBoundsAdmissionWait(t *testing.T) {
	pump := newPersistencePump(1)
	pump.admission <- struct{}{} // an admission that has not finished sending
	result := make(chan bool, 1)
	go func() { result <- pump.closeUntil(time.After(20 * time.Millisecond)) }()
	select {
	case closed := <-result:
		if closed {
			t.Fatal("close succeeded while admission was occupied")
		}
	case <-time.After(time.Second):
		t.Fatal("close ignored its deadline while waiting for admission")
	}
	if pump.enqueue(persistEntry{}, nil) || pump.flush() {
		t.Fatal("stopping pump accepted a new write or flush")
	}
	<-pump.admission
	pump.start(func(persistEntry) {})
	select {
	case <-pump.done:
	case <-time.After(time.Second):
		t.Fatal("pump did not stop after admission was released")
	}
}

func TestPersistencePumpFlushBoundsAdmissionWait(t *testing.T) {
	pump := newPersistencePump(1)
	pump.admission <- struct{}{}
	result := make(chan bool, 1)
	go func() { result <- pump.flushUntil(time.After(20 * time.Millisecond)) }()
	select {
	case flushed := <-result:
		if flushed {
			t.Fatal("flush succeeded while admission was occupied")
		}
	case <-time.After(time.Second):
		t.Fatal("flush ignored its deadline while waiting for admission")
	}
	<-pump.admission
}

func TestPersistencePumpStopPreservesAcceptedWriteOrder(t *testing.T) {
	pump := newPersistencePump(2)
	if !pump.enqueue(persistEntry{agentID: "first"}, nil) || !pump.enqueue(persistEntry{agentID: "second"}, nil) {
		t.Fatal("write admission failed")
	}
	if !pump.closeUntil(time.After(time.Second)) {
		t.Fatal("close waited for space in the queue")
	}
	if pump.enqueue(persistEntry{agentID: "late"}, nil) {
		t.Fatal("closed pump allowed a later write")
	}
	var handled []string
	pump.start(func(entry persistEntry) { handled = append(handled, entry.agentID) })
	select {
	case <-pump.done:
	case <-time.After(time.Second):
		t.Fatal("closed pump did not drain")
	}
	if len(handled) != 2 || handled[0] != "first" || handled[1] != "second" {
		t.Fatalf("accepted write order changed: %v", handled)
	}
}

func TestPersistencePumpFailureBacklogCap(t *testing.T) {
	pump := newPersistencePump(1)
	manager := &recovery.RecoveryManager{}
	other := &recovery.RecoveryManager{}
	for range maxRetainedPersistFailures + 6 {
		pump.rememberFailure(persistEntry{recovery: manager, agentID: "main", msg: message.Message{Content: "payload"}}, errors.New("disk full"))
	}
	pump.rememberFailure(persistEntry{recovery: other}, errors.New("other session failure"))

	pump.failureMu.Lock()
	retained := len(pump.failures)
	dropped := pump.droppedFailures
	pump.failureMu.Unlock()
	// 70 manager failures plus 1 from another session: 7 entries were evicted
	// from the 64-slot backlog.
	if retained != maxRetainedPersistFailures || dropped != 7 {
		t.Fatalf("backlog = %d (cap %d), dropped = %d, want %d/7", retained, maxRetainedPersistFailures, dropped, maxRetainedPersistFailures)
	}

	err := pump.strictError(manager)
	if err == nil || !strings.Contains(err.Error(), "7 earlier persistence failures were dropped") {
		t.Fatalf("strict error should report the evicted entries, got %v", err)
	}
	// A session with no retained failures stays clean even though the global
	// backlog evicted entries from other sessions.
	if err := pump.strictError(&recovery.RecoveryManager{}); err != nil {
		t.Fatalf("healthy manager should have no strict error, got %v", err)
	}
	if !pump.managerFailed(manager) || pump.managerFailed(&recovery.RecoveryManager{}) {
		t.Fatal("managerFailed should reflect only retained failures")
	}
}
