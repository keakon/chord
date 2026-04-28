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
