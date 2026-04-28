package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

type shutdownHookEngine struct {
	mu        sync.Mutex
	durations []time.Duration
	calls     int
}

type shutdownBlockingProvider struct {
	started   chan struct{}
	release   chan struct{}
	startOnce sync.Once
}

func (p *shutdownBlockingProvider) CompleteStream(
	ctx context.Context,
	_ string,
	_ string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	_ int,
	_ llm.RequestTuning,
	cb llm.StreamCallback,
) (*message.Response, error) {
	p.startOnce.Do(func() { close(p.started) })
	if cb != nil {
		cb(message.StreamDelta{Progress: &message.StreamProgressDelta{Bytes: 1, Events: 1}})
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.release:
		return &message.Response{}, nil
	}
}

func (p *shutdownBlockingProvider) Complete(
	ctx context.Context,
	apiKey string,
	model string,
	systemPrompt string,
	messages []message.Message,
	tools []message.ToolDefinition,
	maxTokens int,
	tuning llm.RequestTuning,
) (*message.Response, error) {
	return p.CompleteStream(ctx, apiKey, model, systemPrompt, messages, tools, maxTokens, tuning, nil)
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

func TestShutdownWaitsForMainLLMEmittersBeforeClosingOutput(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()

	providerCfg := llm.NewProviderConfig("test-provider", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"test-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"test-key"})
	provider := &shutdownBlockingProvider{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	client := llm.NewClient(providerCfg, provider, "test-model", 4096, "sys")
	a.swapLLMClientWithRef(client, "test-model", 128000, "test-provider/test-model")

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	runDone := make(chan error, 1)
	go func() {
		runDone <- a.Run(runCtx)
	}()

	turnCtx, cancelTurn := context.WithCancel(context.Background())
	a.spawnMainLLMResponseGoroutine(turnCtx, 1, []message.Message{{Role: "user", Content: "hello"}}, "")
	<-provider.started

	cancelTurn()
	if err := a.Shutdown(2 * time.Second); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	select {
	case err := <-runDone:
		if err == nil {
			t.Fatal("Run() error = nil, want cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Run to exit")
	}
}
