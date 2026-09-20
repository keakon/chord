//go:build unix

package tools

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestConfigureCommandProcessGroupUsesNewSession(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 0")
	configureCommandProcessGroup(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Fatalf("SysProcAttr = %#v, want Setsid=true", cmd.SysProcAttr)
	}
	if cmd.SysProcAttr.Setpgid {
		t.Fatalf("SysProcAttr = %#v, want Setpgid=false", cmd.SysProcAttr)
	}
}

func TestConfiguredCommandTTYAccessFailsFastWithoutControllingTTY(t *testing.T) {
	cmd := exec.Command("sh", "-c", "cat </dev/tty")
	configureCommandProcessGroup(cmd)
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
	origGrace := killGracePeriod
	killGracePeriod = 100 * time.Millisecond
	t.Cleanup(func() { killGracePeriod = origGrace })

	pidFile := t.TempDir() + "/sleep.pid"
	start := time.Now()
	out, err := ShellTool{}.Execute(context.Background(), mustMarshal(t, map[string]any{
		"command":    "sh -c 'trap \"\" TERM; (trap \"\" TERM; sleep 60) & echo $! >" + strconv.Quote(pidFile) + "; wait'",
		"timeout_ms": 1000,
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
func TestProcessGroupAliveFollowsTheRealGroup(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 30")
	configureCommandProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start command: %v", err)
	}
	pgid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
	})

	if alive, known := processGroupAlive(pgid); !alive || !known {
		t.Fatalf("processGroupAlive(running) = (%v, %v), want (true, true)", alive, known)
	}

	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill process group: %v", err)
	}
	_, _ = cmd.Process.Wait()

	deadline := time.Now().Add(2 * time.Second)
	for {
		alive, known := processGroupAlive(pgid)
		if !alive && known {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("processGroupAlive(reaped) = (%v, %v), want a definitively empty group", alive, known)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A non-positive id names no group, so the probe answers rather than
	// falling through to a signal against the caller's own group.
	if alive, known := processGroupAlive(0); alive || !known {
		t.Fatalf("processGroupAlive(0) = (%v, %v), want (false, true)", alive, known)
	}
}

func TestParseProcessGroupMembersFiltersLeaderAndForeignGroups(t *testing.T) {
	output := strings.Join([]string{
		" 100  100",
		" 101  100",
		" 102  100",
		" 103  200",
		"not a row",
		" 104  x",
		"",
	}, "\n")
	got := parseProcessGroupMembers(output, 100)
	if len(got) != 2 || got[0] != 101 || got[1] != 102 {
		t.Fatalf("parseProcessGroupMembers = %v, want [101 102] (leader and foreign groups excluded)", got)
	}
	if members := parseProcessGroupMembers(output, 0); members != nil {
		t.Fatalf("parseProcessGroupMembers(pgid=0) = %v, want nil", members)
	}
}

// Attribution needs a recorded member that is still in the group. Liveness
// alone proves nothing: a process can leave the group on purpose (setsid,
// setpgid) and stay alive, and a recorded pid can be recycled by another
// process, so a stop must re-read the group's members before it trusts a
// witness.
func TestGroupAttributionNeedsARecordedMemberStillInTheGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	cmd := exec.Command("sh", "-c", "sleep 30 & echo $! > "+strconv.Quote(pidFile)+"; exit 0")
	configureCommandProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start command: %v", err)
	}
	pgid := cmd.Process.Pid
	descendantPID := readJobPidFile(t, pidFile)
	t.Cleanup(func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
	})
	waitCh := waitForCommand(cmd)
	if !waitForCommandExit(waitCh, 5*time.Second) {
		t.Fatal("the direct command did not exit")
	}

	if groupAttributionLost(pgid, false, []int{os.Getpid()}) {
		t.Fatal("a still-running leader owns its group by definition")
	}
	if groupAttributionLost(pgid, true, nil) {
		t.Fatal("an empty witness is not proof that the group number was recycled")
	}
	if groupAttributionLost(pgid, true, []int{descendantPID}) {
		t.Fatal("a recorded member still in the group keeps the group attributed to this job")
	}
	if !groupAttributionLost(pgid, true, []int{os.Getpid()}) {
		t.Fatal("a live pid outside the group must not attribute the group to this job")
	}
	if !groupAttributionLost(pgid, true, []int{reapedProcessID(t)}) {
		t.Fatal("a witness that is entirely gone must stop attributing the group")
	}
}

// reapedProcessID returns the pid of a process that has already been waited
// for, so probes against it observe a process the kernel no longer has.
func reapedProcessID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start command: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait command: %v", err)
	}
	return pid
}

func TestBashTimeoutTerminatesBackgroundChild(t *testing.T) {
	pidFile := t.TempDir() + "/sleep.pid"
	out, err := ShellTool{}.Execute(context.Background(), mustMarshal(t, map[string]any{
		"command":    "sleep 60 & echo $! >" + strconv.Quote(pidFile) + "; wait",
		"timeout_ms": 1000,
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
