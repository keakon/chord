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

func mustRunGit(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), gitTestEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return out
}

// gitRevParse returns the trimmed output of `git rev-parse <rev>`.
func gitRevParse(t *testing.T, dir, rev string) string {
	t.Helper()
	return strings.TrimSpace(string(mustRunGit(t, dir, "rev-parse", rev)))
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

// seedProjectState creates the per-project state chord generates for a
// worktree path (sessions, exports, runtime cache, registry metadata) and
// returns its locator.
func seedProjectState(t *testing.T, pl *config.PathLocator, worktreePath string) *config.ProjectLocator {
	t.Helper()
	pj, err := pl.LocateProject(worktreePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{pj.ProjectSessionsDir, pj.ProjectExportsDir, pj.RuntimeCacheDir, filepath.Dir(pj.RegistryMetaPath)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(pj.ProjectSessionsDir, "marker"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pj.RegistryMetaPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return pj
}

func assertPathExists(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Stat(path)
	if want && err != nil {
		t.Errorf("%s missing, want it kept: %v", path, err)
	}
	if !want && err == nil {
		t.Errorf("%s still exists, want it removed", path)
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
	if info.Name != "feat-a" || info.Branch != "chord/feat-a" {
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
	if second.Name != "feat-a" {
		t.Errorf("resumed Info = %+v, want Name feat-a", second)
	}
}

// TestCreate_FastResumeDerivesNameFromBranch pins the branch-only resume: the
// caller names only a branch, so the name is derived from it and opts.Name is
// still empty. The resumed Info must still carry the derived name, or the index
// entry built from it records no worktree name.
func TestCreate_FastResumeDerivesNameFromBranch(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	if _, err := Create(ctx, CreateOptions{Name: "feat-b", RepoRoot: repo, PathLocator: pl}); err != nil {
		t.Fatalf("Create #1: %v", err)
	}
	resumed, err := Create(ctx, CreateOptions{Branch: "chord/feat-b", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatalf("Create #2 (fast-resume by branch): %v", err)
	}
	if !resumed.Existed {
		t.Errorf("Existed=false on resume")
	}
	if resumed.Name != "feat-b" {
		t.Errorf("resumed Info = %+v, want Name feat-b", resumed)
	}
}

func TestCreate_RefusesLeftoverBranchWithoutReset(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	info, err := Create(ctx, CreateOptions{Name: "feat", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatal(err)
	}
	// Put a commit only on the worktree branch, then remove the worktree while
	// keeping the branch: recreating the name must not reset it silently.
	if err := os.WriteFile(filepath.Join(info.Path, "extra.txt"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, info.Path, "add", "extra.txt")
	runTestGit(t, info.Path, "commit", "-q", "-m", "extra")
	branchTip := gitRevParse(t, repo, "chord/feat")
	if err := Remove(ctx, repo, "feat", RemoveOptions{}, pl); err != nil {
		t.Fatal(err)
	}

	_, err = Create(ctx, CreateOptions{Name: "feat", RepoRoot: repo, PathLocator: pl})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("recreate over leftover branch: err=%v, want refusal mentioning the existing branch", err)
	}
	if got := gitRevParse(t, repo, "chord/feat"); got != branchTip {
		t.Fatalf("branch moved even though Create was refused: %s -> %s", branchTip, got)
	}

	recreated, err := Create(ctx, CreateOptions{Name: "feat", RepoRoot: repo, PathLocator: pl, ResetBranch: true})
	if err != nil {
		t.Fatalf("Create with ResetBranch: %v", err)
	}
	if recreated.Path != info.Path {
		t.Errorf("reset-recreate path drift: %q vs %q", recreated.Path, info.Path)
	}
	if got, want := gitRevParse(t, repo, "chord/feat"), gitRevParse(t, repo, "HEAD"); got != want {
		t.Errorf("ResetBranch left the branch at %s, want the main HEAD %s", got, want)
	}
}

// TestCreate_PrunesStaleRegistrationInsteadOfResumingIt pins what happens when
// a worktree directory disappears but its git registration survives (a manual
// rm -rf, an interrupted create): the fast-resume path must not hand back a
// binding to a directory that is not there, and the stale registration must be
// dropped so the branch guard owns what happens next.
func TestCreate_PrunesStaleRegistrationInsteadOfResumingIt(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	info, err := Create(ctx, CreateOptions{Name: "feat-stale", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(info.Path); err != nil {
		t.Fatalf("remove checkout directory: %v", err)
	}

	_, err = Create(ctx, CreateOptions{Name: "feat-stale", RepoRoot: repo, PathLocator: pl})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("recreate over a stale registration: err=%v, want the branch refusal", err)
	}
	if _, err := ResolveByName(ctx, repo, "feat-stale", DefaultBranchPrefix); err == nil {
		t.Fatal("stale worktree registration survived, want it pruned")
	}

	recreated, err := Create(ctx, CreateOptions{Name: "feat-stale", RepoRoot: repo, PathLocator: pl, ResetBranch: true})
	if err != nil {
		t.Fatalf("Create with ResetBranch after prune: %v", err)
	}
	if recreated.Existed {
		t.Error("recreated Info.Existed = true, want a fresh create")
	}
	if _, err := os.Stat(recreated.Path); err != nil {
		t.Fatalf("recreated checkout missing: %v", err)
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

	infos, err := List(ctx, repo, "")
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
		if !strings.HasPrefix(i.Branch, DefaultBranchPrefix) {
			t.Errorf("List included non-chord branch: %s", i.Branch)
		}
	}
}

func TestGitMainRootResolvesBareRepositoryToItself(t *testing.T) {
	ctx := context.Background()
	seed := setupTestRepo(t)
	bare := filepath.Join(t.TempDir(), "repo.git")
	runTestGit(t, seed, "clone", "-q", "--bare", seed, bare)
	canonicalBare, err := config.CanonicalProjectRoot(bare)
	if err != nil {
		t.Fatalf("canonical bare: %v", err)
	}

	// A bare repository has no working tree; its container directory is not a
	// repository, so the bare dir itself is the main root.
	if got, err := GitMainRoot(ctx, bare); err != nil || got != canonicalBare {
		t.Fatalf("GitMainRoot(%q) = %q, %v; want %q", bare, got, err, canonicalBare)
	}

	// A linked worktree of a bare repository shares that bare dir as its
	// common dir: resolving its parent would anchor the project to the
	// container directory.
	linked := filepath.Join(t.TempDir(), "linked")
	runTestGit(t, bare, "worktree", "add", "-q", "-b", "feat", linked)
	if got, err := GitMainRoot(ctx, linked); err != nil || got != canonicalBare {
		t.Fatalf("GitMainRoot(%q) = %q, %v; want %q", linked, got, err, canonicalBare)
	}
}

func TestRemove_PreservesBranchAndSessions(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	info, err := Create(ctx, CreateOptions{Name: "feat", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatal(err)
	}
	pj := seedProjectState(t, pl, info.Path)

	if err := Remove(ctx, repo, "feat", RemoveOptions{}, pl); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(info.Path); err == nil {
		t.Errorf("worktree dir still exists after Remove")
	}
	// Sessions and exports belong to the repository, not to the checkout, so
	// removing a worktree must leave them alone.
	assertPathExists(t, pj.ProjectSessionsDir, true)
	assertPathExists(t, pj.ProjectExportsDir, true)
	// Runtime cache and registry metadata are derived from the checkout.
	assertPathExists(t, pj.RuntimeCacheDir, false)
	assertPathExists(t, pj.RegistryMetaPath, false)
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

	pj, err := pl.LocateProject(info.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(pj.RuntimeCacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(pj.RegistryMetaPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pj.RegistryMetaPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RegisterInIndex(pl, info); err != nil {
		t.Fatal(err)
	}

	err = Remove(ctx, repo, "feat", RemoveOptions{DeleteBranch: true}, pl)
	if err == nil || !strings.Contains(err.Error(), "delete branch") {
		t.Errorf("expected delete-branch refusal on unmerged branch, got %v", err)
	}
	branches, _ := exec.Command("git", "-C", repo, "branch", "--list", "chord/feat").CombinedOutput()
	if !strings.Contains(string(branches), "chord/feat") {
		t.Errorf("branch was removed despite refusal: %s", branches)
	}
	// The refusal must come before the checkout is touched: an unmerged branch
	// can only be reported by `git branch -d` after `git worktree remove`, and
	// running it there would leave the removal half-executed.
	if _, err := os.Stat(info.Path); err != nil {
		t.Errorf("checkout dir gone after a refused delete-branch removal: %v", err)
	}
	if _, err := ResolveByName(ctx, repo, "feat", DefaultBranchPrefix); err != nil {
		t.Errorf("worktree no longer resolves after a refused removal: %v", err)
	}
	if _, err := os.Stat(pj.RuntimeCacheDir); err != nil {
		t.Errorf("runtime cache removed by a refused removal: %v", err)
	}
	if _, err := os.Stat(pj.RegistryMetaPath); err != nil {
		t.Errorf("registry metadata removed by a refused removal: %v", err)
	}
	idx, err := LoadRepoIndex(pl.StateDir, info.RepoID)
	if err != nil {
		t.Fatalf("LoadRepoIndex: %v", err)
	}
	if idx == nil || idx.FindWorktree("feat") == nil {
		t.Errorf("repo index entry dropped by a refused removal: %+v", idx)
	}
}

func TestRemove_DeleteBranch_OnMergedDeletesBranch(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	info, err := Create(ctx, CreateOptions{Name: "feat", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatal(err)
	}
	// A fresh worktree branch starts at the main checkout's HEAD, so this is
	// the branch `git branch -d` accepts: the pre-check must not refuse it.
	if err := Remove(ctx, repo, "feat", RemoveOptions{DeleteBranch: true}, pl); err != nil {
		t.Fatalf("Remove --delete-branch on a merged branch: %v", err)
	}
	if _, err := os.Stat(info.Path); err == nil {
		t.Errorf("worktree dir still exists after Remove")
	}
	branches, _ := exec.Command("git", "-C", repo, "branch", "--list", "chord/feat").CombinedOutput()
	if strings.Contains(string(branches), "chord/feat") {
		t.Errorf("merged branch survived --delete-branch: %s", branches)
	}
}

func TestBranchFullyMergedTracksHead(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	info, err := Create(ctx, CreateOptions{Name: "feat", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatal(err)
	}
	merged, target, err := BranchFullyMerged(ctx, repo, info.Branch)
	if err != nil {
		t.Fatalf("BranchFullyMerged: %v", err)
	}
	if !merged || target != "HEAD" {
		t.Errorf("fresh branch = (%v, %q), want (true, HEAD)", merged, target)
	}
	if err := os.WriteFile(filepath.Join(info.Path, "extra.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, info.Path, "add", "extra.txt")
	runTestGit(t, info.Path, "commit", "-q", "-m", "extra")
	merged, target, err = BranchFullyMerged(ctx, repo, info.Branch)
	if err != nil {
		t.Fatalf("BranchFullyMerged after commit: %v", err)
	}
	if merged || target != "HEAD" {
		t.Errorf("diverged branch = (%v, %q), want (false, HEAD)", merged, target)
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

// TestFinish_RefusesBeforeTouchingTheMainLineWhenHeld pins the ordering of the
// holder check: merging, squashing and fast-forwarding the target branch cannot
// be taken back, so a refusal that only arrived at the deletion step would leave
// the main line already advanced while the checkout survives.
func TestFinish_RefusesBeforeTouchingTheMainLineWhenHeld(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	info, err := Create(ctx, CreateOptions{Name: "feat-held", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(info.Path, "extra.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, info.Path, "add", "extra.txt")
	runTestGit(t, info.Path, "commit", "-q", "-m", "worktree commit")

	beforeMain := strings.TrimSpace(string(mustRunGit(t, repo, "rev-parse", "main")))
	if branchHead := strings.TrimSpace(string(mustRunGit(t, info.Path, "rev-parse", "HEAD"))); branchHead == beforeMain {
		t.Fatal("test setup: the worktree branch must be ahead of main")
	}

	err = Finish(ctx, repo, "feat-held", FinishOptions{
		Holders: func(*Info) []string {
			return []string{"chord session 20260101000000000 is still open in this checkout"}
		},
	}, pl)
	if err == nil || !strings.Contains(err.Error(), "still in use") {
		t.Fatalf("Finish error = %v, want an in-use refusal", err)
	}
	if after := strings.TrimSpace(string(mustRunGit(t, repo, "rev-parse", "main"))); after != beforeMain {
		t.Errorf("main moved to %s despite the refusal (was %s)", after, beforeMain)
	}
	if _, err := os.Stat(info.Path); err != nil {
		t.Errorf("checkout should survive a refused finish: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "extra.txt")); err == nil {
		t.Error("worktree content landed in the main checkout despite the refusal")
	}
	if _, err := ResolveByName(ctx, repo, "feat-held", DefaultBranchPrefix); err != nil {
		t.Errorf("branch should survive a refused finish: %v", err)
	}
}

func TestFinish_Basic_MergesOntoThenSquashesAndReclaims(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	info, err := Create(ctx, CreateOptions{Name: "feat", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("from main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "shared.txt")
	runTestGit(t, repo, "commit", "-q", "-m", "main adds shared")

	if err := os.WriteFile(filepath.Join(info.Path, "extra.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, info.Path, "add", "extra.txt")
	runTestGit(t, info.Path, "commit", "-q", "-m", "worktree commit 1")
	if err := os.WriteFile(filepath.Join(info.Path, "extra.txt"), []byte("hi again\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, info.Path, "add", "extra.txt")
	runTestGit(t, info.Path, "commit", "-q", "-m", "worktree commit 2")

	beforeMain := strings.TrimSpace(string(mustRunGit(t, repo, "rev-parse", "main")))
	branchHead := strings.TrimSpace(string(mustRunGit(t, info.Path, "rev-parse", "HEAD")))

	if err := Finish(ctx, repo, "feat", FinishOptions{}, pl); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if _, err := os.Stat(info.Path); err == nil {
		t.Errorf("worktree dir still exists after Finish")
	}
	branches, _ := exec.Command("git", "-C", repo, "branch", "--list", "chord/feat").CombinedOutput()
	if strings.Contains(string(branches), "chord/feat") {
		t.Errorf("branch chord/feat still present after Finish: %s", branches)
	}
	if _, err := os.Stat(filepath.Join(repo, "extra.txt")); err != nil {
		t.Errorf("expected extra.txt in main repo after Finish: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "shared.txt")); err != nil {
		t.Errorf("expected shared.txt in main repo after Finish: %v", err)
	}

	afterMain := strings.TrimSpace(string(mustRunGit(t, repo, "rev-parse", "main")))
	if afterMain == branchHead {
		t.Fatalf("main HEAD unexpectedly equals original worktree HEAD; wanted a new squashed commit")
	}
	parents := strings.Fields(strings.TrimSpace(string(mustRunGit(t, repo, "rev-list", "--parents", "-n", "1", afterMain))))
	if len(parents) != 2 {
		t.Fatalf("squashed commit should have exactly one parent, got %v", parents)
	}
	if parents[1] != beforeMain {
		t.Fatalf("squashed commit parent = %s, want previous main HEAD %s", parents[1], beforeMain)
	}
	body := string(mustRunGit(t, repo, "show", "-s", "--format=%B", afterMain))
	if !strings.Contains(body, "worktree commit 1") || !strings.Contains(body, "worktree commit 2") {
		t.Fatalf("squashed commit message did not preserve squash message contents:\n%s", body)
	}
}

func TestFinish_WhenWorktreeOnlyBehindMain_ReclaimsWithoutSquashCommit(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	info, err := Create(ctx, CreateOptions{Name: "feat", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatal(err)
	}

	beforeMain := strings.TrimSpace(string(mustRunGit(t, repo, "rev-parse", "main")))
	beforeBranch := strings.TrimSpace(string(mustRunGit(t, info.Path, "rev-parse", "HEAD")))
	if beforeMain != beforeBranch {
		t.Fatalf("expected freshly created worktree branch to match main HEAD: main=%s branch=%s", beforeMain, beforeBranch)
	}

	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("from main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "shared.txt")
	runTestGit(t, repo, "commit", "-q", "-m", "main adds shared")
	afterMainAdvance := strings.TrimSpace(string(mustRunGit(t, repo, "rev-parse", "main")))
	if afterMainAdvance == beforeMain {
		t.Fatal("expected main HEAD to advance")
	}

	if err := Finish(ctx, repo, "feat", FinishOptions{}, pl); err != nil {
		t.Fatalf("Finish behind-only branch: %v", err)
	}
	if _, err := os.Stat(info.Path); err == nil {
		t.Errorf("worktree dir still exists after Finish")
	}
	branches, _ := exec.Command("git", "-C", repo, "branch", "--list", "chord/feat").CombinedOutput()
	if strings.Contains(string(branches), "chord/feat") {
		t.Errorf("branch chord/feat still present after Finish: %s", branches)
	}
	if _, err := os.Stat(filepath.Join(repo, "shared.txt")); err != nil {
		t.Errorf("expected shared.txt in main repo after Finish: %v", err)
	}

	afterMain := strings.TrimSpace(string(mustRunGit(t, repo, "rev-parse", "main")))
	if afterMain != afterMainAdvance {
		t.Fatalf("main HEAD changed unexpectedly: got %s want %s", afterMain, afterMainAdvance)
	}
}

func TestFinish_UsesCustomSquashMessageWhenProvided(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	info, err := Create(ctx, CreateOptions{Name: "feat", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(info.Path, "extra.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, info.Path, "add", "extra.txt")
	runTestGit(t, info.Path, "commit", "-q", "-m", "worktree commit 1")
	runTestGit(t, info.Path, "commit", "--allow-empty", "-q", "-m", "worktree commit 2")

	if err := Finish(ctx, repo, "feat", FinishOptions{Message: "feat: custom finish"}, pl); err != nil {
		t.Fatalf("Finish with custom message: %v", err)
	}
	body := string(mustRunGit(t, repo, "show", "-s", "--format=%B", "HEAD"))
	if strings.TrimSpace(body) != "feat: custom finish" {
		t.Fatalf("unexpected finish commit message: %q", body)
	}
}

func TestFinish_RestoresOriginalMainBranchAfterSquashFinish(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()

	runTestGit(t, repo, "checkout", "-q", "-b", "release")
	runTestGit(t, repo, "checkout", "-q", "main")

	info, err := Create(ctx, CreateOptions{Name: "feat", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatal(err)
	}

	runTestGit(t, repo, "checkout", "-q", "release")
	beforeBranch := strings.TrimSpace(string(mustRunGit(t, repo, "branch", "--show-current")))
	if beforeBranch != "release" {
		t.Fatalf("expected main repo to start on release, got %q", beforeBranch)
	}

	if err := os.WriteFile(filepath.Join(info.Path, "extra.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, info.Path, "add", "extra.txt")
	runTestGit(t, info.Path, "commit", "-q", "-m", "worktree commit")

	if err := Finish(ctx, repo, "feat", FinishOptions{Onto: "main"}, pl); err != nil {
		t.Fatalf("Finish onto main: %v", err)
	}

	afterBranch := strings.TrimSpace(string(mustRunGit(t, repo, "branch", "--show-current")))
	if afterBranch != "release" {
		t.Fatalf("main repo branch after Finish = %q, want release", afterBranch)
	}
}

func TestFinish_RefusesExistingRebaseInProgress(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	info, err := Create(ctx, CreateOptions{Name: "feat", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatal(err)
	}

	rebaseDir, err := runGitText(ctx, info.Path, "rev-parse", "--git-path", "rebase-merge")
	if err != nil {
		t.Fatalf("rev-parse rebase-merge: %v", err)
	}
	if err := os.MkdirAll(resolveGitPath(info.Path, rebaseDir), 0o755); err != nil {
		t.Fatalf("mkdir rebase-merge: %v", err)
	}

	err = Finish(ctx, repo, "feat", FinishOptions{}, pl)
	if err == nil || !strings.Contains(err.Error(), "already has a rebase in progress") {
		t.Fatalf("expected rebase-in-progress refusal, got %v", err)
	}
}

func TestFinish_RefusesExistingMergeInProgress(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	info, err := Create(ctx, CreateOptions{Name: "feat", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatal(err)
	}

	mergeHead, err := runGitText(ctx, info.Path, "rev-parse", "--git-path", "MERGE_HEAD")
	if err != nil {
		t.Fatalf("rev-parse MERGE_HEAD: %v", err)
	}
	if err := os.WriteFile(resolveGitPath(info.Path, mergeHead), []byte("deadbeef\n"), 0o644); err != nil {
		t.Fatalf("write MERGE_HEAD: %v", err)
	}

	err = Finish(ctx, repo, "feat", FinishOptions{}, pl)
	if err == nil || !strings.Contains(err.Error(), "already has a merge in progress") {
		t.Fatalf("expected merge-in-progress refusal, got %v", err)
	}
}

func TestFinish_CheckDoesNotRequireCommitIdentity(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	info, err := Create(ctx, CreateOptions{Name: "feat", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(info.Path, "extra.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, info.Path, "add", "extra.txt")
	runTestGit(t, info.Path, "commit", "-q", "-m", "worktree commit")

	oldAuthorName, hadAuthorName := os.LookupEnv("GIT_AUTHOR_NAME")
	oldAuthorEmail, hadAuthorEmail := os.LookupEnv("GIT_AUTHOR_EMAIL")
	oldCommitterName, hadCommitterName := os.LookupEnv("GIT_COMMITTER_NAME")
	oldCommitterEmail, hadCommitterEmail := os.LookupEnv("GIT_COMMITTER_EMAIL")
	for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
	}
	defer func() {
		if hadAuthorName {
			_ = os.Setenv("GIT_AUTHOR_NAME", oldAuthorName)
		}
		if hadAuthorEmail {
			_ = os.Setenv("GIT_AUTHOR_EMAIL", oldAuthorEmail)
		}
		if hadCommitterName {
			_ = os.Setenv("GIT_COMMITTER_NAME", oldCommitterName)
		}
		if hadCommitterEmail {
			_ = os.Setenv("GIT_COMMITTER_EMAIL", oldCommitterEmail)
		}
	}()

	if err := Finish(ctx, repo, "feat", FinishOptions{Check: true}, pl); err != nil {
		t.Fatalf("Finish --check without identity: %v", err)
	}
}

func TestFinish_ReportsMissingCommitIdentity(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	info, err := Create(ctx, CreateOptions{Name: "feat", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(info.Path, "extra.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, info.Path, "add", "extra.txt")
	runTestGit(t, info.Path, "commit", "-q", "-m", "worktree commit")
	beforeHead := strings.TrimSpace(string(mustRunGit(t, info.Path, "rev-parse", "HEAD")))

	oldEnv := map[string]struct {
		value string
		ok    bool
	}{}
	for _, key := range []string{
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL",
		"HOME", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM",
		"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0",
	} {
		value, ok := os.LookupEnv(key)
		oldEnv[key] = struct {
			value string
			ok    bool
		}{value: value, ok: ok}
	}
	for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
	}
	if err := os.Setenv("HOME", t.TempDir()); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	if err := os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull); err != nil {
		t.Fatalf("set GIT_CONFIG_GLOBAL: %v", err)
	}
	if err := os.Setenv("GIT_CONFIG_SYSTEM", os.DevNull); err != nil {
		t.Fatalf("set GIT_CONFIG_SYSTEM: %v", err)
	}
	if err := os.Setenv("GIT_CONFIG_COUNT", "1"); err != nil {
		t.Fatalf("set GIT_CONFIG_COUNT: %v", err)
	}
	if err := os.Setenv("GIT_CONFIG_KEY_0", "user.useConfigOnly"); err != nil {
		t.Fatalf("set GIT_CONFIG_KEY_0: %v", err)
	}
	if err := os.Setenv("GIT_CONFIG_VALUE_0", "true"); err != nil {
		t.Fatalf("set GIT_CONFIG_VALUE_0: %v", err)
	}
	defer func() {
		for key, prev := range oldEnv {
			if prev.ok {
				_ = os.Setenv(key, prev.value)
			} else {
				_ = os.Unsetenv(key)
			}
		}
	}()

	err = Finish(ctx, repo, "feat", FinishOptions{}, pl)
	if err == nil {
		t.Fatal("expected missing identity error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "requires git author/committer identity") || !strings.Contains(msg, "user.name") || !strings.Contains(msg, "user.email") {
		t.Fatalf("unexpected missing identity error: %v", err)
	}
	afterHead := strings.TrimSpace(string(mustRunGit(t, info.Path, "rev-parse", "HEAD")))
	if beforeHead != afterHead {
		t.Fatalf("worktree HEAD changed despite missing identity: before=%s after=%s", beforeHead, afterHead)
	}
	if _, ok, merr := detectMergeInProgress(ctx, info.Path); merr != nil {
		t.Fatalf("detectMergeInProgress: %v", merr)
	} else if ok {
		t.Fatal("real worktree left in merge state despite missing identity")
	}
	status := strings.TrimSpace(string(mustRunGit(t, info.Path, "status", "--short")))
	if status != "" {
		t.Fatalf("real worktree became dirty despite missing identity: %s", status)
	}
}

func TestFinish_ReportsMissingCommitIdentity_WhenMergeCommitRequiredWithoutTreeDiff(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	info, err := Create(ctx, CreateOptions{Name: "feat", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatal(err)
	}

	// Make both branches end up with the same tree while keeping divergent history,
	// so finish still needs a merge commit before the squash phase.
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("main-updated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "shared.txt")
	runTestGit(t, repo, "commit", "-q", "-m", "main updates shared")
	if err := os.WriteFile(filepath.Join(info.Path, "shared.txt"), []byte("feat-updated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, info.Path, "add", "shared.txt")
	runTestGit(t, info.Path, "commit", "-q", "-m", "feat updates shared differently")
	if err := os.WriteFile(filepath.Join(info.Path, "shared.txt"), []byte("main-updated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, info.Path, "add", "shared.txt")
	runTestGit(t, info.Path, "commit", "-q", "-m", "feat aligns tree with main")
	mainTree := strings.TrimSpace(string(mustRunGit(t, repo, "rev-parse", "main^{tree}")))
	featTree := strings.TrimSpace(string(mustRunGit(t, repo, "rev-parse", "chord/feat^{tree}")))
	if mainTree != featTree {
		t.Fatalf("expected same trees before finish, got main=%s feat=%s", mainTree, featTree)
	}

	oldEnv := map[string]struct {
		value string
		ok    bool
	}{}
	for _, key := range []string{
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL",
		"HOME", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM",
		"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0",
	} {
		value, ok := os.LookupEnv(key)
		oldEnv[key] = struct {
			value string
			ok    bool
		}{value: value, ok: ok}
	}
	for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
	}
	if err := os.Setenv("HOME", t.TempDir()); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	if err := os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull); err != nil {
		t.Fatalf("set GIT_CONFIG_GLOBAL: %v", err)
	}
	if err := os.Setenv("GIT_CONFIG_SYSTEM", os.DevNull); err != nil {
		t.Fatalf("set GIT_CONFIG_SYSTEM: %v", err)
	}
	if err := os.Setenv("GIT_CONFIG_COUNT", "1"); err != nil {
		t.Fatalf("set GIT_CONFIG_COUNT: %v", err)
	}
	if err := os.Setenv("GIT_CONFIG_KEY_0", "user.useConfigOnly"); err != nil {
		t.Fatalf("set GIT_CONFIG_KEY_0: %v", err)
	}
	if err := os.Setenv("GIT_CONFIG_VALUE_0", "true"); err != nil {
		t.Fatalf("set GIT_CONFIG_VALUE_0: %v", err)
	}
	defer func() {
		for key, prev := range oldEnv {
			if prev.ok {
				_ = os.Setenv(key, prev.value)
			} else {
				_ = os.Unsetenv(key)
			}
		}
	}()

	err = Finish(ctx, repo, "feat", FinishOptions{}, pl)
	if err == nil {
		t.Fatal("expected missing identity error, got nil")
	}
	if !strings.Contains(err.Error(), "requires git author/committer identity") {
		t.Fatalf("unexpected missing identity error: %v", err)
	}
}

func TestFinish_NoNetTreeDiff_ReclaimsWithoutChangingMain(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	info, err := Create(ctx, CreateOptions{Name: "feat", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatal(err)
	}

	beforeMain := strings.TrimSpace(string(mustRunGit(t, repo, "rev-parse", "main")))
	runTestGit(t, info.Path, "commit", "--allow-empty", "-q", "-m", "empty history-only commit")

	if err := Finish(ctx, repo, "feat", FinishOptions{}, pl); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	afterMain := strings.TrimSpace(string(mustRunGit(t, repo, "rev-parse", "main")))
	if beforeMain != afterMain {
		t.Fatalf("main HEAD changed for same-tree finish: before=%s after=%s", beforeMain, afterMain)
	}
}

func TestFinish_RefusesDirtyWorktree(t *testing.T) {
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
	err = Finish(ctx, repo, "feat", FinishOptions{}, pl)
	if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("expected dirty refusal, got %v", err)
	}
	if _, err := os.Stat(info.Path); err != nil {
		t.Errorf("worktree dir missing after refused Finish: %v", err)
	}
}

func TestFinish_RefusesDirtyMainRepo(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	if _, err := Create(ctx, CreateOptions{Name: "feat", RepoRoot: repo, PathLocator: pl}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "dirty-main.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Finish(ctx, repo, "feat", FinishOptions{}, pl)
	if err == nil || !strings.Contains(err.Error(), "main repository has uncommitted changes") {
		t.Fatalf("expected dirty main refusal, got %v", err)
	}
}

func TestFinish_RefusesNilPathLocator(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()
	if _, err := Create(ctx, CreateOptions{Name: "feat", RepoRoot: repo, PathLocator: setupTestLocator(t)}); err != nil {
		t.Fatal(err)
	}
	err := Finish(ctx, repo, "feat", FinishOptions{}, nil)
	if err == nil || !strings.Contains(err.Error(), "finish worktree: nil PathLocator") {
		t.Fatalf("expected nil PathLocator error, got %v", err)
	}
}

func TestFinish_CheckAllowsNilPathLocator(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()
	if _, err := Create(ctx, CreateOptions{Name: "feat", RepoRoot: repo, PathLocator: setupTestLocator(t)}); err != nil {
		t.Fatal(err)
	}
	err := Finish(ctx, repo, "feat", FinishOptions{Check: true}, nil)
	if err != nil {
		t.Fatalf("expected check-only finish to allow nil PathLocator, got %v", err)
	}
}

func TestFinish_MergeConflictError_LeavesWorktreeForResolution(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	info, err := Create(ctx, CreateOptions{Name: "feat", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(repo, "clash.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "clash.txt")
	runTestGit(t, repo, "commit", "-q", "-m", "main adds clash")

	if err := os.WriteFile(filepath.Join(info.Path, "clash.txt"), []byte("worktree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, info.Path, "add", "clash.txt")
	runTestGit(t, info.Path, "commit", "-q", "-m", "worktree adds clash")

	err = Finish(ctx, repo, "feat", FinishOptions{}, pl)
	if err == nil {
		t.Fatalf("expected merge conflict error, got nil")
	}
	msg := err.Error()
	for _, want := range []string{
		"would conflict",
		"Conflicted files:",
		"clash.txt",
		"The target branch was left unchanged.",
		"git status",
		"chord worktree finish feat",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error message missing %q:\n%s", want, msg)
		}
	}
	if _, ok, merr := detectMergeInProgress(ctx, info.Path); merr != nil {
		t.Fatalf("detectMergeInProgress: %v", merr)
	} else if !ok {
		t.Fatalf("real worktree should be left in merge state for resolution")
	}
	if dir, ok, derr := detectRebaseInProgress(ctx, info.Path); derr != nil {
		t.Fatalf("detectRebaseInProgress: %v", derr)
	} else if ok {
		t.Fatalf("real worktree unexpectedly left in rebase state: %s", dir)
	}
	mainHead := strings.TrimSpace(string(mustRunGit(t, repo, "rev-parse", "main")))
	mergeHead := strings.TrimSpace(string(mustRunGit(t, repo, "rev-parse", "HEAD")))
	if mainHead != mergeHead {
		t.Fatalf("target branch moved despite conflict: main=%s head=%s", mainHead, mergeHead)
	}
}

func TestFinish_Check_SucceedsWithoutMutatingRealWorktree(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	info, err := Create(ctx, CreateOptions{Name: "feat", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("from main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "shared.txt")
	runTestGit(t, repo, "commit", "-q", "-m", "main adds shared")
	if err := os.WriteFile(filepath.Join(info.Path, "extra.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, info.Path, "add", "extra.txt")
	runTestGit(t, info.Path, "commit", "-q", "-m", "worktree commit")

	beforeHead := strings.TrimSpace(string(mustRunGit(t, info.Path, "rev-parse", "HEAD")))
	beforeMain := strings.TrimSpace(string(mustRunGit(t, repo, "rev-parse", "main")))
	if err := Finish(ctx, repo, "feat", FinishOptions{Check: true}, pl); err != nil {
		t.Fatalf("Finish --check: %v", err)
	}
	afterHead := strings.TrimSpace(string(mustRunGit(t, info.Path, "rev-parse", "HEAD")))
	if beforeHead != afterHead {
		t.Fatalf("worktree HEAD changed during check: before=%s after=%s", beforeHead, afterHead)
	}
	afterMain := strings.TrimSpace(string(mustRunGit(t, repo, "rev-parse", "main")))
	if beforeMain != afterMain {
		t.Fatalf("target branch HEAD changed during check: before=%s after=%s", beforeMain, afterMain)
	}
	if _, err := os.Stat(info.Path); err != nil {
		t.Fatalf("worktree missing after check: %v", err)
	}
	status := strings.TrimSpace(string(mustRunGit(t, info.Path, "status", "--short")))
	if status != "" {
		t.Fatalf("worktree became dirty after check: %s", status)
	}
	if _, ok, merr := detectMergeInProgress(ctx, info.Path); merr != nil {
		t.Fatalf("detectMergeInProgress: %v", merr)
	} else if ok {
		t.Fatalf("real worktree left in merge state after check")
	}
	branches := string(mustRunGit(t, repo, "branch", "--list", "chord/feat"))
	if !strings.Contains(branches, "chord/feat") {
		t.Fatalf("worktree branch missing after check: %s", branches)
	}
}

func TestFinish_NoNetDiff_ReclaimsWithoutChangingMain(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	info, err := Create(ctx, CreateOptions{Name: "feat", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatal(err)
	}

	beforeMain := strings.TrimSpace(string(mustRunGit(t, repo, "rev-parse", "main")))
	if err := os.WriteFile(filepath.Join(info.Path, "extra.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, info.Path, "add", "extra.txt")
	runTestGit(t, info.Path, "commit", "-q", "-m", "worktree adds extra")
	if err := os.Remove(filepath.Join(info.Path, "extra.txt")); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, info.Path, "add", "-A")
	runTestGit(t, info.Path, "commit", "-q", "-m", "worktree removes extra")

	if err := Finish(ctx, repo, "feat", FinishOptions{}, pl); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if _, err := os.Stat(info.Path); err == nil {
		t.Fatalf("worktree dir still exists after Finish")
	}
	branches := string(mustRunGit(t, repo, "branch", "--list", "chord/feat"))
	if strings.Contains(branches, "chord/feat") {
		t.Fatalf("branch chord/feat still present after Finish: %s", branches)
	}
	afterMain := strings.TrimSpace(string(mustRunGit(t, repo, "rev-parse", "main")))
	if beforeMain != afterMain {
		t.Fatalf("main HEAD changed for no-net-diff finish: before=%s after=%s", beforeMain, afterMain)
	}
}

func TestFinish_Check_ReportsConflictsWithoutLeavingRealWorktreeDirty(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	info, err := Create(ctx, CreateOptions{Name: "feat", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(repo, "clash.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "clash.txt")
	runTestGit(t, repo, "commit", "-q", "-m", "main adds clash")

	if err := os.WriteFile(filepath.Join(info.Path, "clash.txt"), []byte("worktree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, info.Path, "add", "clash.txt")
	runTestGit(t, info.Path, "commit", "-q", "-m", "worktree adds clash")

	err = Finish(ctx, repo, "feat", FinishOptions{Check: true}, pl)
	if err == nil {
		t.Fatalf("expected check conflict error, got nil")
	}
	msg := err.Error()
	for _, want := range []string{
		"would conflict",
		"Conflicted files:",
		"clash.txt",
		"No changes were made to the real worktree, branch, or target branch.",
		"git merge main",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("check error missing %q:\n%s", want, msg)
		}
	}
	if dir, ok, derr := detectRebaseInProgress(ctx, info.Path); derr != nil {
		t.Fatalf("detectRebaseInProgress: %v", derr)
	} else if ok {
		t.Fatalf("real worktree left in rebase state: %s", dir)
	}
	if _, ok, merr := detectMergeInProgress(ctx, info.Path); merr != nil {
		t.Fatalf("detectMergeInProgress: %v", merr)
	} else if ok {
		t.Fatalf("real worktree left in merge state after failed check")
	}
	status := strings.TrimSpace(string(mustRunGit(t, info.Path, "status", "--short")))
	if status != "" {
		t.Fatalf("real worktree became dirty after failed check: %s", status)
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

func TestNormalizeBranchPrefix(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"empty falls back to default", "", DefaultBranchPrefix, false},
		{"whitespace falls back to default", "   ", DefaultBranchPrefix, false},
		{"trailing slash kept", "myteam/", "myteam/", false},
		{"slash appended when missing", "myteam", "myteam/", false},
		{"nested namespace allowed", "agents/chord", "agents/chord/", false},
		{"leading slash rejected", "/foo", "", true},
		{"leading dash rejected", "-foo", "", true},
		{"dot-dot rejected", "foo..bar", "", true},
		{"double slash rejected", "foo//bar", "", true},
		{"whitespace inside rejected", "foo bar", "", true},
		{"reserved char rejected", "foo:bar", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeBranchPrefix(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got %q", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("NormalizeBranchPrefix(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCreate_RespectsCustomBranchPrefix(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	prefix, err := NormalizeBranchPrefix("myteam")
	if err != nil {
		t.Fatalf("NormalizeBranchPrefix: %v", err)
	}
	info, err := Create(ctx, CreateOptions{Name: "feat", RepoRoot: repo, PathLocator: pl, BranchPrefix: prefix})
	if err != nil {
		t.Fatalf("Create with custom prefix: %v", err)
	}
	if info.Branch != "myteam/feat" {
		t.Errorf("Branch = %q, want %q", info.Branch, "myteam/feat")
	}

	// List with the same prefix sees it; List with a different prefix
	// (e.g. the default) does not.
	got, err := List(ctx, repo, prefix)
	if err != nil {
		t.Fatalf("List custom: %v", err)
	}
	if len(got) != 1 || got[0].Name != "feat" || got[0].Branch != "myteam/feat" {
		t.Errorf("List with prefix %q = %+v, want one entry with Branch=myteam/feat", prefix, got)
	}
	defGot, err := List(ctx, repo, "")
	if err != nil {
		t.Fatalf("List default: %v", err)
	}
	if len(defGot) != 0 {
		t.Errorf("List with default prefix returned non-chord/* worktree: %+v", defGot)
	}

	// Remove must use the same prefix to find the worktree; using the
	// default prefix should fail with not-found.
	if err := Remove(ctx, repo, "feat", RemoveOptions{Force: true}, pl); err == nil {
		t.Errorf("Remove with default prefix unexpectedly succeeded for myteam/feat")
	}
	if err := Remove(ctx, repo, "feat", RemoveOptions{Force: true, BranchPrefix: prefix}, pl); err != nil {
		t.Errorf("Remove with matching prefix: %v", err)
	}
}
