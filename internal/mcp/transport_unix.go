//go:build unix

package mcp

import (
	"os/exec"
	"syscall"
)

func configureStdioCommand(cmd *exec.Cmd) {
	if cmd != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
}

// terminateStdioCommand signals the child process group. When force is false a
// SIGTERM is sent so well-behaved children can shut down cleanly; when force is
// true SIGKILL is sent, which cannot be trapped or ignored. Always signalling
// the process group (negative pid) ensures grandchildren are reaped too.
func terminateStdioCommand(cmd *exec.Cmd, force bool) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}
	_ = syscall.Kill(-cmd.Process.Pid, sig)
}
