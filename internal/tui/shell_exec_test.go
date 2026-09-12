package tui

import (
	"os/exec"
	"strings"
	"testing"
)

func TestRunBangShell(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not in PATH")
	}
	dir := t.TempDir()
	out, err := runBangShell(dir, "echo chord-bang-test")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "chord-bang-test" {
		t.Fatalf("output = %q", out)
	}
}

func TestRunBangShellExitErrorStillCapturesOutput(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not in PATH")
	}
	out, err := runBangShell(t.TempDir(), "echo out; exit 42")
	if err == nil {
		t.Fatal("expected error for exit 42")
	}
	if !strings.Contains(out, "out") {
		t.Fatalf("expected stdout in output, got %q", out)
	}
}

// A local ! command that outgrows the capture cap must keep its newest output,
// the same tail the shell tool keeps: the failure that made the output long
// lands at the end, so a stale head would hide exactly what the reader needs.
func TestRunBangShellKeepsTheNewestOutput(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not in PATH")
	}
	if _, err := exec.LookPath("yes"); err != nil {
		t.Skip("yes not in PATH")
	}
	// Well past shellBangMaxBytes, with a marker at the very end.
	out, err := runBangShell(t.TempDir(), "yes x | head -c 600000; printf END_MARKER")
	if err != nil {
		t.Fatalf("runBangShell: %v", err)
	}
	if !strings.Contains(out, "END_MARKER") {
		t.Fatalf("captured output dropped the newest bytes (len=%d)", len(out))
	}
	if !strings.HasPrefix(out, "...(output truncated:") {
		t.Fatalf("captured output is missing the truncation notice: %.60q", out)
	}
	if len(out) >= 600000 {
		t.Fatalf("captured output len = %d, want the window bounded below the produced size", len(out))
	}
}
