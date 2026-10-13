package agent

import "context"

// SetMCPPreparationHook installs credential reconciliation for outbound LLM
// requests. Synchronous session control never invokes this network-capable hook.
func (a *MainAgent) SetMCPPreparationHook(fn func(context.Context) error) {
	a.mcpServersPromptMu.Lock()
	defer a.mcpServersPromptMu.Unlock()
	a.mcpPreparationHook = fn
}

func (a *MainAgent) prepareMCPRequest(ctx context.Context) error {
	a.mcpServersPromptMu.RLock()
	fn := a.mcpPreparationHook
	a.mcpServersPromptMu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn(ctx)
}

// CurrentMCPReadyGeneration captures the publication fence without changing the
// readiness barrier or delaying conversation for an interactive login.
func (a *MainAgent) CurrentMCPReadyGeneration() MCPReadyGeneration {
	a.mcpReadyMu.Lock()
	defer a.mcpReadyMu.Unlock()
	return MCPReadyGeneration{id: a.mcpReadyGeneration, ready: a.mcpReady}
}
