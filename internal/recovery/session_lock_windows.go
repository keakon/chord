//go:build windows

package recovery

import (
	"errors"
	"math"
	"os"

	"golang.org/x/sys/windows"
)

const sessionGuardLockAllBytes = math.MaxUint32

func lockSessionGuardFile(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, sessionGuardLockAllBytes, sessionGuardLockAllBytes, ol)
}

func unlockSessionGuardFile(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, sessionGuardLockAllBytes, sessionGuardLockAllBytes, ol)
}

func isSessionGuardWouldBlock(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
