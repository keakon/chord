package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/tools"
)

func testSettlement(taskID string, attempt, revision uint64) *TaskSettlement {
	return &TaskSettlement{
		TaskID:           taskID,
		Attempt:          attempt,
		TerminalRevision: revision,
		Outcome:          string(SubAgentStateCompleted),
		Summary:          "done",
		SettledAt:        time.Unix(100, 0).UTC(),
	}
}

func TestTaskSettlementJournalRoundTripAndDuplicate(t *testing.T) {
	dir := t.TempDir()
	settlement := testSettlement("task-a", 2, 7)
	if err := appendTaskSettlement(dir, settlement); err != nil {
		t.Fatalf("appendTaskSettlement: %v", err)
	}
	if err := appendTaskSettlement(dir, settlement); err != nil {
		t.Fatalf("append duplicate settlement: %v", err)
	}
	got, err := loadTaskSettlements(dir)
	if err != nil {
		t.Fatalf("loadTaskSettlements: %v", err)
	}
	loaded := got[taskAttemptKey{TaskID: "task-a", Attempt: 2}]
	if loaded == nil || loaded.TerminalRevision != 7 || loaded.Summary != "done" {
		t.Fatalf("loaded settlement = %#v", loaded)
	}
}

func TestTaskSettlementJournalRejectsConflictingAttempt(t *testing.T) {
	dir := t.TempDir()
	if err := appendTaskSettlement(dir, testSettlement("task-a", 1, 3)); err != nil {
		t.Fatal(err)
	}
	conflict := testSettlement("task-a", 1, 4)
	if err := appendTaskSettlement(dir, conflict); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTaskSettlements(dir); err == nil {
		t.Fatal("expected conflicting settlement error")
	}
}

func TestTaskSettlementJournalIgnoresIncompleteTailOnly(t *testing.T) {
	dir := t.TempDir()
	if err := appendTaskSettlement(dir, testSettlement("task-a", 1, 1)); err != nil {
		t.Fatal(err)
	}
	path := taskSettlementJournalPath(dir)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"task_id":`)
	_ = f.Close()
	got, err := loadTaskSettlements(dir)
	if err != nil {
		t.Fatalf("load with partial tail: %v", err)
	}
	if got[taskAttemptKey{TaskID: "task-a", Attempt: 1}] == nil {
		t.Fatal("valid prefix settlement missing")
	}
	if err := os.WriteFile(path, []byte("not-json\n{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTaskSettlements(dir); err == nil {
		t.Fatal("expected middle corruption error")
	}
}

func TestTaskSettlementAppendTruncatesIncompleteTail(t *testing.T) {
	dir := t.TempDir()
	if err := appendTaskSettlement(dir, testSettlement("task-a", 1, 1)); err != nil {
		t.Fatal(err)
	}
	path := taskSettlementJournalPath(dir)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"task_id":`)
	_ = f.Close()
	if err := appendTaskSettlement(dir, testSettlement("task-b", 1, 1)); err != nil {
		t.Fatalf("append after partial tail: %v", err)
	}
	got, err := loadTaskSettlements(dir)
	if err != nil {
		t.Fatalf("loadTaskSettlements: %v", err)
	}
	if got[taskAttemptKey{TaskID: "task-a", Attempt: 1}] == nil || got[taskAttemptKey{TaskID: "task-b", Attempt: 1}] == nil {
		t.Fatalf("settlements = %#v", got)
	}
}

func TestTaskSettlementAppendTruncatesLargeIncompleteTail(t *testing.T) {
	dir := t.TempDir()
	if err := appendTaskSettlement(dir, testSettlement("task-a", 1, 1)); err != nil {
		t.Fatal(err)
	}
	path := taskSettlementJournalPath(dir)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(strings.Repeat("x", 16*1024)); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := appendTaskSettlement(dir, testSettlement("task-b", 1, 1)); err != nil {
		t.Fatalf("append after large partial tail: %v", err)
	}
	got, err := loadTaskSettlements(dir)
	if err != nil {
		t.Fatalf("loadTaskSettlements: %v", err)
	}
	if got[taskAttemptKey{TaskID: "task-a", Attempt: 1}] == nil || got[taskAttemptKey{TaskID: "task-b", Attempt: 1}] == nil {
		t.Fatalf("settlements = %#v", got)
	}
}

func TestMigrateLegacyTaskSettlement(t *testing.T) {
	dir := t.TempDir()
	resultRef := &tools.ResultRef{ID: "sha256-result", ResultType: "type/test", RelPath: "artifacts/results/result.json", SHA256: strings.Repeat("a", 64), SizeBytes: 12}
	records := map[string]*DurableTaskRecord{
		"legacy": {
			TaskID:         "legacy",
			State:          string(SubAgentStateCompleted),
			Attempt:        1,
			LastSummary:    "legacy done",
			LastCompletion: &CompletionEnvelope{ResultType: "type/test", ResultRef: resultRef},
			UpdatedAt:      time.Unix(200, 0).UTC(),
			CreatedAt:      time.Unix(100, 0).UTC(),
		},
	}
	settlements, err := migrateLegacyTaskSettlements(dir, records, nil)
	if err != nil {
		t.Fatalf("migrateLegacyTaskSettlements: %v", err)
	}
	if got := settlements[taskAttemptKey{TaskID: "legacy", Attempt: 1}]; got == nil || got.Summary != "legacy done" || got.ResultRef == nil || got.ResultRef.ID != resultRef.ID {
		t.Fatalf("migrated settlement = %#v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "subagents", "task-settlements.jsonl")); err != nil {
		t.Fatalf("journal stat: %v", err)
	}
}

func TestRepairTaskRecordsUsesJournalContentAtSameRevision(t *testing.T) {
	journal := testSettlement("task-a", 1, 2)
	journal.Completion = &CompletionEnvelope{Summary: "journal", VerificationRun: []string{"go test ./internal/agent"}}
	records := map[string]*DurableTaskRecord{
		"task-a": {
			TaskID:            "task-a",
			Attempt:           1,
			State:             string(SubAgentStateCompleted),
			LifecycleRevision: 2,
			LatestSettlement: &TaskSettlement{
				TaskID: "task-a", Attempt: 1, TerminalRevision: 2, Outcome: string(SubAgentStateCompleted),
				Summary: "stale", Completion: &CompletionEnvelope{Summary: "stale"}, SettledAt: journal.SettledAt,
			},
			SettlementDurable: true,
		},
	}
	changed := repairTaskRecordsFromSettlements(records, map[taskAttemptKey]*TaskSettlement{{TaskID: "task-a", Attempt: 1}: journal})
	if !changed {
		t.Fatal("repairTaskRecordsFromSettlements did not repair conflicting content")
	}
	rec := records["task-a"]
	if rec.LastSummary != "done" || rec.LastCompletion == nil || rec.LastCompletion.Summary != "journal" || !taskSettlementContentEqual(rec.LatestSettlement, journal) {
		t.Fatalf("repaired record = %#v", rec)
	}
}

func TestCommitTerminalTaskPublishesDurableSettlement(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "task-terminal")
	completion := &CompletionEnvelope{Summary: "done", VerificationRun: []string{"go test ./..."}}
	settlement, durable, err := a.commitTerminalTask(sub, SubAgentStateCompleted, "done", "task completed", completion)
	if err != nil {
		t.Fatalf("commitTerminalTask: %v", err)
	}
	if !durable || settlement == nil || settlement.Attempt != 1 || settlement.Outcome != string(SubAgentStateCompleted) {
		t.Fatalf("settlement = %#v durable=%v", settlement, durable)
	}
	rec := a.taskRecordByTaskID("task-terminal")
	if rec == nil || !rec.SettlementDurable || rec.LatestSettlement == nil || rec.LastCompletion == nil || rec.LastCompletion.VerificationRun[0] != "go test ./..." {
		t.Fatalf("task record = %#v", rec)
	}
	loaded, err := loadTaskSettlements(a.sessionDir)
	if err != nil {
		t.Fatalf("loadTaskSettlements: %v", err)
	}
	if loaded[taskAttemptKey{TaskID: "task-terminal", Attempt: 1}] == nil {
		t.Fatal("durable settlement missing")
	}
}

func TestGuardedDetachedSettlementRollsBackWhenRecordChanges(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "task-guard-rollback")
	a.syncTaskRecordFromSub(sub, "")
	calls := 0
	guard := func(*DurableTaskRecord) bool {
		calls++
		return calls == 1
	}
	if got := a.settleDetachedTerminalTaskGuarded(sub.taskID, SubAgentStateCancelled, "expired", "expired", guard); got != "" {
		t.Fatalf("guarded settlement = %q, want rollback", got)
	}
	settlements, err := loadTaskSettlements(a.sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(settlements) != 0 {
		t.Fatalf("rolled-back settlement journal = %#v", settlements)
	}
}

func TestGuardedDetachedSettlementRollbackKeepsCrashTailTruncated(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	if err := appendTaskSettlement(a.sessionDir, &TaskSettlement{
		TaskID: "task-existing", Attempt: 1, TerminalRevision: 1,
		Outcome: string(SubAgentStateCompleted), Summary: "done", SettledAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(taskSettlementJournalPath(a.sessionDir), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"task_id":"crash-tail"`); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	sub := newControllableTestSubAgent(t, a, "task-guard-crash-tail")
	a.syncTaskRecordFromSub(sub, "")
	calls := 0
	guard := func(*DurableTaskRecord) bool {
		calls++
		return calls == 1
	}
	if got := a.settleDetachedTerminalTaskGuarded(sub.taskID, SubAgentStateCancelled, "expired", "expired", guard); got != "" {
		t.Fatalf("guarded settlement = %q, want rollback", got)
	}
	settlements, err := loadTaskSettlements(a.sessionDir)
	if err != nil {
		t.Fatalf("loadTaskSettlements after rollback: %v", err)
	}
	if len(settlements) != 1 || settlements[taskAttemptKey{TaskID: "task-existing", Attempt: 1}] == nil {
		t.Fatalf("settlements after rollback = %#v, want only the valid pre-crash record", settlements)
	}
}

func TestCommitTerminalTaskIsIdempotent(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "task-terminal")
	first, _, err := a.commitTerminalTask(sub, SubAgentStateCompleted, "done", "task completed", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := a.commitTerminalTask(sub, SubAgentStateCompleted, "done", "task completed", nil)
	if err != nil {
		t.Fatalf("repeat commitTerminalTask: %v", err)
	}
	if first.TerminalRevision != second.TerminalRevision || !first.SettledAt.Equal(second.SettledAt) {
		t.Fatalf("settlements differ: first=%#v second=%#v", first, second)
	}
}

func TestCommitTerminalTaskRejectsConflictingCompletion(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "task-terminal")
	if _, _, err := a.commitTerminalTask(sub, SubAgentStateCompleted, "done", "task completed", &CompletionEnvelope{
		Summary:         "done",
		VerificationRun: []string{"go test ./internal/agent"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.commitTerminalTask(sub, SubAgentStateCompleted, "done", "task completed", &CompletionEnvelope{
		Summary:         "done",
		VerificationRun: []string{"go test ./internal/tools"},
	}); err == nil || !strings.Contains(err.Error(), "conflicting terminal settlement") {
		t.Fatalf("conflicting completion error = %v", err)
	}
}

func TestSettleDetachedTerminalTaskWritesSettlement(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"task-parked": {
			TaskID:            "task-parked",
			Attempt:           1,
			State:             string(SubAgentStateWaitingMain),
			RuntimeParked:     true,
			LifecycleRevision: 3,
		},
	})

	a.settleDetachedTerminalTask("task-parked", SubAgentStateCancelled, "stopped by main agent", "stopped by main agent")

	rec := a.taskRecordByTaskID("task-parked")
	if rec == nil || rec.State != string(SubAgentStateCancelled) || rec.LatestSettlement == nil || !rec.SettlementDurable {
		t.Fatalf("task record = %#v", rec)
	}
	loaded, err := loadTaskSettlements(a.sessionDir)
	if err != nil {
		t.Fatalf("loadTaskSettlements: %v", err)
	}
	settlement := loaded[taskAttemptKey{TaskID: "task-parked", Attempt: 1}]
	if settlement == nil || settlement.Outcome != string(SubAgentStateCancelled) {
		t.Fatalf("journal settlement = %#v, want durable cancelled outcome", settlement)
	}
	a.subs.mu.RLock()
	inMemory := a.subs.settlements[taskAttemptKey{TaskID: "task-parked", Attempt: 1}]
	a.subs.mu.RUnlock()
	if inMemory == nil {
		t.Fatal("in-memory settlement missing; collect would wait forever")
	}
}

func TestSettleDetachedTerminalTaskMirrorsUnsettledTerminalRecord(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"task-replayed": {
			TaskID:            "task-replayed",
			Attempt:           1,
			State:             string(SubAgentStateCompleted),
			LastSummary:       "built and verified",
			RuntimeParked:     true,
			LifecycleRevision: 4,
		},
	})

	// A completed record without any settlement models the crash window where
	// the completion mailbox was replayed but the settlement never hit disk. A
	// later cascade cancel must mint the settlement from the record's own
	// outcome instead of rewriting the completion into a cancel.
	outcome := a.settleDetachedTerminalTask("task-replayed", SubAgentStateCancelled, "stopped by main agent", "stopped by main agent")
	if outcome != SubAgentStateCompleted {
		t.Fatalf("settle outcome = %q, want completed preserved", outcome)
	}

	rec := a.taskRecordByTaskID("task-replayed")
	if rec == nil || rec.State != string(SubAgentStateCompleted) {
		t.Fatalf("task record = %#v, want completed preserved", rec)
	}
	if rec.LastSummary != "built and verified" {
		t.Fatalf("LastSummary = %q, want the recorded completion summary", rec.LastSummary)
	}
	loaded, err := loadTaskSettlements(a.sessionDir)
	if err != nil {
		t.Fatalf("loadTaskSettlements: %v", err)
	}
	settlement := loaded[taskAttemptKey{TaskID: "task-replayed", Attempt: 1}]
	if settlement == nil || settlement.Outcome != string(SubAgentStateCompleted) {
		t.Fatalf("journal settlement = %#v, want completed minted from the record", settlement)
	}
	if settlement.Summary != "built and verified" {
		t.Fatalf("settlement summary = %q, want the recorded completion summary", settlement.Summary)
	}
}

func TestSettleDetachedTerminalTaskPreservesExistingOutcome(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "task-done")
	if _, _, err := a.commitTerminalTask(sub, SubAgentStateCompleted, "done", "task completed", nil); err != nil {
		t.Fatalf("commitTerminalTask: %v", err)
	}
	a.subs.mu.Lock()
	delete(a.subs.subAgents, sub.instanceID)
	a.subs.taskRecords["task-done"].RuntimeParked = true
	a.subs.mu.Unlock()

	// Stopping a parked task whose attempt already settled must not rewrite
	// the completed outcome into a cancel: restore-time repair trusts the
	// journal, so a rewrite would silently revert after restart anyway.
	handle, err := a.stopSubAgentNow("", "", "task-done", "stopped by main agent")
	if err != nil {
		t.Fatalf("stopSubAgentNow: %v", err)
	}
	if handle.Status != string(SubAgentStateCompleted) {
		t.Fatalf("stop status = %q, want completed", handle.Status)
	}

	rec := a.taskRecordByTaskID("task-done")
	if rec == nil || rec.State != string(SubAgentStateCompleted) {
		t.Fatalf("task record state = %#v, want completed preserved", rec)
	}
	if rec.ClosedReason != "task completed" {
		t.Fatalf("ClosedReason = %q, want original completion reason", rec.ClosedReason)
	}
	loaded, err := loadTaskSettlements(a.sessionDir)
	if err != nil {
		t.Fatalf("loadTaskSettlements: %v", err)
	}
	settlement := loaded[taskAttemptKey{TaskID: "task-done", Attempt: 1}]
	if settlement == nil || settlement.Outcome != string(SubAgentStateCompleted) {
		t.Fatalf("journal settlement = %#v, want completed preserved", settlement)
	}
}

func TestCompletedTaskRehydrateRequiresDurableSettlement(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	blockedRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedRoot, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.sessionDir = blockedRoot
	settlement := testSettlement("task-a", 1, 1)
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"task-a": {
			TaskID:            "task-a",
			Attempt:           1,
			State:             string(SubAgentStateCompleted),
			ResumePolicy:      taskResumePolicyNotify,
			LatestSettlement:  settlement,
			SettlementDurable: false,
		},
	})
	_, err := a.sendMessageToSubAgentNow("", "", "task-a", "continue", "follow_up")
	if err == nil || !strings.Contains(err.Error(), "settlement is durable") {
		t.Fatalf("error = %v", err)
	}
}

func TestRetryTaskSettlementDurabilityRepairsCheckpoint(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	resultRef := &tools.ResultRef{ID: "sha256-result", ResultType: "type/test", RelPath: "artifacts/results/result.json", SHA256: strings.Repeat("b", 64), SizeBytes: 12}
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"task-a": {
			TaskID:            "task-a",
			Attempt:           1,
			State:             string(SubAgentStateCompleted),
			LastCompletion:    &CompletionEnvelope{ResultType: "type/test", ResultRef: resultRef},
			SettlementDurable: false,
		},
	})
	rec, err := a.retryTaskSettlementDurability("task-a")
	if err != nil {
		t.Fatalf("retryTaskSettlementDurability: %v", err)
	}
	if rec == nil || !rec.SettlementDurable {
		t.Fatalf("record = %#v", rec)
	}
	if rec.LifecycleRevision != 1 {
		t.Fatalf("lifecycle revision = %d, want terminal revision 1", rec.LifecycleRevision)
	}
	loaded, err := loadTaskSettlements(a.sessionDir)
	if err != nil || loaded[taskAttemptKey{TaskID: "task-a", Attempt: 1}] == nil || loaded[taskAttemptKey{TaskID: "task-a", Attempt: 1}].ResultRef == nil || loaded[taskAttemptKey{TaskID: "task-a", Attempt: 1}].ResultRef.ID != resultRef.ID {
		t.Fatalf("loaded settlements = %#v error=%v", loaded, err)
	}
}

func TestPersistTaskRegistryDoesNotArchiveNonDurableSettlement(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	records := make(map[string]*DurableTaskRecord, maxRetainedTerminalTasks+1)
	for i := 0; i <= maxRetainedTerminalTasks; i++ {
		taskID := fmt.Sprintf("task-%03d", i)
		settlement := testSettlement(taskID, 1, 1)
		records[taskID] = &DurableTaskRecord{
			TaskID: taskID, Attempt: 1, State: string(SubAgentStateCompleted),
			LatestSettlement: settlement, SettlementDurable: i != 0,
			UpdatedAt: time.Unix(int64(i+1), 0),
		}
	}
	a.setTaskRecords(records)
	if err := a.persistTaskRegistry(); err != nil {
		t.Fatalf("persistTaskRegistry: %v", err)
	}
	a.subs.mu.RLock()
	retained := cloneDurableTaskRecord(a.subs.taskRecords["task-000"])
	a.subs.mu.RUnlock()
	if retained == nil || retained.SettlementDurable {
		t.Fatalf("non-durable record = %#v, want retained", retained)
	}
	if _, err := a.retryTaskSettlementDurability("task-000"); err != nil {
		t.Fatalf("retryTaskSettlementDurability: %v", err)
	}
}

func TestUserCancelSettlesTaskBackedSubAgents(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	newControllableTestSubAgent(t, a, "task-user-cancel")

	if !a.interruptSubAgentTurnsForUserCancel() {
		t.Fatal("expected the user cancel to interrupt the live SubAgent")
	}

	rec := a.taskRecordByTaskID("task-user-cancel")
	if rec == nil || rec.State != string(SubAgentStateCancelled) {
		t.Fatalf("task record = %#v, want cancelled", rec)
	}
	if rec.LatestSettlement == nil || rec.LatestSettlement.Outcome != string(SubAgentStateCancelled) {
		t.Fatalf("settlement = %#v, want a cancelled settlement so collect and retention observe the terminal state", rec.LatestSettlement)
	}
	loaded, err := loadTaskSettlements(a.sessionDir)
	if err != nil {
		t.Fatalf("loadTaskSettlements: %v", err)
	}
	if got := loaded[taskAttemptKey{TaskID: "task-user-cancel", Attempt: rec.Attempt}]; got == nil || got.Outcome != string(SubAgentStateCancelled) {
		t.Fatalf("journal settlement = %#v, want durable cancel", got)
	}
}

func TestUserCancelKeepsAlreadySettledOutcome(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "task-user-done")
	if _, _, err := a.commitTerminalTask(sub, SubAgentStateCompleted, "done", "task completed", nil); err != nil {
		t.Fatalf("commitTerminalTask: %v", err)
	}

	// A completed settlement is immutable: the interrupt sweeping over a
	// just-finished SubAgent must not rewrite its outcome into a cancel.
	a.interruptSubAgentTurnsForUserCancel()

	rec := a.taskRecordByTaskID("task-user-done")
	if rec == nil || rec.State != string(SubAgentStateCompleted) {
		t.Fatalf("task record = %#v, want completed preserved", rec)
	}
	if rec.LatestSettlement == nil || rec.LatestSettlement.Outcome != string(SubAgentStateCompleted) {
		t.Fatalf("settlement = %#v, want the completed settlement preserved", rec.LatestSettlement)
	}
}

func TestTerminalStatusAfterCommitMirrorsSettledRecord(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "task-mirror")
	if _, _, err := a.commitTerminalTask(sub, SubAgentStateCompleted, "done", "task completed", nil); err != nil {
		t.Fatalf("commitTerminalTask: %v", err)
	}
	_, _, err := a.commitTerminalTask(sub, SubAgentStateCancelled, "stop", "stop", nil)
	if err == nil {
		t.Fatal("expected a conflicting settlement error")
	}
	if got := a.terminalStatusAfterCommit("task-mirror", SubAgentStateCancelled, err); got != SubAgentStateCompleted {
		t.Fatalf("status after conflict = %v, want completed mirrored from the record", got)
	}
	if got := a.terminalStatusAfterCommit("task-mirror", SubAgentStateCancelled, nil); got != SubAgentStateCancelled {
		t.Fatalf("status after success = %v, want the requested state", got)
	}
}
