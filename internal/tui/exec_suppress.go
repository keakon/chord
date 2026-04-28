package tui

import (
	"bytes"
	"io"
	"os/exec"
)

func suppressExternalCommandOutput(cmd *exec.Cmd) *exec.Cmd {
	if cmd == nil {
		return nil
	}
	if cmd.Stdout == nil {
		cmd.Stdout = io.Discard
	}
	if cmd.Stderr == nil {
		cmd.Stderr = io.Discard
	}
	return cmd
}

func outputWithoutExternalCommandStderr(cmd *exec.Cmd) ([]byte, error) {
	if cmd == nil {
		return nil, nil
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if cmd.Stderr == nil {
		cmd.Stderr = io.Discard
	}
	err := cmd.Run()
	return stdout.Bytes(), err
}
