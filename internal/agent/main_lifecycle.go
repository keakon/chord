package agent

import (
	"context"
	"fmt"
)

// admitRun gives the event loop exclusive ownership of its mutable state.
// Shutdown closes the same gate before it can inspect that state, so a
// goroutine scheduled after Shutdown cannot initialize a closed agent.
func (a *MainAgent) admitRun() error {
	a.runMu.Lock()
	defer a.runMu.Unlock()
	if a.runClosed {
		return context.Canceled
	}
	if a.started.Load() {
		return fmt.Errorf("agent event loop already started")
	}
	a.started.Store(true)
	return nil
}

func (a *MainAgent) closeRunAdmission() {
	a.runMu.Lock()
	a.runClosed = true
	a.runMu.Unlock()
}
