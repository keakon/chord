package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/mcp"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

type blockingProvider struct {
	started chan struct{}
}

func (p *blockingProvider) CompleteStream(
	ctx context.Context,
	_ string,
	_ string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	_ int,
	_ llm.RequestTuning,
	_ llm.StreamCallback,
) (*message.Response, error) {
	select {
	case p.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (p *blockingProvider) Complete(
	ctx context.Context,
	_ string,
	_ string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	_ int,
	_ llm.RequestTuning,
) (*message.Response, error) {
	select {
	case p.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func newBlockingRuntime(t *testing.T) (*Runtime, *agent.MainAgent, chan struct{}, func()) {
	t.Helper()

	projectRoot := t.TempDir()
	sessionDir := filepath.Join(projectRoot, ".chord", "sessions", "test")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"test-model": {
				Limit: config.ModelLimit{Context: 8192, Output: 1024},
			},
		},
	}, []string{"test-key"})
	provider := &blockingProvider{started: make(chan struct{}, 1)}
	ctxMgr := ctxmgr.NewManager(8192, false, 0)
	mainAgent := agent.NewMainAgent(
		context.Background(),
		llm.NewClient(providerCfg, provider, "test-model", 1024, ""),
		ctxMgr,
		tools.NewRegistry(),
		&hook.NoopEngine{},
		sessionDir,
		"test-model",
		projectRoot,
		&config.Config{},
		nil,
		mcp.ClientInfo{Name: "chord-test", Version: "test"},
	)
	mainAgent.MarkSkillsReady()
	mainAgent.ReloadAgentsMD()
	mainAgent.SetMCPServersPromptBlock("")

	acCtx, cancel := context.WithCancel(context.Background())
	rt := &Runtime{Agent: mainAgent}
	wireMainAgentRuntime(acCtx, mainAgent, tools.NewRegistry(), 0)

	cleanup := func() {
		cancel()
		_ = mainAgent.Shutdown(agentShutdownWait)
		for range mainAgent.Events() {
		}
	}
	return rt, mainAgent, provider.started, cleanup
}

func TestWaitIdleOrTimeoutReturnsPromptlyAfterCancelCurrentTurnWithoutTools(t *testing.T) {
	rt, mainAgent, started, cleanup := newBlockingRuntime(t)
	defer cleanup()

	mainAgent.SendUserMessage("hello")
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for blocking provider to start")
	}

	if cancelled := mainAgent.CancelCurrentTurn(); !cancelled {
		t.Fatal("CancelCurrentTurn() = false, want true")
	}

	began := time.Now()
	if ok := rt.WaitIdleOrTimeout(500 * time.Millisecond); !ok {
		t.Fatal("WaitIdleOrTimeout() = false, want true")
	}
	if elapsed := time.Since(began); elapsed > 400*time.Millisecond {
		t.Fatalf("WaitIdleOrTimeout took too long: %v", elapsed)
	}
}

func TestWaitIdleOrTimeoutWouldTimeoutWithoutCancellation(t *testing.T) {
	rt, _, started, cleanup := newBlockingRuntime(t)
	defer cleanup()

	mainAgent := rt.Agent

	mainAgent.SendUserMessage("hello")
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for blocking provider to start")
	}

	began := time.Now()
	if ok := rt.WaitIdleOrTimeout(100 * time.Millisecond); ok {
		t.Fatal("WaitIdleOrTimeout() = true, want false while turn is still busy")
	}
	if elapsed := time.Since(began); elapsed < 80*time.Millisecond {
		t.Fatalf("WaitIdleOrTimeout returned too early: %v", elapsed)
	}
}
