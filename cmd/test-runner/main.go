//go:build unix

// test-runner owns temporary files for one local test command.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/keakon/chord/internal/buildcache"
	"github.com/keakon/chord/internal/externalsupervisor"
)

const cleanupGrace = 2 * time.Second

func main() {
	if len(os.Args) > 1 && os.Args[1] == externalsupervisor.CommandName {
		os.Exit(supervise(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == cacheRunnerFlag {
		os.Exit(runCached(os.Args[2:]))
	}
	os.Exit(runMain(os.Args[1:], ""))
}

func runMain(args []string, executable string) int {
	defaultTemp := len(args) > 0 && args[0] == "--default-temp"
	if defaultTemp {
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: test-runner command [args...]")
		return 2
	}
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	var base string
	if !defaultTemp {
		resolver := exec.Command("./scripts/test_tmpdir.sh")
		resolver.Stderr = os.Stderr
		output, err := resolver.Output()
		if err != nil {
			fmt.Fprintln(os.Stderr, "test-runner: resolve temporary directory:", err)
			return 1
		}
		base = strings.TrimSpace(string(output))
	}
	if base == "" {
		base = os.TempDir()
	}
	cacheBase, err := os.UserCacheDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "test-runner: resolve cache directory:", err)
		return 1
	}
	code, cleaned := runCommand(base, filepath.Join(cacheBase, "chord", "build-cache"), executable, args, signals, os.Stdout, os.Stderr)
	if cleaned && !defaultTemp {
		release := exec.Command("./scripts/test_tmpdir.sh", "--release", base)
		release.Stderr = os.Stderr
		if err := release.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "test-runner: release temporary disk:", err)
			if code == 0 {
				code = 1
			}
		}
	}
	return code
}

func runCommand(base, cacheRoot, executable string, args []string, signals <-chan os.Signal, stdout, stderr io.Writer) (code int, cleaned bool) {
	run, err := newRun(base, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "test-runner:", err)
		return 1, false
	}
	defer run.lock.Close()
	cache, err := buildcache.Open(cacheRoot)
	if err != nil {
		fmt.Fprintln(stderr, "test-runner:", err)
		return 1, run.remove() == nil
	}
	if err := cache.Begin(run.id); err != nil {
		fmt.Fprintln(stderr, "test-runner:", err)
		return 1, run.remove() == nil
	}
	trimOnReturn := true
	defer func() {
		record := filepath.Join(cacheRoot, "users", run.id)
		if _, err := os.Lstat(record); err == nil {
			fmt.Fprintf(stderr, "test-runner: retained cache usage record %s; automatic eviction remains blocked; verify command cleanup before removing the record\n", record)
		}
		if trimOnReturn {
			trimBuildCache(cache, stderr)
		}
	}()
	remove := func() bool {
		if err := cache.End(run.id); err != nil {
			fmt.Fprintln(stderr, "test-runner:", err)
			return false
		}
		if removeErr := run.remove(); removeErr != nil {
			fmt.Fprintf(stderr, "test-runner: retained %s: %v\n", run.dir, removeErr)
			return false
		}
		return true
	}
	select {
	case sig := <-signals:
		return signalCode(sig), remove()
	default:
	}
	if executable == "" {
		executable, err = os.Executable()
		if err != nil {
			fmt.Fprintln(stderr, "test-runner: locate executable:", err)
			return 1, remove()
		}
	}
	fmt.Fprintf(stdout, "test-runner: TMPDIR=%s\n", run.work())
	sup, err := externalsupervisor.Start(externalsupervisor.Config{
		Command: []string{executable, externalsupervisor.CommandName, run.dir, run.id, cacheRoot},
		RunID:   run.id, ReceiptDir: run.dir, KillGrace: cleanupGrace,
		Child: externalsupervisor.ChildSpec{
			Command: args[0], Args: args[1:], Env: testEnvironment(run.work(), cache.Dir()),
		},
	})
	if err != nil {
		fmt.Fprintln(stderr, "test-runner: start command:", err)
		if _, known := externalsupervisor.StartCleanupProven(err); !known {
			return 1, remove()
		}
		receipt, receiptErr := externalsupervisor.LoadReceipt(run.dir, run.id)
		if run.ready() == nil || (receiptErr == nil && !receipt.ProcessStarted && receipt.Cleanup.Proven) {
			return 1, remove()
		}
		fmt.Fprintf(stderr, "test-runner: retained %s: command cleanup is unproven\n", run.dir)
		return 1, false
	}
	// Tests are noninteractive. Keeping stdin open can leave a child waiting
	// forever for input that make will never supply.
	_ = sup.Stdin().Close()
	drained := make(chan error, 2)
	go copyOutput(stdout, sup.Stdout(), drained)
	go copyOutput(stderr, sup.Stderr(), drained)
	waited := make(chan error, 1)
	go func() { waited <- sup.Wait() }()
	select {
	case err = <-waited:
	case sig := <-signals:
		code = signalCode(sig)
		err = sup.Stop(time.Millisecond)
		<-waited
	}
	if err != nil {
		err = errors.Join(err, sup.Stop(time.Millisecond))
	}
	report := sup.Report()
	if code == 0 {
		code = report.ExitCode
		if report.ExitSignal != 0 {
			code = 128 + report.ExitSignal
		}
	}
	timer := time.NewTimer(cleanupGrace)
	defer timer.Stop()
	for range 2 {
		select {
		case copyErr := <-drained:
			err = errors.Join(err, copyErr)
		case <-timer.C:
			_ = sup.Stdout().Close()
			_ = sup.Stderr().Close()
			err = errors.Join(err, errors.New("output streams did not close"))
			goto finished
		}
	}
finished:
	if err != nil {
		fmt.Fprintln(stderr, "test-runner:", err)
		if code == 0 {
			code = 1
		}
	}
	// The supervisor adapter records both group termination and output EOF.
	// A group receipt alone cannot exclude a descendant in another session.
	if err := run.ready(); err != nil {
		fmt.Fprintf(stderr, "test-runner: retained %s: %v\n", run.dir, err)
		if code == 0 {
			code = 1
		}
		return code, false
	}
	// A successful supervisor exit includes its cache scan. Retry here when
	// cancellation or a failed launch may have interrupted that scan.
	trimOnReturn = err != nil
	cleaned = remove()
	if !cleaned && code == 0 {
		code = 1
	}
	return code, cleaned
}

func copyOutput(dst io.Writer, src io.ReadCloser, done chan<- error) {
	_, err := io.Copy(dst, src)
	if err != nil {
		// A broken output consumer must not prevent the child from exiting.
		_, _ = io.Copy(io.Discard, src)
	}
	_ = src.Close()
	done <- err
}

func signalCode(sig os.Signal) int {
	if s, ok := sig.(syscall.Signal); ok {
		return 128 + int(s)
	}
	return 1
}

func testEnvironment(work, cacheDir string) []string {
	env := os.Environ()
	for _, key := range []string{"TMPDIR", "TMP", "TEMP", "GOTMPDIR"} {
		env = slices.DeleteFunc(env, func(entry string) bool {
			return strings.HasPrefix(entry, key+"=")
		})
		env = append(env, key+"="+work)
	}
	for key, value := range map[string]string{
		"GOCACHE": cacheDir, "GOCACHEPROG": "", "CHORD_TEST_RUNNER_ACTIVE": "1",
	} {
		env = slices.DeleteFunc(env, func(entry string) bool {
			return strings.HasPrefix(entry, key+"=")
		})
		env = append(env, key+"="+value)
	}
	return env
}

func trimBuildCache(cache *buildcache.Cache, diagnostics io.Writer) {
	result, err := cache.Trim()
	switch {
	case err != nil:
		fmt.Fprintln(diagnostics, "test-runner: cache cleanup deferred:", err)
	case result.Users > 0:
		fmt.Fprintf(diagnostics, "test-runner: cache trim skipped: %d registered cache user(s)\n", result.Users)
	case result.Removed > 0:
		fmt.Fprintf(diagnostics, "test-runner: cache reclaimed %.2f GiB (%d files), %.2f GiB retained\n",
			float64(result.Before-result.After)/(1<<30), result.Removed, float64(result.After)/(1<<30))
	}
	if result.OverBudget {
		fmt.Fprintln(diagnostics, "test-runner: cache remains over budget after cleanup")
	}
}
