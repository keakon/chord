package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
