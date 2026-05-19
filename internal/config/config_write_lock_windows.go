//go:build windows

package config

import (
	"math"
	"os"

	"golang.org/x/sys/windows"
)

const configMutationLockAllBytes = math.MaxUint32

func lockConfigMutationFile(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, configMutationLockAllBytes, configMutationLockAllBytes, ol)
}

func unlockConfigMutationFile(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, configMutationLockAllBytes, configMutationLockAllBytes, ol)
}
