package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

const (
	shellBangTimeoutSec = 120
	shellBangMaxBytes   = 512 * 1024 // cap captured output for viewport performance
)

// cappedWriterShell mirrors tools.BashTool output limiting for local ! commands.
type cappedWriterShell struct {
	buf      []byte
	total    int64
	maxBytes int64
}

func (c *cappedWriterShell) Write(p []byte) (int, error) {
	c.total += int64(len(p))
	if remaining := c.maxBytes - int64(len(c.buf)); remaining > 0 {
		if int64(len(p)) <= remaining {
			c.buf = append(c.buf, p...)
		} else {
			c.buf = append(c.buf, p[:remaining]...)
		}
	}
	return len(p), nil
}

func (c *cappedWriterShell) String() string {
	s := string(c.buf)
	if c.total > c.maxBytes {
		s += fmt.Sprintf("\n...(output truncated: showed %d of %d bytes total)", len(c.buf), c.total)
	}
	return s
}

// runBangShell runs bash -c with a timeout and combined stdout/stderr capture.
// workDir may be empty to use the process working directory.
func runBangShell(workDir, bashLine string) (output string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), shellBangTimeoutSec*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", bashLine)
	if workDir != "" {
		cmd.Dir = workDir
	}
	buf := &cappedWriterShell{maxBytes: shellBangMaxBytes}
	cmd.Stdout = buf
	cmd.Stderr = buf
	err = cmd.Run()
	out := buf.String()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			if out != "" {
				return out, fmt.Errorf("timed out after %ds", shellBangTimeoutSec)
			}
			return "", fmt.Errorf("timed out after %ds", shellBangTimeoutSec)
		}
		return out, err
	}
	return out, nil
}
