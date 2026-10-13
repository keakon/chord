//go:build darwin

package power

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// DarwinBackend prevents idle system sleep with an owned caffeinate process.
type DarwinBackend struct{ command func() *exec.Cmd }

// NewBackend constructs a backend without acquiring any resources.
func NewBackend() Backend { return &DarwinBackend{} }

func caffeinateCommand() *exec.Cmd {
	return exec.Command("/usr/bin/caffeinate", "-i", "-w", strconv.Itoa(os.Getpid()))
}

// Acquire starts a process whose lifetime is independent of the acquisition context.
func (b *DarwinBackend) Acquire(ctx context.Context) (Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	command := b.command
	if command == nil {
		command = caffeinateCommand
	}
	cmd := command()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("caffeinate start: %w", err)
	}
	l := &darwinLease{cmd: cmd, done: make(chan struct{}), waitDone: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		l.mu.Lock()
		if !l.closing {
			if err == nil {
				err = errors.New("caffeinate exited unexpectedly")
			}
			l.err = fmt.Errorf("caffeinate wait: %w", err)
			l.doneOnce.Do(func() { close(l.done) })
		}
		l.mu.Unlock()
		close(l.waitDone)
	}()
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, l.Close())
	}
	select {
	case <-l.done:
		return nil, errors.Join(l.Err(), l.Close())
	default:
		return l, nil
	}
}

type darwinLease struct {
	cmd                 *exec.Cmd
	done, waitDone      chan struct{}
	mu                  sync.Mutex
	closing             bool
	err                 error
	doneOnce, closeOnce sync.Once
	closeErr            error
}

func (l *darwinLease) Done() <-chan struct{} { return l.done }
func (l *darwinLease) Err() error            { l.mu.Lock(); defer l.mu.Unlock(); return l.err }
func (l *darwinLease) Close() error {
	l.closeOnce.Do(func() {
		l.mu.Lock()
		l.closing = true
		l.mu.Unlock()
		killErr := l.cmd.Process.Kill()
		if errors.Is(killErr, os.ErrProcessDone) {
			killErr = nil
		}
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-l.waitDone:
			// A reaped process confirms cleanup even if Kill raced with its exit.
		case <-timer.C:
			l.closeErr = errors.Join(ErrCleanupFailed, errors.New("caffeinate reap timed out"), killErr)
		}
		l.mu.Lock()
		if l.closeErr != nil {
			l.err = errors.Join(l.err, l.closeErr)
		}
		l.doneOnce.Do(func() { close(l.done) })
		l.mu.Unlock()
	})
	return l.closeErr
}
