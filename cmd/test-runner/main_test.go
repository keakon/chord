//go:build unix

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/keakon/chord/internal/externalsupervisor"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case externalsupervisor.CommandName:
			os.Exit(supervise(os.Args[2:]))
		case "runner-fixture":
			signals := make(chan os.Signal, 2)
			signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
			code, _ := runCommand(os.Args[2], os.Args[3:], signals, os.Stdout, os.Stderr)
			os.Exit(code)
		case "child-fixture":
			os.Exit(childFixture(os.Args[2:]))
		}
	}
	os.Exit(m.Run())
}

func childFixture(args []string) int {
	work := os.TempDir()
	if err := os.WriteFile(filepath.Join(work, "payload"), []byte("sample"), 0o600); err != nil {
		panic(err)
	}
	for _, key := range []string{"TMPDIR", "TMP", "TEMP", "GOTMPDIR"} {
		if os.Getenv(key) != work {
			panic("temporary environment mismatch: " + key)
		}
	}
	switch args[0] {
	case "success":
		fmt.Println("sample output")
		fmt.Fprintln(os.Stderr, "sample error output")
		return 0
	case "failure":
		return 7
	case "uncleanable":
		if err := os.Chmod(work, 0o500); err != nil {
			panic(err)
		}
		return 0
	case "panic":
		panic("sample panic")
	case "hold":
		if err := os.WriteFile(args[1], []byte(work), 0o600); err != nil {
			panic(err)
		}
		time.Sleep(20 * time.Second)
		return 0
	case "escaped":
		if _, err := syscall.Setsid(); err != nil {
			panic(err)
		}
		if err := os.WriteFile(args[1], []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			panic(err)
		}
		time.Sleep(20 * time.Second)
		return 0
	case "spawn-escaped":
		cmd := exec.Command(os.Args[0], "child-fixture", "escaped", args[1])
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			panic(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(args[1]); err == nil {
				return 0
			}
			time.Sleep(time.Millisecond)
		}
		panic("escaped child did not start")
	}
	return 2
}

func TestCommandCleanup(t *testing.T) {
	for _, tc := range []struct {
		mode string
		code int
	}{
		{"success", 0},
		{"failure", 7},
		{"panic", 2},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			base := t.TempDir()
			cmd := runnerCommand(t, base, os.Args[0], "child-fixture", tc.mode)
			output, err := cmd.CombinedOutput()
			if got := commandCode(err); got != tc.code {
				t.Fatalf("exit = %d, want %d: %s", got, tc.code, output)
			}
			assertNoRuns(t, base)
			if tc.mode == "success" && (!bytes.Contains(output, []byte("sample output")) || !bytes.Contains(output, []byte("sample error output"))) {
				t.Fatalf("lost child output: %s", output)
			}
		})
	}
}

func TestLaunchFailureCleansDirectory(t *testing.T) {
	base := t.TempDir()
	cmd := runnerCommand(t, base, filepath.Join(base, "missing"))
	output, err := cmd.CombinedOutput()
	if commandCode(err) != 1 {
		t.Fatalf("unexpected exit: %v: %s", err, output)
	}
	assertNoRuns(t, base)
}

func TestCleanupFailurePreservesEvidenceForRetry(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	base := t.TempDir()
	cmd := runnerCommand(t, base, os.Args[0], "child-fixture", "uncleanable")
	output, err := cmd.CombinedOutput()
	if commandCode(err) != 1 || !bytes.Contains(output, []byte("permission denied")) {
		t.Fatalf("cleanup failure hidden: %v: %s", err, output)
	}
	entries, err := os.ReadDir(filepath.Join(base, runsDirectory))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "run-") {
			continue
		}
		dir := filepath.Join(base, runsDirectory, entry.Name())
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "work"), 0o700) })
		run := testRun{dir: dir, id: strings.TrimPrefix(entry.Name(), "run-")}
		if err := run.ready(); err != nil {
			t.Fatalf("failed cleanup lost its evidence: %v", err)
		}
		if err := os.Chmod(run.work(), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	next := runnerCommand(t, base, os.Args[0], "child-fixture", "success")
	if output, err := next.CombinedOutput(); err != nil {
		t.Fatalf("retry failed: %v: %s", err, output)
	}
	assertNoRuns(t, base)
}

func TestReapDeletionFailureKeepsDirectoryAndRunsCommand(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	base := t.TempDir()
	cmd := runnerCommand(t, base, os.Args[0], "child-fixture", "uncleanable")
	output, err := cmd.CombinedOutput()
	if commandCode(err) != 1 || !bytes.Contains(output, []byte("permission denied")) {
		t.Fatalf("cleanup failure hidden: %v: %s", err, output)
	}
	var dir string
	entries, err := os.ReadDir(filepath.Join(base, runsDirectory))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "run-") {
			dir = filepath.Join(base, runsDirectory, entry.Name())
		}
	}
	if dir == "" {
		t.Fatal("no retained run found")
	}
	work := filepath.Join(dir, "work")
	t.Cleanup(func() { _ = os.Chmod(work, 0o700) })
	// Recovery must report the undeletable stale run without failing the new
	// command; the next run retries once the work directory is writable again.
	blocked := runnerCommand(t, base, os.Args[0], "child-fixture", "success")
	blockedOutput, err := blocked.CombinedOutput()
	if err != nil {
		t.Fatalf("reap deletion failure blocked the run: %v: %s", err, blockedOutput)
	}
	if !bytes.Contains(blockedOutput, []byte("retained")) {
		t.Fatalf("missing retained diagnostic: %s", blockedOutput)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("stale run disappeared: %v", err)
	}
	if err := os.Chmod(work, 0o700); err != nil {
		t.Fatal(err)
	}
	last := runnerCommand(t, base, os.Args[0], "child-fixture", "success")
	if output, err := last.CombinedOutput(); err != nil {
		t.Fatalf("recovery failed: %v: %s", err, output)
	}
	assertNoRuns(t, base)
}

func TestSignalsCleanDirectory(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			base := t.TempDir()
			marker := filepath.Join(t.TempDir(), "ready")
			cmd := runnerCommand(t, base, os.Args[0], "child-fixture", "hold", marker)
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			startCommand(t, cmd)
			awaitFile(t, marker)
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			err := waitCommand(t, cmd)
			if commandCode(err) != 128+int(sig) {
				t.Fatalf("exit: %v: %s", err, output.String())
			}
			assertNoRuns(t, base)
		})
	}
}

func TestKilledRunnerRecoveredWithoutTouchingActiveRun(t *testing.T) {
	base := t.TempDir()
	markers := t.TempDir()
	first := runnerCommand(t, base, os.Args[0], "child-fixture", "hold", filepath.Join(markers, "first"))
	startCommand(t, first)
	work := string(awaitFile(t, filepath.Join(markers, "first")))
	runDir := filepath.Dir(work)
	second := runnerCommand(t, base, os.Args[0], "child-fixture", "hold", filepath.Join(markers, "second"))
	startCommand(t, second)
	secondWork := string(awaitFile(t, filepath.Join(markers, "second")))
	if err := syscall.Kill(-first.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = waitCommand(t, first)
	awaitFile(t, filepath.Join(runDir, readyFile))
	third := runnerCommand(t, base, os.Args[0], "child-fixture", "success")
	if output, err := third.CombinedOutput(); err != nil {
		t.Fatalf("recovery failed: %v: %s", err, output)
	}
	if _, err := os.Stat(runDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("killed run was not removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(secondWork, "payload")); err != nil {
		t.Fatalf("active run was changed: %v", err)
	}
	_ = second.Process.Signal(syscall.SIGTERM)
	_ = waitCommand(t, second)
	assertNoRuns(t, base)
}

func TestEscapedWriterRetainsDirectory(t *testing.T) {
	base := t.TempDir()
	marker := filepath.Join(t.TempDir(), "escaped")
	cmd := runnerCommand(t, base, os.Args[0], "child-fixture", "spawn-escaped", marker)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	startCommand(t, cmd)
	pid, err := strconv.Atoi(string(awaitFile(t, marker)))
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	if err = waitCommand(t, cmd); commandCode(err) != 1 {
		t.Fatalf("escaped writer accepted: %v: %s", err, output.String())
	}
	entries, err := os.ReadDir(filepath.Join(base, runsDirectory))
	if err != nil {
		t.Fatal(err)
	}
	var dir string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "run-") {
			dir = filepath.Join(base, runsDirectory, e.Name())
		}
	}
	if dir == "" || !strings.Contains(output.String(), "retained") {
		t.Fatalf("missing retained directory diagnostic: %s", output.String())
	}
	if _, err := os.Stat(filepath.Join(dir, readyFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("escaped writer produced cleanup proof: %v", err)
	}
	// Even after acquiring its lock, another run must preserve this unknown.
	next := runnerCommand(t, base, os.Args[0], "child-fixture", "success")
	if output, err := next.CombinedOutput(); err != nil {
		t.Fatalf("next run failed: %v: %s", err, output)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("unproven run removed: %v", err)
	}
}

func TestTimeoutCleanup(t *testing.T) {
	base := t.TempDir()
	cmd := runnerCommand(t, base, os.Args[0], "-test.run=^TestTimeoutFixture$", "-test.timeout=100ms")
	cmd.Env = append(os.Environ(), "CHORD_RUNNER_TIMEOUT_FIXTURE=1")
	output, err := cmd.CombinedOutput()
	if err == nil || !bytes.Contains(output, []byte("test timed out")) {
		t.Fatalf("timeout was not exercised: %v: %s", err, output)
	}
	assertNoRuns(t, base)
}

func TestTimeoutFixture(t *testing.T) {
	if os.Getenv("CHORD_RUNNER_TIMEOUT_FIXTURE") != "1" {
		return
	}
	if err := os.WriteFile(filepath.Join(t.TempDir(), "sample"), []byte("sample"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-t.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("timeout did not fire")
	}
}

func runnerCommand(t *testing.T, base string, args ...string) *exec.Cmd {
	t.Helper()
	argv := append([]string{"runner-fixture", base}, args...)
	cmd := exec.CommandContext(t.Context(), os.Args[0], argv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.WaitDelay = 10 * time.Second
	return cmd
}

func startCommand(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
}

func waitCommand(t *testing.T, cmd *exec.Cmd) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("runner did not finish")
		return nil
	}
}

func awaitFile(t *testing.T, path string) []byte {
	t.Helper()
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return data
		}
		select {
		case <-timeout.C:
			t.Fatalf("file did not appear: %s", path)
		case <-ticker.C:
		}
	}
}

func commandCode(err error) int {
	if err == nil {
		return 0
	}
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		return exit.ExitCode()
	}
	return -1
}

func assertNoRuns(t *testing.T, base string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(base, runsDirectory))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != registryLock {
			t.Errorf("leftover: %s", e.Name())
		}
	}
}

func TestRunLockAndUnknownRecords(t *testing.T) {
	base := t.TempDir()
	var diagnostics bytes.Buffer
	first, err := newRun(base, &diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newRun(base, &diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	_ = second.lock.Close()
	_ = first.lock.Close()
	third, err := newRun(base, &diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	defer third.lock.Close()
	for _, dir := range []string{first.dir, second.dir} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("unknown run removed: %v", err)
		}
	}
	if strings.Count(diagnostics.String(), "retained") != 2 {
		t.Fatalf("unknown states were not reported: %s", diagnostics.String())
	}
}

func TestRefusesSharedOrSymlinkRoot(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(strconv.FormatBool(symlink), func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, runsDirectory)
			if symlink {
				if err := os.Symlink(t.TempDir(), root); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(root, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := newRun(base, io.Discard); err == nil {
				t.Fatal("accepted unsafe root")
			}
		})
	}
}
