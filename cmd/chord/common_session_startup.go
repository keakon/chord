package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/worktree"
)

type sessionStartupOptions struct {
	ContinueLatest   bool
	ResumeID         string
	ExpectedTransfer recovery.TransferIdentity
	// NewSessionMeta is written via recovery.SaveSessionMeta when the
	// startup creates a fresh session. Resumed/continued sessions keep
	// their original metadata untouched. Used by the worktree path to
	// stamp worktree provenance into the session's metadata file.
	NewSessionMeta *recovery.SessionMeta
	// Plan is a startup plan resolved before initApp ran. --continue has to
	// know which session it opens before the process can enter the checkout
	// that session was working in, so its entrypoint resolves the plan (and
	// owns the session lock) first and passes it here instead of planning
	// twice. An empty SessionDir means the entrypoint already walked the
	// candidates and could not claim one, so the fresh session it fell through
	// to is planned here.
	Plan *sessionStartupPlan
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

func planSessionStartup(sessionsDir, stateDir string, opts sessionStartupOptions) (sessionStartupPlan, error) {
	if opts.Plan != nil {
		if opts.Plan.SessionDir != "" {
			if opts.ExpectedTransfer.TransferID != "" {
				if opts.Plan.SessionLock == nil {
					return sessionStartupPlan{}, fmt.Errorf("restore_failed: transfer requires an owned session lock")
				}
				if _, err := validateTakeoverStartup(opts.Plan.SessionDir, opts.ExpectedTransfer); err != nil {
					return sessionStartupPlan{}, err
				}
			}
			return *opts.Plan, nil
		}
		// --continue was resolved before initApp and could not claim a session;
		// create the fresh session it fell through to.
		return newSessionStartupPlan(sessionsDir, stateDir, opts.NewSessionMeta, opts.Plan.SkippedLockedIDs)
	}
	if opts.ResumeID != "" {
		if !recovery.ValidResumeSessionID(opts.ResumeID) {
			return sessionStartupPlan{}, fmt.Errorf("session %s not found or has no messages in the current project", opts.ResumeID)
		}
		sessionDir := filepath.Join(sessionsDir, opts.ResumeID)
		mainPath := filepath.Join(sessionDir, identity.MainSessionLogFilename)
		info, err := os.Stat(mainPath)
		if (err != nil || info.Size() == 0) && !hasCompletePausedCheckpoint(sessionDir) {
			return sessionStartupPlan{}, fmt.Errorf("session %s not found or has no messages in the current project", opts.ResumeID)
		}
		// --resume names one specific session: silently substituting another
		// would not be honoring the request, so a busy session is an error.
		// Existing-only acquisition: a session that vanished between the check
		// above and the lock (renamed for deletion by a concurrent cleanup)
		// must surface as "not found", not be recreated as an empty shell.
		lock, lockErr := recovery.AcquireExistingSessionLock(sessionDir)
		if lockErr != nil {
			if _, ok := errors.AsType[*recovery.SessionLockedError](lockErr); ok {
				return sessionStartupPlan{}, fmt.Errorf("%w; close that process, or run chord --continue to pick up the most recent session you can open", lockErr)
			}
			return sessionStartupPlan{}, lockErr
		}
		if !ownedSessionDirHoldsTranscript(sessionDir) {
			_ = lock.Release()
			return sessionStartupPlan{}, fmt.Errorf("session %s not found or has no messages in the current project", opts.ResumeID)
		}
		if opts.ExpectedTransfer.TransferID != "" {
			if _, err := validateTakeoverStartup(sessionDir, opts.ExpectedTransfer); err != nil {
				_ = lock.Release()
				return sessionStartupPlan{}, err
			}
		}
		return sessionStartupPlan{
			SessionDir:       sessionDir,
			RestoreOnStartup: true,
			SessionLock:      lock,
		}, nil
	}
	if opts.ContinueLatest {
		plan, taken, err := planContinueSessionStartup(sessionsDir)
		if err != nil {
			return sessionStartupPlan{}, err
		}
		if taken {
			return plan, nil
		}
		return newSessionStartupPlan(sessionsDir, stateDir, opts.NewSessionMeta, plan.SkippedLockedIDs)
	}
	return newSessionStartupPlan(sessionsDir, stateDir, opts.NewSessionMeta, nil)
}

func planContinueSessionStartup(sessionsDir string) (sessionStartupPlan, bool, error) {
	return planContinueFromCandidates(recovery.RecentSessionCandidates(sessionsDir, ""))
}

// planContinueFromCandidates claims the first candidate this process can own.
// Separated from the listing so tests can drive the claim protocol (locked,
// vanished mid-flight, lost transcript) against explicit candidates.
func planContinueFromCandidates(candidates []string) (sessionStartupPlan, bool, error) {
	var skipped []string
	for _, sessionDir := range candidates {
		lock, lockErr := recovery.AcquireExistingSessionLock(sessionDir)
		if lockErr == nil {
			if !ownedSessionDirHoldsTranscript(sessionDir) {
				// The candidate vanished or lost its transcript between the
				// listing and the lock; fall through to the next one.
				_ = lock.Release()
				continue
			}
			return sessionStartupPlan{
				SessionDir:       sessionDir,
				RestoreOnStartup: true,
				SessionLock:      lock,
				SkippedLockedIDs: skipped,
			}, true, nil
		}
		if errors.Is(lockErr, fs.ErrNotExist) {
			continue
		}
		if _, ok := errors.AsType[*recovery.SessionLockedError](lockErr); ok {
			skipped = append(skipped, filepath.Base(sessionDir))
			continue
		}
		return sessionStartupPlan{}, false, lockErr
	}
	return sessionStartupPlan{SkippedLockedIDs: skipped}, false, nil
}

// ownedSessionDirHoldsTranscript reports whether a session directory still
// holds a usable session once this process owns its lock: a non-empty
// main.jsonl or a complete paused checkpoint. It closes the window between a
// pre-lock existence check (or a listing) and the lock acquisition, which a
// concurrent deletion protocol can exploit by renaming the directory away.
func ownedSessionDirHoldsTranscript(sessionDir string) bool {
	mainPath := filepath.Join(sessionDir, identity.MainSessionLogFilename)
	if info, err := os.Stat(mainPath); err == nil && info.Size() > 0 {
		return true
	}
	return hasCompletePausedCheckpoint(sessionDir)
}

// newSessionStartupPlan creates a fresh session directory and owns it. The
// session lock is taken before the metadata is written on purpose: a checkout
// claim in that metadata is only trustworthy next to a live session lock — the
// removal scan pairs them — and the claim itself is written under the checkout
// mutation lock removal takes.
func newSessionStartupPlan(sessionsDir, stateDir string, meta *recovery.SessionMeta, skipped []string) (sessionStartupPlan, error) {
	sessionDir, err := createNewSessionDir(sessionsDir)
	if err != nil {
		return sessionStartupPlan{}, err
	}
	lock, err := recovery.AcquireSessionLock(sessionDir)
	if err != nil {
		return sessionStartupPlan{}, err
	}
	if meta != nil {
		if err := saveSessionMetaClaimingCheckout(sessionDir, *meta, stateDir); err != nil {
			_ = lock.Release()
			return sessionStartupPlan{}, fmt.Errorf("save session meta: %w", err)
		}
	}
	return sessionStartupPlan{SessionDir: sessionDir, SessionLock: lock, SkippedLockedIDs: skipped}, nil
}

// saveSessionMetaClaimingCheckout writes a session's metadata. A claim on a
// checkout is recorded under the checkout mutation lock removal takes, and only
// while the directory is still there: when a removal won the lock first the
// claim is dropped rather than pointing the session at a directory that no
// longer exists.
func saveSessionMetaClaimingCheckout(sessionDir string, meta recovery.SessionMeta, stateDir string) error {
	path := strings.TrimSpace(meta.WorktreePath)
	if path == "" || strings.TrimSpace(stateDir) == "" {
		return recovery.SaveSessionMeta(sessionDir, meta)
	}
	err := worktree.ClaimCheckout(stateDir, path, func() error {
		return recovery.SaveSessionMeta(sessionDir, meta)
	})
	if !errors.Is(err, worktree.ErrCheckoutGone) {
		return err
	}
	log.Warnf("worktree is gone before the session recorded it; writing unbound path=%v session_dir=%v", path, sessionDir)
	meta.WorktreeName = ""
	meta.WorktreeBranch = ""
	meta.WorktreePath = ""
	meta.IsMainWorktree = false
	return recovery.SaveSessionMeta(sessionDir, meta)
}

func createNewSessionDir(sessionsDir string) (string, error) {
	return recovery.CreateNewSessionDir(sessionsDir)
}

func applyInitialMCPPromptState(ac *AppContext, asyncMCP bool, mcpConfigured bool, syncPromptBlock string) {
	if ac == nil || ac.MainAgent == nil {
		return
	}
	if asyncMCP && len(ac.MCPTargets) > 0 {
		return
	}
	if mcpConfigured {
		// Sync path: register main-agent server names as sentinels now that
		// MainAgent exists, so SubAgents never reconnect them.
		var names []string
		for _, cfg := range ac.MCPTargets {
			names = append(names, cfg.Config.Name)
		}
		ac.MainAgent.RegisterMainMCPServers(names)
		ac.MainAgent.SetMCPServersPromptBlock(syncPromptBlock)
		return
	}
	ac.MainAgent.SetPendingMCPDiscovery(nil, "")
}
