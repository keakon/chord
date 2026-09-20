package tools

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

const (
	// localShellTimeout caps every synchronous local shell command: the TUI's
	// `!` lines and the headless local_shell command.
	localShellTimeout = 120 * time.Second
	// localShellMaxBytes caps the captured combined output the caller sees.
	localShellMaxBytes = 512 * 1024
)

// RunLocalShellCapture runs command under `bash -c` with a deadline and returns
// the newest combined output, the same tail-keeping capture the shell tool
// uses. workDir may be empty to use the process working directory.
func RunLocalShellCapture(ctx context.Context, workDir, command string) (string, error) {
	return runLocalShellCapture(ctx, workDir, command, commandWaitDelay)
}

// runLocalShellCapture takes the wait delay as a parameter so tests can shrink
// the grace period instead of waiting out the production value.
func runLocalShellCapture(ctx context.Context, workDir, command string, waitDelay time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, localShellTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	// The command runs in its own process group so the deadline reaches
	// daemonized descendants, and the wait delay bounds how long Wait keeps
	// draining pipes after the shell exited (or after the deadline killed it):
	// a descendant that holds stdout open forever must not wedge the caller.
	// This is the same teardown discipline the job registry applies.
	configureCommandProcessGroup(cmd)
	cmd.WaitDelay = waitDelay
	// CommandContext's default cancel kills only the direct child; with the
	// command in its own process group, terminate the group instead.
	cmd.Cancel = func() error { return terminateCommandProcessGroup(cmd) }
	if workDir != "" {
		cmd.Dir = workDir
	}
	// TailBuffer keeps the newest output, so a long command's failure at the
	// end is not the part that gets dropped. It needs no lock because os/exec
	// serializes writes when Stdout and Stderr are the same writer.
	buf := NewTailBuffer(localShellMaxBytes)
	cmd.Stdout = buf
	cmd.Stderr = buf
	err := cmd.Run()
	out := buf.String()
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return out, fmt.Errorf("timed out after %ds", int(localShellTimeout/time.Second))
	}
	// WaitDelay expiry means the shell exited successfully while a descendant
	// kept the pipes open: the recorded exit status is authoritative, and the
	// abandoned I/O only fed the capture buffer. A non-zero exit surfaces as
	// *exec.ExitError and never carries ErrWaitDelay, so success is the only
	// status this maps. The same mapping applies in the job registry's run loop.
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	return out, err
}
