package main

import "context"

// reconcileRuntimeMCPCredentials runs only during outbound request preparation,
// after startup wiring. The restore fence rejects results from a prior session.
func reconcileRuntimeMCPCredentials(ctx context.Context, ac *AppContext) error {
	if ac == nil || ac.MCPMgr == nil || ac.MainAgent == nil {
		return nil
	}
	startRuntimeMCP(ac)
	ac.mcpRestoreRunMu.Lock()
	defer ac.mcpRestoreRunMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	generation := ac.MainAgent.CurrentMCPReadyGeneration()
	_, err := ac.MCPMgr.ReconcileCredentials(ctx)
	if err != nil {
		return err
	}
	// Sample before reading the surface. A concurrent publication can only
	// cause another reconciliation, never stamp an older surface as current.
	snapshot := ac.MCPMgr.Snapshot()
	if ac.mcpPublishedBinding == snapshot.BindingID && ac.mcpPublishedVersion == snapshot.Version && ac.mcpPublishedGeneration == generation {
		return nil
	}
	result, _ := loadMCPState(ctx, ac.MCPCatalog, ac.MCPMgr)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if ac.MainAgent.SetRuntimeMCPDiscoveryForGeneration(generation, result.Tools, result.PromptBlock) {
		ac.mcpPublishedBinding = snapshot.BindingID
		ac.mcpPublishedVersion = snapshot.Version
		ac.mcpPublishedGeneration = generation
	}
	ac.MainAgent.NotifyEnvStatusUpdated()
	return nil
}
