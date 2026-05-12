package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/worktree"
)

// captureStdout swaps os.Stdout for the duration of fn and returns the
// captured bytes. Each command writes to os.Stdout directly via Cobra so
// this is the simplest way to assert table output.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	errCh := make(chan error, 1)
	go func() { errCh <- fn() }()

	go func() {
		<-errCh
		_ = w.Close()
	}()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	return buf.String(), nil
}

func TestWorktreeListCmd_EmptyMessage(t *testing.T) {
	repo := setupStartupRepo(t)
	withTestStateDir(t)
	chdirForTest(t, repo)

	cmd := newWorktreeListCmd()
	out, _ := captureStdout(t, func() error { return cmd.RunE(cmd, nil) })
	if !strings.Contains(out, "No chord-managed worktrees") {
		t.Errorf("expected empty message, got %q", out)
	}
}

func TestWorktreeListCmd_ShowsCreated(t *testing.T) {
	repo := setupStartupRepo(t)
	withTestStateDir(t)
	chdirForTest(t, repo)

	if _, err := prepareStartupWorktree(context.Background(), "alpha"); err != nil {
		t.Fatalf("prepareStartupWorktree alpha: %v", err)
	}
	chdirForTest(t, repo)
	if _, err := prepareStartupWorktree(context.Background(), "beta"); err != nil {
		t.Fatalf("prepareStartupWorktree beta: %v", err)
	}
	chdirForTest(t, repo)

	cmd := newWorktreeListCmd()
	out, _ := captureStdout(t, func() error { return cmd.RunE(cmd, nil) })
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
		t.Errorf("list missing entries: %q", out)
	}
	if !strings.Contains(out, "chord/alpha") || !strings.Contains(out, "chord/beta") {
		t.Errorf("list missing branches: %q", out)
	}
}

func TestWorktreeRemoveCmd_DefaultPreservesBranch(t *testing.T) {
	repo := setupStartupRepo(t)
	withTestStateDir(t)
	chdirForTest(t, repo)

	if _, err := prepareStartupWorktree(context.Background(), "alpha"); err != nil {
		t.Fatalf("prepareStartupWorktree: %v", err)
	}
	chdirForTest(t, repo)

	cmd := newWorktreeRemoveCmd()
	out, err := captureStdout(t, func() error { return cmd.RunE(cmd, []string{"alpha"}) })
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !strings.Contains(out, "Removed worktree alpha") {
		t.Errorf("missing remove confirmation: %q", out)
	}
	if !strings.Contains(out, "branch was kept") {
		t.Errorf("missing branch-preserved note: %q", out)
	}
	branches, _ := exec.Command("git", "-C", repo, "branch", "--list", "chord/alpha").CombinedOutput()
	if !strings.Contains(string(branches), "chord/alpha") {
		t.Errorf("branch chord/alpha removed by default; should be preserved")
	}
}

func TestWorktreeFinishCmd_CheckReportsCleanPreview(t *testing.T) {
	repo := setupStartupRepo(t)
	withTestStateDir(t)
	chdirForTest(t, repo)

	info, err := prepareStartupWorktree(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("prepareStartupWorktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(info.Path, "extra.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("write extra: %v", err)
	}
	runStartupGit(t, info.Path, "add", "extra.txt")
	runStartupGit(t, info.Path, "commit", "-q", "-m", "worktree commit")
	beforeHead := strings.TrimSpace(string(mustRunStartupGit(t, info.Path, "rev-parse", "HEAD")))

	chdirForTest(t, repo)
	cmd := newWorktreeFinishCmd()
	out, err := captureStdout(t, func() error {
		cmd.SetArgs([]string{"alpha", "--check", "--onto", "main"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("finish --check: %v", err)
	}
	if !strings.Contains(out, "Worktree alpha can finish cleanly into main") {
		t.Fatalf("unexpected output: %q", out)
	}
	afterHead := strings.TrimSpace(string(mustRunStartupGit(t, info.Path, "rev-parse", "HEAD")))
	if beforeHead != afterHead {
		t.Fatalf("worktree HEAD changed during check: before=%s after=%s", beforeHead, afterHead)
	}
}

func TestRunWorktreeSessionEntry_SetsWorktreeAndResumeFlags(t *testing.T) {
	repo := setupStartupRepo(t)
	withTestStateDir(t)
	chdirForTest(t, repo)

	prevContinue := flagContinueSession
	prevResume := flagResumeSession
	prevInfo := flagWorktreeStartupInfo
	prevMeta := flagWorktreeStartupMeta
	defer func() {
		flagContinueSession = prevContinue
		flagResumeSession = prevResume
		flagWorktreeStartupInfo = prevInfo
		flagWorktreeStartupMeta = prevMeta
	}()

	var gotContinue bool
	var gotResume string
	var gotInfo *worktree.Info
	var gotMetaName string
	var gotCwd string
	err := runWorktreeSessionEntry(&cobra.Command{}, "alpha", false, "sid-123", func(*cobra.Command, []string) error {
		gotContinue = flagContinueSession
		gotResume = flagResumeSession
		gotInfo = flagWorktreeStartupInfo
		if flagWorktreeStartupMeta != nil {
			gotMetaName = flagWorktreeStartupMeta.WorktreeName
		}
		cwd, cwdErr := os.Getwd()
		if cwdErr != nil {
			return cwdErr
		}
		gotCwd = cwd
		return nil
	})
	if err != nil {
		t.Fatalf("runWorktreeSessionEntry: %v", err)
	}
	if gotContinue {
		t.Fatal("flagContinueSession = true, want false")
	}
	if gotResume != "sid-123" {
		t.Fatalf("flagResumeSession = %q, want sid-123", gotResume)
	}
	if gotInfo == nil || gotInfo.Name != "alpha" {
		t.Fatalf("flagWorktreeStartupInfo = %+v, want worktree alpha", gotInfo)
	}
	if gotMetaName != "alpha" {
		t.Fatalf("flagWorktreeStartupMeta.WorktreeName = %q, want alpha", gotMetaName)
	}
	if !samePath(gotCwd, gotInfo.Path) {
		t.Fatalf("cwd = %q, want worktree path %q", gotCwd, gotInfo.Path)
	}
	if flagResumeSession != prevResume || flagContinueSession != prevContinue || flagWorktreeStartupInfo != prevInfo || flagWorktreeStartupMeta != prevMeta {
		t.Fatal("global startup flags were not restored after worktree entry run")
	}
}
