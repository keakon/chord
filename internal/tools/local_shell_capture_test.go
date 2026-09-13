package tools

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// A shell that exits on its own while a daemonized descendant holds the output
// pipe must return once the wait delay expires, not when the descendant dies,
// and the shell's own exit status stays authoritative.
func TestRunLocalShellCaptureBoundsDescendantPipeHold(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not in PATH")
	}
	start := time.Now()
	out, err := runLocalShellCapture(context.Background(), "", "echo started; (sleep 30) &", 200*time.Millisecond)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("runLocalShellCapture: %v", err)
	}
	if elapsed >= 30*time.Second {
		t.Fatalf("Wait blocked on the descendant's pipe for %v", elapsed)
	}
	if !strings.Contains(out, "started") {
		t.Fatalf("captured output lost the shell's own line: %q", out)
	}
}

func TestRunLocalShellCaptureMapsWaitDelayToExitStatus(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not in PATH")
	}
	_, err := runLocalShellCapture(context.Background(), "", "(sleep 30) & exit 3", 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected the shell's exit status, got nil")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("err = %v, want exit code 3", err)
	}
}
