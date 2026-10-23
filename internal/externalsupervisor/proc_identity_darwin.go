//go:build darwin

package externalsupervisor

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// processStartToken returns a value that changes when the pid is reused.
// KERN_PROC_PID exposes the process start time as a timeval.
func processStartToken(pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("invalid pid %d", pid)
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	if info == nil || int(info.Proc.P_pid) != pid {
		return "", unix.ESRCH
	}
	return fmt.Sprintf("darwin:%d.%d", info.Proc.P_starttime.Sec, info.Proc.P_starttime.Usec), nil
}
