//go:build unix

package externalsupervisor

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// GroupState is what a bound process-group identity resolves to right now.
type GroupState int

const (
	// GroupGone means nothing is left in the group.
	GroupGone GroupState = iota
	// GroupAlive means the group still has members.
	GroupAlive
	// GroupUnknown means the bound identity no longer describes one group:
	// the recorded pid now leads or belongs to a different group. Callers must
	// not signal it — the pid was recycled and the group is someone else's.
	GroupUnknown
)

func (s GroupState) String() string {
	switch s {
	case GroupGone:
		return "gone"
	case GroupAlive:
		return "alive"
	default:
		return "unknown"
	}
}

// ErrIdentityMismatch reports that a bound pid/pgid pair no longer describes
// the supervised process group, so a signal by that identity could hit an
// unrelated process.
var ErrIdentityMismatch = errors.New("supervised process identity no longer matches the bound process group")

func lifecycleOutputFile(fd int) (*os.File, error) {
	// Inherited descriptors arrive in blocking mode. NewFile only registers
	// nonblocking descriptors with the poller, which enforces write deadlines.
	if err := syscall.SetNonblock(fd, true); err != nil {
		return nil, fmt.Errorf("make lifecycle pipe nonblocking: %w", err)
	}
	return os.NewFile(uintptr(fd), "lifecycle-out"), nil
}

// configureProcessGroup makes the managed process lead its own session so the
// supervisor can signal it and every child it spawns as one unit.
func configureProcessGroup(cmd *exec.Cmd) {
	if cmd != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
}

// processGroupID is the group the managed process leads. A process that
// called setsid leads the group named by its own pid.
func processGroupID(cmd *exec.Cmd) (pid, pgid int, err error) {
	if cmd == nil || cmd.Process == nil {
		return 0, 0, errors.New("managed process was not started")
	}
	pid = cmd.Process.Pid
	pgid, err = syscall.Getpgid(pid)
	if err != nil {
		return pid, 0, fmt.Errorf("read managed process group: %w", err)
	}
	if pgid != pid {
		// A setsid leader always leads its own group; anything else means the
		// identity is not the one we can signal as a unit.
		return pid, pgid, fmt.Errorf("managed process %d leads group %d", pid, pgid)
	}
	return pid, pgid, nil
}

// probeGroup resolves a bound identity. It never treats a live pid as proof on
// its own: the group must still be the one that was bound. Once the leader is
// gone and its start token cannot be read, a live group id is attributed to the
// bound group: the members that keep it alive inherited it from that leader.
// Ruling out a recycled pid leading a new group under the same id needs a
// pidfd, which is not available on every supported platform. The alternative,
// refusing every unverifiable group, would leave Chord's own leaked children
// running and fail the task, so cleanup accepts that narrow window instead.
func probeGroup(pid, pgid int, startToken string) GroupState {
	if pgid <= 0 {
		return GroupUnknown
	}
	if startToken != "" {
		token, err := processStartToken(pid)
		switch {
		case err == nil:
			if token != startToken {
				return GroupUnknown
			}
			actual, err := syscall.Getpgid(pid)
			if err != nil || actual != pgid {
				return GroupUnknown
			}
			return GroupAlive
		case errors.Is(err, syscall.EIO):
			// Darwin reports EIO once the process was reaped. The token is
			// gone, but the group id may still belong to our leaked child.
			// Only treat the group as ours when the pid itself is also gone:
			// a live Getpgid means the pid was recycled and may lead or belong
			// to another group, so refuse to signal it.
			if _, gerr := syscall.Getpgid(pid); !errors.Is(gerr, syscall.ESRCH) {
				return GroupUnknown
			}
			if err := syscall.Kill(-pgid, 0); errors.Is(err, syscall.ESRCH) {
				return GroupGone
			}
			return GroupAlive
		case processStartTokenGone(err):
			// The leader is gone; a live group id means members still hold the
			// group it named.
			if err := syscall.Kill(-pgid, 0); errors.Is(err, syscall.ESRCH) {
				return GroupGone
			} else if err != nil {
				return GroupUnknown
			}
			return GroupAlive
		default:
			return GroupUnknown
		}
	}
	if err := syscall.Kill(-pgid, 0); errors.Is(err, syscall.ESRCH) {
		return GroupGone
	}
	actual, err := syscall.Getpgid(pid)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			// The leader is gone but the group is not: another member still
			// holds it, and the group id is still the one we bound.
			return GroupAlive
		}
		return GroupUnknown
	}
	if actual != pgid {
		return GroupUnknown
	}
	return GroupAlive
}

// terminateGroup tears the bound process group down: SIGTERM first, SIGKILL
// after grace if members remain, and a final proof that the group is gone.
// It refuses to signal an identity that no longer matches.
func terminateGroup(pid, pgid int, startToken string, grace time.Duration) (Cleanup, error) {
	switch state := probeGroup(pid, pgid, startToken); state {
	case GroupGone:
		return Cleanup{Proven: true, Note: "process group was already gone"}, nil
	case GroupUnknown:
		return Cleanup{}, fmt.Errorf("%w: group state is %s", ErrIdentityMismatch, state)
	}
	cleanup := Cleanup{Terminated: true}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	if waitGroupGone(pid, pgid, startToken, grace) {
		cleanup.Proven = true
		return cleanup, nil
	}
	cleanup.Killed = true
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	if waitGroupGone(pid, pgid, startToken, grace) {
		cleanup.Proven = true
		return cleanup, nil
	}
	return cleanup, fmt.Errorf("termination of process group %d is not proven", pgid)
}

func waitGroupGone(pid, pgid int, startToken string, timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for probeGroup(pid, pgid, startToken) != GroupGone {
		select {
		case <-ticker.C:
		case <-deadline.C:
			return probeGroup(pid, pgid, startToken) == GroupGone
		}
	}
	return true
}

func processStartTokenGone(err error) bool {
	return errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.ENOENT) || errors.Is(err, os.ErrNotExist)
}

// setCloseOnExec keeps a descriptor out of every process the supervisor
// starts. Extra files are inherited without the flag, so the lifecycle channel
// has to be closed to the managed process explicitly.
func setCloseOnExec(files ...*os.File) {
	for _, file := range files {
		if file != nil {
			syscall.CloseOnExec(int(file.Fd()))
		}
	}
}

// shutdownSignals are the signals that end a supervisor without ending its
// host: a terminal interrupt or a shutdown of Chord's own process group must
// not leave the managed process behind.
func shutdownSignals() []os.Signal {
	return []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP}
}

// exitStatus splits a managed process's wait error into an exit code and a
// terminating signal; both stay zero when the process exited cleanly.
func exitStatus(err error) (code, signal int) {
	if err == nil {
		return 0, 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return 1, 0
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		return exitErr.ExitCode(), 0
	}
	if status.Signaled() {
		return 0, int(status.Signal())
	}
	return status.ExitStatus(), 0
}
