//go:build unix

package tools

import (
	"errors"
	"os/exec"
	"syscall"
)

type unixProcessGroupHandle struct{}

func (unixProcessGroupHandle) Close() error { return nil }

func configureCommandProcessGroupImpl(cmd *exec.Cmd) (processGroupHandle, error) {
	if cmd == nil {
		return unixProcessGroupHandle{}, nil
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return unixProcessGroupHandle{}, nil
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
