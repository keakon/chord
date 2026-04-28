package tools

import (
	"os/exec"
)

type processGroupHandle interface {
	Close() error
}

func configureCommandProcessGroup(cmd *exec.Cmd) (processGroupHandle, error) {
	return configureCommandProcessGroupImpl(cmd)
}

func terminateCommandProcessGroup(cmd *exec.Cmd) error {
	return terminateCommandProcessGroupImpl(cmd)
}
