//go:build unix

package tools

import (
	"os/exec"
	"syscall"
)

type unixProcessGroupHandle struct{}

func (unixProcessGroupHandle) Close() error { return nil }

func configureCommandProcessGroupImpl(cmd *exec.Cmd) (processGroupHandle, error) {
	if cmd == nil {
		return unixProcessGroupHandle{}, nil
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return unixProcessGroupHandle{}, nil
}

func terminateCommandProcessGroupImpl(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	return nil
}
