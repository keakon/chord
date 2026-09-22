//go:build unix

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// configureChildProcessGroup makes the session child lead its own process
// group, so a stop can signal the child and the helpers it spawned as one unit.
// Setpgid, not Setsid: the child has to leave the frontend's process group —
// otherwise a group signal would reach the frontend itself — while staying in
// its session, which is what keeps it attached to the terminal the frontend
// still owns.
func configureChildProcessGroup(cmd *exec.Cmd) {
	if cmd != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
}

// terminateChildProcess signals the child's whole process group: SIGTERM so a
// child that traps it can shut its runtime down, SIGKILL when it does not. The
// negative pid is what reaches the helpers a session child left in its group; a
// descendant that moved to a group of its own (Chord's jobs call Setsid, MCP
// servers Setpgid) is out of reach and has to be stopped by its own owner.
func terminateChildProcess(cmd *exec.Cmd, force bool) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	// The group number is the child's pid, so signalling a reaped child by
	// group could reach whoever holds that pid now. Process tracks the reap:
	// this probe turns into ErrProcessDone once the child has been waited for.
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		return err
	}
	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}
	if err := syscall.Kill(-cmd.Process.Pid, sig); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			// The group is gone; report the state a reaped process reports so
			// the stop sequence treats it as already finished.
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}
