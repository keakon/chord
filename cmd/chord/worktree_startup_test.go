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

func mustRunStartupGit(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), gitTestEnvForStartup...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return out
}

// writeStartupTestConfig writes a global config.yaml into the test config
// home so planInitAppStartup can resolve providers and storage paths.
func writeStartupTestConfig(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(os.Getenv("CHORD_CONFIG_HOME"), "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write global config: %v", err)
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

// TestWorktreeSessionsShareRepositoryKey verifies the storage half of repo-scoped
// sessions: planning a session from inside a worktree resolves the repository
// content root's project key, so --continue there finds the sessions created in
// the main checkout.
func TestWorktreeSessionsShareRepositoryKey(t *testing.T) {
	repo := setupStartupRepo(t)
	withTestStateDir(t)
	chdirForTest(t, repo)
	// planInitAppStartup requires a readable global config.
	writeStartupTestConfig(t, "paths:\n  state_dir: "+flagStateDir+"\n  cache_dir: "+flagCacheDir+"\n  logs_dir: "+flagLogsDir+"\n  sessions_dir: "+flagSessionsDir+"\n")
	prepareStartupWorktreeForTest(t, context.Background(), "feat-shared")

	pl, err := startupPathLocator()
	if err != nil {
		t.Fatalf("startupPathLocator: %v", err)
	}
	repoPL, err := pl.LocateProject(repo)
	if err != nil {
		t.Fatalf("LocateProject(repo): %v", err)
	}
	const sid = "01HXSHARED000000000001"
	sessionDir := filepath.Join(repoPL.ProjectSessionsDir, sid)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "main.jsonl"), []byte(`{"role":"user","content":"hi"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write main.jsonl: %v", err)
	}

	// Cwd is the worktree after prepareStartupWorktree.
	workDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	contentRoot := resolveContentRoot(context.Background(), workDir)
	if contentRoot != repo {
		t.Fatalf("resolveContentRoot from worktree = %s, want %s", contentRoot, repo)
	}

	plan, err := planInitAppStartup(contentRoot, workDir)
	if err != nil {
		t.Fatalf("planInitAppStartup: %v", err)
	}
	if plan.ProjectLocator.ProjectKey != repoPL.ProjectKey {
		t.Fatalf("worktree session key = %q, want the repository key %q", plan.ProjectLocator.ProjectKey, repoPL.ProjectKey)
	}

	sp, err := planSessionStartup(plan.ProjectLocator.ProjectSessionsDir, sessionStartupOptions{ContinueLatest: true})
	if err != nil {
		t.Fatalf("planSessionStartup: %v", err)
	}
	defer func() {
		if err := sp.SessionLock.Release(); err != nil {
			t.Errorf("release session lock: %v", err)
		}
	}()
	if sp.SessionDir != sessionDir {
		t.Errorf("--continue picked %s, want the repository session %s", sp.SessionDir, sessionDir)
	}
}

func TestStartupBranchPrefixUsesProjectOverride(t *testing.T) {
	withTestStateDir(t)
	repo := setupStartupRepo(t)
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	if err := os.WriteFile(filepath.Join(configHome, "config.yaml"), []byte("worktree:\n  branch_prefix: global/\n"), 0o644); err != nil {
		t.Fatalf("write global config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".chord"), 0o755); err != nil {
		t.Fatalf("mkdir project .chord: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".chord", "config.yaml"), []byte("worktree:\n  branch_prefix: project\n"), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	chdirForTest(t, repo)

	got, err := startupBranchPrefix()
	if err != nil {
		t.Fatalf("startupBranchPrefix: %v", err)
	}
	if got != "project/" {
		t.Fatalf("startupBranchPrefix() = %q, want project/", got)
	}
}

func prepareStartupWorktreeForTest(t *testing.T, ctx context.Context, name string) *worktree.Info {
	t.Helper()
	var info *worktree.Info
	output, err := captureStderr(t, func() error {
		var createErr error
		info, createErr = prepareStartupWorktree(ctx, name, false)
		return createErr
	})
	if err != nil {
		t.Fatalf("prepareStartupWorktree %q: %v", name, err)
	}
	if output != "" && !strings.Contains(output, "worktree") {
		t.Fatalf("unexpected worktree startup output = %q", output)
	}
	return info
}

func TestPrepareStartupWorktree_ChdirAndIndex(t *testing.T) {
	repo := setupStartupRepo(t)
	withTestStateDir(t)
	chdirForTest(t, repo)

	info := prepareStartupWorktreeForTest(t, context.Background(), "feat-a")
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

	info := prepareStartupWorktreeForTest(t, context.Background(), "")
	if !strings.HasPrefix(info.Name, "task-") {
		t.Errorf("auto slug did not get task- prefix: %s", info.Name)
	}
}

// TestPrepareStartupWorktree_RecordsCLIOwner pins the creator identity of a
// worktree made by the command line. Without the record the worktree is
// anonymous: `chord worktree list` cannot name its creator after the repo index
// is dropped, and the plan's `kind ∈ {main, sub, cli}` has no producer for cli.
func TestPrepareStartupWorktree_RecordsCLIOwner(t *testing.T) {
	repo := setupStartupRepo(t)
	withTestStateDir(t)
	chdirForTest(t, repo)

	info := prepareStartupWorktreeForTest(t, context.Background(), "feat-cli")
	owner, err := worktree.ReadOwner(context.Background(), info.Path)
	if err != nil {
		t.Fatalf("ReadOwner: %v", err)
	}
	if owner.Kind != worktree.OwnerKindCLI || owner.SessionID != "" {
		t.Fatalf("owner = %+v, want a sessionless cli record", owner)
	}
	if owner.CreatedAt.IsZero() {
		t.Error("owner record should carry a creation time")
	}
	// The OWNER column reads the worktree's own git metadata, so it still names
	// the creator when the index is gone (entry == nil) or was rebuilt.
	if label := worktreeOwnerLabel(context.Background(), info.Path, nil); label != "cli" {
		t.Errorf("owner label = %q, want cli", label)
	}
}

func TestPrepareStartupWorktree_InvalidSlug(t *testing.T) {
	repo := setupStartupRepo(t)
	withTestStateDir(t)
	chdirForTest(t, repo)

	_, err := prepareStartupWorktree(context.Background(), "bad/name", false)
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("expected slug validation error, got %v", err)
	}
}

func TestPrepareStartupWorktree_FailsOnBadGlobalConfig(t *testing.T) {
	repo := setupStartupRepo(t)
	withTestStateDir(t)
	chdirForTest(t, repo)
	t.Setenv("CHORD_CONFIG_HOME", flagConfigHome)
	// Malformed YAML prevents startup: the worktree path must surface the
	// parse error instead of proceeding with defaults.
	if err := os.WriteFile(filepath.Join(flagConfigHome, "config.yaml"), []byte("providers: [broken\n"), 0o644); err != nil {
		t.Fatalf("write malformed config: %v", err)
	}

	_, err := prepareStartupWorktree(context.Background(), "feat-bad-config", false)
	if err == nil {
		t.Fatal("prepareStartupWorktree should fail for malformed global config")
	}
	if !strings.Contains(err.Error(), "parse config") {
		t.Fatalf("err = %v, want a config parse error", err)
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

	info := prepareStartupWorktreeForTest(t, context.Background(), "feat-a")

	pl, err := startupPathLocator()
	if err != nil {
		t.Fatalf("startupPathLocator: %v", err)
	}
	// Sessions live under the repository content root key even when they ran
	// in a worktree; the worktree itself is recorded in the session metadata.
	mainPL, err := pl.LocateProject(info.RepoRoot)
	if err != nil {
		t.Fatalf("LocateProject: %v", err)
	}
	sid := "01HXXTESTSESSION0001"
	sessionDir := filepath.Join(pl.SessionsRoot, mainPL.ProjectKey, sid)
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
	if got.ContentRoot != "" {
		t.Errorf("worktree session should not also carry a content root, got %q", got.ContentRoot)
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

	// A session stored under the content root key needs no worktree metadata:
	// resolution hands back the repository root itself.
	loc, err := resolveSessionWorktree(context.Background(), sid)
	if err != nil {
		t.Fatalf("resolveSessionWorktree: %v", err)
	}
	if loc == nil || loc.Worktree != nil {
		t.Errorf("main repo session resolved as worktree: %+v", loc)
	}
	if loc != nil && loc.ContentRoot != repo {
		t.Errorf("content root mismatch: got %s, want %s", loc.ContentRoot, repo)
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

func TestResolveSessionWorktree_NonGitProject(t *testing.T) {
	withTestStateDir(t)
	project := t.TempDir()
	chdirForTest(t, project)

	pl, err := startupPathLocator()
	if err != nil {
		t.Fatalf("startupPathLocator: %v", err)
	}
	projectPL, err := pl.LocateProject(project)
	if err != nil {
		t.Fatalf("LocateProject: %v", err)
	}
	sid := "01HXNONGITSESSION01"
	writeTestSessionMain(t, projectPL.ProjectSessionsDir, sid, `{"role":"user","content":"hi"}`+"\n")

	loc, err := resolveSessionWorktree(context.Background(), sid)
	if err != nil {
		t.Fatalf("resolveSessionWorktree: %v", err)
	}
	if loc == nil || loc.ContentRoot == "" || loc.Worktree != nil {
		t.Fatalf("expected non-git content root resolution, got %+v", loc)
	}
	canonicalProject, err := config.CanonicalProjectRoot(project)
	if err != nil {
		t.Fatalf("canonical project: %v", err)
	}
	if loc.ContentRoot != canonicalProject {
		t.Errorf("content root mismatch: got %s, want %s", loc.ContentRoot, canonicalProject)
	}
}

func TestResolveSessionWorktree_NonGitProjectNotFound(t *testing.T) {
	withTestStateDir(t)
	project := t.TempDir()
	chdirForTest(t, project)

	_, err := resolveSessionWorktree(context.Background(), "01HXNONGITNOSUCH01")
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if strings.Contains(err.Error(), "resolve git main root") || strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("non-git project should fall back to current project lookup, got %v", err)
	}
	if !strings.Contains(err.Error(), "not found in project") {
		t.Fatalf("expected project not-found error, got %v", err)
	}
}

// TestResolveSessionWorktree_FromInsideWorktree verifies that running
// `chord resume <main-sid>` from inside a worktree directory still reports
// the repository content root, so the resume command can chdir back to it
// instead of computing a worktree-local project key.
func TestResolveSessionWorktree_FromInsideWorktree(t *testing.T) {
	repo := setupStartupRepo(t)
	withTestStateDir(t)
	chdirForTest(t, repo)

	// prepareStartupWorktreeForTest chdirs into the new worktree, so the rest
	// of the test runs from inside it.
	prepareStartupWorktreeForTest(t, context.Background(), "feat-x")

	// Seed a session under the content root key, as a main-checkout session
	// would be stored.
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

	// Cwd is currently the worktree (prepareStartupWorktree chdir'd).
	loc, err := resolveSessionWorktree(context.Background(), sid)
	if err != nil {
		t.Fatalf("resolveSessionWorktree from inside worktree: %v", err)
	}
	if loc == nil || loc.Worktree != nil {
		t.Fatalf("expected content root resolution, got %+v", loc)
	}
	if loc.ContentRoot != repo {
		t.Errorf("content root mismatch: got %s, want %s", loc.ContentRoot, repo)
	}
}
