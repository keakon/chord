package agent

import (
	"fmt"
	"testing"
	"time"
)

func TestOrchestrationStatsAggregatesRuntimeAndDurableState(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.sem <- struct{}{}

	now := time.Now()
	a.orchestrationMetrics.recordAdmissionWait(12 * time.Millisecond)
	a.orchestrationMetrics.scopeConflicts.Add(2)
	a.orchestrationMetrics.recordPark("task-parked", now.Add(-40*time.Millisecond))
	a.orchestrationMetrics.recordRehydrate("task-parked", now)
	a.orchestrationMetrics.recordMailboxCreated("msg-1", now.Add(-30*time.Millisecond))
	a.orchestrationMetrics.recordMailboxDelivery("msg-1", time.Time{})
	a.orchestrationMetrics.recordMailboxDelivery("msg-1", time.Time{})
	a.orchestrationMetrics.recordMailboxAck("msg-1")
	a.orchestrationMetrics.recordMailboxAck("msg-1")

	a.subs.mu.Lock()
	a.subs.taskRecords["task-running"] = &DurableTaskRecord{TaskID: "task-running", State: string(SubAgentStateRunning)}
	a.subs.taskRecords["task-failed"] = &DurableTaskRecord{TaskID: "task-failed", State: string(SubAgentStateFailed), ClosedReason: "verification failed"}
	a.subs.taskRecords["task-completed"] = &DurableTaskRecord{TaskID: "task-completed", State: string(SubAgentStateCompleted)}
	a.subs.mu.Unlock()

	stats := a.OrchestrationStats()
	if stats.SemaphoreCapacity != cap(a.sem) || stats.SemaphoreInUse != 1 {
		t.Fatalf("semaphore stats = %d/%d, want 1/%d", stats.SemaphoreInUse, stats.SemaphoreCapacity, cap(a.sem))
	}
	if want := 1 / float64(cap(a.sem)); stats.SemaphoreUtilization != want {
		t.Fatalf("semaphore utilization = %v, want %v", stats.SemaphoreUtilization, want)
	}
	if stats.AdmissionWaitCount != 1 || stats.AdmissionWaitTotal != 12*time.Millisecond || stats.AdmissionWaitAverage != 12*time.Millisecond {
		t.Fatalf("admission stats = count %d total %v average %v", stats.AdmissionWaitCount, stats.AdmissionWaitTotal, stats.AdmissionWaitAverage)
	}
	if stats.ScopeConflicts != 2 || stats.Parks != 1 || stats.Rehydrates != 1 {
		t.Fatalf("lifecycle counters = conflicts %d parks %d rehydrates %d", stats.ScopeConflicts, stats.Parks, stats.Rehydrates)
	}
	if stats.ParkedDurationCount != 1 || stats.ParkedDurationTotal <= 0 || stats.ParkedDurationAverage != stats.ParkedDurationTotal {
		t.Fatalf("parked duration stats = count %d total %v average %v", stats.ParkedDurationCount, stats.ParkedDurationTotal, stats.ParkedDurationAverage)
	}
	if stats.MailboxDeliveries != 1 || stats.MailboxAcks != 1 {
		t.Fatalf("mailbox counters = deliveries %d acks %d, want 1 each", stats.MailboxDeliveries, stats.MailboxAcks)
	}
	if stats.MailboxDeliveryLatencyTotal <= 0 || stats.MailboxAckLatencyTotal <= 0 {
		t.Fatalf("mailbox latency totals = delivery %v ack %v", stats.MailboxDeliveryLatencyTotal, stats.MailboxAckLatencyTotal)
	}
	if stats.TasksTotal != 3 || stats.TasksByState[string(SubAgentStateRunning)] != 1 || stats.TasksByState[string(SubAgentStateFailed)] != 1 || stats.TasksByState[string(SubAgentStateCompleted)] != 1 {
		t.Fatalf("task stats = total %d states %#v", stats.TasksTotal, stats.TasksByState)
	}
	if stats.TerminalReasons["verification failed"] != 1 || stats.TerminalReasons[string(SubAgentStateCompleted)] != 1 {
		t.Fatalf("terminal reasons = %#v", stats.TerminalReasons)
	}

	stats.TasksByState[string(SubAgentStateRunning)] = 99
	if next := a.OrchestrationStats(); next.TasksByState[string(SubAgentStateRunning)] != 1 {
		t.Fatalf("returned task state map aliases runtime state: %#v", next.TasksByState)
	}
}

func TestOrchestrationMailboxAckRequiresTrackedMessage(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.orchestrationMetrics.recordMailboxAck("unknown")
	stats := a.OrchestrationStats()
	if stats.MailboxAcks != 0 || stats.MailboxAckLatencyTotal != 0 {
		t.Fatalf("unknown mailbox ack changed stats: %+v", stats)
	}
}

func TestOrchestrationMetricsEvictionIgnoresStaleQueueEntries(t *testing.T) {
	metrics := orchestrationRuntimeMetrics{
		mailboxes: make(map[string]mailboxMetricState),
		parkedAt:  make(map[string]parkedMetricState),
	}
	now := time.Now()
	for i := 0; i < orchestrationTrackedMailboxLimit; i++ {
		id := fmt.Sprintf("tracked-%d", i)
		metrics.recordMailboxCreated(id, now)
		metrics.recordMailboxAck(id)
		metrics.recordMailboxCreated(id, now.Add(time.Second))
	}
	metrics.recordMailboxCreated("overflow", now.Add(2*time.Second))
	if len(metrics.mailboxes) != orchestrationTrackedMailboxLimit {
		t.Fatalf("tracked mailboxes = %d, want %d", len(metrics.mailboxes), orchestrationTrackedMailboxLimit)
	}
	if _, ok := metrics.mailboxes["overflow"]; !ok {
		t.Fatal("new mailbox was not retained after eviction")
	}
}

func BenchmarkOrchestrationMailboxTrackingAtCapacity(b *testing.B) {
	var metrics orchestrationRuntimeMetrics
	now := time.Now()
	for i := 0; i < orchestrationTrackedMailboxLimit; i++ {
		metrics.recordMailboxCreated(fmt.Sprintf("seed-%d", i), now)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		metrics.recordMailboxCreated(fmt.Sprintf("msg-%d", i), now)
	}
}
