package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/keakon/chord/internal/tools"
)

const (
	shellBangTimeoutSec = 120
	shellBangMaxBytes   = 512 * 1024 // cap captured output for viewport performance
)

// runBangShell runs bash -c with a timeout and combined stdout/stderr capture.
// workDir may be empty to use the process working directory.
func runBangShell(workDir, bashLine string) (output string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), shellBangTimeoutSec*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", bashLine)
	if workDir != "" {
		cmd.Dir = workDir
	}
	// tools.TailBuffer keeps the newest output, like the shell tool's own
	// capture: a local command whose output outgrows the cap still shows the
	// failure at its end. It needs no lock because os/exec serializes writes
	// when Stdout and Stderr are the same writer.
	buf := tools.NewTailBuffer(shellBangMaxBytes)
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
