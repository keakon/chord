package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/recovery"
)

type sessionStartupOptions struct {
	ContinueLatest bool
	ResumeID       string
	// NewSessionMeta is written via recovery.SaveSessionMeta when the
	// startup creates a fresh session. Resumed/continued sessions keep
	// their original metadata untouched. Used by the worktree path to
	// stamp worktree provenance into the session's metadata file.
	NewSessionMeta *recovery.SessionMeta
}

type sessionStartupPlan struct {
	SessionDir       string
	RestoreOnStartup bool
	// SessionLock is the exclusive cross-process claim on SessionDir, acquired
	// as part of planning: a session that cannot be owned is not a usable plan.
	SessionLock *recovery.SessionLock
	// SkippedLockedIDs names the sessions --continue passed over because another
	// live Chord process already owns them. Reported to the user so a fallback is
	// never silent — resuming a different session than expected is worse than a
	// visible notice.
	SkippedLockedIDs []string
}

func planSessionStartup(sessionsDir string, opts sessionStartupOptions) (sessionStartupPlan, error) {
	if opts.ResumeID != "" {
		sessionDir := filepath.Join(sessionsDir, opts.ResumeID)
		mainPath := filepath.Join(sessionDir, identity.MainSessionLogFilename)
		info, err := os.Stat(mainPath)
		if err != nil || info.Size() == 0 {
			return sessionStartupPlan{}, fmt.Errorf("session %s not found or has no messages", opts.ResumeID)
		}
		// --resume names one specific session: silently substituting another
		// would not be honoring the request, so a busy session is an error.
		lock, lockErr := recovery.AcquireSessionLock(sessionDir)
		if lockErr != nil {
			if _, ok := errors.AsType[*recovery.SessionLockedError](lockErr); ok {
				return sessionStartupPlan{}, fmt.Errorf("%w; close that process, or run chord --continue to pick up the most recent session you can open", lockErr)
			}
			return sessionStartupPlan{}, lockErr
		}
		return sessionStartupPlan{
			SessionDir:       sessionDir,
			RestoreOnStartup: true,
			SessionLock:      lock,
		}, nil
	}
	var skipped []string
	if opts.ContinueLatest {
		// --continue means "carry on where I left off", not "that exact session
		// or nothing". Walk candidates newest-first and take the first one this
		// process can actually own. Trying the lock is the authoritative check,
		// so there is no window between testing and claiming.
		for _, sessionDir := range recovery.RecentSessionCandidates(sessionsDir, "") {
			lock, lockErr := recovery.AcquireSessionLock(sessionDir)
			if lockErr == nil {
				return sessionStartupPlan{
					SessionDir:       sessionDir,
					RestoreOnStartup: true,
					SessionLock:      lock,
					SkippedLockedIDs: skipped,
				}, nil
			}
			if _, ok := errors.AsType[*recovery.SessionLockedError](lockErr); ok {
				skipped = append(skipped, filepath.Base(sessionDir))
				continue
			}
			return sessionStartupPlan{}, lockErr
		}
	}
	sessionDir, err := createNewSessionDir(sessionsDir)
	if err != nil {
		return sessionStartupPlan{}, err
	}
	if opts.NewSessionMeta != nil {
		if err := recovery.SaveSessionMeta(sessionDir, *opts.NewSessionMeta); err != nil {
			return sessionStartupPlan{}, fmt.Errorf("save session meta: %w", err)
		}
	}
	lock, err := recovery.AcquireSessionLock(sessionDir)
	if err != nil {
		return sessionStartupPlan{}, err
	}
	return sessionStartupPlan{SessionDir: sessionDir, SessionLock: lock, SkippedLockedIDs: skipped}, nil
}

func createNewSessionDir(sessionsDir string) (string, error) {
	return recovery.CreateNewSessionDir(sessionsDir)
}

func applyInitialMCPPromptState(ac *AppContext, asyncMCP bool, mcpConfigured bool, syncPromptBlock string) {
	if ac == nil || ac.MainAgent == nil {
		return
	}
	if asyncMCP && len(ac.MCPConfigs) > 0 {
		return
	}
	if mcpConfigured {
		// Sync path: register main-agent server names as sentinels now that
		// MainAgent exists, so SubAgents never reconnect them.
		var names []string
		for _, cfg := range ac.MCPConfigs {
			names = append(names, cfg.Name)
		}
		ac.MainAgent.RegisterMainMCPServers(names)
		ac.MainAgent.SetMCPServersPromptBlock(syncPromptBlock)
		return
	}
	ac.MainAgent.SetPendingMCPDiscovery(nil, "")
}
