//go:build unix

package tools

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestConfigureCommandProcessGroupUsesNewSession(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 0")
	if _, err := configureCommandProcessGroup(cmd); err != nil {
		t.Fatalf("configureCommandProcessGroup: %v", err)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Fatalf("SysProcAttr = %#v, want Setsid=true", cmd.SysProcAttr)
	}
	if cmd.SysProcAttr.Setpgid {
		t.Fatalf("SysProcAttr = %#v, want Setpgid=false", cmd.SysProcAttr)
	}
}

func TestConfiguredCommandTTYAccessFailsFastWithoutControllingTTY(t *testing.T) {
	cmd := exec.Command("sh", "-c", "cat </dev/tty")
	_, _ = configureCommandProcessGroup(cmd)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	start := time.Now()
	err := cmd.Run()
	if err == nil {
		t.Fatalf("expected /dev/tty access to fail, output=%q", buf.String())
	}
	if time.Since(start) >= 2*time.Second {
		t.Fatalf("/dev/tty command took too long; output=%q err=%v", buf.String(), err)
	}
	lowerOutput := strings.ToLower(buf.String())
	if !strings.Contains(lowerOutput, "tty") && !strings.Contains(lowerOutput, "device") {
		t.Fatalf("/dev/tty failure output = %q, want tty/device diagnostic", buf.String())
	}
}

func TestBashTTYAccessFailsFastWithoutControllingTTY(t *testing.T) {
	start := time.Now()
	out, err := ShellTool{}.Execute(context.Background(), mustMarshal(t, map[string]any{
		"command": "cat </dev/tty",
		"timeout": 5,
	}))
	if err == nil {
		t.Fatal("expected /dev/tty command to fail or be rejected")
	}
	if time.Since(start) >= 2*time.Second {
		t.Fatalf("/dev/tty command took too long; output=%q err=%v", out, err)
	}
	if !strings.Contains(err.Error(), "interactive command rejected") {
		t.Fatalf("expected static /dev/tty rejection, got output=%q err=%v", out, err)
	}
}

func TestBashTimeoutForceKillsProcessGroupThatIgnoresSIGTERM(t *testing.T) {
	pidFile := t.TempDir() + "/sleep.pid"
	start := time.Now()
	out, err := ShellTool{}.Execute(context.Background(), mustMarshal(t, map[string]any{
		"command": "sh -c 'trap \"\" TERM; (trap \"\" TERM; sleep 60) & echo $! >" + strconv.Quote(pidFile) + "; wait'",
		"timeout": 1,
	}))
	if err == nil {
		t.Fatal("expected timeout")
	}
	if out != "" {
		t.Fatalf("expected no output, got %q", out)
	}
	if time.Since(start) > 7*time.Second {
		t.Fatalf("timeout cleanup took too long (%s); err=%v", time.Since(start), err)
	}
	pidBytes, readErr := exec.Command("cat", pidFile).Output()
	if readErr != nil {
		t.Fatalf("read child pid file: %v", readErr)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if parseErr != nil {
		t.Fatalf("parse child pid %q: %v", pidBytes, parseErr)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		kerr := syscall.Kill(pid, 0)
		if errors.Is(kerr, syscall.ESRCH) {
			return
		}
		if kerr != nil && !errors.Is(kerr, syscall.EPERM) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("child pid %d still appears to be running after forced termination", pid)
}
func TestBashTimeoutTerminatesBackgroundChild(t *testing.T) {
	pidFile := t.TempDir() + "/sleep.pid"
	out, err := ShellTool{}.Execute(context.Background(), mustMarshal(t, map[string]any{
		"command": "sleep 60 & echo $! >" + strconv.Quote(pidFile) + "; wait",
		"timeout": 1,
	}))
	if err == nil {
		t.Fatal("expected timeout")
	}
	if out != "" {
		t.Fatalf("expected no output, got %q", out)
	}
	pidBytes, readErr := exec.Command("cat", pidFile).Output()
	if readErr != nil {
		t.Fatalf("read child pid file: %v", readErr)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if parseErr != nil {
		t.Fatalf("parse child pid %q: %v", pidBytes, parseErr)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed-out child process pid %d still appears to be running", pid)
}
