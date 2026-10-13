package agent

import (
	"github.com/keakon/chord/internal/executiongate"
	"github.com/keakon/chord/internal/llm"
)

// SessionEpoch returns the epoch of the current durable writer. It is safe
// from any goroutine; a session without an open writer reports no epoch.
func (a *MainAgent) SessionEpoch() uint64 { return a.recoverySessionEpoch() }

func (a *MainAgent) sessionExecutionGate() *executiongate.Gate {
	if a == nil {
		return nil
	}
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.executionGate
}

func (a *MainAgent) bindAuxExecutionGate(client *llm.Client) *llm.Client {
	client.SetExecutionGate(a.sessionExecutionGate(), "auxiliary")
	return client
}

func (a *MainAgent) sessionExecutionPaused() bool {
	select {
	case <-a.sessionExecutionGate().Frozen():
		return true
	default:
		return false
	}
}
