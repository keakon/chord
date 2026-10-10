package agent

import (
	"errors"
	"fmt"

	"github.com/keakon/chord/internal/imagegen"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func imageToolRecoveryState(err error) string {
	failure, ok := errors.AsType[*imagegen.Failure](err)
	if !ok {
		return ""
	}
	switch failure.State {
	case imagegen.StateUnknown:
		return message.ToolRecoveryStateOutcomeUnknown
	case imagegen.StateNotSent, imagegen.StateRejected:
		return message.ToolRecoveryStateNotStarted
	default:
		return ""
	}
}

// persistImageResult publishes the ordinary tool event only after its canonical
// tool message has been synced. It runs in the ordered persistence pump, never
// blocking the event loop on filesystem I/O.
func (a *MainAgent) persistImageResult(manager *recovery.RecoveryManager, agentID string, msg message.Message, event ToolResultEvent, noteFailure func(error)) {
	publish := func(err error) {
		if err != nil {
			noteFailure(err)
			event.Status = ToolResultStatusError
			event.RecoveryState = message.ToolRecoveryStateOutcomeUnknown
			event.Result = fmt.Sprintf("Image execution must not be replayed: canonical result persistence failed: %v. Inspect the saved session image manifest.", err)
			event.Payload = event.Result
			event.Diagnostic = imageToolErrorDiagnostic(fmt.Errorf("image result persistence failed; inspect the session manifest; do not regenerate"))
		}
		logImageToolFailure(event)
		a.emitToTUI(event)
	}
	if a.shuttingDown.Load() || !a.persist.enqueue(persistEntry{durable: true, agentID: agentID, msg: msg, recovery: manager, after: publish}, a.stoppingCh) {
		publish(errPersistenceQueueUnavailable)
	}
}
