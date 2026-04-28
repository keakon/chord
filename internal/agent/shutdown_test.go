package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/hook"
)

type shutdownHookEngine struct {
	mu        sync.Mutex
	durations []time.Duration
	calls     int
}

func (e *shutdownHookEngine) Fire(ctx context.Context, env hook.Envelope) (*hook.Result, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	if env.Point == hook.OnSessionEnd {
		started := time.Now()
		<-ctx.Done()
		e.mu.Lock()
		e.durations = append(e.durations, time.Since(started))
		e.mu.Unlock()
		return &hook.Result{Action: hook.ActionContinue}, ctx.Err()
	}
	return &hook.Result{Action: hook.ActionContinue}, nil
}

func (e *shutdownHookEngine) FireBackground(context.Context, hook.Envelope) {}

func (e *shutdownHookEngine) RunAutomation(context.Context, hook.Envelope) ([]hook.AutomationJobResult, error) {
	return nil, nil
}

func (e *shutdownHookEngine) snapshot() (int, []time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := append([]time.Duration(nil), e.durations...)
	return e.calls, out
}

func TestShutdownBoundsSessionEndHookGrace(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	eng := &shutdownHookEngine{}
	a.hookEngine = eng

	if err := a.Shutdown(2 * time.Second); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	calls, durations := eng.snapshot()
	if calls != 1 {
		t.Fatalf("hook calls = %d, want 1", calls)
	}
	if len(durations) != 1 {
		t.Fatalf("durations len = %d, want 1", len(durations))
	}
	if durations[0] > sessionEndHookGrace+150*time.Millisecond {
		t.Fatalf("session_end hook exceeded grace: %v > %v", durations[0], sessionEndHookGrace)
	}
}

func TestShutdownUsesSharedBudgetAcrossStages(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	eng := &shutdownHookEngine{}
	a.hookEngine = eng
	if a.persistCh == nil {
		t.Fatal("expected persist channel")
	}

	a.started.Store(true)
	a.done = make(chan struct{})

	began := time.Now()
	err := a.Shutdown(350 * time.Millisecond)
	elapsed := time.Since(began)
	if err == nil {
		t.Fatal("Shutdown() error = nil, want timeout")
	}
	want := fmt.Sprintf("agent shutdown timed out after %v", 350*time.Millisecond)
	if err.Error() != want {
		t.Fatalf("Shutdown() error = %q, want %q", err.Error(), want)
	}
	if elapsed > 650*time.Millisecond {
		t.Fatalf("Shutdown exceeded shared budget too much: %v", elapsed)
	}
}
