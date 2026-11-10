//go:build unix

package main

import (
	"bytes"
	"encoding/json"
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
			code, _ := runCommand(os.Args[2], filepath.Join(os.Args[2], "cache"), "", os.Args[3:], signals, os.Stdout, os.Stderr)
			os.Exit(code)
		case "child-fixture":
			os.Exit(childFixture(os.Args[2:]))
		case "runner-cache-fixture":
			_, use, err := cacheRunner(os.Args[2], os.Args[3], os.Args[4])
			if err != nil {
				os.Exit(1)
			}
			if err := os.WriteFile(os.Args[5], []byte("ready"), 0o600); err != nil {
				os.Exit(1)
			}
			time.Sleep(20 * time.Second)
			_ = use.Close()
			os.Exit(0)
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

func TestReapDeletionFailureKeepsDirectoryAndRunsCommand(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	base := t.TempDir()
	id, err := externalsupervisor.NewRunID()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, runsDirectory, "run-"+id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Proven cleanup evidence plus an unremovable directory: recovery must
	// report the stale run without failing the new command, and the next run
	// retries once the directory is writable again. While the stale directory
	// remains, the shared work root sweep stays skipped.
	receipt := externalsupervisor.Receipt{Version: 2, RunID: id, Cleanup: externalsupervisor.Cleanup{Proven: true}}
	data, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, readyFile), []byte(id), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, activeLock), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	blocked := runnerCommand(t, base, os.Args[0], "child-fixture", "success")
	blockedOutput, err := blocked.CombinedOutput()
	if err != nil {
		t.Fatalf("reap deletion failure blocked the run: %v: %s", err, blockedOutput)
	}
	assertRetainedPreviousRun(t, string(blockedOutput), dir, "remove test run")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("stale run disappeared: %v", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
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
	_ = string(awaitFile(t, filepath.Join(markers, "first")))
	// The work root is shared, so the child's marker no longer identifies the
	// run directory: capture it while first's run is the only one in the root.
	runDir := onlyRunDir(t, base)
	second := runnerCommand(t, base, os.Args[0], "child-fixture", "hold", filepath.Join(markers, "second"))
	startCommand(t, second)
	secondWork := string(awaitFile(t, filepath.Join(markers, "second")))
	if err := syscall.Kill(-first.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = waitCommand(t, first)
	awaitFile(t, filepath.Join(runDir, readyFile))
	assertCacheUsers(t, base, 1)
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
	record := filepath.Join(base, "cache", "users", strings.TrimPrefix(filepath.Base(dir), "run-"))
	if !strings.Contains(output.String(), record) || !strings.Contains(output.String(), "verify command cleanup before removing the record") {
		t.Fatalf("missing cache usage recovery diagnostic: %s", output.String())
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
	assertCacheUsers(t, base, 1)
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
	assertCacheUsers(t, base, 0)
	entries, err := os.ReadDir(filepath.Join(base, runsDirectory))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == registryLock || e.Name() == workDirectory {
			continue
		}
		t.Errorf("leftover: %s", e.Name())
	}
}

// onlyRunDir returns the single run directory in the test run root.
func onlyRunDir(t *testing.T, base string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(base, runsDirectory))
	if err != nil {
		t.Fatal(err)
	}
	var dir string
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "run-") {
			continue
		}
		if dir != "" {
			t.Fatalf("multiple run directories: %s, %s", dir, e.Name())
		}
		dir = filepath.Join(base, runsDirectory, e.Name())
	}
	if dir == "" {
		t.Fatal("no run directory found")
	}
	return dir
}

// assertRetainedPreviousRun requires the stale-directory diagnostic for one
// previous run: the historical prefix, that directory's path, and the reason.
func assertRetainedPreviousRun(t *testing.T, output, dir, reason string) {
	t.Helper()
	const prefix = "test-runner: previous run directory retained; continuing setup: "
	for line := range strings.SplitSeq(output, "\n") {
		if strings.HasPrefix(line, prefix+dir+": ") && strings.Contains(line, reason) {
			return
		}
	}
	t.Fatalf("missing retained previous run diagnostic for %s (reason %q): %s", dir, reason, output)
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
	// A held lock means the owner may still be running: the directory is
	// skipped silently, and a missing receipt alone must not warn.
	if strings.Contains(diagnostics.String(), "previous run directory retained") {
		t.Fatalf("held locks reported as retained previous runs: %s", diagnostics.String())
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
		assertRetainedPreviousRun(t, diagnostics.String(), dir, "read process cleanup receipt")
	}
	if strings.Count(diagnostics.String(), "previous run directory retained") != 2 {
		t.Fatalf("unknown states were not reported: %s", diagnostics.String())
	}
}

// A previous run directory without a cleanup receipt is retained while the new
// command proceeds; the command's own result follows its existing rules.
func TestMissingReceiptRunRetainedAcrossCommandResults(t *testing.T) {
	for _, tc := range []struct {
		name string
		args func(base string) []string
		code int
	}{
		{"success", func(string) []string { return []string{os.Args[0], "child-fixture", "success"} }, 0},
		{"command failure", func(string) []string { return []string{os.Args[0], "child-fixture", "failure"} }, 7},
		{"launch failure", func(base string) []string { return []string{filepath.Join(base, "missing")} }, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			stale, err := newRun(base, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			if err := stale.lock.Close(); err != nil {
				t.Fatal(err)
			}
			cmd := runnerCommand(t, base, tc.args(base)...)
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			startCommand(t, cmd)
			err = waitCommand(t, cmd)
			if got := commandCode(err); got != tc.code {
				t.Fatalf("exit = %d, want %d: %s", got, tc.code, output.String())
			}
			assertRetainedPreviousRun(t, output.String(), stale.dir, "read process cleanup receipt")
			if _, err := os.Stat(stale.dir); err != nil {
				t.Fatalf("missing-receipt run removed: %v", err)
			}
			// The current command kept its own cleanup rules: only the stale
			// run directory and the shared work root remain beside the
			// registry lock.
			assertCacheUsers(t, base, 0)
			entries, err := os.ReadDir(filepath.Join(base, runsDirectory))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.Name() == registryLock || entry.Name() == workDirectory || entry.Name() == filepath.Base(stale.dir) {
					continue
				}
				t.Errorf("leftover: %s", entry.Name())
			}
		})
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
