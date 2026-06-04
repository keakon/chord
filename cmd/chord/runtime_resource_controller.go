package main

import (
	"context"
	"sync"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/lsp"
	"github.com/keakon/chord/internal/mcp"
)

const (
	runtimeResourceIdleTimeout = 5 * time.Minute
	mainAgentID                = "main"
)

type runtimeResourceController struct {
	ctx        context.Context
	cancel     context.CancelFunc
	lsp        *lsp.Manager
	mcp        *mcp.Manager
	restoreMCP func(context.Context) error
	notifyEnv  func()

	mu             sync.Mutex
	idleTimer      *time.Timer
	idleGen        uint64
	idleUnloading  bool
	idleUnloadDone chan struct{}
	idleUnloaded   bool
	lspUnloaded    bool
	lspLoaded      map[string]struct{}
	mcpWasLoaded   bool
	mcpLoaded      map[string]struct{}
	stopped        bool
}

func newRuntimeResourceController(parent context.Context, lspMgr *lsp.Manager, mcpMgr *mcp.Manager, restoreMCP func(context.Context) error, notifyEnv func()) *runtimeResourceController {
	ctx, cancel := context.WithCancel(parent)
	return &runtimeResourceController{
		ctx:        ctx,
		cancel:     cancel,
		lsp:        lspMgr,
		mcp:        mcpMgr,
		restoreMCP: restoreMCP,
		notifyEnv:  notifyEnv,
	}
}

func (c *runtimeResourceController) OnAgentActivity(agentID string, activity agent.ActivityType) {
	if c == nil {
		return
	}
	if agentID != "" && agentID != mainAgentID {
		return
	}
	if activity == agent.ActivityIdle {
		c.scheduleIdleUnload()
		return
	}
	c.cancelIdleUnload()
}

func (c *runtimeResourceController) Stop() {
	if c == nil {
		return
	}
	c.cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped = true
	if c.idleTimer != nil {
		c.idleTimer.Stop()
		c.idleTimer = nil
	}
}

func (c *runtimeResourceController) EnsureReady(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.cancelIdleUnload()

	for {
		c.mu.Lock()
		done := c.idleUnloadDone
		unloading := c.idleUnloading
		if !unloading || done == nil {
			break
		}
		c.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if !c.idleUnloaded || c.stopped {
		c.mu.Unlock()
		return nil
	}
	restoreMCP := c.mcpWasLoaded
	c.mu.Unlock()

	if restoreMCP && c.restoreMCP != nil {
		if err := c.restoreMCP(ctx); err != nil {
			c.notifyEnvUpdated()
			return err
		}
	}

	c.mu.Lock()
	c.idleUnloaded = false
	c.lspUnloaded = false
	c.lspLoaded = nil
	c.mcpWasLoaded = false
	c.mcpLoaded = nil
	c.mu.Unlock()
	c.notifyEnvUpdated()
	return nil
}

func (c *runtimeResourceController) scheduleIdleUnload() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return
	}
	c.idleGen++
	gen := c.idleGen
	if c.idleTimer != nil {
		c.idleTimer.Stop()
	}
	c.idleTimer = time.AfterFunc(runtimeResourceIdleTimeout, func() {
		c.runIdleUnload(gen)
	})
}

func (c *runtimeResourceController) cancelIdleUnload() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.idleGen++
	if c.idleTimer != nil {
		c.idleTimer.Stop()
		c.idleTimer = nil
	}
}

func (c *runtimeResourceController) runIdleUnload(gen uint64) {
	c.mu.Lock()
	if c.stopped || gen != c.idleGen || c.idleUnloaded || c.idleUnloading {
		c.mu.Unlock()
		return
	}
	done := make(chan struct{})
	c.idleUnloading = true
	c.idleUnloadDone = done
	c.mu.Unlock()

	lspUnloaded := false
	var lspLoaded map[string]struct{}
	if c.lsp != nil {
		loaded := c.lsp.LoadedServerNames()
		lspUnloaded = len(loaded) > 0
		if len(loaded) > 0 {
			lspLoaded = make(map[string]struct{}, len(loaded))
			for _, name := range loaded {
				lspLoaded[name] = struct{}{}
			}
		}
	}

	if c.lsp != nil {
		stopCtx, cancel := newLSPShutdownContext()
		c.lsp.Stop(stopCtx)
		cancel()
	}
	mcpWasLoaded := false
	var mcpLoaded map[string]struct{}
	if c.mcp != nil {
		loaded := c.mcp.Clients()
		mcpWasLoaded = len(loaded) > 0
		if len(loaded) > 0 {
			mcpLoaded = make(map[string]struct{}, len(loaded))
			for name := range loaded {
				mcpLoaded[name] = struct{}{}
			}
		}
		c.mcp.Close()
	}
	c.mu.Lock()
	c.idleUnloading = false
	c.idleUnloadDone = nil
	if !c.stopped {
		c.idleUnloaded = true
		c.lspUnloaded = lspUnloaded
		c.lspLoaded = lspLoaded
		c.mcpWasLoaded = mcpWasLoaded
		c.mcpLoaded = mcpLoaded
	}
	close(done)
	c.mu.Unlock()
	c.notifyEnvUpdated()
	log.Debug("runtime resource controller: unloaded idle LSP/MCP resources")
}

func (c *runtimeResourceController) notifyEnvUpdated() {
	if c == nil || c.notifyEnv == nil {
		return
	}
	c.notifyEnv()
}

func (c *runtimeResourceController) IdleStatus() (lspUnloaded, mcpIdle bool) {
	if c == nil {
		return false, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.idleUnloaded {
		return false, false
	}
	return c.lspUnloaded, c.mcpWasLoaded
}

func (c *runtimeResourceController) WasLSPLoaded(name string) bool {
	if c == nil || name == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.lspLoaded) == 0 {
		return false
	}
	_, ok := c.lspLoaded[name]
	return ok
}

func (c *runtimeResourceController) MCPLoadedNames() map[string]struct{} {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.mcpLoaded) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(c.mcpLoaded))
	for name := range c.mcpLoaded {
		out[name] = struct{}{}
	}
	return out
}

func restoreRuntimeMCP(ac *AppContext, ctrl *runtimeResourceController) func(context.Context) error {
	if ac == nil || ac.MCPMgr == nil || len(ac.MCPConfigs) == 0 || ac.MainAgent == nil {
		return nil
	}
	return func(ctx context.Context) error {
		ac.MainAgent.ResetMCPReady()
		ac.MainAgent.NotifyEnvStatusUpdated()
		ac.MCPMgr.ConnectAll(ctx, runtimeMCPReconnectConfigs(ac.MCPConfigs, ctrl.MCPLoadedNames()))
		result, err := loadMCPState(ctx, ac.MCPMgr)
		ac.MainAgent.SetRuntimeMCPDiscovery(result.Tools, result.PromptBlock)
		ac.MainAgent.NotifyEnvStatusUpdated()
		return err
	}
}

func runtimeMCPReconnectConfigs(configs []mcp.ServerConfig, loaded map[string]struct{}) []mcp.ServerConfig {
	if len(configs) == 0 {
		return nil
	}
	out := make([]mcp.ServerConfig, 0, len(configs))
	for _, cfg := range configs {
		name := cfg.Name
		if name == "" {
			name = "(unnamed)"
		}
		if cfg.Manual {
			if _, ok := loaded[name]; !ok {
				continue
			}
		}
		out = append(out, cfg)
	}
	return out
}

func loadMCPState(ctx context.Context, mgr *mcp.Manager) (agent.MCPControlResult, error) {
	if mgr == nil {
		return agent.MCPControlResult{}, nil
	}
	mcpTools, toolErr := mcp.DiscoverAllTools(ctx, mgr)
	block := mcp.ConnectedServersPromptBlock(ctx, mgr)
	return agent.MCPControlResult{Tools: mcpTools, PromptBlock: block}, toolErr
}
