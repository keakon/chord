package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/keakon/chord/internal/privatefs"
	"github.com/keakon/chord/internal/tools"
)

type taskAttemptKey struct {
	TaskID  string
	Attempt uint64
}

type taskSettlementJournalCorruptionError struct {
	err error
}

func (e *taskSettlementJournalCorruptionError) Error() string { return e.err.Error() }
func (e *taskSettlementJournalCorruptionError) Unwrap() error { return e.err }

func isTaskSettlementJournalCorruption(err error) bool {
	var corruption *taskSettlementJournalCorruptionError
	return errors.As(err, &corruption)
}

type TaskSettlement struct {
	TaskID           string              `json:"task_id"`
	Attempt          uint64              `json:"attempt"`
	TerminalRevision uint64              `json:"terminal_revision"`
	Outcome          string              `json:"outcome"`
	Summary          string              `json:"summary,omitempty"`
	Completion       *CompletionEnvelope `json:"completion,omitempty"`
	ArtifactRefs     []tools.ArtifactRef `json:"artifact_refs,omitempty"`
	ResultRef        *tools.ResultRef    `json:"result_ref,omitempty"`
	SettledAt        time.Time           `json:"settled_at"`
}

func cloneTaskSettlement(in *TaskSettlement) *TaskSettlement {
	if in == nil {
		return nil
	}
	out := *in
	out.TaskID = strings.TrimSpace(out.TaskID)
	out.Outcome = strings.TrimSpace(out.Outcome)
	out.Summary = strings.TrimSpace(out.Summary)
	out.Completion = normalizeCompletionEnvelope(out.Completion)
	out.ArtifactRefs = tools.NormalizeArtifactRefs(out.ArtifactRefs)
	if out.ResultRef != nil {
		ref := *out.ResultRef
		out.ResultRef = &ref
	}
	return &out
}

func taskSettlementJournalPath(sessionDir string) string {
	if strings.TrimSpace(sessionDir) == "" {
		return ""
	}
	return filepath.Join(sessionDir, "subagents", "task-settlements.jsonl")
}

func quarantineCorruptTaskSettlementJournal(sessionDir string) (string, error) {
	path := taskSettlementJournalPath(sessionDir)
	if path == "" {
		return "", nil
	}
	quarantinePath := fmt.Sprintf("%s.corrupt-%d", path, time.Now().UnixNano())
	if err := os.Rename(path, quarantinePath); err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("quarantine corrupt task settlement journal: %w", err)
	}
	return quarantinePath, nil
}

func canonicalTaskSettlement(settlement *TaskSettlement) ([]byte, error) {
	settlement = cloneTaskSettlement(settlement)
	if settlement == nil || settlement.TaskID == "" || settlement.Attempt == 0 || settlement.TerminalRevision == 0 {
		return nil, fmt.Errorf("invalid task settlement identity")
	}
	switch settlement.Outcome {
	case string(SubAgentStateCompleted), string(SubAgentStateFailed), string(SubAgentStateCancelled):
	default:
		return nil, fmt.Errorf("invalid task settlement outcome %q", settlement.Outcome)
	}
	return json.Marshal(settlement)
}

func taskSettlementContentEqual(left, right *TaskSettlement) bool {
	if left == nil || right == nil {
		return left == right
	}
	left = cloneTaskSettlement(left)
	right = cloneTaskSettlement(right)
	left.SettledAt = time.Time{}
	right.SettledAt = time.Time{}
	leftCanonical, leftErr := canonicalTaskSettlement(left)
	rightCanonical, rightErr := canonicalTaskSettlement(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftCanonical, rightCanonical)
}

func loadTaskSettlements(sessionDir string) (map[taskAttemptKey]*TaskSettlement, error) {
	path := taskSettlementJournalPath(sessionDir)
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	lines := bytes.Split(data, []byte{'\n'})
	out := make(map[taskAttemptKey]*TaskSettlement)
	for i, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var settlement TaskSettlement
		if err := json.Unmarshal(line, &settlement); err != nil {
			if i == len(lines)-1 && len(data) > 0 && data[len(data)-1] != '\n' {
				break
			}
			return out, &taskSettlementJournalCorruptionError{err: fmt.Errorf("decode task settlement journal line %d: %w", i+1, err)}
		}
		canonical, err := canonicalTaskSettlement(&settlement)
		if err != nil {
			return out, &taskSettlementJournalCorruptionError{err: fmt.Errorf("validate task settlement journal line %d: %w", i+1, err)}
		}
		key := taskAttemptKey{TaskID: strings.TrimSpace(settlement.TaskID), Attempt: settlement.Attempt}
		if existing := out[key]; existing != nil {
			existingCanonical, _ := canonicalTaskSettlement(existing)
			if !bytes.Equal(existingCanonical, canonical) {
				return out, &taskSettlementJournalCorruptionError{err: fmt.Errorf("conflicting task settlements for task %s attempt %d", key.TaskID, key.Attempt)}
			}
			continue
		}
		out[key] = cloneTaskSettlement(&settlement)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func appendTaskSettlement(sessionDir string, settlement *TaskSettlement) error {
	_, err := appendTaskSettlementWithRollbackOffset(sessionDir, settlement)
	return err
}

// appendTaskSettlementWithRollbackOffset returns the file offset immediately
// before the new record. The offset is measured after an incomplete crash tail
// is removed, so guarded callers can roll back the append without restoring a
// stale pre-truncation size and manufacturing a new corrupt tail.
func appendTaskSettlementWithRollbackOffset(sessionDir string, settlement *TaskSettlement) (int64, error) {
	path := taskSettlementJournalPath(sessionDir)
	if path == "" {
		return -1, nil
	}
	canonical, err := canonicalTaskSettlement(settlement)
	if err != nil {
		return -1, err
	}
	f, err := privatefs.OpenFile(sessionDir, path, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return -1, fmt.Errorf("open task settlement journal: %w", err)
	}
	stat, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return -1, fmt.Errorf("stat task settlement journal: %w", err)
	}
	if stat.Size() > 0 {
		if err := truncateIncompleteTaskSettlementTail(f, stat.Size()); err != nil {
			_ = f.Close()
			return -1, err
		}
	}
	rollbackOffset, err := f.Seek(0, 2)
	if err != nil {
		_ = f.Close()
		return -1, fmt.Errorf("seek task settlement journal: %w", err)
	}
	w := bufio.NewWriter(f)
	if _, err := w.Write(append(canonical, '\n')); err != nil {
		_ = f.Close()
		return -1, fmt.Errorf("append task settlement journal: %w", err)
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return -1, fmt.Errorf("flush task settlement journal: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return -1, fmt.Errorf("sync task settlement journal: %w", err)
	}
	if err := f.Close(); err != nil {
		return -1, fmt.Errorf("close task settlement journal: %w", err)
	}
	return rollbackOffset, nil
}

func truncateTaskSettlementJournal(sessionDir string, size int64) error {
	path := taskSettlementJournalPath(sessionDir)
	if path == "" || size < 0 {
		return nil
	}
	f, err := privatefs.OpenFile(sessionDir, path, os.O_RDWR)
	if err != nil {
		return err
	}
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func truncateIncompleteTaskSettlementTail(f *os.File, size int64) error {
	if f == nil || size <= 0 {
		return nil
	}
	var last [1]byte
	if _, err := f.ReadAt(last[:], size-1); err != nil {
		return fmt.Errorf("inspect task settlement journal tail: %w", err)
	}
	if last[0] == '\n' {
		return nil
	}
	const scanBytes = 4 * 1024
	buf := make([]byte, scanBytes)
	for end := size; end > 0; {
		start := max(end-int64(len(buf)), 0)
		chunk := buf[:int(end-start)]
		if _, err := f.ReadAt(chunk, start); err != nil {
			return fmt.Errorf("inspect incomplete task settlement tail: %w", err)
		}
		if newline := bytes.LastIndexByte(chunk, '\n'); newline >= 0 {
			if err := f.Truncate(start + int64(newline) + 1); err != nil {
				return fmt.Errorf("truncate incomplete task settlement tail: %w", err)
			}
			return nil
		}
		end = start
	}
	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("truncate incomplete task settlement tail: %w", err)
	}
	return nil
}

func repairTaskRecordsFromSettlements(records map[string]*DurableTaskRecord, settlements map[taskAttemptKey]*TaskSettlement) bool {
	changed := false
	for key, settlement := range settlements {
		rec := records[key.TaskID]
		if rec == nil {
			continue
		}
		if rec.Attempt == 0 {
			rec.Attempt = 1
			changed = true
		}
		if key.Attempt != rec.Attempt {
			continue
		}
		if !taskSettlementContentEqual(rec.LatestSettlement, settlement) || !rec.SettlementDurable {
			rec.LatestSettlement = cloneTaskSettlement(settlement)
			rec.SettlementDurable = true
			rec.State = settlement.Outcome
			rec.LastSummary = settlement.Summary
			rec.LastCompletion = normalizeCompletionEnvelope(settlement.Completion)
			rec.LastArtifactRefs = mergeArtifactRefs(rec.LastArtifactRefs, settlement.ArtifactRefs)
			if rec.LifecycleRevision < settlement.TerminalRevision {
				rec.LifecycleRevision = settlement.TerminalRevision
			}
			changed = true
		}
	}
	return changed
}

func migrateLegacyTaskSettlements(sessionDir string, records map[string]*DurableTaskRecord, settlements map[taskAttemptKey]*TaskSettlement) (map[taskAttemptKey]*TaskSettlement, error) {
	if settlements == nil {
		settlements = make(map[taskAttemptKey]*TaskSettlement)
	}
	for _, rec := range records {
		if rec == nil || !isTerminalSubAgentState(SubAgentState(rec.State)) {
			continue
		}
		if rec.Attempt == 0 {
			rec.Attempt = 1
		}
		key := taskAttemptKey{TaskID: rec.TaskID, Attempt: rec.Attempt}
		if settlements[key] != nil {
			continue
		}
		revision := rec.LifecycleRevision
		if revision == 0 {
			revision = 1
		}
		settledAt := rec.UpdatedAt
		if settledAt.IsZero() {
			settledAt = rec.CreatedAt
		}
		if settledAt.IsZero() {
			settledAt = time.Now()
		}
		settlement := &TaskSettlement{
			TaskID:           rec.TaskID,
			Attempt:          rec.Attempt,
			TerminalRevision: revision,
			Outcome:          rec.State,
			Summary:          rec.LastSummary,
			Completion:       rec.LastCompletion,
			ArtifactRefs:     rec.LastArtifactRefs,
			SettledAt:        settledAt,
		}
		if rec.LastCompletion != nil && rec.LastCompletion.ResultRef != nil {
			ref := *rec.LastCompletion.ResultRef
			settlement.ResultRef = &ref
		}
		if err := appendTaskSettlement(sessionDir, settlement); err != nil {
			return settlements, fmt.Errorf("migrate legacy task %s settlement: %w", rec.TaskID, err)
		}
		settlements[key] = cloneTaskSettlement(settlement)
	}
	return settlements, nil
}

func (a *MainAgent) resetTaskCoordination(sessionEpoch uint64, settlements map[taskAttemptKey]*TaskSettlement) {
	a.subs.mu.Lock()
	a.subs.settlements = make(map[taskAttemptKey]*TaskSettlement, len(settlements))
	for key, settlement := range settlements {
		a.subs.settlements[key] = cloneTaskSettlement(settlement)
	}
	a.subs.sessionEpoch = sessionEpoch
	a.subs.notifyTaskChangeLocked()
	a.subs.mu.Unlock()
}
