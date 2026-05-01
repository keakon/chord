//go:build darwin

package power

import (
	"context"
	"github.com/keakon/golog/log"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// DarwinBackend uses the caffeinate command to prevent idle sleep.
type DarwinBackend struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	cancel context.CancelFunc
}

// NewBackend creates a new darwin power backend.
func NewBackend() *DarwinBackend {
	return &DarwinBackend{}
}

// Acquire starts a caffeinate process to prevent idle sleep.
func (b *DarwinBackend) Acquire() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.cmd != nil {
		// Already running.
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	// Use -i to prevent idle sleep only. Do not use -d (display) or -s (sleep on AC).
	cmd := exec.CommandContext(ctx, "caffeinate", "-i")
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return err
	}

	b.cmd = cmd
	b.cancel = cancel
	return nil
}

// Release stops the caffeinate process.
func (b *DarwinBackend) Release() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.cmd == nil {
		return nil
	}

	if b.cancel != nil {
		b.cancel()
	}

	// Wait for the process to exit with a timeout.
	done := make(chan struct{})
	go func() {
		if b.cmd != nil {
			_ = b.cmd.Wait()
		}
		close(done)
	}()

	select {
	case <-done:
		// Process exited cleanly.
	case <-time.After(2 * time.Second):
		// Force kill if it doesn't exit gracefully.
		if b.cmd != nil && b.cmd.Process != nil {
			log.Warn("power: caffeinate did not exit gracefully, killing")
			b.cmd.Process.Kill()
			<-done
		}
	}

	b.cmd = nil
	b.cancel = nil
	return nil
}

// Close releases any held assertion.
func (b *DarwinBackend) Close() error {
	return b.Release()
}

// No-op implementation for non-darwin platforms.
