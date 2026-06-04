package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/mcp"
)

type recordingObserver struct {
	calls int32
}

func (r *recordingObserver) OnAgentActivity(_ string, _ agent.ActivityType) {
	atomic.AddInt32(&r.calls, 1)
}

func TestCombineActivityObserversFanout(t *testing.T) {
	left := &recordingObserver{}
	right := &recordingObserver{}
	obs := combineActivityObservers(left, nil, right)
	if obs == nil {
		t.Fatal("combineActivityObservers() = nil, want observer")
	}
	obs.OnAgentActivity("main", agent.ActivityIdle)
	if got := atomic.LoadInt32(&left.calls); got != 1 {
		t.Fatalf("left calls = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&right.calls); got != 1 {
		t.Fatalf("right calls = %d, want 1", got)
	}
}

func TestRuntimeResourceControllerEnsureReadyAfterIdleUnload(t *testing.T) {
	var restored int32
	ctrl := newRuntimeResourceController(context.Background(), nil, nil, func(context.Context) error {
		atomic.AddInt32(&restored, 1)
		return nil
	}, nil)
	defer ctrl.Stop()

	ctrl.OnAgentActivity("main", agent.ActivityIdle)
	ctrl.runIdleUnload(ctrl.idleGen)

	if err := ctrl.EnsureReady(context.Background()); err != nil {
		t.Fatalf("EnsureReady() error = %v", err)
	}
	if got := atomic.LoadInt32(&restored); got != 0 {
		t.Fatalf("restore calls = %d, want 0 without MCP loaded", got)
	}

	ctrl.mu.Lock()
	ctrl.idleUnloaded = true
	ctrl.mcpWasLoaded = true
	ctrl.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := ctrl.EnsureReady(ctx); err != nil {
		t.Fatalf("EnsureReady() with loaded MCP error = %v", err)
	}
	if got := atomic.LoadInt32(&restored); got != 1 {
		t.Fatalf("restore calls = %d, want 1", got)
	}
	if _, mcpIdle := ctrl.IdleStatus(); mcpIdle {
		t.Fatal("MCP idle state should be cleared after restore")
	}
	if loaded := ctrl.MCPLoadedNames(); loaded != nil {
		t.Fatalf("MCPLoadedNames() after restore = %#v, want nil", loaded)
	}
	if err := ctrl.EnsureReady(ctx); err != nil {
		t.Fatalf("second EnsureReady() error = %v", err)
	}
	if got := atomic.LoadInt32(&restored); got != 1 {
		t.Fatalf("restore calls after second EnsureReady = %d, want 1", got)
	}
}

func TestRuntimeResourceControllerEnsureReadyWaitsForInFlightUnload(t *testing.T) {
	var restored int32
	ctrl := newRuntimeResourceController(context.Background(), nil, nil, func(context.Context) error {
		atomic.AddInt32(&restored, 1)
		return nil
	}, nil)
	defer ctrl.Stop()

	done := make(chan struct{})
	ctrl.mu.Lock()
	ctrl.idleUnloading = true
	ctrl.idleUnloadDone = done
	ctrl.mu.Unlock()

	ready := make(chan error, 1)
	go func() {
		ready <- ctrl.EnsureReady(context.Background())
	}()

	time.Sleep(10 * time.Millisecond)
	if got := atomic.LoadInt32(&restored); got != 0 {
		t.Fatalf("restore calls before unload completion = %d, want 0", got)
	}

	ctrl.mu.Lock()
	ctrl.idleUnloading = false
	ctrl.idleUnloadDone = nil
	ctrl.idleUnloaded = true
	ctrl.mcpWasLoaded = true
	close(done)
	ctrl.mu.Unlock()

	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("EnsureReady() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("EnsureReady() did not return after unload completion")
	}
	if got := atomic.LoadInt32(&restored); got != 1 {
		t.Fatalf("restore calls = %d, want 1", got)
	}
}

func TestRuntimeResourceControllerEnsureReadyLeavesLSPLazy(t *testing.T) {
	var restored int32
	ctrl := newRuntimeResourceController(context.Background(), nil, nil, func(context.Context) error {
		atomic.AddInt32(&restored, 1)
		return nil
	}, nil)
	defer ctrl.Stop()

	ctrl.mu.Lock()
	ctrl.idleUnloaded = true
	ctrl.lspUnloaded = true
	ctrl.lspLoaded = map[string]struct{}{"gopls": {}}
	ctrl.mu.Unlock()

	if err := ctrl.EnsureReady(context.Background()); err != nil {
		t.Fatalf("EnsureReady() error = %v", err)
	}
	if got := atomic.LoadInt32(&restored); got != 0 {
		t.Fatalf("restore calls = %d, want 0 for LSP-only idle unload", got)
	}
	if lspIdle, _ := ctrl.IdleStatus(); lspIdle {
		t.Fatal("LSP idle state should be cleared so future LSP use lazy-loads normally")
	}
	if ctrl.WasLSPLoaded("gopls") {
		t.Fatal("WasLSPLoaded(gopls) after ready = true, want false")
	}
}

func TestRuntimeResourceControllerIgnoresSubagentActivity(t *testing.T) {
	ctrl := newRuntimeResourceController(context.Background(), nil, nil, nil, nil)
	defer ctrl.Stop()

	ctrl.OnAgentActivity("agent-1", agent.ActivityIdle)
	ctrl.mu.Lock()
	defer ctrl.mu.Unlock()
	if ctrl.idleTimer != nil {
		t.Fatal("subagent idle activity should not schedule runtime idle unload")
	}
}

func TestRuntimeMCPReconnectConfigsKeepsAutoAndLoadedManualServers(t *testing.T) {
	configs := []mcp.ServerConfig{
		{Name: "auto"},
		{Name: "manual-on", Manual: true},
		{Name: "manual-off", Manual: true},
	}
	filtered := runtimeMCPReconnectConfigs(configs, map[string]struct{}{"manual-on": {}})
	if len(filtered) != 2 {
		t.Fatalf("runtimeMCPReconnectConfigs len = %d, want 2", len(filtered))
	}
	if filtered[0].Name != "auto" || filtered[1].Name != "manual-on" {
		t.Fatalf("runtimeMCPReconnectConfigs = %#v, want auto + loaded manual", filtered)
	}
}

func TestRuntimeResourceControllerNotifiesEnvOnUnloadAndReady(t *testing.T) {
	var updates int32
	ctrl := newRuntimeResourceController(context.Background(), nil, nil, nil, func() {
		atomic.AddInt32(&updates, 1)
	})
	defer ctrl.Stop()

	ctrl.OnAgentActivity("main", agent.ActivityIdle)
	ctrl.runIdleUnload(ctrl.idleGen)
	if got := atomic.LoadInt32(&updates); got != 1 {
		t.Fatalf("env updates after unload = %d, want 1", got)
	}
	if err := ctrl.EnsureReady(context.Background()); err != nil {
		t.Fatalf("EnsureReady() error = %v", err)
	}
	if got := atomic.LoadInt32(&updates); got != 2 {
		t.Fatalf("env updates after ready = %d, want 2", got)
	}
}
