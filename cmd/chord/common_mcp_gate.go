package main

import "context"

// acquireMCPRuntime serializes connection and catalog changes across control,
// restore and credential reconciliation. Cancellation must cover time spent
// waiting for another operation, before this caller can change session intent.
func (ac *AppContext) acquireMCPRuntime(ctx context.Context) error {
	ac.mcpRestoreStateMu.Lock()
	if ac.mcpRuntimeGate == nil {
		ac.mcpRuntimeGate = make(chan struct{}, 1)
	}
	gate := ac.mcpRuntimeGate
	ac.mcpRestoreStateMu.Unlock()
	select {
	case gate <- struct{}{}:
		// Both cases can be ready when the previous owner releases the gate.
		if err := ctx.Err(); err != nil {
			<-gate
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (ac *AppContext) releaseMCPRuntime() {
	<-ac.mcpRuntimeGate
}
