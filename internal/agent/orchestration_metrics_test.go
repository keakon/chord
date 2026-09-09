package agent

import (
	"fmt"
	"sync"
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
	a.subs.taskRecords["task-completed"] = &DurableTaskRecord{TaskID: "task-completed", State: string(SubAgentStateCompleted), LatestInstanceID: "agent-9", Attempt: 2, LifecycleRevision: 7, SettlementDurable: true}
	a.subs.mu.Unlock()
	a.subAgentMailboxIDsMu.Lock()
	a.ownedSubAgentMailboxes = map[string][]SubAgentMailboxMessage{"agent-9": {{MessageID: "m1"}}}
	a.ownedMailboxSpool = map[string][]string{"agent-9": {"m2", "m3"}}
	a.subAgentMailboxIDsMu.Unlock()

	stats := a.OrchestrationStats()
	tasks := a.OrchestrationTaskDiagnostics()
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
	if len(tasks) != 3 {
		t.Fatalf("task snapshot rows = %d, want 3", len(tasks))
	}
	if tasks[0].TaskID != "task-completed" || tasks[0].Attempt != 2 || tasks[0].LifecycleRevision != 7 || !tasks[0].SettlementDurable {
		t.Fatalf("completed snapshot row = %#v", tasks[0])
	}
	if tasks[0].MailboxBacklog != 3 {
		t.Fatalf("completed mailbox backlog = %d, want 3 (1 owned + 2 spooled)", tasks[0].MailboxBacklog)
	}
	if tasks[2].TaskID != "task-running" || tasks[2].MailboxBacklog != 0 {
		t.Fatalf("running snapshot row = %#v", tasks[2])
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
	for i := range orchestrationTrackedMailboxLimit {
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
	for i := range orchestrationTrackedMailboxLimit {
		metrics.recordMailboxCreated(fmt.Sprintf("seed-%d", i), now)
	}
	b.ReportAllocs()
	var sent int
	for b.Loop() {
		sent++
		metrics.recordMailboxCreated(fmt.Sprintf("msg-%d", sent), now)
	}
}

// TestOrchestrationDiagnosticsConcurrentWithOwnedMailboxWriters is the race
// regression for the diagnostic snapshot reading the per-owner mailbox queues
// (ownedSubAgentMailboxes / ownedMailboxSpool) on the TUI-facing goroutine
// while production delivery paths mutate the same maps on the event-loop
// goroutine. Every reader and writer of those maps must share
// subAgentMailboxIDsMu; a missing lock crashes the process with a concurrent
// map read/write instead of failing the test, so this must also be run under
// -race (the CI race check does that centrally).
func TestOrchestrationDiagnosticsConcurrentWithOwnedMailboxWriters(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	ownerID := "agent-writer-1"
	a.subs.mu.Lock()
	a.subs.taskRecords["task-writer"] = &DurableTaskRecord{
		TaskID:           "task-writer",
		State:            string(SubAgentStateRunning),
		LatestInstanceID: ownerID,
		Attempt:          1,
	}
	a.subs.mu.Unlock()

	stop := make(chan struct{})
	var reader sync.WaitGroup
	reader.Add(1)
	go func() {
		defer reader.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			a.OrchestrationTaskDiagnostics()
			a.OrchestrationStats()
		}
	}()
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		var seq int
		for {
			select {
			case <-stop:
				return
			default:
			}
			seq++
			msg := SubAgentMailboxMessage{
				MessageID:    fmt.Sprintf("msg-%d", seq),
				AgentID:      "worker-child",
				TaskID:       "task-child",
				OwnerAgentID: ownerID,
				OwnerTaskID:  "task-writer",
				Kind:         SubAgentMailboxKindCompleted,
				Priority:     SubAgentMailboxPriorityUrgent,
				Summary:      "child update",
			}
			a.enqueueOwnedSubAgentMailbox(msg)
			a.drainOwnedSubAgentMailboxes(ownerID)
			a.refreshSubAgentInboxSummary()
		}
	}()

	// Give both sides enough overlap to expose an unlocked map access; the
	// duration is bounded so the test stays fast when the locks are correct.
	time.Sleep(300 * time.Millisecond)
	close(stop)
	reader.Wait()
	writer.Wait()

	// Sanity: the diagnostic still returns the expected row after the churn.
	rows := a.OrchestrationTaskDiagnostics()
	found := false
	for _, row := range rows {
		if row.TaskID == "task-writer" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("task-writer row missing from diagnostics: %#v", rows)
	}
}
