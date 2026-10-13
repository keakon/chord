package agent

import (
	"context"

	"github.com/keakon/chord/internal/recovery"
)

// SessionTransferPreparingEvent coordinates the existing output consumer with
// the input cutoff. Reply acknowledges only inputs already submitted by the UI.
type SessionTransferPreparingEvent struct {
	Identity recovery.TransferIdentity
	Reply    chan error
}

func (SessionTransferPreparingEvent) agentEvent() {}

// SessionPendingInputsUpdatedEvent retires UI projections only after the
// corresponding checkpoint update has been durably saved.
type SessionPendingInputsUpdatedEvent struct {
	InputIDs []string
}

func (SessionPendingInputsUpdatedEvent) agentEvent() {}

// CurrentSessionPauseStatus returns a read-only view for the local UI.
func (a *MainAgent) CurrentSessionPauseStatus() SessionPauseStatus {
	a.pause.mu.Lock()
	defer a.pause.mu.Unlock()
	status := a.pause.status
	status.Blockers = append([]SessionPauseBlocker(nil), status.Blockers...)
	return status
}

// UseSessionPauseEventPreparer installs the local UI's input preparation barrier.
// It shares the regular output consumer and never reads from the event channel.
func (a *MainAgent) UseSessionPauseEventPreparer() {
	a.SetSessionPausePreparer(func(ctx context.Context, req SessionPauseRequest) error {
		reply := make(chan error, 1)
		a.emitReliableToTUI(SessionTransferPreparingEvent{Identity: req.Identity, Reply: reply}, "session transfer preparation delivery")
		select {
		case err := <-reply:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	})
}
