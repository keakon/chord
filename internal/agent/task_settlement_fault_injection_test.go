package agent

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// settlementFaultModel drives the durable task-settlement protocol through
// random commit attempts (terminal outcomes, duplicates, conflicts, stale
// attempts), crash windows (journal append durable while the task registry
// write is lost), journal damage (crash tails, corrupt complete lines) and
// restart reconciliation. Every mutation goes through the production
// functions: commitTerminalTask, appendTaskSettlement, loadTaskSettlements,
// persistTaskRegistry/loadDurableTaskRecords, migrateLegacyTaskSettlements,
// repairTaskRecordsFromSettlements, quarantine + reseed. The model's own
// winner map is only an oracle of "first durable content per attempt".
type settlementFaultModel struct {
	t          *testing.T
	rng        *rand.Rand
	dir        string
	sessionDir string
	taskID     string
	agent      *MainAgent
	sub        *SubAgent
	attempt    uint64
	winner     map[uint64]*TaskSettlement
	instance   int
}

func newSettlementFaultModel(t *testing.T, seed int64) *settlementFaultModel {
	t.Helper()
	dir := t.TempDir()
	m := &settlementFaultModel{
		t:          t,
		rng:        rand.New(rand.NewSource(seed)),
		dir:        dir,
		sessionDir: filepath.Join(dir, ".chord", "sessions", "test"),
		taskID:     "task-fault-model",
		winner:     make(map[uint64]*TaskSettlement),
	}
	m.agent = newTestMainAgent(t, dir)
	m.focusAttempt(1)
	return m
}

func (m *settlementFaultModel) freshSub() {
	m.instance++
	ctx, cancel := context.WithCancel(m.agent.parentCtx)
	sub := NewSubAgent(SubAgentConfig{
		InstanceID:   fmt.Sprintf("worker-fault-%d", m.instance),
		TaskID:       m.taskID,
		AgentDefName: "worker",
		TaskDesc:     "model task",
		LLMClient:    newTestLLMClient(),
		Recovery:     m.agent.recoveryManager(),
		Parent:       m.agent,
		ParentCtx:    ctx,
		Cancel:       cancel,
		BaseTools:    m.agent.tools,
		WorkDir:      m.agent.contentRoot,
		SessionDir:   m.agent.sessionDir,
		ModelName:    "test-model",
	})
	m.agent.subs.mu.Lock()
	m.agent.subs.subAgents[sub.instanceID] = sub
	m.agent.subs.mu.Unlock()
	m.sub = sub
}

// focusAttempt points the agent at a fresh attempt lineage: an in-memory
// running record persisted to the registry (mirrors spawn registration) and a
// fresh runtime instance.
func (m *settlementFaultModel) focusAttempt(attempt uint64) {
	if attempt == 0 {
		attempt = 1
	}
	records, err := loadDurableTaskRecords(m.sessionDir)
	if err != nil {
		m.t.Fatalf("focus attempt %d: load records: %v", attempt, err)
	}
	rec := records[m.taskID]
	if rec == nil {
		rec = &DurableTaskRecord{TaskID: m.taskID}
	}
	rec = cloneDurableTaskRecord(rec)
	rec.Attempt = attempt
	rec.State = string(SubAgentStateRunning)
	if rec.LifecycleRevision == 0 {
		rec.LifecycleRevision = 1
	}
	m.agent.setTaskRecords(map[string]*DurableTaskRecord{m.taskID: rec})
	if err := m.agent.persistTaskRegistry(); err != nil {
		m.t.Fatalf("focus attempt %d: persist registry: %v", attempt, err)
	}
	m.attempt = attempt
	m.freshSub()
}

func (m *settlementFaultModel) journalLines() []string {
	path := taskSettlementJournalPath(m.sessionDir)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		m.t.Fatalf("read journal: %v", err)
	}
	var lines []string
	for line := range bytes.SplitSeq(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) > 0 {
			lines = append(lines, string(bytes.TrimSpace(line)))
		}
	}
	return lines
}

func (m *settlementFaultModel) checkJournal() {
	m.t.Helper()
	loaded, err := loadTaskSettlements(m.sessionDir)
	if err != nil {
		m.t.Fatalf("journal load failed: %v", err)
	}
	for attempt, winner := range m.winner {
		got := loaded[taskAttemptKey{TaskID: m.taskID, Attempt: attempt}]
		if got == nil {
			m.t.Fatalf("attempt %d winner missing from journal", attempt)
		}
		if !taskSettlementContentEqual(got, winner) {
			m.t.Fatalf("attempt %d journal content diverged from winner", attempt)
		}
	}
}

// barrierRegistryPersist makes the next persistTaskRegistry fail (rename onto
// a directory at the registry path), emulating a crash between the durable
// journal append and the registry write inside commitTerminalTask.
func (m *settlementFaultModel) barrierRegistryPersist() (restore func()) {
	path := durableTaskRegistryPath(m.sessionDir)
	saved := ""
	if _, err := os.Stat(path); err == nil {
		saved = path + ".saved"
		if err := os.Rename(path, saved); err != nil {
			m.t.Fatalf("move registry aside: %v", err)
		}
	} else if !os.IsNotExist(err) {
		m.t.Fatalf("stat registry: %v", err)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		m.t.Fatalf("mkdir registry barrier: %v", err)
	}
	return func() {
		if err := os.Remove(path); err != nil {
			m.t.Fatalf("remove registry barrier: %v", err)
		}
		if saved != "" {
			if err := os.Rename(saved, path); err != nil {
				m.t.Fatalf("restore registry: %v", err)
			}
		}
	}
}

// commit runs one settlement attempt through the production commit path. With
// crashWindow the registry write fails after the journal append succeeded.
func (m *settlementFaultModel) commit(crashWindow bool) error {
	attempt := m.attempt
	linesBefore := len(m.journalLines())
	winner := m.winner[attempt]

	var outcome SubAgentState
	summary := fmt.Sprintf("done-attempt-%d", attempt)
	if winner == nil {
		options := []SubAgentState{SubAgentStateCompleted, SubAgentStateCancelled, SubAgentStateFailed}
		outcome = options[m.rng.Intn(len(options))]
	} else if m.rng.Intn(2) == 0 {
		// Duplicate of the committed winner.
		outcome = SubAgentState(winner.Outcome)
		summary = winner.Summary
	} else {
		// Conflict: different outcome.
		if winner.Outcome == string(SubAgentStateCompleted) {
			outcome = SubAgentStateCancelled
		} else {
			outcome = SubAgentStateCompleted
		}
		summary = "conflicting-summary"
	}

	if crashWindow {
		restore := m.barrierRegistryPersist()
		defer restore()
	}
	settlement, durable, err := m.agent.commitTerminalTask(m.sub, outcome, summary, "model closed", nil)

	if winner != nil && (winner.Outcome != string(outcome) || winner.Summary != summary) {
		if err == nil || !strings.Contains(err.Error(), "conflicting terminal settlement") {
			return fmt.Errorf("expected conflicting terminal settlement, got err=%v", err)
		}
		if len(m.journalLines()) != linesBefore {
			return fmt.Errorf("conflict mutated journal: %d -> %d lines", linesBefore, len(m.journalLines()))
		}
		return nil
	}
	if winner == nil && !crashWindow && err != nil {
		return fmt.Errorf("commit attempt %d failed: err=%v", attempt, err)
	}
	if winner == nil && crashWindow && (err == nil || !durable) {
		return fmt.Errorf("crash-window commit: err=%v durable=%v, want durable journal append with registry failure", err, durable)
	}
	if winner != nil {
		if !crashWindow && err != nil {
			return fmt.Errorf("duplicate commit failed: %v", err)
		}
		if !taskSettlementContentEqual(settlement, winner) {
			return fmt.Errorf("duplicate settlement diverged from winner")
		}
		if len(m.journalLines()) != linesBefore {
			return fmt.Errorf("duplicate commit appended to journal: %d -> %d lines", linesBefore, len(m.journalLines()))
		}
		return nil
	}
	m.winner[attempt] = cloneTaskSettlement(settlement)
	return nil
}

func (m *settlementFaultModel) restart() {
	records, err := loadDurableTaskRecords(m.sessionDir)
	if err != nil {
		m.t.Fatalf("restart: load records: %v", err)
	}
	settlements, err := loadTaskSettlements(m.sessionDir)
	if err != nil {
		if !isTaskSettlementJournalCorruption(err) {
			m.t.Fatalf("restart: load settlements: %v", err)
		}
		// Mirror restore: quarantine and reseed the valid prefix.
		quarantinePath, quarantineErr := quarantineCorruptTaskSettlementJournal(m.sessionDir)
		if quarantineErr != nil || quarantinePath == "" {
			m.t.Fatalf("restart: quarantine corrupt journal: %v %q", quarantineErr, quarantinePath)
		}
		if reseedErr := reseedTaskSettlementJournal(m.sessionDir, settlements); reseedErr != nil {
			m.t.Fatalf("restart: reseed journal: %v", reseedErr)
		}
	}
	settlements, err = migrateLegacyTaskSettlements(m.sessionDir, records, settlements)
	if err != nil {
		m.t.Fatalf("restart: migrate settlements: %v", err)
	}
	repairTaskRecordsFromSettlements(records, settlements)

	// Convergence: a record whose attempt has a journal winner must carry that
	// winner's terminal state and be marked durable.
	if rec := records[m.taskID]; rec != nil {
		if winner := m.winner[rec.Attempt]; winner != nil {
			if rec.State != winner.Outcome || !rec.SettlementDurable || !taskSettlementContentEqual(rec.LatestSettlement, winner) {
				m.t.Fatalf("restart: record not converged: state=%q durable=%v", rec.State, rec.SettlementDurable)
			}
		}
	}

	m.agent = newTestMainAgent(m.t, m.dir)
	m.agent.setTaskRecords(records)
	m.agent.resetTaskCoordination(1, settlements)
	m.freshSub()
}

func (m *settlementFaultModel) injectCrashTail() {
	path := taskSettlementJournalPath(m.sessionDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		m.t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		m.t.Fatal(err)
	}
	if _, err := f.WriteString(`{"task_id":"crash-partial`); err != nil {
		_ = f.Close()
		m.t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		m.t.Fatal(err)
	}
}

func (m *settlementFaultModel) injectCorruptLine() {
	path := taskSettlementJournalPath(m.sessionDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		m.t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		m.t.Fatal(err)
	}
	if _, err := f.WriteString("this is not a settlement line\n"); err != nil {
		_ = f.Close()
		m.t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		m.t.Fatal(err)
	}
}

func TestTaskSettlementFaultInjectionModel(t *testing.T) {
	for seed := range int64(6) {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			model := newSettlementFaultModel(t, seed)
			bumped := false
			corrupted := false
			for round := range 50 {
				roll := model.rng.Intn(100)
				var opErr error
				switch {
				case roll < 45:
					opErr = model.commit(model.rng.Intn(100) < 20)
				case roll < 65:
					model.restart()
				case roll < 75:
					if !bumped && model.winner[1] != nil {
						bumped = true
						model.focusAttempt(2)
					}
				case roll < 85:
					model.injectCrashTail()
					model.restart()
				default:
					if !corrupted {
						corrupted = true
						model.injectCorruptLine()
						if _, err := loadTaskSettlements(model.sessionDir); err == nil || !isTaskSettlementJournalCorruption(err) {
							opErr = fmt.Errorf("corrupt complete line not detected as journal corruption: %v", err)
						} else {
							model.restart()
						}
					}
				}
				if opErr != nil {
					t.Fatalf("seed=%d round=%d: %v", seed, round, opErr)
				}
				model.checkJournal()
			}
		})
	}
}
