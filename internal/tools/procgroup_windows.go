//go:build windows

package tools

import (
	"errors"
	"os"
	"os/exec"
)

// tracksProcessGroups is false on Windows: there is no process-group primitive,
// so a job's completion is the direct process exiting, exactly as before.
const tracksProcessGroups = false

func configureCommandProcessGroupImpl(cmd *exec.Cmd) {
	// Windows does not support Unix process groups; keep default process attributes.
	_ = cmd
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

// processGroupAlreadyGone reports whether a failed termination means the
// process was already reaped, i.e. the command exited on its own before the
// stop signal arrived.
func processGroupAlreadyGone(err error) bool {
	return errors.Is(err, os.ErrProcessDone)
}

// processGroupAliveImpl cannot answer on Windows: there is no group to probe, so
// the result is an unknown-yes and callers fall back to the direct process.
func processGroupAliveImpl(pgid int) (bool, bool) {
	_ = pgid
	return true, false
}

// processGroupMembersImpl has no Windows equivalent: there is no group whose
// members could be witnessed.
func processGroupMembersImpl(pgid int) []int {
	_ = pgid
	return nil
}
