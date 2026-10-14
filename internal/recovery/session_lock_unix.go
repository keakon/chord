//go:build unix

package recovery

import (
	"errors"
	"os"
	"syscall"
)

func lockSessionGuardFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// Readers share the probe lock while excluding an ownership writer. A shared
// lock also works with read-only descriptors on filesystems that emulate flock.
func lockSessionGuardForProbe(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
}

func unlockSessionGuardFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

func isSessionGuardWouldBlock(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}
