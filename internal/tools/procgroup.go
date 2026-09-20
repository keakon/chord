package tools

import (
	"os/exec"
)

// configureCommandProcessGroup puts the command in its own process group so its
// descendants can be signalled as one unit.
func configureCommandProcessGroup(cmd *exec.Cmd) {
	configureCommandProcessGroupImpl(cmd)
}

func terminateCommandProcessGroup(cmd *exec.Cmd) error {
	return terminateCommandProcessGroupImpl(cmd)
}

func forceTerminateCommandProcessGroup(cmd *exec.Cmd) error {
	return forceTerminateCommandProcessGroupImpl(cmd)
}

// processGroupAlive reports whether any process still belongs to the process
// group pgid. known is false when the probe could not decide (permission or
// platform limits); alive is then the conservative guess, so an unreadable
// probe can never be mistaken for a drained group.
func processGroupAlive(pgid int) (alive, known bool) {
	return processGroupAliveImpl(pgid)
}

// processGroupMembers lists the descendants that currently belong to pgid,
// excluding the group leader (the tracked command itself). The result is a
// best-effort witness for the stop path: see groupAttributionLost for how it is
// used and where it stops being proof.
func processGroupMembers(pgid int) []int {
	return processGroupMembersImpl(pgid)
}
