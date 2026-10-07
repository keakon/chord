package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestShutdownBeforeRunRejectsDeferredStartup(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	start := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		<-start
		result <- a.Run(context.Background())
	}()
	if err := a.Shutdown(2 * time.Second); err != nil {
		close(start)
		t.Fatal(err)
	}
	close(start)
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) || a.started.Load() {
			t.Fatalf("late Run = %v, started = %v", err, a.started.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("late Run did not reject startup")
	}
}

func TestShutdownWaitsForAdmittedQuestionInitialization(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	// Hold the exact boundary between startup admission and Question restore.
	if err := a.admitRun(); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- a.Shutdown(2 * time.Second) }()
	select {
	case <-a.stoppingCh:
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not begin")
	}
	select {
	case err := <-result:
		t.Fatalf("Shutdown inspected loop state before startup finished: %v", err)
	default:
	}
	// This write must precede Shutdown's persistQuestionShutdown read. Without
	// the admission gate, Shutdown can read it before the delayed Run starts.
	a.restoreQuestions(a.ctxMgr.Snapshot())
	a.stopQuestionWork()
	close(a.done)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown did not finish after loop exit")
	}
}

func TestRunAdmissionRejectsDuplicateLoop(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	if err := a.admitRun(); err != nil {
		t.Fatal(err)
	}
	if err := a.Run(context.Background()); err == nil {
		t.Fatal("duplicate Run was admitted")
	}
	close(a.done)
}
