package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/hook"
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

func TestCLIExitCodeAndPrintPolicy(t *testing.T) {
	baseErr := errors.New("boom")
	tests := []struct {
		name      string
		err       error
		wantCode  int
		wantPrint bool
	}{
		{name: "generic", err: baseErr, wantCode: 1, wantPrint: true},
		{name: "context canceled", err: context.Canceled, wantCode: 130, wantPrint: false},
		{name: "wrapped exit", err: cliExitError{code: 7, err: baseErr}, wantCode: 7, wantPrint: true},
		{name: "interrupt exit", err: cliExitError{code: 130, err: baseErr}, wantCode: 130, wantPrint: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cliExitCode(tt.err); got != tt.wantCode {
				t.Fatalf("cliExitCode() = %d, want %d", got, tt.wantCode)
			}
			if got := shouldPrintCLIError(tt.err); got != tt.wantPrint {
				t.Fatalf("shouldPrintCLIError() = %v, want %v", got, tt.wantPrint)
			}
		})
	}
	if shouldPrintCLIError(nil) {
		t.Fatal("nil error should not print")
	}
	if got := (cliExitError{code: 5}).Error(); got != "exit code 5" {
		t.Fatalf("cliExitError nil wrapped error string = %q", got)
	}
	if !errors.Is(cliExitError{code: 9, err: baseErr}, baseErr) {
		t.Fatal("cliExitError should unwrap the underlying error")
	}
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

func TestHookDefsFromConfigFlattensEntries(t *testing.T) {
	hc := config.HookConfig{
		OnToolCall: []config.HookEntry{
			{
				Command:         config.HookCommand{Shell: "echo tool"},
				Timeout:         3,
				Tools:           []string{"Read"},
				Paths:           []string{"*.go"},
				Agents:          []string{"planner"},
				AgentKinds:      []string{"main"},
				Models:          []string{"provider/model-1"},
				MinChangedFiles: 1,
				OnlyOnError:     true,
				Join:            "all",
				Result:          hook.ResultAlwaysAppend,
				ResultFormat:    hook.ResultFormatTail,
				MaxResultLines:  7,
				MaxResultBytes:  1024,
				DebounceMS:      50,
				Concurrency:     "serial",
				RetryOnFailure:  2,
				RetryDelayMS:    100,
				Environment:     map[string]string{"SAMPLE": "1"},
			},
		},
		OnIdle: []config.HookEntry{
			{},
			{Name: "idle-hook", Command: config.HookCommand{Args: []string{"echo", "idle"}}},
		},
	}

	defs := hookDefsFromConfig(hc)
	if len(defs) != 2 {
		t.Fatalf("len(hookDefsFromConfig) = %d, want 2", len(defs))
	}
	if defs[0].Name != hook.OnToolCall+"-0" || defs[0].Point != hook.OnToolCall {
		t.Fatalf("first hook def = %#v", defs[0])
	}
	if defs[0].Command.Shell != "echo tool" || defs[0].Timeout != 3 || defs[0].Result != hook.ResultAlwaysAppend {
		t.Fatalf("first hook def did not preserve scalar fields: %#v", defs[0])
	}
	if defs[0].Tools[0] != "Read" || defs[0].Paths[0] != "*.go" || defs[0].Environment["SAMPLE"] != "1" {
		t.Fatalf("first hook def did not preserve filters/env: %#v", defs[0])
	}
	if defs[1].Name != "idle-hook" || defs[1].Point != hook.OnIdle || len(defs[1].Command.Args) != 2 {
		t.Fatalf("second hook def = %#v", defs[1])
	}
	hc.OnIdle[1].Command.Args[0] = "changed"
	if defs[1].Command.Args[0] != "echo" {
		t.Fatal("hookDefsFromConfig should copy command args")
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
