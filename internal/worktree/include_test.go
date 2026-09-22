package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
)

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Errorf("%s = %q, want %q", path, data, want)
	}
}

func assertFileMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s exists, want it absent (err=%v)", path, err)
	}
}

func TestWorktreeRoot(t *testing.T) {
	pl := setupTestLocator(t)
	mainRoot := t.TempDir()

	got, err := WorktreeRoot(pl, mainRoot, "")
	if err != nil {
		t.Fatalf("WorktreeRoot(default): %v", err)
	}
	if want := filepath.Join(pl.StateDir, "worktrees", RepoIDFor(mainRoot)); got != want {
		t.Errorf("WorktreeRoot(default) = %s, want %s", got, want)
	}

	got, err = WorktreeRoot(pl, mainRoot, ".chord/worktrees")
	if err != nil {
		t.Fatalf("WorktreeRoot(relative): %v", err)
	}
	if want := filepath.Join(mainRoot, ".chord", "worktrees"); got != want {
		t.Errorf("WorktreeRoot(relative) = %s, want %s", got, want)
	}

	abs := filepath.Join(t.TempDir(), "custom")
	got, err = WorktreeRoot(pl, mainRoot, abs)
	if err != nil {
		t.Fatalf("WorktreeRoot(absolute): %v", err)
	}
	if got != abs {
		t.Errorf("WorktreeRoot(absolute) = %s, want %s", got, abs)
	}

	if _, err := WorktreeRoot(nil, mainRoot, ""); err == nil {
		t.Error("WorktreeRoot(nil locator) succeeded, want an error")
	}
}

// TestCreateConfiguredRootKeepsMainCheckoutClean places a worktree inside the
// repository (D2's opt-in root) and checks both the layout and the self-ignoring
// .gitignore that keeps the main checkout clean.
func TestCreateConfiguredRootKeepsMainCheckoutClean(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)

	info, err := Create(context.Background(), CreateOptions{
		Name:         "feat-root",
		RepoRoot:     repo,
		PathLocator:  pl,
		BranchPrefix: DefaultBranchPrefix,
		Root:         ".chord/worktrees",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	wantContainer := filepath.Join(repo, ".chord", "worktrees")
	wantPath := filepath.Join(wantContainer, "feat-root")
	if canonical, cerr := config.CanonicalProjectRoot(wantPath); cerr == nil {
		wantPath = canonical
	}
	if info.Path != wantPath {
		t.Fatalf("worktree path = %s, want %s", info.Path, wantPath)
	}
	assertFileContent(t, filepath.Join(wantContainer, ".gitignore"), "*\n")

	status := strings.TrimSpace(string(mustRunGit(t, repo, "status", "--porcelain", "-uall")))
	if status != "" {
		t.Fatalf("main checkout is not clean after create:\n%s", status)
	}
}

// TestCreateKeepsUserGitignoreInContainerRoot guards the "never overwrite what
// the user placed there" rule of the self-ignoring .gitignore.
func TestCreateKeepsUserGitignoreInContainerRoot(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	container := filepath.Join(repo, ".chord", "worktrees")
	mustWriteFile(t, filepath.Join(container, ".gitignore"), "!keep\n")

	if _, err := Create(context.Background(), CreateOptions{
		Name:         "feat-keep",
		RepoRoot:     repo,
		PathLocator:  pl,
		BranchPrefix: DefaultBranchPrefix,
		Root:         ".chord/worktrees",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	assertFileContent(t, filepath.Join(container, ".gitignore"), "!keep\n")
}

// TestCreateDefaultRootOutsideRepoSkipsGitignore pins that the historical
// state-dir layout is untouched by the self-ignoring .gitignore.
func TestCreateDefaultRootOutsideRepoSkipsGitignore(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)

	info, err := Create(context.Background(), CreateOptions{
		Name:         "feat-outside",
		RepoRoot:     repo,
		PathLocator:  pl,
		BranchPrefix: DefaultBranchPrefix,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	stateRoot := pl.StateDir
	if canonical, cerr := config.CanonicalProjectRoot(stateRoot); cerr == nil {
		stateRoot = canonical
	}
	if !strings.HasPrefix(info.Path, filepath.Join(stateRoot, "worktrees")) {
		t.Fatalf("worktree path = %s, want it under the state dir %s", info.Path, stateRoot)
	}
	assertFileMissing(t, filepath.Join(filepath.Dir(info.Path), ".gitignore"))
}

// TestCopyWorktreeIncludeFilesDefaultsToEnv covers the default include set:
// only gitignored .env* files are copied.
func TestCopyWorktreeIncludeFilesDefaultsToEnv(t *testing.T) {
	repo := setupTestRepo(t)
	mustWriteFile(t, filepath.Join(repo, ".gitignore"), ".env*\nsecret.txt\n")
	mustWriteFile(t, filepath.Join(repo, ".env"), "TOKEN=1\n")
	mustWriteFile(t, filepath.Join(repo, ".env.local"), "TOKEN=2\n")
	mustWriteFile(t, filepath.Join(repo, "secret.txt"), "s\n")
	mustWriteFile(t, filepath.Join(repo, "untracked.txt"), "u\n")
	dst := setupTestRepo(t)

	if err := copyWorktreeIncludeFiles(context.Background(), repo, dst); err != nil {
		t.Fatalf("copyWorktreeIncludeFiles: %v", err)
	}
	assertFileContent(t, filepath.Join(dst, ".env"), "TOKEN=1\n")
	assertFileContent(t, filepath.Join(dst, ".env.local"), "TOKEN=2\n")
	assertFileMissing(t, filepath.Join(dst, "secret.txt"))
	assertFileMissing(t, filepath.Join(dst, "untracked.txt"))
}

// TestCopyWorktreeIncludeFilesUsesCustomPatterns covers the declared include
// list replacing the default, including a nested path.
func TestCopyWorktreeIncludeFilesUsesCustomPatterns(t *testing.T) {
	repo := setupTestRepo(t)
	mustWriteFile(t, filepath.Join(repo, ".gitignore"), ".env*\nconfig/local.ini\n")
	mustWriteFile(t, filepath.Join(repo, WorktreeIncludeFile), "# local overrides\nconfig/local.ini\n")
	mustWriteFile(t, filepath.Join(repo, ".env"), "TOKEN=1\n")
	mustWriteFile(t, filepath.Join(repo, "config", "local.ini"), "[x]\n")
	dst := setupTestRepo(t)

	if err := copyWorktreeIncludeFiles(context.Background(), repo, dst); err != nil {
		t.Fatalf("copyWorktreeIncludeFiles: %v", err)
	}
	assertFileContent(t, filepath.Join(dst, "config", "local.ini"), "[x]\n")
	assertFileMissing(t, filepath.Join(dst, ".env"))
}

// TestCopyWorktreeIncludeFilesKeepsTrackedFiles covers the rule that a file
// tracked in the worktree is never overwritten by its gitignored twin.
func TestCopyWorktreeIncludeFilesKeepsTrackedFiles(t *testing.T) {
	repo := setupTestRepo(t)
	mustWriteFile(t, filepath.Join(repo, ".gitignore"), "secret.txt\n")
	mustWriteFile(t, filepath.Join(repo, WorktreeIncludeFile), "secret.txt\n")
	mustWriteFile(t, filepath.Join(repo, "secret.txt"), "main\n")
	dst := setupTestRepo(t)
	mustWriteFile(t, filepath.Join(dst, "secret.txt"), "worktree\n")
	runTestGit(t, dst, "add", "secret.txt")
	runTestGit(t, dst, "commit", "-q", "-m", "track secret")

	if err := copyWorktreeIncludeFiles(context.Background(), repo, dst); err != nil {
		t.Fatalf("copyWorktreeIncludeFiles: %v", err)
	}
	assertFileContent(t, filepath.Join(dst, "secret.txt"), "worktree\n")
}

// TestCopyWorktreeIncludeFilesMissingFileIsFine pins that a repository without
// the configured files copies nothing and does not error.
func TestCopyWorktreeIncludeFilesMissingFileIsFine(t *testing.T) {
	repo := setupTestRepo(t)
	dst := setupTestRepo(t)
	if err := copyWorktreeIncludeFiles(context.Background(), repo, dst); err != nil {
		t.Fatalf("copyWorktreeIncludeFiles: %v", err)
	}
}
