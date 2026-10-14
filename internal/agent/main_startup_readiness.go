package agent

import (
	"github.com/keakon/chord/internal/tools"
)

// SetMCPServersPromptBlock sets the MCP section appended to the system prompt
// and refreshes the installed system prompt on the LLM client and context manager.
func (a *MainAgent) SetMCPServersPromptBlock(block string) {
	a.mcpServersPromptMu.RLock()
	current := a.mcpServersPrompt
	a.mcpServersPromptMu.RUnlock()
	if current == block {
		a.mcpServersPromptMu.Lock()
		a.mcpRuntimePrompt = block
		a.mcpServersPromptMu.Unlock()
		a.markMCPReady()
		return
	}
	a.mcpServersPromptMu.Lock()
	a.mcpServersPrompt = block
	a.mcpRuntimePrompt = block
	a.mcpServersPromptMu.Unlock()
	a.markMCPReady()
	// Pre-first-turn: refresh the stable system prompt so the MCP
	// discoverability snapshot is in place when ensureSessionBuilt freezes it.
	// Post-first-turn: MCP connection changes are runtime state and must not
	// invalidate the frozen prefix; the current snapshot stays in place.
	if !a.sessionBuilt.Load() {
		a.refreshSystemPrompt()
	}
}

func (a *MainAgent) SetPendingMCPDiscovery(mcpTools []tools.Tool, block string) {
	a.mcpServersPromptMu.Lock()
	a.mcpServersPrompt = block
	a.mcpRuntimePrompt = block
	a.pendingMCPReplace = false
	if len(mcpTools) == 0 {
		a.pendingMCPTools = nil
	} else {
		a.pendingMCPTools = append([]tools.Tool(nil), mcpTools...)
	}
	a.mcpServersPromptMu.Unlock()
	a.markMCPReady()
}

// SetRuntimeMCPDiscoveryForGeneration applies a background MCP restore only
// when no newer restore has replaced its readiness barrier. The generation's
// own wait channel is always closed so a superseded waiter cannot leak.
func (a *MainAgent) SetRuntimeMCPDiscoveryForGeneration(generation MCPReadyGeneration, mcpTools []tools.Tool, block string) bool {
	a.mcpReadyMu.Lock()
	defer a.mcpReadyMu.Unlock()
	if generation.id != a.mcpReadyGeneration || generation.ready != a.mcpReady {
		closeReadyChannel(generation.ready)
		return false
	}
	a.stageRuntimeMCPDiscovery(mcpTools, block)
	a.markRuntimeSurfaceDirty()
	a.closeMCPReadyLocked()
	return true
}

// applyMCPControlSurfaceForGeneration applies a completed runtime MCP control
// result only when no newer restore has replaced its readiness barrier,
// mirroring SetRuntimeMCPDiscoveryForGeneration. apply runs and the current
// barrier is released only for the owning generation; a superseded request has
// its own wait channel closed so a waiter that grabbed it cannot leak.
func (a *MainAgent) applyMCPControlSurfaceForGeneration(generation MCPReadyGeneration, apply func()) bool {
	a.mcpReadyMu.Lock()
	defer a.mcpReadyMu.Unlock()
	if generation.id != a.mcpReadyGeneration || generation.ready != a.mcpReady {
		closeReadyChannel(generation.ready)
		return false
	}
	apply()
	a.closeMCPReadyLocked()
	return true
}

func (a *MainAgent) stageRuntimeMCPDiscovery(mcpTools []tools.Tool, block string) {
	a.mcpServersPromptMu.Lock()
	a.mcpServersPrompt = block
	a.mcpRuntimePrompt = block
	a.pendingMCPReplace = true
	if len(mcpTools) == 0 {
		a.pendingMCPTools = nil
	} else {
		a.pendingMCPTools = append([]tools.Tool(nil), mcpTools...)
	}
	a.mcpServersPromptMu.Unlock()
}

// RegisterMainMCPServers registers the main-agent's MCP server names as
// sentinels in mcpServerCache so that SubAgents never reconnect them.
// Called after the main-agent MCP servers are connected.
func (a *MainAgent) RegisterMainMCPServers(serverNames []string) {
	if len(serverNames) == 0 {
		return
	}
	a.mcpServerCacheMu.Lock()
	defer a.mcpServerCacheMu.Unlock()
	if a.mcpServerCache == nil {
		a.mcpServerCache = make(map[string]*mcpServerEntry)
	}
	for _, name := range serverNames {
		key := mainMCPServerCacheKey(name)
		if _, ok := a.mcpServerCache[key]; !ok {
			a.mcpServerCache[key] = &mcpServerEntry{} // sentinel: Mgr==nil
		}
	}
}

func (a *MainAgent) markAgentsMDReady() {
	a.agentsMDReadyOnce.Do(func() { close(a.agentsMDReady) })
}

func (a *MainAgent) MarkSkillsReady() {
	a.skillsReadyOnce.Do(func() {
		if a.skillsReady != nil {
			close(a.skillsReady)
		}
	})
}

func (a *MainAgent) markMCPReady() {
	a.mcpReadyMu.Lock()
	defer a.mcpReadyMu.Unlock()
	a.closeMCPReadyLocked()
}

func (a *MainAgent) closeMCPReadyLocked() {
	closeReadyChannel(a.mcpReady)
}

func closeReadyChannel(ch chan struct{}) {
	if ch == nil {
		return
	}
	select {
	case <-ch:
		// already closed
	default:
		close(ch)
	}
}
