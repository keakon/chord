package tools

import "errors"

// ExternalCallOutcome describes what the runtime can prove about a failed
// external tool call. Success is expressed by a nil error, so the enum has no
// success value. The outcome never asserts that side effects were rolled back:
// a reported error can still have committed a partial operation.
type ExternalCallOutcome string

const (
	// ExternalCallNotSent proves the remote operation was never sent.
	ExternalCallNotSent ExternalCallOutcome = "not_sent"
	// ExternalCallReportedError means a valid, matching error receipt was
	// received from the remote service.
	ExternalCallReportedError ExternalCallOutcome = "reported_error"
	// ExternalCallOutcomeUnknown means the request may have been sent, but no
	// reliable result was received. Callers must check the actual state before
	// retrying.
	ExternalCallOutcomeUnknown ExternalCallOutcome = "outcome_unknown"
)

type externalCallError struct {
	err     error
	outcome ExternalCallOutcome
}

func (e *externalCallError) Error() string { return e.err.Error() }
func (e *externalCallError) Unwrap() error { return e.err }

// WrapExternalCallError labels err with the external call outcome while
// preserving its message and complete cause chain. It returns nil for a nil
// error. The classification is intentionally not a retry policy: consumers
// only decide how to present the result and whether the outcome is unknown.
func WrapExternalCallError(err error, outcome ExternalCallOutcome) error {
	if err == nil {
		return nil
	}
	return &externalCallError{err: err, outcome: outcome}
}

// ExternalCallOutcomeOf reports the external call outcome recorded in err's
// chain. The second result is false for an ordinary tool error.
func ExternalCallOutcomeOf(err error) (ExternalCallOutcome, bool) {
	callErr, ok := errors.AsType[*externalCallError](err)
	if !ok {
		return "", false
	}
	return callErr.outcome, true
}
