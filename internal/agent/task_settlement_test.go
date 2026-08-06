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
