package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

// countMailboxLogEntries returns the number of entries in a session's mailbox
// log, so a test can prove a prepared expiry alert was rolled back.
func countMailboxLogEntries(t *testing.T, sessionDir string) int {
	t.Helper()
	msgs, err := loadSubAgentMailboxMessages(sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxMessages: %v", err)
	}
	return len(msgs)
}

// TestParkedExpirySettleBackoffWithdrawsPreparedAlert pins the parked half of
// the phantom-expiry bug: the sweep persists the expiry risk_alert mailbox
// before its guarded terminal settle for crash-window ordering, but when the
// guarded settle backs off (the parked wait was revived or otherwise changed
// between collection and settle) the wait did not actually expire. The
// prepared alert must be rolled out of the mailbox log; otherwise a crash
// restart replays it as an unconsumed message whose task later settles as a
// matching expiry, telling the owner about an expiry that never happened.
func TestParkedExpirySettleBackoffWithdrawsPreparedAlert(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "expiry-backoff")
	if err := os.MkdirAll(filepath.Join(sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	rm := recovery.NewRecoveryManager(sessionDir)
	if err := rm.PersistMessage("main", message.Message{Role: message.RoleUser, Content: "resume this session"}); err != nil {
		t.Fatalf("PersistMessage(main): %v", err)
	}
	rm.Close()

	const taskID, instanceID = "task-expiry-backoff", "worker-expiry-1"
	expired := &DurableTaskRecord{
		TaskID:           taskID,
		AgentDefName:     "worker",
		LatestInstanceID: instanceID,
		InstanceHistory:  []string{instanceID},
		State:            string(SubAgentStateWaitingMain),
		RuntimeParked:    true,
		Attempt:          1,
		LastUpdatedTurn:  1,
		UpdatedAt:        time.Now().Add(-time.Hour),
	}
	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	configureNestedDelegationTestRuntime(a, 2)
	a.subs.mu.Lock()
	a.subs.taskRecords[taskID] = cloneDurableTaskRecord(expired)
	a.subs.mu.Unlock()
	if err := a.persistTaskRegistry(); err != nil {
		t.Fatalf("persistTaskRegistry: %v", err)
	}
	reason := waitingMainExpiryClosedReasonPrefix + " (no reply within the wait limit)"
	// The guard stands in for a revival landing between the sweep's collection
	// pass and the guarded settle: the parked wait is no longer expired, so the
	// guarded settle must back off.
	committed := a.settleExpiredParkedWaitingMainTask(taskID, expired, reason, func(*DurableTaskRecord) bool { return false })
	if committed {
		t.Fatal("guarded expiry settle committed against a backing-off guard")
	}
	if rec := a.taskRecordByTaskID(taskID); rec == nil || rec.State != string(SubAgentStateWaitingMain) || !rec.RuntimeParked {
		t.Fatalf("record after backed-off settle = %#v, want untouched parked waiting_main", rec)
	}
	if got := countMailboxLogEntries(t, sessionDir); got != 0 {
		t.Fatalf("prepared expiry alert survived the backoff in the mailbox log (%d entries); a restore would replay a wait that never expired", got)
	}
	// Park the first process before the restart simulation so both agents do
	// not hold the same session's recovery/usage handles concurrently.
	if err := a.Shutdown(time.Second); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	// The process restarts: the rolled-back alert must not be replayed and the
	// record must still be the untouched parked wait.
	restored := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	configureNestedDelegationTestRuntime(restored, 2)
	if _, err := restored.restoreSessionState(sessionDir); err != nil {
		t.Fatalf("restoreSessionState: %v", err)
	}
	if alerts := restoredRiskAlertsForTask(restored, taskID); len(alerts) != 0 {
		t.Fatalf("restore replayed a withdrawn expiry alert: %#v", alerts)
	}
	if rec := restored.taskRecordByTaskID(taskID); rec == nil || rec.State != string(SubAgentStateWaitingMain) || !rec.RuntimeParked {
		t.Fatalf("restored record = %#v, want untouched parked waiting_main", rec)
	}
}

// TestLiveExpiryConflictWithCompletedSettlementWithdrawsPreparedAlert pins the
// live half of the phantom-expiry bug: the expiry sweep's live branch persists
// the alert and then commits Cancelled through the close-requested handler,
// but a conflicting durable settlement (the task actually completed while its
// stale live runtime still reported waiting_main) makes that commit lose. The
// prepared alert must be withdrawn instead of lingering for a crash restart to
// replay against the completed task.
func TestLiveExpiryConflictWithCompletedSettlementWithdrawsPreparedAlert(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	if err := os.MkdirAll(filepath.Join(a.sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	const taskID = "task-expiry-live-conflict"
	sub := newControllableTestSubAgent(t, a, taskID)
	sub.setState(SubAgentStateWaitingMain, "need answer")
	a.noteSubAgentStateTransition(sub, SubAgentStateWaitingMain)
	// Backdate the wall-clock wait-since anchor so the sweep reliably sees the
	// wait as expired without sleeping.
	sub.runtimeState.stateChangedAt = time.Now().Add(-time.Minute)
	// The durable record and its settlement already carry Completed (the task
	// really finished), so the sweep's Cancelled commit loses the conflict.
	completed := &TaskSettlement{
		TaskID: taskID, Attempt: 1, TerminalRevision: 2,
		Outcome: string(SubAgentStateCompleted), Summary: "already done", SettledAt: time.Now(),
	}
	a.subs.mu.Lock()
	a.subs.taskRecords[taskID] = &DurableTaskRecord{
		TaskID:            taskID,
		AgentDefName:      "worker",
		LatestInstanceID:  sub.instanceID,
		InstanceHistory:   []string{sub.instanceID},
		State:             string(SubAgentStateCompleted),
		Attempt:           1,
		LifecycleRevision: 2,
		LastSummary:       "already done",
		LatestSettlement:  cloneTaskSettlement(completed),
		SettlementDurable: true,
		ClosedReason:      "task completed",
		RuntimeParked:     true,
	}
	a.subs.settlements[taskAttemptKey{TaskID: taskID, Attempt: 1}] = cloneTaskSettlement(completed)
	a.subs.mu.Unlock()

	a.waitingMainExpiry = waitingMainExpiryPolicy{turns: 1 << 30, minWait: time.Hour, maxWait: time.Nanosecond}
	a.sweepSubAgentLifecycle()

	if rec := a.taskRecordByTaskID(taskID); rec == nil || rec.State != string(SubAgentStateCompleted) {
		t.Fatalf("record after conflicting expiry sweep = %#v, want the completed winner untouched", rec)
	}
	if got := countMailboxLogEntries(t, a.sessionDir); got != 0 {
		t.Fatalf("expiry alert persisted for a completed task (%d entries); a restore would replay a wait that never expired", got)
	}
	if alerts := restoredRiskAlertsForTask(a, taskID); len(alerts) != 0 {
		t.Fatalf("phantom expiry alert delivered to the owner: %#v", alerts)
	}
}
