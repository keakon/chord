package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
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
