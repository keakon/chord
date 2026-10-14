package tools

import (
	"errors"
	"fmt"
	"strings"
)

type committedChangesError struct {
	err error
}

func (e *committedChangesError) Error() string { return e.err.Error() }
func (e *committedChangesError) Unwrap() error { return e.err }

func markErrorWithCommittedChanges(err error) error {
	if err == nil {
		return nil
	}
	return &committedChangesError{err: err}
}

// ErrorHasCommittedChanges reports whether a tool returned an error after
// committing a successful subset of its requested file mutations. Callers
// must still surface the error, while deriving changed-file state from the
// execution result instead of treating the whole invocation as uncommitted.
func ErrorHasCommittedChanges(err error) bool {
	_, ok := errors.AsType[*committedChangesError](err)
	return ok
}

type resultDescribedError struct {
	err error
}

func (e *resultDescribedError) Error() string { return e.err.Error() }
func (e *resultDescribedError) Unwrap() error { return e.err }

func markErrorDescribedInResult(err error) error {
	if err == nil {
		return nil
	}
	return &resultDescribedError{err: err}
}

// ErrorDescribedInResult reports whether a tool's non-empty result already
// contains the failure details and recovery instructions. Callers should keep
// the error status but must not append a second textual error summary.
func ErrorDescribedInResult(err error) bool {
	_, ok := errors.AsType[*resultDescribedError](err)
	return ok
}

func NormalizeEmptySuccessOutput(toolName, result string, err error) string {
	if err != nil || result != "" {
		return result
	}
	name := strings.TrimSpace(toolName)
	if name == "" {
		name = "Tool"
	}
	return fmt.Sprintf("(%s completed with no output)", name)
}
