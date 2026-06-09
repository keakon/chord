package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/worktree"
)

// startupPathOptions returns the user-facing path overrides currently in
// effect. Mirrors the persistent-flag values consumed by initApp so the
// worktree startup uses the same state directory as the rest of chord.
func startupPathOptions() config.PathOptions {
	return config.PathOptions{
		ConfigHome:  flagConfigHome,
		StateDir:    flagStateDir,
		CacheDir:    flagCacheDir,
		SessionsDir: flagSessionsDir,
		LogsDir:     flagLogsDir,
	}
}

// startupPathLocator builds a PathLocator using the same precedence rules
// (flags > env > config) that initApp will apply later, so the worktree
// startup writes to the directory chord will subsequently read from.
func startupPathLocator() (*config.PathLocator, error) {
	cfg, err := config.LoadConfig()
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		cfg = nil
	}
	return config.ResolvePathLocator(cfg, startupPathOptions())
}

// startupBranchPrefix returns the normalized worktree branch prefix from
// config.yaml (`worktree.branch_prefix`), falling back to the default
// "chord/" when unset. Errors propagate so a bad config value surfaces at
// startup instead of silently falling back to the default.
func startupBranchPrefix() (string, error) {
	cfg, err := config.LoadConfig()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return worktree.DefaultBranchPrefix, nil
		}
		return "", err
	}
	if cfg == nil {
		return worktree.DefaultBranchPrefix, nil
	}
	if cwd, cwdErr := os.Getwd(); cwdErr == nil {
		_, mergedCfg, mergeErr := config.MergeProjectConfig(cfg, config.ProjectConfigPath(cwd))
		if mergeErr != nil {
			return "", mergeErr
		}
		cfg = mergedCfg
	}
	return worktree.NormalizeBranchPrefix(cfg.Worktree.BranchPrefix)
}

// prepareStartupWorktree creates or reuses a chord-managed worktree for
// the requested name, updates the repo index, switches the process cwd
// into the worktree, and returns Info describing it. Callers should
// build a recovery.SessionMeta from the returned Info and pass it via
// sessionStartupOptions.NewSessionMeta so new sessions remember their
// worktree provenance.
func prepareStartupWorktree(ctx context.Context, name string) (*worktree.Info, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = worktree.GenerateAutoSlug(time.Now())
	}
	if err := worktree.ValidateSlug(name); err != nil {
		return nil, err
	}
	pl, err := startupPathLocator()
	if err != nil {
		return nil, fmt.Errorf("resolve storage paths: %w", err)
	}
	branchPrefix, err := startupBranchPrefix()
	if err != nil {
		return nil, fmt.Errorf("resolve worktree branch_prefix: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}
	info, err := worktree.Create(ctx, worktree.CreateOptions{
		Name:         name,
		RepoRoot:     cwd,
		PathLocator:  pl,
		BranchPrefix: branchPrefix,
	})
	if err != nil {
		return nil, err
	}

	if err := registerWorktreeInIndex(pl, info); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}
	printWorktreeStartupSummary(info)
	if err := os.Chdir(info.Path); err != nil {
		return info, fmt.Errorf("chdir to worktree: %w", err)
	}
	return info, nil
}

// registerWorktreeInIndex inserts/updates the repo index entry for info
// and stamps last-used. Wrapped in a cross-process lock.
func registerWorktreeInIndex(pl *config.PathLocator, info *worktree.Info) error {
	mainPL, mainErr := pl.LocateProject(info.RepoRoot)
	wtPL, wtErr := pl.LocateProject(info.Path)
	return worktree.WithRepoIndexLock(pl.StateDir, info.RepoID, func(idx *worktree.RepoIndex) error {
		idx.RepoID = info.RepoID
		idx.MainRepoRoot = info.RepoRoot
		if idx.DisplayName == "" {
			idx.DisplayName = filepath.Base(info.RepoRoot)
		}
		if mainErr == nil && mainPL != nil {
			idx.MainProject = worktree.RepoIndexProject{
				ProjectKey:  mainPL.ProjectKey,
				ProjectRoot: info.RepoRoot,
			}
		}
		entry := worktree.RepoIndexWorktree{
			Name:   info.Name,
			Slug:   info.Slug,
			Branch: info.Branch,
			Path:   info.Path,
		}
		if wtErr == nil && wtPL != nil {
			entry.ProjectKey = wtPL.ProjectKey
		}
		idx.UpsertWorktree(entry)
		idx.TouchLastUsed(info.Name)
		return nil
	})
}

// printWorktreeStartupSummary emits a short stderr block on first
// creation and a one-liner on fast-resume; goes to stderr so it doesn't
// interfere with headless stdout protocol.
func printWorktreeStartupSummary(info *worktree.Info) {
	if info == nil {
		return
	}
	if info.Existed {
		fmt.Fprintf(os.Stderr, "Entered worktree %s (branch %s)\n", info.Name, info.Branch)
		return
	}
	headBranch := info.HEADBranch
	if headBranch == "" || headBranch == "HEAD" {
		headBranch = "(detached HEAD)"
	}
	shortSHA := info.BaseSHA
	if len(shortSHA) > 12 {
		shortSHA = shortSHA[:12]
	}
	fmt.Fprintf(os.Stderr, "Created worktree %s\n  branch: %s\n  path:   %s\n  base:   HEAD %s on %s\n",
		info.Name, info.Branch, info.Path, shortSHA, headBranch)
	fmt.Fprintln(os.Stderr, "Note: worktree contains tracked files only; uncommitted changes in main repo are NOT included.")
	if info.MainDirty {
		fmt.Fprintln(os.Stderr, "Warning: main repo has uncommitted changes; they were left in place but are not visible inside the worktree.")
	}
}

// worktreeMetaForInfo converts info into the SessionMeta payload written
// when a new session is created in the worktree.
func worktreeMetaForInfo(info *worktree.Info) *recovery.SessionMeta {
	if info == nil {
		return nil
	}
	return &recovery.SessionMeta{
		RepoID:         info.RepoID,
		RepoRoot:       info.RepoRoot,
		WorktreeName:   info.Name,
		WorktreeBranch: info.Branch,
		WorktreePath:   info.Path,
	}
}

// SessionLocation describes where a session id was resolved to. Exactly
// one of Worktree / MainRepoRoot is non-empty. Callers should chdir to
// Worktree.Path (when set) or MainRepoRoot before resuming so initApp's
// ProjectKey computation matches the session's storage location.
type SessionLocation struct {
	Worktree     *worktree.Info
	MainRepoRoot string
}

// resolveSessionWorktree returns the location of the session with the
// given id within the current repo's chord-managed projects. It walks
// the repo index — main project + each registered worktree — and probes
// each project's sessions directory for <sid>/main.jsonl.
//
// Returns:
//   - (loc, nil) where loc.Worktree != nil    → session belongs to a chord-managed worktree
//   - (loc, nil) where loc.MainRepoRoot != "" → session belongs to the main repo
//   - (nil, err)                              → not found or error to abort startup
func resolveSessionWorktree(ctx context.Context, sid string) (*SessionLocation, error) {
	sid = strings.TrimSpace(sid)
	if sid == "" {
		return nil, fmt.Errorf("empty session id")
	}
	pl, err := startupPathLocator()
	if err != nil {
		return nil, fmt.Errorf("resolve storage paths: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("cwd: %w", err)
	}
	mainRoot, err := worktree.GitMainRoot(ctx, cwd)
	if err != nil {
		return nil, fmt.Errorf("resolve git main root: %w", err)
	}
	repoID := worktree.RepoIDFor(mainRoot)
	idx, err := worktree.LoadRepoIndex(pl.StateDir, repoID)
	if err != nil {
		return nil, fmt.Errorf("load repo index: %w", err)
	}
	if idx != nil {
		// Probe the registered worktrees first so the worktree case is
		// reported even when the main repo also has a stale entry.
		for i := range idx.Worktrees {
			w := &idx.Worktrees[i]
			if w.ProjectKey == "" {
				continue
			}
			if sessionExistsInProject(pl, w.ProjectKey, sid) {
				return &SessionLocation{Worktree: &worktree.Info{
					Slug:     w.Slug,
					Name:     w.Name,
					Branch:   w.Branch,
					Path:     w.Path,
					RepoRoot: mainRoot,
					RepoID:   repoID,
				}}, nil
			}
		}
		if idx.MainProject.ProjectKey != "" && sessionExistsInProject(pl, idx.MainProject.ProjectKey, sid) {
			return &SessionLocation{MainRepoRoot: mainRoot}, nil
		}
	}
	// Fall back: maybe the main project hasn't been registered yet but
	// the session lives there.
	mainPL, perr := pl.LocateProject(mainRoot)
	if perr == nil && sessionExistsInProject(pl, mainPL.ProjectKey, sid) {
		return &SessionLocation{MainRepoRoot: mainRoot}, nil
	}
	return nil, fmt.Errorf("session %q not found in this repo's chord-managed worktrees", sid)
}

// sessionExistsInProject reports whether <stateDir>/sessions/<key>/<sid>/main.jsonl
// is non-empty, mirroring planSessionStartup's resume probe.
func sessionExistsInProject(pl *config.PathLocator, projectKey, sid string) bool {
	if projectKey == "" || sid == "" {
		return false
	}
	main := filepath.Join(pl.SessionsRoot, projectKey, sid, identity.MainSessionLogFilename)
	st, err := os.Stat(main)
	return err == nil && st.Size() > 0
}
