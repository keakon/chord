package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/takeover"
)

type takeoverLocation struct {
	stateDir, workspace, sessionDir string
}

func locateTakeover(ctx context.Context, dir, sid string) (takeoverLocation, error) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return takeoverLocation{}, fmt.Errorf("unsupported_platform: takeover requires macOS or Linux")
	}
	if !takeover.ValidID(sid, 128) {
		return takeoverLocation{}, fmt.Errorf("invalid_request: invalid session ID")
	}
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return takeoverLocation{}, err
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return takeoverLocation{}, err
	}
	root := resolveContentRoot(ctx, abs)
	if root == "" {
		root = abs
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return takeoverLocation{}, fmt.Errorf("wrong_workspace: %w", err)
	}
	pl, err := startupPathLocator()
	if err != nil {
		return takeoverLocation{}, err
	}
	project, err := pl.LocateProject(root)
	if err != nil {
		return takeoverLocation{}, err
	}
	return takeoverLocation{stateDir: pl.StateDir, workspace: project.CanonicalRoot, sessionDir: filepath.Join(project.ProjectSessionsDir, sid)}, nil
}

func preflightTakeover(loc takeoverLocation) error {
	if info, err := os.Stat(loc.sessionDir); err != nil || !info.IsDir() {
		return fmt.Errorf("restore_failed: session directory unavailable")
	}
	cfg, err := config.LoadConfig()
	if err != nil {
		return fmt.Errorf("target_not_ready: load configuration: %w", err)
	}
	_, merged, err := config.MergeProjectConfig(cfg, config.ProjectConfigPath(loc.workspace))
	if err != nil {
		return fmt.Errorf("target_not_ready: %w", err)
	}
	roles, err := config.ResolveAgentConfigs(filepath.Join(loc.workspace, ".chord", "agents"), filepath.Join(flagConfigHomeResolved(), "agents"))
	if err != nil {
		return fmt.Errorf("target_not_ready: load roles: %w", err)
	}
	if merged != nil {
		if err = config.ResolveAgentModelPools(roles, merged.ModelPools); err != nil {
			return fmt.Errorf("target_not_ready: %w", err)
		}
	}
	meta, err := recovery.LoadSessionMeta(loc.sessionDir)
	if err != nil {
		return fmt.Errorf("restore_failed: load session metadata: %w", err)
	}
	if meta != nil && meta.WorktreePath != "" {
		if info, err := os.Stat(meta.WorktreePath); err != nil || !info.IsDir() {
			return fmt.Errorf("restore_failed: saved working directory unavailable")
		}
	}

	return nil
}

func flagConfigHomeResolved() string {
	pl, err := startupPathLocator()
	if err != nil {
		return ""
	}
	return pl.ConfigHome
}

func startTakeover(ctx context.Context, loc takeoverLocation, sid, transferID, targetKind, targetInstance string, timeout *time.Duration) (recovery.SessionPauseStatus, error) {
	id := recovery.TransferIdentity{TransferID: transferID, SessionID: sid, Workspace: loc.workspace, TargetKind: targetKind, TargetInstanceID: targetInstance}
	initial := recovery.SessionPauseStatus{TransferIdentity: id, State: recovery.SessionPausePreflight}
	if err := validateTakeoverCLIArguments(takeover.ActionStart, sid, transferID, targetKind, targetInstance); err != nil {
		return recovery.SessionPauseStatus{}, err
	}
	if timeout != nil && *timeout < 0 {
		return initial, fmt.Errorf("invalid_request: timeout must be nonnegative")
	}
	rec, initial, err := inspectTakeoverTarget(ctx, loc, sid, transferID, targetKind, targetInstance)
	if err != nil {
		return initial, err
	}
	id = initial.TransferIdentity
	client := takeover.Client{StateDir: loc.stateDir, Record: rec}
	// The start transport has its own handshake budget. The observer's budget
	// begins after admission and never becomes the source operation's context.
	initial.State = takeover.CodeStatusUnknown
	status, err := client.Start(ctx, id)
	if err != nil {
		if status.TransferID == "" {
			status = initial
		}
		return status, err
	}
	if timeout != nil && *timeout == 0 {
		return status, nil
	}
	observe := ctx
	var cancel context.CancelFunc
	if timeout != nil {
		observe, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		switch status.State {
		case recovery.SessionPauseReleased:
			return status, nil
		case recovery.SessionPauseSaveFailed:
			return status, fmt.Errorf("persistence_failed: %s", status.Error)
		}
		select {
		case <-observe.Done():
			status.WaitExpired = true
			return status, nil
		case <-timer.C:
		}
		next, queryErr := queryTakeover(observe, loc, sid, transferID)
		err = queryErr
		if err != nil {
			if observe.Err() != nil {
				status.WaitExpired = true
				return status, nil
			}
			return status, err
		}
		status = next
		if status.TransferIdentity != id {
			return status, fmt.Errorf("stale_source: transfer identity changed")
		}
	}
}

func queryTakeover(ctx context.Context, loc takeoverLocation, sid, transferID string) (recovery.SessionPauseStatus, error) {
	if rec, err := takeover.Find(loc.stateDir, sid); err == nil {
		if rec.Workspace != loc.workspace {
			return recovery.SessionPauseStatus{}, fmt.Errorf("wrong_workspace: source belongs to another workspace")
		}
		status, err := (takeover.Client{StateDir: loc.stateDir, Record: rec}).Status(ctx, transferID)
		if err == nil {
			return status, nil
		}
		// Durable proof is also useful after the source has exited and its socket
		// disappeared. It never substitutes PID disappearance for a checkpoint.
	}
	manager := recovery.NewRecoveryManager(loc.sessionDir)
	snapshot, err := manager.Recover()
	if err != nil {
		return recovery.SessionPauseStatus{}, fmt.Errorf("status_unknown: %w", err)
	}
	p := snapshot.Pause
	if p == nil || p.Identity.TransferID != transferID || p.Identity.SessionID != sid || p.Identity.Workspace != loc.workspace {
		return recovery.SessionPauseStatus{}, fmt.Errorf("status_unknown: transfer identity is unknown")
	}
	status := recovery.SessionPauseStatus{TransferIdentity: p.Identity, StartedAt: p.FrozenAt, CheckpointID: p.CheckpointID, PendingInputCount: len(p.PendingInputs), State: recovery.SessionPauseDraining}
	if !p.CheckpointComplete {
		return status, nil
	}
	status.State = recovery.SessionPauseSaved
	lock, err := recovery.AcquireSessionLock(loc.sessionDir)
	if err == nil {
		if err = lock.Release(); err != nil {
			return status, err
		}
		status.State = recovery.SessionPauseReleased
		return status, nil
	}
	if _, ok := errors.AsType[*recovery.SessionLockedError](err); ok {
		status.State = "occupied"
		status.RecoverableAction = "status"
		return status, nil
	}
	return status, err
}

func newTakeoverIdentity() string { return rand.Text() }

func queryTakeoverPage(ctx context.Context, loc takeoverLocation, sid, transferID string, offset int) (takeover.Status, error) {
	if offset < 0 {
		return takeover.Status{}, &takeover.Error{Code: takeover.CodeInvalidRequest, Message: "blocker offset must be nonnegative"}
	}
	if record, err := takeover.Find(loc.stateDir, sid); err == nil {
		if record.Workspace != loc.workspace {
			return takeover.Status{}, &takeover.Error{Code: takeover.CodeWrongWorkspace, Message: "source belongs to another workspace"}
		}
		page, err := (takeover.Client{Record: record, StateDir: loc.stateDir}).StatusPage(ctx, transferID, offset)
		if err == nil {
			return page, nil
		}
	}
	status, err := queryTakeover(ctx, loc, sid, transferID)
	page := takeover.PublicStatus(status)
	if offset > 0 {
		page.Blockers = nil
		page.BlockerOffset = offset
		page.NextOffset = 0
	}
	return page, err
}
