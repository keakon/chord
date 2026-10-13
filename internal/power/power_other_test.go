//go:build !darwin && !linux && !windows

package power

import (
	"context"
	"errors"
	"testing"
)

func TestUnsupportedBackend(t *testing.T) {
	l, err := NewBackend().Acquire(context.Background())
	if l != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Acquire=(%v,%v)", l, err)
	}
}
