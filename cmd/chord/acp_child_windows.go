//go:build windows

package main

import "os/exec"

// configureChildProcessGroup is a no-op on Windows: there is no process group
// that signal delivery can name, so nothing is set at spawn time.
func configureChildProcessGroup(*exec.Cmd) {}

// terminateChildProcess stops the child process itself. Windows cannot signal a
// process tree, so a grandchild outlives the session unless the frontend tracks
// it in a Job Object, which this stop sequence does not do. Closing the child's
// stdin is the graceful signal here, so only the forced stop kills anything.
func terminateChildProcess(cmd *exec.Cmd, force bool) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if !force {
		return nil
	}
	return cmd.Process.Kill()
}
