//go:build unix

package externalsupervisor

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestProbeGroupRejectsMismatchedStartToken(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	configureProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	token, err := processStartToken(pid)
	if err != nil {
		t.Skipf("process start token is unavailable: %v", err)
	}
	if got := probeGroup(pid, pid, token); got != GroupAlive {
		t.Fatalf("probe of the bound identity = %s, want alive", got)
	}
	stale := token + ":recycled"
	if got := probeGroup(pid, pid, stale); got != GroupUnknown {
		t.Fatalf("probe with a mismatched token = %s, want unknown", got)
	}
	if _, err := terminateGroup(pid, pid, stale, 20*time.Millisecond); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("cleanup with a mismatched token = %v, want ErrIdentityMismatch", err)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("mismatched identity signalled the live process: %v", err)
	}
}
