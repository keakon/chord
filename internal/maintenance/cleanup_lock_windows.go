//go:build windows

package maintenance

import (
	"errors"
	"math"
	"os"

	"golang.org/x/sys/windows"
)

const cleanupLockAllBytes = math.MaxUint32

func lockCleanupFile(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, cleanupLockAllBytes, cleanupLockAllBytes, ol)
}

func unlockCleanupFile(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, cleanupLockAllBytes, cleanupLockAllBytes, ol)
}

func isCleanupWouldBlock(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
