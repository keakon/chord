package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/keakon/chord/internal/privatefs"
)

const (
	mailboxCompactionThreshold = 1024
	mailboxConsumedHistoryKeep = 128
)

type archivedTaskRecord struct {
	ArchivedAt time.Time          `json:"archived_at"`
	Task       *DurableTaskRecord `json:"task"`
}

func (a *MainAgent) archiveEligibleTerminalTasks(records map[string]*DurableTaskRecord, sessionDir string) (map[string]*DurableTaskRecord, error) {
	if len(records) <= maxRetainedTerminalTasks {
		return records, nil
	}
	a.focusedTaskMu.RLock()
	focusedTaskID := strings.TrimSpace(a.focusedTaskID)
	a.focusedTaskMu.RUnlock()
	a.subAgentMailboxIDsMu.Lock()
	consumed := make(map[string]struct{}, len(a.subAgentMailboxConsumed))
	for id := range a.subAgentMailboxConsumed {
		consumed[id] = struct{}{}
	}
	a.subAgentMailboxIDsMu.Unlock()

	hasNonTerminalChild := make(map[string]bool)
	for _, rec := range records {
		if rec != nil && isNonTerminalTaskState(rec.State) && strings.TrimSpace(rec.OwnerTaskID) != "" {
			hasNonTerminalChild[strings.TrimSpace(rec.OwnerTaskID)] = true
		}
	}
	candidates := make([]*DurableTaskRecord, 0)
	for _, rec := range records {
		if rec == nil || rec.TaskID == focusedTaskID || hasNonTerminalChild[rec.TaskID] || isNonTerminalTaskState(rec.State) {
			continue
		}
		if mailboxID := strings.TrimSpace(rec.LastMailboxID); mailboxID != "" {
			if _, ok := consumed[mailboxID]; !ok {
				continue
			}
		}
		candidates = append(candidates, cloneDurableTaskRecord(rec))
	}
	if len(candidates) <= maxRetainedTerminalTasks {
		return records, nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].UpdatedAt.Equal(candidates[j].UpdatedAt) {
			return candidates[i].TaskID < candidates[j].TaskID
		}
		return candidates[i].UpdatedAt.Before(candidates[j].UpdatedAt)
	})
	toArchive := candidates[:len(candidates)-maxRetainedTerminalTasks]
	if err := appendTaskArchive(sessionDir, toArchive); err != nil {
		return records, err
	}
	out := cloneDurableTaskRecordMap(records)
	for _, rec := range toArchive {
		delete(out, rec.TaskID)
	}
	return out, nil
}

func loadArchivedTaskRecordByTaskID(sessionDir, taskID string) (*DurableTaskRecord, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, nil
	}
	return loadArchivedTaskRecord(sessionDir, func(rec *DurableTaskRecord) bool {
		return rec.TaskID == taskID
	})
}

func loadArchivedTaskRecordByInstanceID(sessionDir, instanceID string) (*DurableTaskRecord, error) {
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return nil, nil
	}
	return loadArchivedTaskRecord(sessionDir, func(rec *DurableTaskRecord) bool {
		return durableTaskRecordIncludesInstance(rec, instanceID)
	})
}

func loadArchivedTaskRecord(sessionDir string, matches func(*DurableTaskRecord) bool) (*DurableTaskRecord, error) {
	path := durableTaskArchivePath(sessionDir)
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open task archive: %w", err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	var found *DurableTaskRecord
	for {
		var entry archivedTaskRecord
		if err := dec.Decode(&entry); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decode task archive: %w", err)
		}
		rec := cloneDurableTaskRecord(entry.Task)
		if rec != nil && matches(rec) {
			found = rec
		}
	}
	return found, nil
}

func appendTaskArchive(sessionDir string, records []*DurableTaskRecord) error {
	path := durableTaskArchivePath(sessionDir)
	if path == "" || len(records) == 0 {
		return nil
	}
	f, err := privatefs.OpenFile(sessionDir, path, os.O_CREATE|os.O_WRONLY|os.O_APPEND)
	if err != nil {
		return fmt.Errorf("open task archive: %w", err)
	}
	enc := json.NewEncoder(f)
	now := time.Now()
	for _, rec := range records {
		if err := enc.Encode(archivedTaskRecord{ArchivedAt: now, Task: rec}); err != nil {
			_ = f.Close()
			return fmt.Errorf("append task archive: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close task archive: %w", err)
	}
	return nil
}

func compactSubAgentMailboxLogs(sessionDir string, msgs []SubAgentMailboxMessage) error {
	if len(msgs) < mailboxCompactionThreshold {
		return nil
	}
	latestProgress := make(map[string]int)
	for i, msg := range msgs {
		if msg.Kind == SubAgentMailboxKindProgress {
			key := strings.TrimSpace(msg.TaskID)
			if key == "" {
				key = strings.TrimSpace(msg.AgentID)
			}
			latestProgress[key] = i
		}
	}
	keepConsumedFrom := max(len(msgs)-mailboxConsumedHistoryKeep, 0)
	kept := make([]SubAgentMailboxMessage, 0, len(msgs))
	for i, msg := range msgs {
		if !msg.Consumed {
			if msg.Kind != SubAgentMailboxKindProgress {
				kept = append(kept, msg)
				continue
			}
			key := strings.TrimSpace(msg.TaskID)
			if key == "" {
				key = strings.TrimSpace(msg.AgentID)
			}
			if latestProgress[key] == i {
				kept = append(kept, msg)
			}
			continue
		}
		if i >= keepConsumedFrom {
			kept = append(kept, msg)
		}
	}
	if len(kept) == len(msgs) {
		return nil
	}
	if err := rewriteMailboxLog(sessionDir, kept); err != nil {
		return err
	}
	return compactMailboxAckLog(sessionDir, kept)
}

func rewriteMailboxLog(sessionDir string, msgs []SubAgentMailboxMessage) error {
	path := filepath.Join(sessionDir, "subagents", "mailbox.jsonl")
	tmpPath := filepath.Join(sessionDir, "subagents", fmt.Sprintf("mailbox.%d.jsonl.tmp", time.Now().UnixNano()))
	f, err := privatefs.OpenFile(sessionDir, tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("open compacted mailbox log: %w", err)
	}
	enc := json.NewEncoder(f)
	for _, msg := range msgs {
		msg.Consumed = false
		if err := enc.Encode(msg); err != nil {
			_ = f.Close()
			return fmt.Errorf("write compacted mailbox log: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close compacted mailbox log: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace mailbox log: %w", err)
	}
	return nil
}

func compactMailboxAckLog(sessionDir string, kept []SubAgentMailboxMessage) error {
	acks, err := loadSubAgentMailboxAcks(sessionDir)
	if err != nil {
		return err
	}
	keepIDs := make(map[string]struct{}, len(kept))
	for _, msg := range kept {
		if id := strings.TrimSpace(msg.MessageID); id != "" {
			keepIDs[id] = struct{}{}
		}
	}
	path := filepath.Join(sessionDir, "subagents", "mailbox-acks.jsonl")
	tmpPath := filepath.Join(sessionDir, "subagents", fmt.Sprintf("mailbox-acks.%d.jsonl.tmp", time.Now().UnixNano()))
	f, err := privatefs.OpenFile(sessionDir, tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("open compacted mailbox ack log: %w", err)
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	ids := make([]string, 0, len(keepIDs))
	for id := range keepIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if ack, ok := acks[id]; ok {
			if err := enc.Encode(ack); err != nil {
				_ = f.Close()
				return fmt.Errorf("write compacted mailbox ack log: %w", err)
			}
		}
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return fmt.Errorf("flush compacted mailbox ack log: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close compacted mailbox ack log: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace mailbox ack log: %w", err)
	}
	return nil
}
