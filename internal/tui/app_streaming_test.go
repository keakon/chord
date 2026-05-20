package tui

import (
	"testing"
	"time"
)

func TestScheduleStreamFlushCoalescesUntilConsumed(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	first := m.scheduleStreamFlush(0)
	if first == nil {
		t.Fatal("first scheduleStreamFlush should return a tick command")
	}
	if !m.streamFlushScheduled {
		t.Fatal("scheduleStreamFlush should mark flush as scheduled")
	}
	gen := m.streamFlushGeneration
	if second := m.scheduleStreamFlush(0); second != nil {
		t.Fatal("second scheduleStreamFlush before consume should be coalesced")
	}
	if !m.consumeStreamFlush(streamFlushTickMsg{generation: gen}) {
		t.Fatal("consumeStreamFlush should accept current generation")
	}
	if m.streamFlushScheduled {
		t.Fatal("consumeStreamFlush should clear scheduled flag")
	}
	if third := m.scheduleStreamFlush(0); third == nil {
		t.Fatal("scheduleStreamFlush should schedule again after consume")
	}
}

func TestScheduleStreamFlushUrgentDelayBypassesCadenceFloor(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	if cmd := m.scheduleStreamFlush(1 * time.Millisecond); cmd == nil {
		t.Fatal("urgent scheduleStreamFlush should return a tick command")
	}
	if !m.streamFlushScheduled {
		t.Fatal("urgent scheduleStreamFlush should mark flush as scheduled")
	}
	if m.streamFlushDelay != 1*time.Millisecond {
		t.Fatalf("streamFlushDelay = %s, want 1ms", m.streamFlushDelay)
	}
}

func TestScheduleStreamFlushUrgentDelayPreemptsSlowerPendingFlush(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	if cmd := m.scheduleStreamFlush(0); cmd == nil {
		t.Fatal("default scheduleStreamFlush should return a tick command")
	}
	firstGen := m.streamFlushGeneration
	if m.streamFlushDelay != foregroundCadence.contentFlushDelay {
		t.Fatalf("streamFlushDelay = %s, want %s", m.streamFlushDelay, foregroundCadence.contentFlushDelay)
	}
	if cmd := m.scheduleStreamFlush(1 * time.Millisecond); cmd == nil {
		t.Fatal("urgent scheduleStreamFlush should preempt slower pending flush")
	}
	if !m.streamFlushScheduled {
		t.Fatal("preempted stream flush should remain scheduled")
	}
	if m.streamFlushDelay != 1*time.Millisecond {
		t.Fatalf("streamFlushDelay = %s, want 1ms", m.streamFlushDelay)
	}
	if m.streamFlushGeneration <= firstGen {
		t.Fatalf("streamFlushGeneration = %d, want > %d after preemption", m.streamFlushGeneration, firstGen)
	}
	if m.consumeStreamFlush(streamFlushTickMsg{generation: firstGen}) {
		t.Fatal("stale preempted generation should be rejected")
	}
	if !m.streamFlushScheduled {
		t.Fatal("stale preempted generation should keep urgent flush scheduled")
	}
}

func TestScheduleStreamFlushRejectsStaleGeneration(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	_ = m.scheduleStreamFlush(0)
	if m.consumeStreamFlush(streamFlushTickMsg{generation: m.streamFlushGeneration - 1}) {
		t.Fatal("consumeStreamFlush should reject stale generation")
	}
	if !m.streamFlushScheduled {
		t.Fatal("stale generation should not clear scheduled flag")
	}
}
