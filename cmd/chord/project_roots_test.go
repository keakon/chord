package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/worktree"
)

// TestResolvePathRootsListsCheckoutsAndContainer covers the policy-root wiring:
// the resolver reports the main checkout, every chord-managed worktree, and the
// container directory whose immediate children are worktrees.
func TestResolvePathRootsListsCheckoutsAndContainer(t *testing.T) {
	repo := setupStartupRepo(t)
	withTestStateDir(t)
	chdirForTest(t, repo)
	writeStartupTestConfig(t, "paths:\n  state_dir: "+flagStateDir+"\n  cache_dir: "+flagCacheDir+"\n  logs_dir: "+flagLogsDir+"\n  sessions_dir: "+flagSessionsDir+"\n")
	info := prepareStartupWorktreeForTest(t, context.Background(), "feat-roots")

	pl, err := startupPathLocator()
	if err != nil {
		t.Fatalf("startupPathLocator: %v", err)
	}
	roots, containers := resolvePathRoots(context.Background(), repo, pl, "")

	wantContainer := filepath.Join(pl.StateDir, "worktrees", worktree.RepoIDFor(repo))
	if len(containers) != 1 || containers[0] != wantContainer {
		t.Errorf("containers = %v, want [%s]", containers, wantContainer)
	}
	seen := make(map[string]bool, len(roots))
	for _, r := range roots {
		seen[r] = true
	}
	if !seen[repo] {
		t.Errorf("roots = %v, want the main checkout %s", roots, repo)
	}
	if !seen[info.Path] {
		t.Errorf("roots = %v, want the linked worktree %s", roots, info.Path)
	}
	for i := 1; i < len(roots); i++ {
		if len(roots[i-1]) < len(roots[i]) {
			t.Fatalf("roots not ordered longest-first: %v", roots)
		}
	}
}

// TestResolvePathRootsCoversCheckoutsChordDidNotCreate pins the scope: the
// policy-root set is every checkout of the repository, not only the worktrees
// chord manages. A hand-made worktree whose branch lacks the chord prefix must
// still be a root, otherwise a repository-relative rule would match its `src/**`
// in the main checkout and silently stop matching in that one — the
// cwd-dependent drift the merged-roots design exists to remove.
func TestResolvePathRootsCoversCheckoutsChordDidNotCreate(t *testing.T) {
	repo := setupStartupRepo(t)
	withTestStateDir(t)
	chdirForTest(t, repo)
	writeStartupTestConfig(t, "paths:\n  state_dir: "+flagStateDir+"\n  cache_dir: "+flagCacheDir+"\n  logs_dir: "+flagLogsDir+"\n  sessions_dir: "+flagSessionsDir+"\n")

	// A worktree created the way a user would: no chord branch prefix, so
	// worktree.List filters it out of the management view.
	manualDir := filepath.Join(t.TempDir(), "manual")
	runStartupGit(t, repo, "worktree", "add", "-q", "-b", "user-branch", manualDir)
	manual, err := config.CanonicalProjectRoot(manualDir)
	if err != nil {
		t.Fatalf("canonical manual worktree: %v", err)
	}

	// Guard the fixture: if this worktree ever became chord-managed the test
	// would stop exercising the case it exists for.
	managed, err := worktree.List(context.Background(), repo, worktree.DefaultBranchPrefix)
	if err != nil {
		t.Fatalf("worktree.List: %v", err)
	}
	for _, info := range managed {
		if info.Path == manual {
			t.Fatalf("fixture is wrong: %s is reported as chord-managed", manual)
		}
	}

	pl, err := startupPathLocator()
	if err != nil {
		t.Fatalf("startupPathLocator: %v", err)
	}
	roots, _ := resolvePathRoots(context.Background(), repo, pl, "")
	seen := false
	for _, r := range roots {
		if r == manual {
			seen = true
		}
	}
	if !seen {
		t.Errorf("roots = %v, want the hand-made worktree %s", roots, manual)
	}
}

func TestResolveContentRootEmptyWorkDir(t *testing.T) {
	if got := resolveContentRoot(context.Background(), ""); got != "" {
		t.Fatalf("resolveContentRoot(%q) = %q, want empty", "", got)
	}
}

func TestResolveContentRootOutsideGitReturnsWorkDir(t *testing.T) {
	dir := t.TempDir()
	if got := resolveContentRoot(context.Background(), dir); got != dir {
		t.Fatalf("resolveContentRoot(%q) = %q, want the workDir itself", dir, got)
	}
}

func TestResolveContentRootMainWorktreeKeepsWorkDir(t *testing.T) {
	repo := setupStartupRepo(t)
	sub := filepath.Join(repo, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	// A subdirectory of the main worktree is its own content root: sessions
	// started there keep the existing per-directory project key.
	if got := resolveContentRoot(context.Background(), sub); got != sub {
		t.Fatalf("resolveContentRoot(%q) = %q, want %q", sub, got, sub)
	}
}

func TestResolveContentRootLinkedWorktreeUsesMainRoot(t *testing.T) {
	repo := setupStartupRepo(t)
	withTestStateDir(t)
	chdirForTest(t, repo)

	// prepareStartupWorktreeForTest leaves the process inside the new worktree.
	info := prepareStartupWorktreeForTest(t, context.Background(), "feat-a")
	sub := filepath.Join(info.Path, "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	// Every checkout of one repository shares a content root, so a linked
	// worktree resolves to the main worktree.
	if got := resolveContentRoot(context.Background(), sub); got != repo {
		t.Fatalf("resolveContentRoot(%q) = %q, want the main worktree root %q", sub, got, repo)
	}
}

func TestResolveContentRootBareRepositoryUsesWorkDir(t *testing.T) {
	bare := t.TempDir()
	runStartupGit(t, bare, "init", "-q", "--bare")

	// `chord init` in a bare repository has no worktree to fall back to.
	if got := resolveContentRoot(context.Background(), bare); got != bare {
		t.Fatalf("resolveContentRoot(%q) = %q, want the workDir itself", bare, got)
	}
}

// TestStartupWorktreeConfigUsesProjectRootOverride covers the worktree.root knob
// being read from the project config while the global branch prefix survives.
func TestStartupWorktreeConfigUsesProjectRootOverride(t *testing.T) {
	setProjectTrustFlagsForTest(t, false, true)
	withTestStateDir(t)
	repo := setupStartupRepo(t)
	writeStartupTestConfig(t, "worktree:\n  branch_prefix: global/\n  root: global-wt\n")
	if err := os.MkdirAll(filepath.Join(repo, ".chord"), 0o755); err != nil {
		t.Fatalf("mkdir project .chord: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".chord", "config.yaml"), []byte("worktree:\n  root: .chord/worktrees\n"), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	chdirForTest(t, repo)

	wc, _, err := startupWorktreeConfig("local")
	if err != nil {
		t.Fatalf("startupWorktreeConfig: %v", err)
	}
	if wc.Root != ".chord/worktrees" {
		t.Errorf("worktree.root = %q, want the project override", wc.Root)
	}
	if wc.BranchPrefix != "global/" {
		t.Errorf("worktree.branch_prefix = %q, want the global value to survive the merge", wc.BranchPrefix)
	}
}

func TestPrepareStartupWorktreeAppliesLowRiskProjectRoot(t *testing.T) {
	setProjectTrustFlagsForTest(t, false, false)
	withTestStateDir(t)
	repo := setupStartupRepo(t)
	writeStartupTestConfig(t, "worktree:\n  root: global-wt\n")
	if err := os.MkdirAll(filepath.Join(repo, ".chord"), 0o755); err != nil {
		t.Fatalf("mkdir project .chord: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".chord", "config.yaml"), []byte("worktree:\n  root: project-wt\n"), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	chdirForTest(t, repo)

	info, decision, err := prepareStartupWorktree(context.Background(), "feature", false, "headless")
	if err != nil {
		t.Fatal(err)
	}
	if decision == nil || !decision.Plan.LowRisk || decision.Plan.Authorized {
		t.Fatalf("worktree-only delta should be low risk: %+v", decision)
	}
	if info.Path != filepath.Join(repo, "project-wt", "feature") {
		t.Fatalf("worktree path=%q", info.Path)
	}
}

func TestPrepareStartupWorktreeRejectsUnauthorizedHighRiskLayer(t *testing.T) {
	setProjectTrustFlagsForTest(t, false, false)
	withTestStateDir(t)
	repo := setupStartupRepo(t)
	writeStartupTestConfig(t, "worktree:\n  root: global-wt\n")
	writeProjectConfigForTrustTest(t, repo, "worktree:\n  root: project-wt\nmcp:\n  sample:\n    command: sample-server\n")
	chdirForTest(t, repo)

	if _, _, err := prepareStartupWorktree(context.Background(), "feature", false, "headless"); err == nil || !strings.Contains(err.Error(), "project") {
		t.Fatalf("prepareStartupWorktree error=%v, want project trust rejection", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "project-wt")); !os.IsNotExist(err) {
		t.Fatalf("high-risk project layer created a worktree: %v", err)
	}
}

// TestResolvePathRootsFollowsConfiguredRoot pins the container rule to the
// configured location: a worktree created inside the repository must be covered
// by the container the resolver reports.
func TestResolvePathRootsFollowsConfiguredRoot(t *testing.T) {
	repo := setupStartupRepo(t)
	withTestStateDir(t)
	writeStartupTestConfig(t, "paths:\n  state_dir: "+flagStateDir+"\n  cache_dir: "+flagCacheDir+"\n  logs_dir: "+flagLogsDir+"\n  sessions_dir: "+flagSessionsDir+"\nworktree:\n  root: .chord/worktrees\n")
	chdirForTest(t, repo)

	info := prepareStartupWorktreeForTest(t, context.Background(), "feat-inrepo")
	wantContainer := filepath.Join(repo, ".chord", "worktrees")
	wantPath := filepath.Join(wantContainer, "feat-inrepo")
	if info.Path != wantPath {
		t.Fatalf("worktree path = %s, want %s", info.Path, wantPath)
	}

	pl, err := startupPathLocator()
	if err != nil {
		t.Fatalf("startupPathLocator: %v", err)
	}
	roots, containers := resolvePathRoots(context.Background(), repo, pl, ".chord/worktrees")
	if len(containers) != 1 || containers[0] != wantContainer {
		t.Errorf("containers = %v, want [%s]", containers, wantContainer)
	}
	seen := false
	for _, r := range roots {
		if r == info.Path {
			seen = true
		}
	}
	if !seen {
		t.Errorf("roots = %v, want the in-repo worktree %s", roots, info.Path)
	}
}
