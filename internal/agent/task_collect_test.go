package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/tools"
)

func installCollectTask(a *MainAgent, rec *DurableTaskRecord, settlement *TaskSettlement, durable bool) {
	a.subs.mu.Lock()
	if a.subs.taskRecords == nil {
		a.subs.taskRecords = make(map[string]*DurableTaskRecord)
	}
	a.subs.taskRecords[rec.TaskID] = cloneDurableTaskRecord(rec)
	if settlement != nil {
		key := taskAttemptKey{TaskID: settlement.TaskID, Attempt: settlement.Attempt}
		a.subs.settlements[key] = cloneTaskSettlement(settlement)
		a.subs.taskRecords[rec.TaskID].LatestSettlement = cloneTaskSettlement(settlement)
		a.subs.taskRecords[rec.TaskID].SettlementDurable = durable
	}
	a.subs.notifyTaskChangeLocked()
	a.subs.mu.Unlock()
}

func TestCollectTasksWaitsForSettlement(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	installCollectTask(a, &DurableTaskRecord{TaskID: "task-a", Attempt: 1, State: string(SubAgentStateRunning)}, nil, false)

	done := make(chan tools.TaskCollectResult, 1)
	go func() {
		result, _ := a.CollectTasks(context.Background(), tools.TaskCollectRequest{TaskIDs: []string{"task-a"}, Wait: true, Timeout: time.Second})
		done <- result
	}()
	time.Sleep(20 * time.Millisecond)
	settlement := testSettlement("task-a", 1, 2)
	installCollectTask(a, &DurableTaskRecord{TaskID: "task-a", Attempt: 1, LifecycleRevision: 2, State: string(SubAgentStateCompleted)}, settlement, true)

	select {
	case result := <-done:
		if !result.AllSettled || !result.AllDurable || result.TimedOut || len(result.Tasks) != 1 || result.Tasks[0].Summary != "done" {
			t.Fatalf("result = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("CollectTasks did not wake")
	}
}

func TestCollectTasksPinsAttemptAcrossRehydrate(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	installCollectTask(a, &DurableTaskRecord{TaskID: "task-a", Attempt: 1, State: string(SubAgentStateRunning)}, nil, false)

	done := make(chan tools.TaskCollectResult, 1)
	go func() {
		result, _ := a.CollectTasks(context.Background(), tools.TaskCollectRequest{TaskIDs: []string{"task-a"}, Wait: true, Timeout: time.Second})
		done <- result
	}()
	time.Sleep(20 * time.Millisecond)
	settlement := testSettlement("task-a", 1, 2)
	a.subs.mu.Lock()
	a.subs.settlements[taskAttemptKey{TaskID: "task-a", Attempt: 1}] = settlement
	a.subs.taskRecords["task-a"] = &DurableTaskRecord{TaskID: "task-a", Attempt: 2, LifecycleRevision: 3, State: string(SubAgentStateRunning)}
	a.subs.notifyTaskChangeLocked()
	a.subs.mu.Unlock()

	result := <-done
	if !result.AllSettled || result.Tasks[0].MemberAttempt != 1 || result.Tasks[0].CurrentAttempt != 2 || result.Tasks[0].Outcome != string(SubAgentStateCompleted) {
		t.Fatalf("result = %#v", result)
	}
}

func TestCollectTasksTimeoutReturnsSnapshot(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	installCollectTask(a, &DurableTaskRecord{TaskID: "task-a", Attempt: 1, State: string(SubAgentStateRunning)}, nil, false)
	result, err := a.CollectTasks(context.Background(), tools.TaskCollectRequest{TaskIDs: []string{"task-a"}, Wait: true, Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("CollectTasks: %v", err)
	}
	if result.AllSettled || !result.TimedOut || len(result.Tasks) != 1 || result.Tasks[0].State != string(SubAgentStateRunning) {
		t.Fatalf("result = %#v", result)
	}
}

func TestCollectTasksRejectsNonDirectOwner(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	installCollectTask(a, &DurableTaskRecord{TaskID: "task-a", Attempt: 1, OwnerTaskID: "parent", State: string(SubAgentStateRunning)}, nil, false)
	_, err := a.CollectTasks(context.Background(), tools.TaskCollectRequest{TaskIDs: []string{"task-a"}})
	if err == nil || !strings.Contains(err.Error(), "not directly owned") {
		t.Fatalf("error = %v", err)
	}
}

func TestCollectTasksInvalidatedBySessionChange(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.resetTaskCoordination(1, nil)
	installCollectTask(a, &DurableTaskRecord{TaskID: "task-a", Attempt: 1, State: string(SubAgentStateRunning)}, nil, false)
	done := make(chan error, 1)
	go func() {
		_, err := a.CollectTasks(context.Background(), tools.TaskCollectRequest{TaskIDs: []string{"task-a"}, Wait: true, Timeout: time.Second})
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	a.resetTaskCoordination(2, nil)
	if err := <-done; err == nil || !strings.Contains(err.Error(), "session change") {
		t.Fatalf("error = %v", err)
	}
}

func TestCollectTasksReadsArchivedMembersInBatch(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	settlementA := testSettlement("task-a", 1, 2)
	settlementB := testSettlement("task-b", 1, 3)
	if err := appendTaskArchive(a.sessionDir, []*DurableTaskRecord{
		{TaskID: "task-a", Attempt: 1, State: string(SubAgentStateCompleted), LatestSettlement: settlementA, SettlementDurable: true},
		{TaskID: "task-b", Attempt: 1, State: string(SubAgentStateCompleted), LatestSettlement: settlementB, SettlementDurable: true},
	}); err != nil {
		t.Fatalf("appendTaskArchive: %v", err)
	}
	a.resetTaskCoordination(a.sessionEpoch, map[taskAttemptKey]*TaskSettlement{
		{TaskID: "task-a", Attempt: 1}: settlementA,
		{TaskID: "task-b", Attempt: 1}: settlementB,
	})
	result, err := a.CollectTasks(context.Background(), tools.TaskCollectRequest{TaskIDs: []string{"task-b", "task-a"}})
	if err != nil {
		t.Fatalf("CollectTasks: %v", err)
	}
	if !result.AllSettled || !result.AllDurable || len(result.Tasks) != 2 || result.Tasks[0].TaskID != "task-a" || result.Tasks[1].TaskID != "task-b" {
		t.Fatalf("result = %#v", result)
	}
}
