//go:build unix

package config

import (
	"os"
	"syscall"
)

func lockConfigMutationFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

func unlockConfigMutationFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
