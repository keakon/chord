//go:build !darwin

package power

// NoopBackend is a no-op backend for non-darwin platforms.
type NoopBackend struct{}

// NewBackend creates a no-op backend.
func NewBackend() *NoopBackend {
	return &NoopBackend{}
}

// Acquire is a no-op.
func (b *NoopBackend) Acquire() error { return nil }

// Release is a no-op.
func (b *NoopBackend) Release() error { return nil }

// Close is a no-op.
func (b *NoopBackend) Close() error { return nil }
