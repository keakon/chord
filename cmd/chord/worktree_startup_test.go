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
		info, createErr = prepareStartupWorktree(ctx, name)
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

func TestPrepareStartupWorktree_InvalidSlug(t *testing.T) {
	repo := setupStartupRepo(t)
	withTestStateDir(t)
	chdirForTest(t, repo)

	_, err := prepareStartupWorktree(context.Background(), "bad/name")
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("expected slug validation error, got %v", err)
	}
}

func TestPrepareStartupWorktree_BadGlobalConfigFailsBeforeCreatingWorktree(t *testing.T) {
	repo := setupStartupRepo(t)
	withTestStateDir(t)
	chdirForTest(t, repo)
	t.Setenv("CHORD_CONFIG_HOME", flagConfigHome)
	if err := os.WriteFile(filepath.Join(flagConfigHome, "config.yaml"), []byte("providers: [broken\n"), 0o644); err != nil {
		t.Fatalf("write malformed config: %v", err)
	}

	_, err := prepareStartupWorktree(context.Background(), "feat-bad-config")
	if err == nil || !strings.Contains(err.Error(), "parse config") {
		t.Fatalf("expected malformed config error, got %v", err)
	}
	branches := string(mustRunStartupGit(t, repo, "branch", "--list", "chord/feat-bad-config"))
	if strings.Contains(branches, "chord/feat-bad-config") {
		t.Fatalf("worktree branch unexpectedly created despite malformed config: %s", branches)
	}
	list := string(mustRunStartupGit(t, repo, "worktree", "list", "--porcelain"))
	if strings.Contains(list, "feat-bad-config") {
		t.Fatalf("git worktree unexpectedly created despite malformed config: %s", list)
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
	if loc == nil || loc.ProjectRoot == "" || loc.Worktree != nil || loc.MainRepoRoot != "" {
		t.Fatalf("expected non-git ProjectRoot resolution, got %+v", loc)
	}
	canonicalProject, err := config.CanonicalProjectRoot(project)
	if err != nil {
		t.Fatalf("canonical project: %v", err)
	}
	if loc.ProjectRoot != canonicalProject {
		t.Errorf("ProjectRoot mismatch: got %s, want %s", loc.ProjectRoot, canonicalProject)
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
	if !strings.Contains(err.Error(), "not found in current project") {
		t.Fatalf("expected current-project not-found error, got %v", err)
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
	wtInfo := prepareStartupWorktreeForTest(t, context.Background(), "feat-x")

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
