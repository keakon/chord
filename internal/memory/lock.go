package memory

import (
	"fmt"
	"os"
	"time"

	"github.com/keakon/chord/internal/privatefs"
)

// MemoryLock is an acquired cross-process memory lock. It serializes record,
// index, and checkpoint commits against other Chord processes. Holding the lock
// for the full commit sequence prevents lost index/checkpoint updates; it is
// not a substitute for reading the file fresh under the lock.
type MemoryLock struct {
	f *os.File
}

// AcquireLock obtains the cross-process lock for this project's memory state,
// waiting up to timeout. The lock file lives under the machine state dir, so it
// survives across processes and does not touch project files.
func (l *Layout) AcquireLock(timeout time.Duration) (*MemoryLock, error) {
	if l.LockPath == "" {
		return nil, fmt.Errorf("memory lock path is empty")
	}
	if err := os.MkdirAll(l.StateDir, 0o755); err != nil {
		return nil, fmt.Errorf("create memory state dir: %w", err)
	}
	f, err := privatefs.OpenFile(l.StateDir, l.LockPath, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return nil, fmt.Errorf("open memory lock: %w", err)
	}
	if err := tryLockFile(f); err == nil {
		return &MemoryLock{f: f}, nil
	}
	// Resource locked and needs retry; also convert a genuine non-block error.
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("memory lock held by another process")
		}
		time.Sleep(25 * time.Millisecond)
		if err := tryLockFile(f); err == nil {
			return &MemoryLock{f: f}, nil
		}
	}
}

// Release releases the cross-process lock.
func (m *MemoryLock) Release() {
	if m == nil || m.f == nil {
		return
	}
	_ = unlockFile(m.f)
	_ = m.f.Close()
	m.f = nil
}
