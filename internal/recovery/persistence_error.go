package recovery

// PersistWriteError distinguishes an append that can safely be retried from an
// append whose bytes may already be present. Sync failures cannot authorize
// another append: re-appending would duplicate an accepted transcript record.
type PersistWriteError struct {
	RetrySafe bool
	Cause     error
}

func (e *PersistWriteError) Error() string { return e.Cause.Error() }
func (e *PersistWriteError) Unwrap() error { return e.Cause }

func persistWriteError(err error, retrySafe bool) error {
	if err == nil {
		return nil
	}
	return &PersistWriteError{RetrySafe: retrySafe, Cause: err}
}
