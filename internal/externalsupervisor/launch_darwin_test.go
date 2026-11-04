package externalsupervisor

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func waitZombie(t *testing.T, pid int) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
		if err == nil && info.Proc.P_stat == 5 { // Darwin SZOMB.
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatalf("process %d did not exit before the identity query", pid)
		}
	}
}

func TestLaunchBindsExitedChild(t *testing.T) {
	for _, exitCode := range []int{0, 7} {
		for _, timing := range []string{"before query", "after query", "query failure", "missing identity"} {
			t.Run(fmt.Sprintf("%d/%s", exitCode, timing), func(t *testing.T) {
				dir := t.TempDir()
				runID, err := NewRunID()
				if err != nil {
					t.Fatal(err)
				}
				stdinR, stdinW, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				outR, outW, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				hostR, hostW, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				stdout, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				stderr, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer closeAll(stdinR, stdinW, outR, outW, hostR, hostW, stdout, stderr)
				request := Message{
					Type: TypeStart, RunID: runID, ReceiptDir: dir, Command: "/bin/sh",
					Args: []string{"-c", fmt.Sprintf("read gate; exit %d", exitCode)}, KillGraceMS: 500,
				}
				original := managedProcessIdentity
				managedProcessIdentity = func(cmd *exec.Cmd) (int, int, string, error) {
					pid := cmd.Process.Pid
					t.Cleanup(func() {
						if cmd.ProcessState == nil {
							_ = cmd.Process.Kill()
							_ = cmd.Wait()
						}
					})
					var pgid int
					var token string
					var identityErr error
					if timing == "after query" {
						_, pgid, token, identityErr = original(cmd)
					}
					if _, err := stdinW.Write([]byte("continue\n")); err != nil {
						t.Fatal(err)
					}
					waitZombie(t, pid)
					if timing != "after query" {
						_, pgid, token, identityErr = original(cmd)
					}
					if timing == "query failure" && identityErr == nil {
						identityErr = errors.New("injected identity rejection")
					}
					if timing == "missing identity" {
						return pid, 0, "", errors.New("injected identity read failure")
					}
					return pid, pgid, token, identityErr
				}
				defer func() { managedProcessIdentity = original }()
				code := launchManaged(outW, request, bufio.NewReader(hostR), stdinR, stdout, stderr, nil, 500*time.Millisecond)
				receipt, err := LoadReceipt(dir, runID)
				if err != nil {
					t.Fatal(err)
				}
				if timing == "missing identity" {
					if code != ExitFailure || receipt.Reason != ReasonLaunchFailed || receipt.Cleanup.Proven {
						t.Fatalf("unknown identity produced cleanup proof: code=%d receipt=%+v", code, receipt)
					}
					return
				}
				if receipt.StartToken == "" || !receipt.Cleanup.Proven {
					t.Fatalf("missing cleanup evidence: %+v", receipt)
				}
				if timing == "query failure" {
					if code != ExitFailure || receipt.Reason != ReasonLaunchFailed {
						t.Fatalf("rejected launch: code=%d receipt=%+v", code, receipt)
					}
				} else if code != ExitOK || receipt.Reason != ReasonChildExit || receipt.ExitCode != exitCode {
					t.Fatalf("fast exit: code=%d receipt=%+v", code, receipt)
				}
			})
		}
	}
}

func TestZombieLeaderIdentityAndDescendantCleanup(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "sleep 30 & exit 0")
	configureProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	defer func() {
		if cmd.ProcessState == nil {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			_ = cmd.Wait()
		}
	}()
	waitZombie(t, pid)
	_, token, err := readProcessIdentity(pid)
	if err != nil {
		t.Fatal(err)
	}
	if got := probeGroup(pid, pid, token); got != GroupAlive {
		t.Fatalf("zombie with descendants: %s, want alive", got)
	}
	for _, identity := range []struct {
		pgid  int
		token string
	}{{pid, token + ":other"}, {os.Getpid(), token}} {
		if _, mismatchErr := terminateGroup(pid, identity.pgid, identity.token, time.Millisecond); !errors.Is(mismatchErr, ErrIdentityMismatch) {
			t.Fatalf("mismatched identity accepted: %v", mismatchErr)
		}
	}
	done := make(chan error, 1)
	startChildReaper(cmd, done)
	cleanup, err := terminateGroup(pid, pid, token, time.Second)
	select {
	case waitErr := <-done:
		if waitErr != nil {
			t.Fatal(waitErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("leader was not reaped")
	}
	if err != nil || !cleanup.Proven || !cleanup.Terminated {
		t.Fatalf("descendant cleanup: %+v, %v", cleanup, err)
	}
}
