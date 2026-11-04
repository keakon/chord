//go:build darwin

package externalsupervisor

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// KERN_PROC_PID includes zombies, unlike getpgid. Read the group and start
// time from the same entry so exit cannot split the two identity checks.
func readProcessIdentity(pid int) (pgid int, startToken string, err error) {
	if pid <= 0 {
		return 0, "", fmt.Errorf("invalid pid %d", pid)
	}
	// The slice API distinguishes an empty result after reap from a failed
	// sysctl; SysctlKinfoProc reports both a short result and absence as EIO.
	infos, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return 0, "", fmt.Errorf("read process %d identity: %w", pid, err)
	}
	if len(infos) == 0 {
		return 0, "", unix.ESRCH
	}
	if len(infos) != 1 || int(infos[0].Proc.P_pid) != pid {
		return 0, "", fmt.Errorf("process %d identity: %w", pid, ErrIdentityMismatch)
	}
	info := &infos[0]
	return int(info.Eproc.Pgid), fmt.Sprintf("darwin:%d.%d", info.Proc.P_starttime.Sec, info.Proc.P_starttime.Usec), nil
}
