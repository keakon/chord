package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/takeover"
)

func hasCompletePausedCheckpoint(sessionDir string) bool {
	snapshot, err := recovery.NewRecoveryManager(sessionDir).Recover()
	if err != nil || snapshot.Pause == nil {
		return false
	}
	pause := snapshot.Pause
	return (pause.Paused || len(pause.PendingInputs) > 0) && pause.CheckpointComplete && pause.Identity.SessionID == filepath.Base(sessionDir)
}

// validateTakeoverStartup runs under the session lock. A released source is
// insufficient evidence if another owner has already changed its checkpoint.
func validateTakeoverStartup(sessionDir string, expected recovery.TransferIdentity) (string, error) {
	manager := recovery.NewRecoveryManager(sessionDir)
	snapshot, err := manager.Recover()
	if err != nil {
		return "", fmt.Errorf("restore_failed: read saved transfer: %w", err)
	}
	pause := snapshot.Pause
	if pause == nil || !pause.Paused || !pause.CheckpointComplete || pause.Identity != expected || pause.CheckpointID == "" || expected.SessionID != filepath.Base(sessionDir) {
		return "", fmt.Errorf("restore_failed: saved paused checkpoint does not match transfer %s", expected.TransferID)
	}
	if _, err := manager.RestorePendingInputs(pause.PendingInputs); err != nil {
		return "", fmt.Errorf("restore_failed: pending inputs: %w", err)
	}
	return strictSavedWorkDir(sessionDir, expected.Workspace)
}

func strictSavedWorkDir(sessionDir, workspace string) (string, error) {
	meta, err := recovery.LoadSessionMeta(sessionDir)
	if err != nil {
		return "", fmt.Errorf("restore_failed: session metadata: %w", err)
	}
	workDir := ""
	if meta != nil {
		workDir = meta.WorktreePath
	}
	if workDir == "" {
		workDir = workspace
	}
	for _, path := range []string{workspace, workDir} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			return "", fmt.Errorf("restore_failed: saved directory unavailable: %w", statErr)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("restore_failed: saved directory is not a directory")
		}
	}
	return workDir, nil
}

func claimTakeoverStartup(ctx context.Context, opts sessionStartupOptions) (sessionStartupPlan, string, error) {
	loc, err := locateTakeover(ctx, "", opts.ResumeID)
	if err != nil {
		return sessionStartupPlan{}, "", err
	}
	if loc.workspace != opts.ExpectedTransfer.Workspace {
		return sessionStartupPlan{}, "", fmt.Errorf("wrong_workspace: incoming transfer belongs to another workspace")
	}
	plan, err := planSessionStartup(filepath.Dir(loc.sessionDir), loc.stateDir, opts)
	if err != nil {
		return sessionStartupPlan{}, "", err
	}
	workDir, err := validateTakeoverStartup(plan.SessionDir, opts.ExpectedTransfer)
	if err != nil {
		_ = plan.SessionLock.Release()
		return sessionStartupPlan{}, "", err
	}
	return plan, workDir, nil
}

// claimPausedResumeStartup keeps ordinary resume from silently substituting
// a checkout or clearing its metadata before a paused session is validated.
func claimPausedResumeStartup(ctx context.Context, sid string) (sessionStartupPlan, string, bool, error) {
	if !takeover.Supported() {
		return sessionStartupPlan{}, "", false, nil
	}
	loc, err := locateTakeover(ctx, "", sid)
	if err != nil {
		return sessionStartupPlan{}, "", false, err
	}
	manager := recovery.NewRecoveryManager(loc.sessionDir)
	snapshot, err := manager.Recover()
	if err != nil || snapshot.Pause == nil || (!snapshot.Pause.Paused && len(snapshot.Pause.PendingInputs) == 0) {
		return sessionStartupPlan{}, "", false, nil
	}
	plan, err := planSessionStartup(filepath.Dir(loc.sessionDir), loc.stateDir, sessionStartupOptions{ResumeID: sid})
	if err != nil {
		return sessionStartupPlan{}, "", true, err
	}
	snapshot, err = manager.Recover()
	if err == nil && (snapshot.Pause == nil || (!snapshot.Pause.Paused && len(snapshot.Pause.PendingInputs) == 0)) {
		err = fmt.Errorf("restore_failed: paused session changed before lock acquisition")
	}
	if err == nil {
		_, err = manager.RestorePendingInputs(snapshot.Pause.PendingInputs)
	}
	var workDir string
	if err == nil {
		workDir, err = strictSavedWorkDir(loc.sessionDir, loc.workspace)
	}
	if err != nil {
		_ = plan.SessionLock.Release()
		return sessionStartupPlan{}, "", true, fmt.Errorf("restore_failed: paused session: %w", err)
	}
	return plan, workDir, true, nil
}
