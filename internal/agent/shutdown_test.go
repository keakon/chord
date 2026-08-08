package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/mcp"
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
	if a.persist.ch == nil {
		t.Fatal("expected persist channel")
	}

	a.started.Store(true)
	a.done = make(chan struct{})
	a.mcpServerCache = map[string]*mcpServerEntry{
		agentMCPServerCacheKey("worker", "search"): {
			Mgr: mcp.NewPendingManager([]mcp.ServerConfig{{Name: "search", URL: "https://worker.example/mcp"}}),
		},
	}

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
	// Aborting on timeout must still leave a best-effort recovery snapshot.
	snapshotPath := filepath.Join(projectRoot, ".chord", "sessions", "test", "snapshot.json")
	if _, statErr := os.Stat(snapshotPath); statErr != nil {
		t.Fatalf("expected best-effort snapshot after shutdown timeout: %v", statErr)
	}
	a.mcpServerCacheMu.Lock()
	if a.mcpServerCache == nil {
		a.mcpServerCacheMu.Unlock()
		t.Fatal("MCP cache closed before the event loop stopped")
	}
	a.mcpServerCacheMu.Unlock()

	close(a.done)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		a.mcpServerCacheMu.Lock()
		closed := a.mcpServerCache == nil
		a.mcpServerCacheMu.Unlock()
		if closed {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("MCP cache was not closed after the event loop stopped")
}

func TestShutdownClosesSubAgentMCPServersWhenCompactionDrainTimesOut(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.mcpServerCache = map[string]*mcpServerEntry{
		agentMCPServerCacheKey("worker", "search"): {
			Mgr: mcp.NewPendingManager([]mcp.ServerConfig{{Name: "search", URL: "https://worker.example/mcp"}}),
		},
	}

	a.compactionWg.Add(1)
	defer a.compactionWg.Done()

	err := a.Shutdown(100 * time.Millisecond)
	if err == nil {
		t.Fatal("Shutdown() error = nil, want timeout")
	}
	if a.mcpServerCache != nil {
		t.Fatalf("mcpServerCache = %#v, want nil after timeout cleanup", a.mcpServerCache)
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

	runCtx := t.Context()
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

func TestShutdownWaitsForStartedSubAgentRunLoop(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "shutdown-subagent")
	sub.startRunLoop()
	t.Cleanup(func() {
		sub.cancel()
		_ = sub.waitDone(context.Background())
	})

	if err := a.Shutdown(2 * time.Second); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case <-sub.done:
	default:
		t.Fatal("Shutdown returned before the SubAgent run loop finished")
	}
}

func TestShutdownClosesMainLLMClient(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	client := newTestLLMClient()
	a.swapLLMClientWithRef(client, "test-model", 128000, "test-provider/test-model")

	if err := a.Shutdown(2 * time.Second); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if _, err := client.CompleteStream(context.Background(), nil, nil, nil); err == nil || err.Error() != "llm client is closed" {
		t.Fatalf("main client after Shutdown error = %v, want closed", err)
	}
}
