//go:build darwin

package power

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDarwinNativePowerHelper(t *testing.T) {
	path := os.Getenv("CHORD_NATIVE_POWER_PID_FILE")
	if path == "" {
		return
	}
	lease, err := NewBackend().Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	pid := lease.(*darwinLease).cmd.Process.Pid
	if err := os.WriteFile(path, []byte(strconv.Itoa(pid)), 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Minute)
}

// This uses a temporary assertion only; it never changes persistent power settings.
func TestDarwinNativeParentDeathReleasesAssertion(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "caffeinate.pid")
	cmd := exec.Command(os.Args[0], "-test.run=^TestDarwinNativePowerHelper$")
	cmd.Env = append(os.Environ(), "CHORD_NATIVE_POWER_PID_FILE="+pidFile)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	var caffeinatePID int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(pidFile); err == nil {
			caffeinatePID, _ = strconv.Atoi(string(data))
		}
		if caffeinatePID > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if caffeinatePID == 0 {
		t.Fatal("helper did not publish caffeinate PID")
	}
	defer func() {
		// Limit fallback cleanup to the exact caffeinate instance started by this test.
		out, _ := exec.Command("/bin/ps", "-p", strconv.Itoa(caffeinatePID), "-o", "command=").Output()
		if strings.TrimSpace(string(out)) == fmt.Sprintf("/usr/bin/caffeinate -i -w %d", cmd.Process.Pid) {
			_ = syscall.Kill(caffeinatePID, syscall.SIGKILL)
		}
	}()
	marker := fmt.Sprintf("pid %d(caffeinate):", caffeinatePID)
	var assertion string
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.Command("/usr/bin/pmset", "-g", "assertions").Output()
		if err != nil {
			t.Fatal(err)
		}
		var owned []string
		for line := range strings.SplitSeq(string(out), "\n") {
			if strings.Contains(line, marker) {
				owned = append(owned, strings.TrimSpace(line))
			}
		}
		if len(owned) > 0 {
			assertion = strings.Join(owned, "\n")
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(assertion, "PreventUserIdleSystemSleep") {
		t.Fatalf("missing system idle assertion: %s", assertion)
	}
	if strings.Contains(assertion, "Display") {
		t.Fatalf("unexpected display assertion: %s", assertion)
	}
	t.Logf("owned assertion before SIGKILL: %s", assertion)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()
	waited = true
	if err == nil {
		t.Fatal("helper unexpectedly exited successfully")
	}
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(caffeinatePID, 0); err == syscall.ESRCH {
			out, err := exec.Command("/usr/bin/pmset", "-g", "assertions").Output()
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(out), marker) {
				time.Sleep(20 * time.Millisecond)
				continue
			}
			t.Log("caffeinate process and owned assertion disappeared after parent SIGKILL")
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("caffeinate or its assertion remained after parent SIGKILL")
}
