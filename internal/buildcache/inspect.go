//go:build unix

package buildcache

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// BootState classifies whether a usage record can still protect a live owner.
type BootState string

const (
	// BootCurrent means the record belongs to this system boot.
	BootCurrent BootState = "current"
	// BootPrevious means a different known boot proves the recorded owner is gone.
	BootPrevious BootState = "previous"
	// BootUnknown means the record cannot be safely classified.
	BootUnknown BootState = "unknown"
)

// UserStatus is a read-only view of one persistent usage record.
type UserStatus struct {
	ID       string
	Boot     BootState
	Age      time.Duration
	RecordOK bool
}

// Inspection reports the logical cache size and registered users.
type Inspection struct {
	LogicalBytes      int64
	Users             []UserStatus
	Scanned           bool
	RegistryBusy      bool
	RegistryAvailable bool
	Approximate       bool
}

// Inspect reports cache state without creating or changing files.
func (c *Cache) Inspect() (Inspection, error) {
	result := Inspection{}
	lock, available, busy, err := c.inspectLock()
	if err != nil {
		return result, err
	}
	if busy {
		result.RegistryBusy = true
		result.Approximate = true
		return result, nil
	}
	result.RegistryAvailable = available
	result.Approximate = !available
	if lock != nil {
		defer lock.Close()
	}

	entries, err := os.ReadDir(filepath.Join(c.root, "users"))
	if err != nil {
		return result, fmt.Errorf("read build cache users: %w", err)
	}
	now, bootID := time.Now(), systemBootID()
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return result, fmt.Errorf("inspect build cache user: %w", err)
		}
		record, ok := readUserRecord(filepath.Join(c.root, "users", entry.Name()))
		user := UserStatus{ID: entry.Name(), Boot: BootUnknown, RecordOK: ok}
		if ok && bootID != "" {
			if record.BootID == bootID {
				user.Boot = BootCurrent
			} else {
				user.Boot = BootPrevious
			}
		}
		if age := now.Sub(info.ModTime()); age > 0 {
			user.Age = age
		}
		if user.Boot != BootPrevious {
			result.Approximate = true
		}
		result.Users = append(result.Users, user)
	}
	_, result.LogicalBytes, err = c.scan()
	if err != nil {
		return result, err
	}
	result.Scanned = true
	return result, nil
}

func (c *Cache) inspectLock() (*os.File, bool, bool, error) {
	fd, err := syscall.Open(filepath.Join(c.root, ".lock"), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, false, nil
	}
	if err != nil {
		return nil, false, false, fmt.Errorf("open build cache lock for inspection: %w", err)
	}
	file := os.NewFile(uintptr(fd), "build-cache-inspection-lock")
	if err := syscall.Flock(fd, syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, true, true, nil
		}
		return nil, false, false, fmt.Errorf("lock build cache for inspection: %w", err)
	}
	return file, true, false, nil
}
