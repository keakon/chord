//go:build windows

package memory

import (
	"math"
	"os"

	"golang.org/x/sys/windows"
)

const memoryLockAllBytes = math.MaxUint32

func tryLockFile(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, memoryLockAllBytes, memoryLockAllBytes, ol)
}

func unlockFile(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, memoryLockAllBytes, memoryLockAllBytes, ol)
}
