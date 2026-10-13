package power

import (
	"context"
	"errors"
	"runtime"
)

var (
	ErrUnsupported   = errors.New("sleep prevention is unsupported")
	ErrCleanupFailed = errors.New("sleep prevention cleanup failed")
)

// Backend acquires an independent resource. ctx controls acquisition only.
// A failed acquisition must clean up all resources before returning.
type Backend interface {
	Acquire(context.Context) (Lease, error)
}

// Lease owns one acquisition. Close is concurrent-safe and idempotent.
// Err is published before Done closes; normal closure has no error.
type Lease interface {
	Done() <-chan struct{}
	Err() error
	Close() error
}

// SupportedPlatform reports compile-time support, not service availability.
func SupportedPlatform() bool {
	return runtime.GOOS == "darwin" || runtime.GOOS == "linux" || runtime.GOOS == "windows"
}
