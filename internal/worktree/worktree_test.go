package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
)

// gitTestEnv silences git's interactive prompts and pins identity so
// `git commit` works on CI runners that have no global git config.
var gitTestEnv = []string{
	"GIT_TERMINAL_PROMPT=0",
	"GIT_ASKPASS=",
	"GIT_AUTHOR_NAME=test",
	"GIT_AUTHOR_EMAIL=test@example.invalid",
	"GIT_COMMITTER_NAME=test",
	"GIT_COMMITTER_EMAIL=test@example.invalid",
}

// runTestGit fails the test on non-zero exit; convenience for tests
// where every git command is expected to succeed.
func runTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), gitTestEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

// setupTestRepo creates an empty git repo under t.TempDir() with a
// single commit on its default branch, returns the canonical repo path.
func setupTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	dir := t.TempDir()
	runTestGit(t, dir, "init", "-q", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	runTestGit(t, dir, "add", "README.md")
	runTestGit(t, dir, "commit", "-q", "-m", "init")
	canonical, err := config.CanonicalProjectRoot(dir)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	return canonical
}

// setupTestLocator returns a PathLocator with state/cache/sessions all
// rooted under a fresh tmp dir, isolated per test.
func setupTestLocator(t *testing.T) *config.PathLocator {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), "state")
	cacheDir := filepath.Join(t.TempDir(), "cache")
	configHome := filepath.Join(t.TempDir(), "config")
	logsDir := filepath.Join(t.TempDir(), "logs")
	sessionsDir := filepath.Join(stateDir, "sessions")
	for _, p := range []string{stateDir, cacheDir, configHome, logsDir, sessionsDir} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
	}
	return &config.PathLocator{
		ConfigHome:   configHome,
		StateDir:     stateDir,
		CacheDir:     cacheDir,
		SessionsRoot: sessionsDir,
		LogsDir:      logsDir,
		ExportsDir:   filepath.Join(stateDir, "exports"),
	}
}

func TestCreate_Basic(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	info, err := Create(ctx, CreateOptions{Name: "feat-a", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info.Existed {
		t.Errorf("Existed=true on first create")
	}
	if info.Slug != "feat-a" || info.Branch != "chord/feat-a" {
		t.Errorf("name fields wrong: %+v", info)
	}
	if !strings.Contains(info.Path, "worktrees") || !strings.Contains(info.Path, info.RepoID) {
		t.Errorf("path layout wrong: %s", info.Path)
	}
	if _, err := os.Stat(info.Path); err != nil {
		t.Errorf("worktree dir not created: %v", err)
	}
	branches, err := exec.Command("git", "-C", repo, "branch", "--list", "chord/feat-a").CombinedOutput()
	if err != nil || !strings.Contains(string(branches), "chord/feat-a") {
		t.Errorf("branch chord/feat-a not present: %s err=%v", branches, err)
	}
}

// TestCreate_DirtyMainRepo_ReportsMainDirty verifies that creating a
// worktree from a main repo with uncommitted changes still succeeds (per
// `git worktree add` semantics) and reports MainDirty=true so callers
// can warn the user that those changes are NOT visible in the worktree.
func TestCreate_DirtyMainRepo_ReportsMainDirty(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatalf("seed dirty file: %v", err)
	}
	info, err := Create(context.Background(), CreateOptions{Name: "feat-dirty", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatalf("Create on dirty main repo: %v", err)
	}
	if !info.MainDirty {
		t.Errorf("expected MainDirty=true, got Info=%+v", info)
	}
}

func TestCreate_FastResume(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	first, err := Create(ctx, CreateOptions{Name: "feat-a", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatalf("Create #1: %v", err)
	}
	second, err := Create(ctx, CreateOptions{Name: "feat-a", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatalf("Create #2 (fast-resume): %v", err)
	}
	if !second.Existed {
		t.Errorf("Existed=false on resume")
	}
	if first.Path != second.Path {
		t.Errorf("path drift: %q vs %q", first.Path, second.Path)
	}
}

func TestCreate_NotAGitRepo(t *testing.T) {
	dir := t.TempDir()
	pl := setupTestLocator(t)
	_, err := Create(context.Background(), CreateOptions{Name: "x", RepoRoot: dir, PathLocator: pl})
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("got err=%v, want 'not a git repository'", err)
	}
}

func TestCreate_RejectsNestedWorktree(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	first, err := Create(ctx, CreateOptions{Name: "outer", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatalf("Create #1: %v", err)
	}
	// Try creating from inside the linked worktree.
	_, err = Create(ctx, CreateOptions{Name: "inner", RepoRoot: first.Path, PathLocator: pl})
	if err == nil || !strings.Contains(err.Error(), "nested worktree creation refused") {
		t.Errorf("nested create allowed: err=%v", err)
	}
}

func TestCreate_FromSubdirectory_FindsMainRoot(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	info, err := Create(context.Background(), CreateOptions{Name: "feat", RepoRoot: sub, PathLocator: pl})
	if err != nil {
		t.Fatalf("Create from subdir: %v", err)
	}
	if info.RepoRoot != repo {
		t.Errorf("RepoRoot = %s, want %s (must canonicalize from subdir)", info.RepoRoot, repo)
	}
}

func TestList_FiltersByBranchPrefix(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	if _, err := Create(ctx, CreateOptions{Name: "alpha", RepoRoot: repo, PathLocator: pl}); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(ctx, CreateOptions{Name: "beta", RepoRoot: repo, PathLocator: pl}); err != nil {
		t.Fatal(err)
	}
	// Add a non-chord-managed worktree manually; List must skip it.
	other := filepath.Join(t.TempDir(), "manual")
	runTestGit(t, repo, "worktree", "add", "-b", "manual-branch", other)

	infos, err := List(ctx, repo)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	names := map[string]bool{}
	for _, i := range infos {
		names[i.Name] = true
	}
	if !names["alpha"] || !names["beta"] {
		t.Errorf("List missed chord worktrees: %+v", names)
	}
	for _, i := range infos {
		if !strings.HasPrefix(i.Branch, BranchPrefix) {
			t.Errorf("List included non-chord branch: %s", i.Branch)
		}
	}
}

func TestRemove_DefaultPreservesBranch(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	info, err := Create(ctx, CreateOptions{Name: "feat", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatal(err)
	}
	// Seed sessions/cache so we can assert cleanup happened.
	pj, err := pl.LocateProject(info.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(pj.ProjectSessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pj.ProjectSessionsDir, "marker"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Remove(ctx, repo, "feat", RemoveOptions{}, pl); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(info.Path); err == nil {
		t.Errorf("worktree dir still exists after Remove")
	}
	if _, err := os.Stat(pj.ProjectSessionsDir); err == nil {
		t.Errorf("sessions dir still exists after Remove (cascade cleanup failed)")
	}
	// Branch must remain — Remove default keeps it.
	branches, err := exec.Command("git", "-C", repo, "branch", "--list", "chord/feat").CombinedOutput()
	if err != nil {
		t.Fatalf("branch --list: %v: %s", err, branches)
	}
	if !strings.Contains(string(branches), "chord/feat") {
		t.Errorf("branch chord/feat removed by default; should require --delete-branch / --force")
	}
}

func TestRemove_DeleteBranch_OnUnmergedRefuses(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	info, err := Create(ctx, CreateOptions{Name: "feat", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatal(err)
	}
	// Make a commit ONLY on the worktree's branch so it diverges from main.
	if err := os.WriteFile(filepath.Join(info.Path, "extra.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, info.Path, "add", "extra.txt")
	runTestGit(t, info.Path, "commit", "-q", "-m", "extra")

	err = Remove(ctx, repo, "feat", RemoveOptions{DeleteBranch: true}, pl)
	if err == nil || !strings.Contains(err.Error(), "delete branch") {
		t.Errorf("expected delete-branch refusal on unmerged branch, got %v", err)
	}
	branches, _ := exec.Command("git", "-C", repo, "branch", "--list", "chord/feat").CombinedOutput()
	if !strings.Contains(string(branches), "chord/feat") {
		t.Errorf("branch was removed despite refusal: %s", branches)
	}
}

func TestRemove_DirtyRefusedWithoutForce(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	info, err := Create(ctx, CreateOptions{Name: "feat", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(info.Path, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = Remove(ctx, repo, "feat", RemoveOptions{}, pl)
	if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Errorf("expected dirty refusal, got %v", err)
	}
	if _, err := os.Stat(info.Path); err != nil {
		t.Errorf("worktree dir gone despite refusal: %v", err)
	}
}

func TestRemove_ForceRemovesDirtyAndBranch(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	info, err := Create(ctx, CreateOptions{Name: "feat", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(info.Path, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Remove(ctx, repo, "feat", RemoveOptions{Force: true}, pl); err != nil {
		t.Fatalf("Remove --force: %v", err)
	}
	branches, _ := exec.Command("git", "-C", repo, "branch", "--list", "chord/feat").CombinedOutput()
	if strings.Contains(string(branches), "chord/feat") {
		t.Errorf("branch should be force-deleted, still present: %s", branches)
	}
}

func TestRemove_RefusesCwdSelf(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Chdir under t.TempDir + worktree subprocess + cleanup is flaky on Windows; covered indirectly elsewhere")
	}
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	info, err := Create(ctx, CreateOptions{Name: "feat", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatal(err)
	}
	orig, _ := os.Getwd()
	defer func() { _ = os.Chdir(orig) }()
	if err := os.Chdir(info.Path); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	err = Remove(ctx, repo, "feat", RemoveOptions{Force: true}, pl)
	if err == nil || !strings.Contains(err.Error(), "current working directory") {
		t.Errorf("cwd-self removal allowed: err=%v", err)
	}
}

func TestIsInsideLinkedWorktree_MainRepoSubdirIsFalse(t *testing.T) {
	repo := setupTestRepo(t)
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := IsInsideLinkedWorktree(context.Background(), sub)
	if err != nil {
		t.Fatalf("IsInsideLinkedWorktree: %v", err)
	}
	if got {
		t.Errorf("subdir of main repo reported as linked worktree (path-relativity bug)")
	}
}

func TestIsInsideLinkedWorktree_LinkedTrue(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	info, err := Create(context.Background(), CreateOptions{Name: "x", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatal(err)
	}
	got, err := IsInsideLinkedWorktree(context.Background(), info.Path)
	if err != nil {
		t.Fatalf("IsInsideLinkedWorktree: %v", err)
	}
	if !got {
		t.Errorf("linked worktree path reported as not linked")
	}
}
