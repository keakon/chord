//go:build unix

package main

import (
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

const (
	sessionChildGroupHelperEnv  = "CHORD_TEST_SESSION_CHILD_GROUP_HELPER"
	sessionChildGroupPidFileEnv = "CHORD_TEST_SESSION_CHILD_GROUP_PID_FILE"
)

func TestConfigureChildProcessGroupUsesSetpgid(t *testing.T) {
	cmd := exec.Command("sleep", "1")
	configureChildProcessGroup(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatalf("SysProcAttr = %#v, want Setpgid=true", cmd.SysProcAttr)
	}
	if cmd.SysProcAttr.Setsid {
		t.Fatalf("SysProcAttr = %#v, want Setsid=false: the child stays in the frontend's session", cmd.SysProcAttr)
	}
}

// TestSessionChildGroupHelperProcess is not a test: it is the session child
// TestStopTerminatesChildProcessGroup starts. Like a real child it leaves a
// helper process of its own running — one the frontend can only reach through
// the group — reports that helper's pid, and then waits to be stopped.
func TestSessionChildGroupHelperProcess(t *testing.T) {
	if os.Getenv(sessionChildGroupHelperEnv) != "1" {
		t.Skip("helper process for TestStopTerminatesChildProcessGroup")
	}
	helper := exec.Command("sleep", "60")
	if err := helper.Start(); err != nil {
		os.Exit(2)
	}
	if err := os.WriteFile(os.Getenv(sessionChildGroupPidFileEnv), []byte(strconv.Itoa(helper.Process.Pid)), 0o600); err != nil {
		os.Exit(3)
	}
	select {}
}

// TestStopTerminatesChildProcessGroup covers what a stop has to reach: not only
// the session child but the helpers that share its process group. The child
// here starts one and then waits, so a stop that signalled the child alone
// would leave the helper running.
func TestStopTerminatesChildProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "helper.pid")
	cmd := exec.Command(os.Args[0], "-test.run=TestSessionChildGroupHelperProcess")
	cmd.Env = append(os.Environ(), sessionChildGroupHelperEnv+"=1", sessionChildGroupPidFileEnv+"="+pidFile)
	child, err := startChildProcess(cmd)
	if err != nil {
		t.Fatalf("startChildProcess: %v", err)
	}
	t.Cleanup(func() {
		_ = child.Stop(0)
		_ = child.Close()
	})
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatalf("session child SysProcAttr = %#v, want Setpgid=true", cmd.SysProcAttr)
	}
	helperPID := waitForProcessGroupHelperPid(t, pidFile)

	if err := child.Stop(0); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	waitForProcessGone(t, helperPID)
}

// waitForProcessGroupHelperPid waits for the helper process to report its pid.
func waitForProcessGroupHelperPid(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the session child never reported its helper pid in %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForProcessGone waits until pid names no process. A reaped process reports
// ESRCH, which is the only answer that proves it is gone.
func waitForProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pid %d still exists after the child's process group was terminated", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
