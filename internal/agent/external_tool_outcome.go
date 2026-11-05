package agent

import (
	"context"
	"errors"

	"github.com/keakon/chord/internal/tools"
)

// An unclassified cancellation from inside Execute is not a remote receipt.
// Leave it to interruption settlement; precise adapter outcomes and failures
// before Execute still carry stronger evidence than the admission marker.
func preserveToolResultOnCancellation(result ToolExecutionResult, err error) bool {
	if !result.ExecutionStarted || (!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)) {
		return true
	}
	if _, ok := tools.ExternalCallOutcomeOf(err); ok {
		return true
	}
	return result.recoveryState(err) != "" || imageToolRecoveryState(err) != ""
}

// externalOutcomeTransportNote tells the model that a failed external call has
// no trustworthy receipt. It must not claim the call succeeded or failed: the
// request may have been delivered and the side effect applied.
const externalOutcomeTransportNote = "The external call may already have been executed, but no usable result was received. Its side effects may have been applied — verify the external state before retrying."

// appendExternalOutcomeNote adds the uncertainty note for an external call
// whose outcome is unknown. It runs after result composition so the tool card
// keeps the plain error text while the model and the durable transcript carry
// the recovery instruction.
func appendExternalOutcomeNote(contextResult string, err error) string {
	outcome, ok := tools.ExternalCallOutcomeOf(err)
	if !ok || outcome != tools.ExternalCallOutcomeUnknown {
		return contextResult
	}
	return appendModelContextNote(contextResult, externalOutcomeTransportNote)
}
