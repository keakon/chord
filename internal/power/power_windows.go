//go:build windows

package power

import (
	"context"
	"errors"
	"sync"

	"golang.org/x/sys/windows"
)

type windowsBackend struct{ api windowsPowerAPI }

// NewBackend constructs a backend without invoking Windows power APIs.
func NewBackend() Backend { return &windowsBackend{api: nativeWindowsPowerAPI{}} }

func (b *windowsBackend) Acquire(ctx context.Context) (Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	handle, err := b.api.create()
	if err != nil {
		return nil, err
	}
	l := &windowsLease{api: b.api, handle: handle, done: make(chan struct{})}
	if err = ctx.Err(); err != nil {
		return nil, errors.Join(err, l.Close())
	}
	if err = b.api.set(handle); err != nil {
		return nil, errors.Join(err, l.Close())
	}
	l.set = true
	if err = ctx.Err(); err != nil {
		return nil, errors.Join(err, l.Close())
	}
	return l, nil
}

type windowsLease struct {
	api    windowsPowerAPI
	handle windows.Handle
	set    bool
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	err    error
}

func (l *windowsLease) Done() <-chan struct{} { return l.done }
func (l *windowsLease) Err() error            { l.mu.Lock(); defer l.mu.Unlock(); return l.err }
func (l *windowsLease) Close() error {
	l.once.Do(func() {
		var clearErr error
		if l.set {
			clearErr = l.api.clear(l.handle)
		}
		closeErr := l.api.close(l.handle)
		err := errors.Join(clearErr, closeErr)
		if err != nil {
			err = errors.Join(ErrCleanupFailed, err)
		}
		l.mu.Lock()
		l.err = err
		close(l.done)
		l.mu.Unlock()
	})
	return l.Err()
}
