//go:build unix

package externalsupervisor

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestReadProcessIdentityMatchesCurrentProcess(t *testing.T) {
	pid := os.Getpid()
	pgid, token, err := readProcessIdentity(pid)
	if err != nil {
		t.Fatal(err)
	}
	want, err := unix.Getpgid(pid)
	if err != nil {
		t.Fatal(err)
	}
	if pgid != want || ((runtime.GOOS == "darwin" || runtime.GOOS == "linux") && token == "") {
		t.Fatalf("identity = (%d, %q), want group %d and a start token on supported platforms", pgid, token, want)
	}
}

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
	_, token, err := readProcessIdentity(pid)
	if err != nil {
		t.Skipf("process start token is unavailable: %v", err)
	}
	if got, err := probeGroup(pid, pid, token); got != GroupAlive || err != nil {
		t.Fatalf("probe of the bound identity = %s, %v, want alive", got, err)
	}
	stale := token + ":recycled"
	if got, err := probeGroup(pid, pid, stale); got != GroupUnknown || !errors.Is(err, ErrIdentityMismatch) || !strings.Contains(err.Error(), stale) || !strings.Contains(err.Error(), "has group") {
		t.Fatalf("probe with a mismatched token = %s, %v, want unknown with identity details", got, err)
	}
	if _, err := terminateGroup(pid, pid, stale, 20*time.Millisecond); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("cleanup with a mismatched token = %v, want ErrIdentityMismatch", err)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("mismatched identity signalled the live process: %v", err)
	}
}
