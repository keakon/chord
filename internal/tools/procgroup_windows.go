//go:build windows

package tools

import (
	"os/exec"
)

type windowsProcessGroupHandle struct{}

func (windowsProcessGroupHandle) Close() error { return nil }

func configureCommandProcessGroupImpl(cmd *exec.Cmd) (processGroupHandle, error) {
	// Windows does not support Unix process groups; keep default process attributes.
	_ = cmd
	return windowsProcessGroupHandle{}, nil
}

func terminateCommandProcessGroupImpl(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

func forceTerminateCommandProcessGroupImpl(cmd *exec.Cmd) error {
	// On Windows we do not have a process group primitive; Process.Kill is the
	// only reliable option and is already forceful.
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
