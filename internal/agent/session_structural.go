package agent

import (
	"context"

	"github.com/keakon/chord/internal/executiongate"
	"github.com/keakon/chord/internal/identity"
)

// beginSessionStructural admits one operation before its first side effect.
// A tool that already entered Execute may finish registering a child task;
// its existing lease remains the drain barrier for that registration.
func (a *MainAgent) beginSessionStructural(ctx context.Context, allowExistingTool bool) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	gate := a.sessionExecutionGate()
	if allowExistingTool && executiongate.HasActiveLease(ctx, gate, executiongate.Tool) {
		return func() {}, nil
	}
	a.pause.mu.Lock()
	defer a.pause.mu.Unlock()
	switch a.pause.status.State {
	case SessionPausePreparing, SessionPauseDraining, SessionPauseSaving, SessionPauseSaved, SessionPauseReleased, SessionPauseSaveFailed, SessionPauseReady:
		return nil, &executiongate.ErrSessionPaused{}
	}
	lease, err := gate.TryBegin(executiongate.Structural, identity.MainAgentID)
	if err != nil {
		return nil, err
	}
	return lease.Finish, nil
}

// newSessionPreparationContext only interrupts work before request dispatch.
// The request itself must keep the original turn context once it has started.
func (a *MainAgent) newSessionPreparationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	prep, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	frozen := a.sessionExecutionGate().Frozen()
	if frozen == nil {
		return prep, cancel
	}
	go func() {
		select {
		case <-frozen:
			cancel()
		case <-done:
		case <-prep.Done():
		}
	}()
	return prep, func() { close(done); cancel() }
}

func (a *MainAgent) preparationError(err error) error {
	if a.sessionExecutionPaused() {
		return &executiongate.ErrSessionPaused{}
	}
	return err
}
