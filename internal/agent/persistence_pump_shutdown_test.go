package agent

import (
	"testing"
	"time"
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
