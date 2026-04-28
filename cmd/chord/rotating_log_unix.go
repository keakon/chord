//go:build unix

package main

import (
	"os"
	"syscall"
)

func lockRuntimeLogFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

func unlockRuntimeLogFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
