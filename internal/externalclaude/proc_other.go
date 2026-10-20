//go:build !unix

package externalclaude

import (
	"fmt"
	"os/exec"
)

func configureProcessGroup(_ *exec.Cmd) error {
	return fmt.Errorf("managed external process groups are supported only on Unix")
}

func terminateGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

func killGroup(cmd *exec.Cmd) error {
	return terminateGroup(cmd)
}

func processGroupAlive(_ *exec.Cmd) bool { return false }
