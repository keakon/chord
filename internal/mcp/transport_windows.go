//go:build windows

package mcp

import "os/exec"

func configureStdioCommand(cmd *exec.Cmd) {
	// Windows does not support Unix process groups; keep default process attributes.
	_ = cmd
}

// terminateStdioCommand stops the child process. Windows does not distinguish
// graceful and forceful termination here — Process.Kill is roughly equivalent
// to SIGKILL — so the force flag is accepted for cross-platform parity but
// ignored.
func terminateStdioCommand(cmd *exec.Cmd, _ bool) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
