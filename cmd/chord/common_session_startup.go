package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/worktree"
)

type sessionStartupOptions struct {
	ContinueLatest bool
	ResumeID       string
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
			return *opts.Plan, nil
		}
		// --continue was resolved before initApp and could not claim a session;
		// create the fresh session it fell through to.
		return newSessionStartupPlan(sessionsDir, stateDir, opts.NewSessionMeta, opts.Plan.SkippedLockedIDs)
	}
	if opts.ResumeID != "" {
		sessionDir := filepath.Join(sessionsDir, opts.ResumeID)
		mainPath := filepath.Join(sessionDir, identity.MainSessionLogFilename)
		info, err := os.Stat(mainPath)
		if err != nil || info.Size() == 0 {
			return sessionStartupPlan{}, fmt.Errorf("session %s not found or has no messages in the current project", opts.ResumeID)
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

// planContinueSessionStartup walks the --continue candidates newest-first and
// takes the first one this process can actually own. Trying the lock is the
// authoritative check, so there is no window between testing and claiming.
// taken reports whether a candidate was claimed; when it is false the returned
// plan carries the sessions that were skipped because another live Chord
// process owns them, and the caller creates a fresh session instead.
func planContinueSessionStartup(sessionsDir string) (sessionStartupPlan, bool, error) {
	var skipped []string
	for _, sessionDir := range recovery.RecentSessionCandidates(sessionsDir, "") {
		lock, lockErr := recovery.AcquireSessionLock(sessionDir)
		if lockErr == nil {
			return sessionStartupPlan{
				SessionDir:       sessionDir,
				RestoreOnStartup: true,
				SessionLock:      lock,
				SkippedLockedIDs: skipped,
			}, true, nil
		}
		if _, ok := errors.AsType[*recovery.SessionLockedError](lockErr); ok {
			skipped = append(skipped, filepath.Base(sessionDir))
			continue
		}
		return sessionStartupPlan{}, false, lockErr
	}
	return sessionStartupPlan{SkippedLockedIDs: skipped}, false, nil
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
