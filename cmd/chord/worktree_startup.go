package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/keakon/golog/log"

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

// startupWorktreeConfig returns the merged worktree configuration, applying
// the same project-config merge as initApp so startup and runtime agree on
// both the branch prefix and the worktree location. The merge is anchored at
// the content root: inside a linked worktree the branch's own
// .chord/config.yaml must not change the prefix or root out from under the
// CLI management commands.
func startupWorktreeConfig() (config.WorktreeConfig, error) {
	var wc config.WorktreeConfig
	cfg, err := config.LoadConfig()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return wc, nil
		}
		return wc, err
	}
	if cfg == nil {
		return wc, nil
	}
	if cwd, cwdErr := os.Getwd(); cwdErr == nil {
		contentRoot := resolveContentRoot(context.Background(), cwd)
		if strings.TrimSpace(contentRoot) == "" {
			contentRoot = cwd
		}
		_, mergedCfg, mergeErr := config.MergeProjectConfig(cfg, config.ProjectConfigPath(contentRoot))
		if mergeErr != nil {
			return wc, mergeErr
		}
		cfg = mergedCfg
	}
	return cfg.Worktree, nil
}

// startupBranchPrefix returns the normalized worktree branch prefix from
// config.yaml (`worktree.branch_prefix`), falling back to the default "chord/"
// when unset. Invalid config values are logged by the loader and treated as
// unset.
func startupBranchPrefix() (string, error) {
	wc, err := startupWorktreeConfig()
	if err != nil {
		return "", err
	}
	return worktree.NormalizeBranchPrefix(wc.BranchPrefix)
}

// startupWorktreeFromCwd returns the chord-managed worktree the process was
// launched inside, or nil when the current directory is not part of one. It
// gives a plain `cd <checkout> && chord` the same binding --worktree and
// --resume install: without it the session works in the checkout with no
// recorded binding, so resume would drop it back to the main checkout and
// another process could not see it as a holder of the directory.
//
// The worktree root is the binding even when chord was launched from a
// subdirectory: the active checkout is the checkout, and that is what a resumed
// session and the removal guard need. The process keeps its own directory.
func startupWorktreeFromCwd(ctx context.Context) *worktree.Info {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	contentRoot := resolveContentRoot(ctx, cwd)
	if strings.TrimSpace(contentRoot) == "" {
		contentRoot = cwd
	}
	branchPrefix, err := startupBranchPrefix()
	if err != nil {
		log.Warnf("resolve worktree branch_prefix for startup detection failed error=%v", err)
		return nil
	}
	info, err := worktree.ResolveContaining(ctx, contentRoot, cwd, branchPrefix)
	if err != nil {
		return nil
	}
	return info
}

// prepareStartupWorktree creates or reuses a chord-managed worktree for
// the requested name, updates the repo index, switches the process cwd
// into the worktree, and returns Info describing it. Callers should
// build a recovery.SessionMeta from the returned Info and pass it via
// sessionStartupOptions.NewSessionMeta so new sessions remember their
// worktree provenance. resetBranch allows resetting a leftover branch of a
// removed worktree; without it such a branch is refused.
func prepareStartupWorktree(ctx context.Context, name string, resetBranch bool) (*worktree.Info, error) {
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
	wc, err := startupWorktreeConfig()
	if err != nil {
		return nil, fmt.Errorf("resolve worktree config: %w", err)
	}
	branchPrefix, err := worktree.NormalizeBranchPrefix(wc.BranchPrefix)
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
		Root:         wc.Root,
		ResetBranch:  resetBranch,
		// The command line has no session to name, but recording the creator
		// keeps the worktree identifiable in `chord worktree list` and keeps it
		// unremovable through the agent tools (the same fail-closed outcome as
		// having no record at all).
		Owner: &worktree.Owner{Kind: worktree.OwnerKindCLI, CreatedAt: time.Now().UTC()},
	})
	if err != nil {
		return nil, err
	}

	if err := worktree.RegisterInIndex(pl, info); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}
	printWorktreeStartupSummary(info)
	if err := os.Chdir(info.Path); err != nil {
		return info, fmt.Errorf("chdir to worktree: %w", err)
	}
	return info, nil
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

// SessionLocation describes where a session id was resolved to. Worktree is
// set when the session was working in a chord-managed worktree, otherwise
// ContentRoot is the repository root the session should run in. ProjectKey is
// the session storage project key (<state>/sessions/<projectKey>/<sid>) and is
// always populated. Callers should chdir to the resolved path before resuming
// so initApp computes the same project key.
type SessionLocation struct {
	Worktree    *worktree.Info
	ContentRoot string
	ProjectKey  string
}

// resolveSessionWorktree returns the location of the session with the
// given id inside the current repository. Sessions are stored under the
// repository's content root project key, shared by every checkout; the
// checkout the session was working in is recorded in its SessionMeta, so a
// session created in a worktree resolves back to that worktree.
//
// Returns:
//   - (loc, nil) where loc.Worktree != nil    → session was working in a chord-managed worktree
//   - (loc, nil) where loc.ContentRoot != ""  → session belongs to the repository content root
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
	contentRoot := resolveContentRoot(ctx, cwd)
	loc, err := resolveSessionInProject(ctx, pl, contentRoot, sid)
	if err != nil {
		return nil, err
	}
	if info := worktreeLocationForSession(ctx, pl, loc.ProjectKey, sid, contentRoot); info != nil {
		loc.Worktree = info
		loc.ContentRoot = ""
	}
	return loc, nil
}

// resolveSessionInProject locates sid inside the project anchored at
// contentRoot, ignoring worktree provenance.
func resolveSessionInProject(ctx context.Context, pl *config.PathLocator, contentRoot, sid string) (*SessionLocation, error) {
	projectPL, err := pl.LocateProject(contentRoot)
	if err != nil {
		return nil, fmt.Errorf("locate project: %w", err)
	}
	if !sessionExistsInProject(pl, projectPL.ProjectKey, sid) {
		return nil, fmt.Errorf("session %q not found in project %s", sid, projectPL.ProjectKey)
	}
	return &SessionLocation{ContentRoot: contentRoot, ProjectKey: projectPL.ProjectKey}, nil
}

// resumeSessionWorktree returns the chord-managed worktree the session was
// working in, when it belongs to the repository containing the current
// directory and that worktree still exists. Returns nil for main-checkout
// sessions, unknown sessions, and sessions of another repository, so the
// caller keeps its current directory and lets the normal startup report the
// mismatch.
func resumeSessionWorktree(ctx context.Context, sid string) *worktree.Info {
	sid = strings.TrimSpace(sid)
	if sid == "" {
		return nil
	}
	pl, err := startupPathLocator()
	if err != nil {
		return nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	contentRoot := resolveContentRoot(ctx, cwd)
	projectPL, err := pl.LocateProject(contentRoot)
	if err != nil || !sessionExistsInProject(pl, projectPL.ProjectKey, sid) {
		return nil
	}
	return worktreeLocationForSession(ctx, pl, projectPL.ProjectKey, sid, contentRoot)
}

// worktreeLocationForSession returns the chord-managed worktree recorded in
// the session's metadata, or nil when the session belongs to the main
// checkout or the recorded worktree is no longer a worktree of this
// repository. A dropped checkout is recorded as a fallback boundary and
// reported to the user (stderr here, toast once the agent starts).
func worktreeLocationForSession(ctx context.Context, pl *config.PathLocator, projectKey, sid, contentRoot string) *worktree.Info {
	sessionDir := filepath.Join(pl.SessionsRoot, projectKey, sid)
	meta, err := recovery.LoadSessionMeta(sessionDir)
	if err != nil || meta == nil {
		return nil
	}
	path := strings.TrimSpace(meta.WorktreePath)
	if path == "" || samePath(path, contentRoot) {
		return nil
	}
	repoRoot := strings.TrimSpace(meta.RepoRoot)
	if repoRoot == "" {
		repoRoot = contentRoot
	}
	if !worktree.IsWorktreeOf(ctx, path, repoRoot) {
		fmt.Fprintf(os.Stderr, "warning: session %s was working in worktree %s, which is no longer a worktree of this repository; resuming in %s\n", sid, path, contentRoot)
		recordWorktreeResumeFallback(sessionDir, meta, path)
		return nil
	}
	repoID := strings.TrimSpace(meta.RepoID)
	if repoID == "" {
		repoID = worktree.ResolveRepoID(ctx, repoRoot, repoRoot)
	}
	name := strings.TrimSpace(meta.WorktreeName)
	if name == "" {
		name = filepath.Base(path)
	}
	return &worktree.Info{
		Name:     name,
		Branch:   meta.WorktreeBranch,
		Path:     path,
		RepoRoot: repoRoot,
		RepoID:   repoID,
	}
}

// recordWorktreeResumeFallback clears the session's active checkout and appends
// the boundary record that explains why resume landed somewhere else.
func recordWorktreeResumeFallback(sessionDir string, meta *recovery.SessionMeta, path string) {
	name, branch := "", ""
	if meta != nil {
		name = strings.TrimSpace(meta.WorktreeName)
		branch = strings.TrimSpace(meta.WorktreeBranch)
	}
	entry := recovery.WorktreeTimelineEntry{
		Reason:   recovery.WorktreeSwitchResumeFallback,
		Name:     name,
		Branch:   branch,
		Path:     path,
		Fallback: true,
		Detail:   "recorded worktree is no longer a worktree of this repository",
		At:       time.Now().UTC(),
	}
	if err := recovery.RecordWorktreeBoundary(sessionDir, recovery.WorktreeBinding{}, entry); err != nil {
		log.Warnf("record worktree resume fallback failed session_dir=%v error=%v", sessionDir, err)
	}
	flagWorktreeResumeNotice = fmt.Sprintf("Session was working in worktree %s, which no longer exists; continuing in the repository checkout.", filepath.Base(path))
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
