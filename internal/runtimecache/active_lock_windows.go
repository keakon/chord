//go:build windows

package runtimecache

import (
	"math"
	"os"

	"golang.org/x/sys/windows"
)

const lockAllBytes = math.MaxUint32

func lockFileExclusiveNonBlocking(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, lockAllBytes, lockAllBytes, ol)
}

func unlockFile(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, lockAllBytes, lockAllBytes, ol)
}
