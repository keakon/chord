//go:build windows

package config

import (
	"context"
	"math"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

const configMutationLockAllBytes = math.MaxUint32

func lockConfigMutationFileContext(ctx context.Context, f *os.File) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ol := new(windows.Overlapped)
		err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, configMutationLockAllBytes, configMutationLockAllBytes, ol)
		if err == nil {
			return nil
		}
		if err != windows.ERROR_LOCK_VIOLATION {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func unlockConfigMutationFile(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, configMutationLockAllBytes, configMutationLockAllBytes, ol)
}
