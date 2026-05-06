package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/worktree"
)

// gitTestEnvForStartup pins git identity so commits work in CI.
var gitTestEnvForStartup = []string{
	"GIT_TERMINAL_PROMPT=0",
	"GIT_ASKPASS=",
	"GIT_AUTHOR_NAME=test",
	"GIT_AUTHOR_EMAIL=test@example.invalid",
	"GIT_COMMITTER_NAME=test",
	"GIT_COMMITTER_EMAIL=test@example.invalid",
}

func runStartupGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), gitTestEnvForStartup...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

func setupStartupRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	dir := t.TempDir()
	runStartupGit(t, dir, "init", "-q", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	runStartupGit(t, dir, "add", "README.md")
	runStartupGit(t, dir, "commit", "-q", "-m", "init")
	canonical, err := config.CanonicalProjectRoot(dir)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	return canonical
}

// withTestStateDir points the persistent flags at a fresh state/cache
// dir for the duration of the test, restoring originals on cleanup.
func withTestStateDir(t *testing.T) {
	t.Helper()
	prevState, prevCache, prevSessions, prevLogs, prevHome :=
		flagStateDir, flagCacheDir, flagSessionsDir, flagLogsDir, flagConfigHome
	state := filepath.Join(t.TempDir(), "state")
	cache := filepath.Join(t.TempDir(), "cache")
	sessions := filepath.Join(state, "sessions")
	logs := filepath.Join(t.TempDir(), "logs")
	home := filepath.Join(t.TempDir(), "config")
	for _, p := range []string{state, cache, sessions, logs, home} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
	}
	flagStateDir = state
	flagCacheDir = cache
	flagSessionsDir = sessions
	flagLogsDir = logs
	flagConfigHome = home
	t.Cleanup(func() {
		flagStateDir = prevState
		flagCacheDir = prevCache
		flagSessionsDir = prevSessions
		flagLogsDir = prevLogs
		flagConfigHome = prevHome
	})
}

func chdirForTest(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

func TestPrepareStartupWorktree_ChdirAndIndex(t *testing.T) {
	repo := setupStartupRepo(t)
	withTestStateDir(t)
	chdirForTest(t, repo)

	info, err := prepareStartupWorktree(context.Background(), "feat-a")
	if err != nil {
		t.Fatalf("prepareStartupWorktree: %v", err)
	}
	cwd, _ := os.Getwd()
	canonicalCwd, _ := config.CanonicalProjectRoot(cwd)
	if canonicalCwd != info.Path {
		t.Errorf("did not chdir into worktree: cwd=%s, want %s", canonicalCwd, info.Path)
	}
}

func TestPrepareStartupWorktree_AutoSlug(t *testing.T) {
	repo := setupStartupRepo(t)
	withTestStateDir(t)
	chdirForTest(t, repo)

	info, err := prepareStartupWorktree(context.Background(), "")
	if err != nil {
		t.Fatalf("prepareStartupWorktree: %v", err)
	}
	if !strings.HasPrefix(info.Name, "task-") {
		t.Errorf("auto slug did not get task- prefix: %s", info.Name)
	}
}

func TestPrepareStartupWorktree_InvalidSlug(t *testing.T) {
	repo := setupStartupRepo(t)
	withTestStateDir(t)
	chdirForTest(t, repo)

	_, err := prepareStartupWorktree(context.Background(), "bad/name")
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("expected slug validation error, got %v", err)
	}
}

func TestWorktreeMetaForInfo_NilInputReturnsNil(t *testing.T) {
	if got := worktreeMetaForInfo(nil); got != nil {
		t.Errorf("nil input: got %+v, want nil", got)
	}
}

func TestResolveSessionWorktree_FindsInWorktree(t *testing.T) {
	repo := setupStartupRepo(t)
	withTestStateDir(t)
	chdirForTest(t, repo)

	info, err := prepareStartupWorktree(context.Background(), "feat-a")
	if err != nil {
		t.Fatalf("prepareStartupWorktree: %v", err)
	}

	pl, err := startupPathLocator()
	if err != nil {
		t.Fatalf("startupPathLocator: %v", err)
	}
	wtPL, err := pl.LocateProject(info.Path)
	if err != nil {
		t.Fatalf("LocateProject: %v", err)
	}
	sid := "01HXXTESTSESSION0001"
	sessionDir := filepath.Join(pl.SessionsRoot, wtPL.ProjectKey, sid)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "main.jsonl"), []byte(`{"role":"user","content":"hi"}`+"\n"), 0o600); err != nil {
		t.Fatalf("seed main.jsonl: %v", err)
	}
	if err := recovery.SaveSessionMeta(sessionDir, recovery.SessionMeta{
		WorktreeName:   info.Name,
		WorktreeBranch: info.Branch,
		WorktreePath:   info.Path,
		RepoID:         info.RepoID,
		RepoRoot:       info.RepoRoot,
	}); err != nil {
		t.Fatalf("save meta: %v", err)
	}

	// Move cwd back to main repo to simulate "user runs chord resume <sid>" from main.
	chdirForTest(t, repo)

	got, err := resolveSessionWorktree(context.Background(), sid)
	if err != nil {
		t.Fatalf("resolveSessionWorktree: %v", err)
	}
	if got == nil || got.Worktree == nil {
		t.Fatalf("expected worktree resolution, got %+v", got)
	}
	if got.Worktree.Name != info.Name || got.Worktree.Path != info.Path {
		t.Errorf("resolved wrong worktree: got %+v, want name=%s path=%s", got.Worktree, info.Name, info.Path)
	}
}

func TestResolveSessionWorktree_MainRepo(t *testing.T) {
	repo := setupStartupRepo(t)
	withTestStateDir(t)
	chdirForTest(t, repo)

	pl, err := startupPathLocator()
	if err != nil {
		t.Fatalf("startupPathLocator: %v", err)
	}
	mainPL, err := pl.LocateProject(repo)
	if err != nil {
		t.Fatalf("LocateProject: %v", err)
	}
	sid := "01HXXMAINSESSION00001"
	sessionDir := filepath.Join(pl.SessionsRoot, mainPL.ProjectKey, sid)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "main.jsonl"), []byte(`{"role":"user","content":"hi"}`+"\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Manually register repo index with main project so resolveSessionWorktree can find it.
	repoID := worktree.RepoIDFor(repo)
	if err := worktree.WithRepoIndexLock(pl.StateDir, repoID, func(idx *worktree.RepoIndex) error {
		idx.RepoID = repoID
		idx.MainRepoRoot = repo
		idx.MainProject = worktree.RepoIndexProject{ProjectKey: mainPL.ProjectKey, ProjectRoot: repo}
		return nil
	}); err != nil {
		t.Fatalf("update repo index: %v", err)
	}

	loc, err := resolveSessionWorktree(context.Background(), sid)
	if err != nil {
		t.Fatalf("resolveSessionWorktree: %v", err)
	}
	if loc == nil || loc.Worktree != nil {
		t.Errorf("main repo session resolved as worktree: %+v", loc)
	}
	if loc != nil && loc.MainRepoRoot != repo {
		t.Errorf("main_root mismatch: got %s, want %s", loc.MainRepoRoot, repo)
	}
}

func TestResolveSessionWorktree_NotFound(t *testing.T) {
	repo := setupStartupRepo(t)
	withTestStateDir(t)
	chdirForTest(t, repo)

	_, err := resolveSessionWorktree(context.Background(), "01HXNOSUCHSESSION0000")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not-found error, got %v", err)
	}
}

// TestResolveSessionWorktree_FromInsideWorktree verifies that running
// `chord resume <main-sid>` from inside a worktree directory still
// reports MainRepoRoot so the resume command can chdir back to the main
// repo before initApp computes the wrong ProjectKey.
func TestResolveSessionWorktree_FromInsideWorktree(t *testing.T) {
	repo := setupStartupRepo(t)
	withTestStateDir(t)
	chdirForTest(t, repo)

	// Create the worktree first so the repo index has both main + wt.
	wtInfo, err := prepareStartupWorktree(context.Background(), "feat-x")
	if err != nil {
		t.Fatalf("prepareStartupWorktree: %v", err)
	}

	// Seed a main-repo session and register the main project in the index.
	pl, err := startupPathLocator()
	if err != nil {
		t.Fatalf("startupPathLocator: %v", err)
	}
	mainPL, err := pl.LocateProject(repo)
	if err != nil {
		t.Fatalf("LocateProject(main): %v", err)
	}
	sid := "01HXMAINFROMWT000001"
	sessionDir := filepath.Join(pl.SessionsRoot, mainPL.ProjectKey, sid)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "main.jsonl"), []byte(`{"role":"user","content":"hi"}`+"\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := worktree.WithRepoIndexLock(pl.StateDir, wtInfo.RepoID, func(idx *worktree.RepoIndex) error {
		idx.MainRepoRoot = repo
		idx.MainProject = worktree.RepoIndexProject{ProjectKey: mainPL.ProjectKey, ProjectRoot: repo}
		return nil
	}); err != nil {
		t.Fatalf("update repo index: %v", err)
	}

	// Cwd is currently the worktree (prepareStartupWorktree chdir'd).
	loc, err := resolveSessionWorktree(context.Background(), sid)
	if err != nil {
		t.Fatalf("resolveSessionWorktree from inside worktree: %v", err)
	}
	if loc == nil || loc.Worktree != nil {
		t.Fatalf("expected MainRepoRoot resolution, got %+v", loc)
	}
	if loc.MainRepoRoot != repo {
		t.Errorf("MainRepoRoot mismatch: got %s, want %s", loc.MainRepoRoot, repo)
	}
}
