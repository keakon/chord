package agent

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/recovery"
)

// Validate saved execution dependencies before installing a paused session.
// Ordinary history retains its best-effort metadata recovery behavior.
func (a *MainAgent) validatePausedSessionRestore(loaded *loadedSessionState) error {
	if loaded.Pause == nil {
		return nil
	}
	meta, err := recovery.LoadSessionMeta(loaded.SessionPath)
	if err != nil {
		return fmt.Errorf("restore_failed: read paused session metadata: %w", err)
	}
	if meta != nil && meta.WorktreePath != "" {
		info, err := os.Stat(meta.WorktreePath)
		if err != nil {
			return fmt.Errorf("restore_failed: saved working directory is unavailable: %w", err)
		}
		if !info.IsDir() {
			return pauseError("restore_failed", "saved working directory is not a directory")
		}
	}
	if role := strings.TrimSpace(loaded.ActiveRole); role != "" && a.agentConfigs != nil {
		cfg := a.agentConfigs[role]
		if cfg == nil || cfg.IsSubAgent() || cfg.IsExternalBackend() {
			return fmt.Errorf("restore_failed: saved main role %q is unavailable", role)
		}
	}
	pendingOwners := make(map[string]bool)
	for _, input := range loaded.Pause.PendingInputs {
		if input.OwnerTaskID == identity.MainAgentID {
			continue
		}
		rec := loaded.TaskRecords[input.OwnerTaskID]
		if rec == nil || rec.TaskID != input.OwnerTaskID {
			return fmt.Errorf("restore_failed: pending input %q owner %q is unavailable", input.InputID, input.OwnerTaskID)
		}
		pendingOwners[input.OwnerTaskID] = true
	}
	for _, rec := range loaded.TaskRecords {
		if rec == nil || isTerminalSubAgentState(SubAgentState(rec.State)) && !pendingOwners[rec.TaskID] {
			continue
		}
		name := strings.TrimSpace(rec.AgentDefName)
		if a.agentConfigs != nil {
			cfg := a.agentConfigs[name]
			if cfg == nil || !cfg.IsSubAgent() {
				return fmt.Errorf("restore_failed: saved task %q agent %q is unavailable", rec.TaskID, name)
			}
		}
		if _, err := a.restorePausedTaskWorkDir(a.parentCtx, loaded.SessionPath, rec); err != nil {
			return err
		}
	}
	return nil
}

// A paused task's instance metadata is its durable execution binding. Never
// resolve its saved relative paths in an inherited directory if that binding
// is missing, unreadable, or no longer belongs to this repository.
func (a *MainAgent) restorePausedTaskWorkDir(ctx context.Context, sessionPath string, record *DurableTaskRecord) (WorkDirState, error) {
	meta, err := loadSubAgentMeta(sessionPath, record.LatestInstanceID)
	if err != nil {
		return WorkDirState{}, fmt.Errorf("restore_failed: read task %q working directory metadata: %w", record.TaskID, err)
	}
	if meta == nil || meta.InstanceID != record.LatestInstanceID || meta.TaskID != record.TaskID || strings.TrimSpace(meta.WorkDir) == "" {
		return WorkDirState{}, fmt.Errorf("restore_failed: saved task %q working directory binding is missing or invalid", record.TaskID)
	}
	if info := a.resolveRestoredWorktree(ctx, meta.WorkDir); info != nil {
		return WorkDirState{Path: info.Path, WorktreeID: info.Name, Branch: info.Branch, BaseSHA: info.BaseSHA, Generation: meta.WorkDirGeneration}, nil
	}
	if dir, ok := a.restoredPlainWorkDir(meta.WorkDir); ok {
		return WorkDirState{Path: dir, Generation: meta.WorkDirGeneration}, nil
	}
	return WorkDirState{}, fmt.Errorf("restore_failed: saved task %q working directory %q is unavailable or outside this repository", record.TaskID, meta.WorkDir)
}
