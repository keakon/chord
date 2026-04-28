//go:build windows

package main

import (
	"math"
	"os"

	"golang.org/x/sys/windows"
)

const runtimeLogLockAllBytes = math.MaxUint32

func lockRuntimeLogFile(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, runtimeLogLockAllBytes, runtimeLogLockAllBytes, ol)
}

func unlockRuntimeLogFile(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, runtimeLogLockAllBytes, runtimeLogLockAllBytes, ol)
}
