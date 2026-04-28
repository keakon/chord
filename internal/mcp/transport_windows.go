//go:build windows

package mcp

import "os/exec"

func configureStdioCommand(cmd *exec.Cmd) {
	// Windows does not support Unix process groups; keep default process attributes.
	_ = cmd
}

func terminateStdioCommand(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
