//go:build unix

package externalsupervisor

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// writeManagedFixture writes a shell script standing in for the managed
// process. It runs under the supervisor, so it inherits nothing but the
// business pipes.
func writeManagedFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "managed")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write managed fixture: %v", err)
	}
	return path
}

// startSupervised starts a supervisor for one fixture process. The supervisor
// is this test binary: Chord re-executes the running executable, which the
// TestMain intercept turns into a supervisor.
func startSupervised(t *testing.T, body, receiptDir string) *Supervisor {
	t.Helper()
	if receiptDir != "" {
		if err := os.MkdirAll(receiptDir, 0o755); err != nil {
			t.Fatalf("create receipt dir: %v", err)
		}
	}
	sup, err := Start(Config{
		Child: ChildSpec{
			Command: writeManagedFixture(t, body),
			Env:     managedFixtureEnv(),
			Dir:     t.TempDir(),
		},
		ReceiptDir:       receiptDir,
		KillGrace:        time.Second,
		HandshakeTimeout: 20 * time.Second,
	})
	if err != nil {
		t.Fatalf("start supervisor: %v", err)
	}
	return sup
}

// managedFixtureEnv is the environment of the managed process. The coverage
// directory is passed through so a supervisor's own statements are counted with
// the test's, even though it runs in a separate process.
func managedFixtureEnv() []string {
	env := []string{"PATH=" + os.Getenv("PATH")}
	if dir := os.Getenv("GOCOVERDIR"); dir != "" {
		env = append(env, "GOCOVERDIR="+dir)
	}
	return env
}

func readReceipt(t *testing.T, dir string) Receipt {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read receipt dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("receipt dir has %d entries, want 1", len(entries))
	}
	data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read receipt: %v", err)
	}
	var receipt Receipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatalf("decode receipt %s: %v", data, err)
	}
	return receipt
}

func TestSupervisorBindsIdentityAndReportsExit(t *testing.T) {
	sup := startSupervised(t, "sleep 0.2\nexit 7\n", "")
	pid, pgid, ok := sup.Identity()
	if !ok || pid <= 0 || pgid <= 0 {
		t.Fatalf("identity = (%d, %d, %v), want a bound process group", pid, pgid, ok)
	}
	if pgid != pid {
		t.Fatalf("supervised process %d does not lead its own group %d", pid, pgid)
	}
	if got := probeGroup(pid, pgid); got != GroupAlive {
		t.Fatalf("group state = %v, want alive", got)
	}
	if err := sup.Stop(20 * time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
	report := sup.Report()
	if !report.Reported {
		t.Fatal("supervisor did not report the run")
	}
	if report.ExitCode != 7 {
		t.Fatalf("exit code = %d, want 7", report.ExitCode)
	}
	if !report.Cleanup.Proven {
		t.Fatalf("cleanup = %+v, want proven", report.Cleanup)
	}
	if !sup.GroupGone() {
		t.Fatal("managed process group survived its supervisor")
	}
}

// A killed Chord is the case the supervisor exists for: the lifecycle channel
// closes with the process, and the supervisor still tears the group down.
func TestSupervisorCleansUpWhenHostDisappears(t *testing.T) {
	receiptDir := filepath.Join(t.TempDir(), "receipts")
	sup := startSupervised(t, "sleep 300 &\nsleep 300\n", receiptDir)
	pid, pgid, ok := sup.Identity()
	if !ok {
		t.Fatal("supervisor did not bind an identity")
	}
	// Closing both host ends of the lifecycle channel is exactly what the
	// kernel does when Chord is killed with its descriptors still open.
	if err := sup.in.Close(); err != nil {
		t.Fatalf("close lifecycle channel: %v", err)
	}
	if err := sup.out.Close(); err != nil {
		t.Fatalf("close lifecycle channel: %v", err)
	}
	if err := sup.Wait(); err == nil {
		t.Fatal("a supervisor that lost its host reported a settled run")
	}
	if got := probeGroup(pid, pgid); got != GroupGone {
		t.Fatalf("group state after host death = %v, want gone", got)
	}
	receipt := readReceipt(t, receiptDir)
	if receipt.Reason != ReasonHostGone {
		t.Fatalf("receipt reason = %q, want %q", receipt.Reason, ReasonHostGone)
	}
	if receipt.PID != pid || receipt.PGID != pgid {
		t.Fatalf("receipt identity = (%d, %d), want (%d, %d)", receipt.PID, receipt.PGID, pid, pgid)
	}
	if !receipt.Cleanup.Proven {
		t.Fatalf("receipt cleanup = %+v, want proven", receipt.Cleanup)
	}
}

// The mirror case: the supervisor dies, Chord does not. Chord then has only
// the identity it bound at handshake — and that is enough.
func TestHostCleansUpWhenSupervisorDies(t *testing.T) {
	sup := startSupervised(t, "sleep 300\n", "")
	pid, pgid, ok := sup.Identity()
	if !ok {
		t.Fatal("supervisor did not bind an identity")
	}
	if err := sup.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill supervisor: %v", err)
	}
	if err := sup.Stop(2 * time.Second); err == nil {
		t.Fatal("a run no supervisor reported ended without an error")
	}
	if got := probeGroup(pid, pgid); got != GroupGone {
		t.Fatalf("group state after supervisor death = %v, want gone", got)
	}
}

// A bound identity that no longer describes one group must not be signalled:
// the pid was recycled and the group now belongs to somebody else.
func TestTerminateGroupRefusesMismatchedIdentity(t *testing.T) {
	sup := startSupervised(t, "sleep 300\n", "")
	defer func() { _ = sup.Stop(2 * time.Second) }()
	pid, pgid, ok := sup.Identity()
	if !ok {
		t.Fatal("supervisor did not bind an identity")
	}
	foreign, err := syscall.Getpgid(0)
	if err != nil {
		t.Fatalf("read test process group: %v", err)
	}
	if foreign == pgid {
		t.Skip("the test process shares the managed process group")
	}
	if _, err := terminateGroup(pid, foreign, 100*time.Millisecond); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("terminateGroup with a foreign group = %v, want %v", err, ErrIdentityMismatch)
	}
	if got := probeGroup(pid, pgid); got != GroupAlive {
		t.Fatalf("group state = %v, want alive: a mismatched identity still signalled", got)
	}
}

// A managed process that ignores SIGTERM must still end: the teardown
// escalates to SIGKILL and only claims success once the group is gone.
func TestSupervisorEscalatesToSIGKILL(t *testing.T) {
	receiptDir := filepath.Join(t.TempDir(), "receipts")
	sup := startSupervised(t, "printf 'ready\\n'\ntrap '' TERM\nwhile :; do sleep 0.2; done\n", receiptDir)
	pid, pgid, ok := sup.Identity()
	if !ok {
		t.Fatal("supervisor did not bind an identity")
	}
	// Wait until the fixture reports it is past the trap: a signal that arrives
	// before it is installed would say nothing about the escalation.
	buf := make([]byte, 16)
	if _, err := sup.Stdout().Read(buf); err != nil {
		t.Fatalf("read managed stdout: %v", err)
	} else if !strings.Contains(string(buf), "ready") {
		t.Fatalf("managed stdout = %q, want a ready marker", buf)
	}
	if err := sup.in.Close(); err != nil {
		t.Fatalf("close lifecycle channel: %v", err)
	}
	if err := sup.out.Close(); err != nil {
		t.Fatalf("close lifecycle channel: %v", err)
	}
	_ = sup.Wait()
	if got := probeGroup(pid, pgid); got != GroupGone {
		t.Fatalf("group state = %v, want gone", got)
	}
	receipt := readReceipt(t, receiptDir)
	if !receipt.Cleanup.Terminated || !receipt.Cleanup.Killed || !receipt.Cleanup.Proven {
		t.Fatalf("receipt cleanup = %+v, want SIGTERM then SIGKILL, proven", receipt.Cleanup)
	}
}

// The lifecycle channel is the supervisor's own contract with Chord: the
// managed process must not be able to read it, let alone forge a handshake.
func TestManagedProcessCannotInheritLifecycleChannel(t *testing.T) {
	sup := startSupervised(t, `
if : <&3 2>/dev/null; then printf 'inherited-3\n'; fi
if : <&4 2>/dev/null; then printf 'inherited-4\n'; fi
printf 'clean\n'
`, "")
	out, err := io.ReadAll(sup.Stdout())
	if err != nil {
		t.Fatalf("read managed stdout: %v", err)
	}
	if strings.Contains(string(out), "inherited") {
		t.Fatalf("managed process inherited the lifecycle channel: %q", out)
	}
	if !strings.Contains(string(out), "clean") {
		t.Fatalf("managed stdout = %q", out)
	}
	if err := sup.Stop(2 * time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestSupervisedRunKeepsNoReceiptWhenHostSettlesIt(t *testing.T) {
	receiptDir := filepath.Join(t.TempDir(), "receipts")
	sup := startSupervised(t, "exit 0\n", receiptDir)
	if err := sup.Stop(2 * time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
	entries, err := os.ReadDir(receiptDir)
	if err != nil {
		t.Fatalf("read receipt dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("receipt dir has %d entries, want none for a run Chord settled", len(entries))
	}
}

// No handshake, no run: a supervisor that never answers must not leave Chord
// talking to a process nobody owns, and it must not leave a process behind
// either.
func TestStartFailsWithoutHandshake(t *testing.T) {
	// A supervisor that never speaks: it reads nothing, answers nothing and
	// starts nothing.
	silent := writeManagedFixture(t, "sleep 60\n")
	_, err := Start(Config{
		Command:          []string{silent},
		Child:            ChildSpec{Command: writeManagedFixture(t, "sleep 300\n")},
		HandshakeTimeout: 200 * time.Millisecond,
		KillGrace:        time.Second,
	})
	if err == nil {
		t.Fatal("a supervisor that never answered the handshake was accepted")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %v, want a handshake timeout", err)
	}
	// The supervisor was started, so Chord has to be sure it is gone before the
	// caller gives up on the run.
	if commandRunning(t, silent) {
		t.Fatal("an unresponsive supervisor outlived the failed handshake")
	}
}

// commandRunning reports whether a process is still executing the given
// command line.
func commandRunning(t *testing.T, command string) bool {
	t.Helper()
	out, err := exec.Command("pgrep", "-f", command).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}
