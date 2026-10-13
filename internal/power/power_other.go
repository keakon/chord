//go:build !darwin && !linux && !windows

package power

import "context"

type unsupportedBackend struct{}

// NewBackend constructs an unsupported-platform backend.
func NewBackend() Backend { return unsupportedBackend{} }

func (unsupportedBackend) Acquire(context.Context) (Lease, error) { return nil, ErrUnsupported }
