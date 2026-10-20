//go:build unix

package externalclaude

import (
	"errors"
	"os/exec"
	"syscall"
)

func configureProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil {
		return nil
	}
	// A dedicated session makes the CLI and every MCP/child process it spawns
	// killable as one unit.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return nil
}

func terminateGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}

func killGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

func processGroupAlive(cmd *exec.Cmd) bool {
	return cmd != nil && cmd.Process != nil && !errors.Is(syscall.Kill(-cmd.Process.Pid, 0), syscall.ESRCH)
}
