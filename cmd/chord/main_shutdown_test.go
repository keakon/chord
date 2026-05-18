package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/worktree"
)

type shutdownTrackingAgent struct {
	cancelResult bool
	cancelCalls  int
}

func (a *shutdownTrackingAgent) CancelCurrentTurn() bool {
	a.cancelCalls++
	return a.cancelResult
}

func TestShutdownLocalRuntimeWaitsForIdleOnlyWhenCancelSucceeds(t *testing.T) {
	agent := &shutdownTrackingAgent{cancelResult: true}
	rt := &Runtime{Agent: nil}
	ac := &AppContext{}

	waitCalled := false
	closeCalled := false
	appCloseCalled := false

	shutdownLocalRuntimeForTest(
		ac,
		rt,
		localExitIdleWait,
		false,
		func() bool { return agent.CancelCurrentTurn() },
		func(time.Duration) bool {
			waitCalled = true
			return true
		},
		func() { closeCalled = true },
		func() { appCloseCalled = true },
	)

	if agent.cancelCalls != 1 {
		t.Fatalf("cancelCalls = %d, want 1", agent.cancelCalls)
	}
	if !waitCalled {
		t.Fatal("expected WaitIdleOrTimeout hook to be called")
	}
	if !closeCalled {
		t.Fatal("expected runtime close hook to be called")
	}
	if !appCloseCalled {
		t.Fatal("expected app close hook to be called")
	}
}

func TestShutdownLocalRuntimeSkipsIdleWaitWhenAlreadyIdle(t *testing.T) {
	agent := &shutdownTrackingAgent{cancelResult: false}
	rt := &Runtime{Agent: nil}
	ac := &AppContext{}

	waitCalled := false
	closeCalled := false
	appCloseCalled := false

	shutdownLocalRuntimeForTest(
		ac,
		rt,
		localExitIdleWait,
		false,
		func() bool { return agent.CancelCurrentTurn() },
		func(time.Duration) bool {
			waitCalled = true
			return true
		},
		func() { closeCalled = true },
		func() { appCloseCalled = true },
	)

	if agent.cancelCalls != 1 {
		t.Fatalf("cancelCalls = %d, want 1", agent.cancelCalls)
	}
	if waitCalled {
		t.Fatal("did not expect WaitIdleOrTimeout hook when cancel returned false")
	}
	if !closeCalled {
		t.Fatal("expected runtime close hook to be called")
	}
	if !appCloseCalled {
		t.Fatal("expected app close hook to be called")
	}
}

func TestShutdownLocalRuntimeSkipsCancelWhenDoneCompletedExpectedClose(t *testing.T) {
	agent := &shutdownTrackingAgent{cancelResult: true}
	rt := &Runtime{Agent: nil}
	ac := &AppContext{}

	waitCalled := false
	closeCalled := false
	appCloseCalled := false

	shutdownLocalRuntimeForTest(
		ac,
		rt,
		localExitIdleWait,
		true,
		func() bool { return agent.CancelCurrentTurn() },
		func(time.Duration) bool {
			waitCalled = true
			return true
		},
		func() { closeCalled = true },
		func() { appCloseCalled = true },
	)

	if agent.cancelCalls != 0 {
		t.Fatalf("cancelCalls = %d, want 0", agent.cancelCalls)
	}
	if waitCalled {
		t.Fatal("did not expect WaitIdleOrTimeout hook when skipping cancel")
	}
	if !closeCalled {
		t.Fatal("expected runtime close hook to be called")
	}
	if !appCloseCalled {
		t.Fatal("expected app close hook to be called")
	}
}

func TestResumeHintCommand_DefaultProject(t *testing.T) {
	got := resumeHintCommand(nil, "sid-123")
	if got != "chord --resume sid-123" {
		t.Fatalf("resumeHintCommand() = %q, want %q", got, "chord --resume sid-123")
	}
}

func TestResumeHintCommand_UsesActiveWorktreeFlag(t *testing.T) {
	prev := flagWorktreeStartupInfo
	flagWorktreeStartupInfo = &worktree.Info{Name: "feat-auth"}
	defer func() { flagWorktreeStartupInfo = prev }()

	got := resumeHintCommand(nil, "sid-123")
	if got != "chord worktree feat-auth --resume sid-123" {
		t.Fatalf("resumeHintCommand() = %q, want %q", got, "chord worktree feat-auth --resume sid-123")
	}
}

func captureStderr(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	err = fn()
	_ = w.Close()

	var buf bytes.Buffer
	if _, readErr := buf.ReadFrom(r); readErr != nil {
		t.Fatalf("read stderr: %v", readErr)
	}
	return buf.String(), err
}

func TestWorktreeResumeCommand_ReservedNameFallsBackToFlagForm(t *testing.T) {
	got := worktreeResumeCommand("list", "sid-123")
	if got != "chord --worktree list --resume sid-123" {
		t.Fatalf("worktreeResumeCommand() = %q, want %q", got, "chord --worktree list --resume sid-123")
	}
}

func TestResumeHintCommand_DetectsWorktreeFromProjectRoot(t *testing.T) {
	repo := setupStartupRepo(t)
	withTestStateDir(t)
	chdirForTest(t, repo)

	var info *worktree.Info
	worktreeOutput, err := captureStderr(t, func() error {
		var createErr error
		info, createErr = prepareStartupWorktree(context.Background(), "feat-auth")
		return createErr
	})
	if err != nil {
		t.Fatalf("prepareStartupWorktree: %v", err)
	}
	if !strings.Contains(worktreeOutput, "Created worktree feat-auth") {
		t.Fatalf("worktree startup output = %q, want created worktree summary", worktreeOutput)
	}
	prev := flagWorktreeStartupInfo
	flagWorktreeStartupInfo = nil
	defer func() { flagWorktreeStartupInfo = prev }()

	pl, err := startupPathLocator()
	if err != nil {
		t.Fatalf("startupPathLocator: %v", err)
	}
	proj, err := pl.LocateProject(info.Path)
	if err != nil {
		t.Fatalf("LocateProject: %v", err)
	}
	ac := &AppContext{
		Ctx:            context.Background(),
		ProjectRoot:    info.Path,
		PathLocator:    pl,
		ProjectLocator: proj,
	}

	got := resumeHintCommand(ac, "sid-123")
	if got != "chord worktree feat-auth --resume sid-123" {
		t.Fatalf("resumeHintCommand() = %q, want %q", got, "chord worktree feat-auth --resume sid-123")
	}
}

func TestPrintResumeHint_PrintsResolvedCommand(t *testing.T) {
	prev := flagWorktreeStartupInfo
	flagWorktreeStartupInfo = &worktree.Info{Name: "feat-auth"}
	defer func() { flagWorktreeStartupInfo = prev }()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	printResumeHint(nil, "sid-123")
	_ = w.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "chord worktree feat-auth --resume sid-123") {
		t.Fatalf("stderr = %q, want worktree resume command", got)
	}
}

func TestWorktreeShortFlagAliases(t *testing.T) {
	var rootWorktree string
	rootCmd := &cobra.Command{RunE: func(*cobra.Command, []string) error { return nil }}
	rootCmd.Flags().StringVarP(&rootWorktree, "worktree", "w", "", "")
	rootCmd.Flags().Lookup("worktree").NoOptDefVal = ""
	rootCmd.SetArgs([]string{"-w", "feat-auth"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("root -w execute: %v", err)
	}
	if rootWorktree != "feat-auth" {
		t.Fatalf("root -w parsed value = %q, want feat-auth", rootWorktree)
	}

	cmd := newHeadlessCmd()
	f := cmd.Flags().Lookup("worktree")
	if f == nil {
		t.Fatal("headless worktree flag not found")
	}
	if f.Shorthand != "w" {
		t.Fatalf("headless worktree shorthand = %q, want w", f.Shorthand)
	}
}

func TestSessionDirHasMessages(t *testing.T) {
	dir := t.TempDir()
	if sessionDirHasMessages(dir) {
		t.Fatal("sessionDirHasMessages should be false when main.jsonl is missing")
	}

	mainPath := filepath.Join(dir, "main.jsonl")
	if err := os.WriteFile(mainPath, nil, 0o600); err != nil {
		t.Fatalf("write empty main.jsonl: %v", err)
	}
	if sessionDirHasMessages(dir) {
		t.Fatal("sessionDirHasMessages should be false for empty main.jsonl")
	}

	if err := os.WriteFile(mainPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write non-empty main.jsonl: %v", err)
	}
	if !sessionDirHasMessages(dir) {
		t.Fatal("sessionDirHasMessages should be true for non-empty main.jsonl")
	}
}
