//go:build unix

package tools

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/keakon/golog/log"
)

// tracksProcessGroups reports whether commands run in a dedicated process group
// whose members can be probed and signalled as a unit. It is false on platforms
// without the Unix process-group primitive.
const tracksProcessGroups = true

func configureCommandProcessGroupImpl(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func terminateCommandProcessGroupImpl(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	return syscall.Kill(-pid, syscall.SIGTERM)
}

// processGroupAlreadyGone reports whether a failed termination means the
// process group was already reaped, i.e. the command exited on its own before
// the stop signal arrived.
func processGroupAlreadyGone(err error) bool {
	return errors.Is(err, syscall.ESRCH)
}

func forceTerminateCommandProcessGroupImpl(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	return nil
}

// processGroupAliveImpl probes the group with signal 0, the only portable way
// to ask whether any process still belongs to it. ESRCH is a definitive "gone";
// EPERM or any other failure cannot prove the group empty, so that becomes an
// unknown-yes and is recorded by the caller instead of being silently read as
// "still running".
func processGroupAliveImpl(pgid int) (bool, bool) {
	if pgid <= 0 {
		return false, true
	}
	err := syscall.Kill(-pgid, 0)
	switch {
	case err == nil:
		return true, true
	case errors.Is(err, syscall.ESRCH):
		return false, true
	default:
		return true, false
	}
}

// processGroupMembersImpl lists the live members of pgid, best effort. The
// process table is read through ps because the alternative (a per-platform
// /proc or sysctl scan) would add a kernel-structure parser per OS for a
// witness that is only read at the moment a tracked command exits. The group
// leader is excluded: it is the tracked command itself, and a witness that
// contains a process known to be leaving would go stale immediately.
func processGroupMembersImpl(pgid int) []int {
	if pgid <= 0 {
		return nil
	}
	out, err := exec.Command("ps", "-Ao", "pid=,pgid=").Output()
	if err != nil {
		log.Debugf("process group witness unavailable pgid=%v error=%v", pgid, err)
		return nil
	}
	return parseProcessGroupMembers(string(out), pgid)
}

// parseProcessGroupMembers reads the "pid pgid" rows of ps -Ao pid=,pgid= and
// returns the members of pgid other than the leader.
func parseProcessGroupMembers(output string, pgid int) []int {
	var members []int
	for line := range strings.SplitSeq(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		group, groupErr := strconv.Atoi(fields[1])
		if pidErr != nil || groupErr != nil || group != pgid || pid == pgid {
			continue
		}
		members = append(members, pid)
	}
	return members
}
