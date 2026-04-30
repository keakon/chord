package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/session"
	"github.com/keakon/chord/internal/tools"
)

func (a *MainAgent) exportCompactionHistory(messages []message.Message, index int) (absPath string, relPath string, err error) {
	if err := os.MkdirAll(a.sessionDir, 0o755); err != nil {
		return "", "", err
	}
	absPath = filepath.Join(a.sessionDir, fmt.Sprintf("history-%d.md", index))
	metadata := map[string]string{
		"model":        a.ModelName(),
		"project_path": a.projectRoot,
		"session_id":   a.exportPersistentSessionID(),
		"instance_id":  a.instanceID,
	}
	exported, err := session.Export(messages, nil, metadata)
	if err != nil {
		return "", "", err
	}
	if err := session.ExportMarkdownToFile(exported, absPath); err != nil {
		return "", "", err
	}
	if err := writeCompactionHistoryMeta(compactionHistoryMetaPath(absPath), compactionHistoryMeta{
		Version:     1,
		HistoryFile: filepath.Base(absPath),
		Status:      compactionHistoryPending,
		ExportedAt:  time.Now(),
	}); err != nil {
		return "", "", err
	}
	relPath, err = filepath.Rel(a.projectRoot, absPath)
	if err != nil {
		relPath = absPath
	}
	return absPath, relPath, nil
}

func compactionHistoryMetaPath(absHistoryPath string) string {
	base := strings.TrimSuffix(absHistoryPath, filepath.Ext(absHistoryPath))
	return base + ".status.json"
}

func readCompactionHistoryMeta(path string) (compactionHistoryMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return compactionHistoryMeta{}, err
	}
	var meta compactionHistoryMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return compactionHistoryMeta{}, err
	}
	return meta, nil
}

func writeCompactionHistoryMeta(path string, meta compactionHistoryMeta) error {
	if meta.Version == 0 {
		meta.Version = 1
	}
	if meta.HistoryFile == "" {
		meta.HistoryFile = filepath.Base(strings.TrimSuffix(path, ".status.json") + ".md")
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// getAbsHistoryPathFromDraft extracts absHistoryPath from a draft if available
func getAbsHistoryPathFromDraft(draft *compactionDraft) string {
	if draft == nil || draft.AbsHistoryPath == "" {
		return ""
	}
	return draft.AbsHistoryPath
}

// cleanupOrphanCompactionFiles removes history files and status.json for a
// compaction that was cancelled or failed before apply. This is called when
// the user cancels compaction (ESC) or when startup detects orphan pending files.
func cleanupOrphanCompactionFiles(absHistoryPath string) {
	if absHistoryPath == "" {
		return
	}
	// Remove the history .md file
	if err := os.Remove(absHistoryPath); err != nil && !os.IsNotExist(err) {
		slog.Warn("failed to remove orphan history file", "path", absHistoryPath, "error", err)
	}
	// Remove the .status.json file
	metaPath := compactionHistoryMetaPath(absHistoryPath)
	if err := os.Remove(metaPath); err != nil && !os.IsNotExist(err) {
		slog.Warn("failed to remove orphan history meta file", "path", metaPath, "error", err)
	}
	slog.Debug("cleaned up orphan compaction files", "history_path", absHistoryPath)
}

// cleanupStalePendingCompactions scans sessionDir for history-N.status.json files
// with status "pending_apply" that are older than the threshold, and removes them.
// This handles the case where a compaction was cancelled but the cleanup didn't run
// (e.g., process exit before the cancel event was processed).
func cleanupStalePendingCompactions(sessionDir string, maxAge time.Duration) {
	if sessionDir == "" {
		return
	}
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return
	}
	now := time.Now()
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".status.json") {
			continue
		}
		metaPath := filepath.Join(sessionDir, name)
		meta, err := readCompactionHistoryMeta(metaPath)
		if err != nil {
			continue
		}
		if meta.Status != compactionHistoryPending {
			continue
		}
		age := now.Sub(meta.ExportedAt)
		if age < maxAge {
			continue
		}
		// Check if the corresponding main.pre-compress file exists
		// If it does, the compaction might still be in progress or the apply failed
		// We only clean up if there's no backup file (meaning apply never started)
		indexStr := strings.TrimSuffix(strings.TrimPrefix(name, "history-"), ".status.json")
		backupPath := filepath.Join(sessionDir, fmt.Sprintf("main.pre-compress-%s.jsonl", indexStr))
		if _, err := os.Stat(backupPath); err == nil {
			// Backup exists - this might be a failed apply, leave it for manual inspection
			slog.Debug("skipping orphan cleanup: backup file exists", "meta_path", metaPath, "backup_path", backupPath)
			continue
		}
		// Safe to clean up
		historyPath := filepath.Join(sessionDir, meta.HistoryFile)
		cleanupOrphanCompactionFiles(historyPath)
	}
}

func listHistoryReferences(projectRoot, sessionDir string) ([]string, error) {
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return nil, err
	}
	type historyEntry struct {
		n   int
		rel string
	}
	var histories []historyEntry
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "history-") || !strings.HasSuffix(name, ".md") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "history-"), ".md"))
		if err != nil {
			continue
		}
		abs := filepath.Join(sessionDir, name)
		rel, err := filepath.Rel(projectRoot, abs)
		if err != nil {
			rel = abs
		}
		histories = append(histories, historyEntry{n: n, rel: rel})
	}
	sort.Slice(histories, func(i, j int) bool { return histories[i].n < histories[j].n })
	refs := make([]string, 0, len(histories))
	for _, item := range histories {
		refs = append(refs, item.rel)
	}
	return refs, nil
}

func nextCompactionIndex(sessionDir string) (int, error) {
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 1, nil
		}
		return 0, err
	}
	maxIndex := 0
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "history-") && strings.HasSuffix(name, ".md") {
			if n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "history-"), ".md")); err == nil && n > maxIndex {
				maxIndex = n
			}
		}
		if strings.HasPrefix(name, "main.pre-compress-") && strings.HasSuffix(name, ".jsonl") {
			if n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "main.pre-compress-"), ".jsonl")); err == nil && n > maxIndex {
				maxIndex = n
			}
		}
	}
	return maxIndex + 1, nil
}

func (a *MainAgent) rewriteSessionAfterCompaction(index int, messages []message.Message) (string, error) {
	a.flushPersist()

	mainPath := filepath.Join(a.sessionDir, "main.jsonl")
	backupPath := filepath.Join(a.sessionDir, fmt.Sprintf("main.pre-compress-%d.jsonl", index))
	hadMain := false
	if info, err := os.Stat(mainPath); err == nil && info.Size() > 0 {
		hadMain = true
	}

	if a.recovery != nil {
		a.recovery.Close()
		a.recovery = nil
	}

	if hadMain {
		if err := os.Rename(mainPath, backupPath); err != nil {
			return "", err
		}
	}

	rm := recovery.NewRecoveryManager(a.sessionDir)
	for _, msg := range messages {
		if err := rm.PersistMessage("main", msg); err != nil {
			rm.Close()
			_ = os.Remove(mainPath)
			if hadMain {
				_ = os.Rename(backupPath, mainPath)
			}
			a.recovery = recovery.NewRecoveryManager(a.sessionDir)
			return "", err
		}
	}
	a.recovery = rm
	if a.usageLedger != nil {
		firstUser := ""
		for _, msg := range messages {
			if msg.Role == "user" {
				firstUser = message.UserPromptPlainText(msg)
				if strings.TrimSpace(firstUser) != "" {
					break
				}
			}
		}
		if err := a.usageLedger.RewriteFirstUserMessage(firstUser); err != nil {
			slog.Warn("failed to rewrite usage summary first user message after compaction", "error", err)
		} else {
			originalFirstUser := ""
			if usageSummary, sumErr := a.usageLedger.Summary(); sumErr == nil && usageSummary != nil {
				originalFirstUser = strings.TrimSpace(usageSummary.OriginalFirstUserMessage)
			}
			a.updateSessionSummary(func(summary *SessionSummary) {
				if summary == nil {
					return
				}
				summary.FirstUserMessage = strings.TrimSpace(firstUser)
				if originalFirstUser != "" {
					summary.OriginalFirstUserMessage = originalFirstUser
				}
			})
		}
	}
	if !hadMain {
		backupPath = "(none)"
	}
	return backupPath, nil
}

func nextHistoryIndexMinusOne(sessionDir string) int {
	next, err := nextCompactionIndex(sessionDir)
	if err != nil || next <= 1 {
		return 0
	}
	return next - 1
}

func spawnStatesForSnapshot() []recovery.BackgroundObjectState {
	jobs := tools.SnapshotSpawnedProcesses()
	if len(jobs) == 0 {
		return nil
	}
	states := make([]recovery.BackgroundObjectState, 0, len(jobs))
	for _, job := range jobs {
		states = append(states, recovery.BackgroundObjectState{
			ID:            job.ID,
			AgentID:       job.AgentID,
			Kind:          job.Kind,
			Description:   job.Description,
			Command:       job.Command,
			StartedAt:     job.StartedAt,
			MaxRuntimeSec: job.MaxRuntimeSec,
			Status:        job.Status,
			FinishedAt:    job.FinishedAt,
		})
	}
	return states
}

func (a *MainAgent) saveRecoverySnapshot() {
	if a.recovery == nil || a.shuttingDown.Load() {
		return
	}

	a.todoMu.RLock()
	todoStates := snapshotTodos(a.todoItems)
	a.todoMu.RUnlock()

	a.mu.RLock()
	agents := make([]recovery.AgentSnapshot, 0, len(a.subAgents))
	for _, sub := range a.subAgents {
		state := sub.State()
		summary := sub.LastSummary()
		pendingComplete := sub.PendingCompleteIntent()
		snap := recovery.AgentSnapshot{
			InstanceID:            sub.instanceID,
			TaskID:                sub.taskID,
			AgentDefName:          sub.agentDefName,
			TaskDesc:              sub.taskDesc,
			OwnerAgentID:          sub.OwnerAgentID(),
			OwnerTaskID:           sub.OwnerTaskID(),
			Depth:                 sub.Depth(),
			JoinToOwner:           sub.joinToOwner,
			State:                 string(state),
			LastSummary:           summary,
			PendingCompleteIntent: pendingComplete != nil,
		}
		if pendingComplete != nil {
			snap.PendingCompleteSummary = pendingComplete.Summary
			snap.PendingCompleteEnvelope = marshalCompletionEnvelope(pendingComplete.Envelope)
		}
		agents = append(agents, snap)
	}
	a.mu.RUnlock()

	usageSnap := a.usageTracker.SessionStats()
	if err := a.recovery.SaveSnapshot(&recovery.SessionSnapshot{
		Todos:                   todoStates,
		ActiveAgents:            agents,
		ModelName:               a.ModelName(),
		ActiveRole:              a.CurrentRole(),
		CreatedAt:               time.Now(),
		LastInputTokens:         a.ctxMgr.LastInputTokens(),
		LastTotalContextTokens:  a.ctxMgr.LastTotalContextTokens(),
		CompactionGeneration:    a.nextCompactionPlanID,
		LastHistoryIndex:        nextHistoryIndexMinusOne(a.sessionDir),
		SessionEpoch:            a.sessionEpoch,
		ActiveBackgroundObjects: spawnStatesForSnapshot(),
		UsageInputTokens:        usageSnap.InputTokens,
		UsageOutputTokens:       usageSnap.OutputTokens,
		UsageCacheReadTokens:    usageSnap.CacheReadTokens,
		UsageCacheWriteTokens:   usageSnap.CacheWriteTokens,
		UsageReasoningTokens:    usageSnap.ReasoningTokens,
		UsageLLMCalls:           usageSnap.LLMCalls,
		UsageEstimatedCost:      usageSnap.EstimatedCost,
		UsageByModel:            usageSnap.ByModel,
		UsageByAgent:            usageSnap.ByAgent,
	}); err != nil {
		slog.Warn("failed to save recovery snapshot", "error", err)
	}
}
