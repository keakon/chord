package mcp

import (
	"errors"

	"github.com/keakon/chord/internal/tools"
)

// callOutcome classifies a failed tools/call attempt by what the MCP client
// can prove about the remote operation. A successful call returns a nil error.
type callOutcome uint8

const (
	callNotSent callOutcome = iota
	callReportedError
	callOutcomeUnknown
)

// callFailure attaches the transport/parse evidence to one call error. The
// cause chain is preserved so HTTP auth diagnostics, context cancellation and
// JSON-RPC errors remain inspectable.
type callFailure struct {
	cause   error
	outcome callOutcome
}

func (e *callFailure) Error() string { return e.cause.Error() }
func (e *callFailure) Unwrap() error { return e.cause }

func newCallFailure(outcome callOutcome, cause error) error {
	if cause == nil {
		return nil
	}
	return &callFailure{cause: cause, outcome: outcome}
}

func callOutcomeOf(err error) (callOutcome, bool) {
	failure, ok := errors.AsType[*callFailure](err)
	if !ok {
		return callOutcomeUnknown, false
	}
	return failure.outcome, true
}

// classifyCallFailure falls back to facts already present in the error chain.
// Unrecognized failures are conservatively unknown: only explicit local
// pre-send evidence may claim the request never left the process.
func classifyCallFailure(err error) callOutcome {
	if outcome, ok := callOutcomeOf(err); ok {
		return outcome
	}
	if _, ok := errors.AsType[*JSONRPCError](err); ok {
		return callReportedError
	}
	if httpErr, ok := errors.AsType[*HTTPError](err); ok && httpErr.SendEvidence == SendNotSent {
		return callNotSent
	}
	return callOutcomeUnknown
}

func externalCallOutcome(err error) tools.ExternalCallOutcome {
	switch classifyCallFailure(err) {
	case callNotSent:
		return tools.ExternalCallNotSent
	case callReportedError:
		return tools.ExternalCallReportedError
	default:
		return tools.ExternalCallOutcomeUnknown
	}
}
