//go:build unix

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/keakon/chord/internal/externalsupervisor"
)

const (
	runsDirectory = "chord-test-runs"
	registryLock  = ".lock"
	activeLock    = "active.lock"
	readyFile     = "reclaimable"
)

type testRun struct {
	dir  string
	id   string
	lock *os.File
}

func (r *testRun) work() string { return filepath.Join(r.dir, "work") }

func newRun(base string, diagnostics io.Writer) (*testRun, error) {
	root, err := filepath.Abs(filepath.Join(base, runsDirectory))
	if err != nil {
		return nil, fmt.Errorf("resolve run root: %w", err)
	}
	if err = os.Mkdir(root, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("create run root: %w", err)
	}
	if err = privateDirectory(root); err != nil {
		return nil, err
	}
	registry, err := lockFile(filepath.Join(root, registryLock), false)
	if err != nil {
		return nil, err
	}
	defer registry.Close()
	if err = reapRuns(root, diagnostics); err != nil {
		return nil, err
	}
	id, err := externalsupervisor.NewRunID()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, "run-"+id)
	if err = os.Mkdir(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create test run: %w", err)
	}
	lock, err := lockFile(filepath.Join(dir, activeLock), false)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	run := &testRun{dir: dir, id: id, lock: lock}
	if err := os.Mkdir(run.work(), 0o700); err != nil {
		_ = lock.Close()
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("create test work directory: %w", err)
	}
	return run, nil
}

func reapRuns(root string, diagnostics io.Writer) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read test runs: %w", err)
	}
	for _, entry := range entries {
		id := strings.TrimPrefix(entry.Name(), "run-")
		if !strings.HasPrefix(entry.Name(), "run-") || !externalsupervisor.ValidRunID(id) {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if err := privateDirectory(dir); err != nil {
			fmt.Fprintf(diagnostics, "test-runner: retained %s: %v\n", dir, err)
			continue
		}
		// Do not create missing lock files: incomplete registration is not
		// evidence that another process has stopped using the directory.
		lock, err := openLock(filepath.Join(dir, activeLock), syscall.O_RDWR, syscall.LOCK_EX|syscall.LOCK_NB)
		if errors.Is(err, syscall.EWOULDBLOCK) {
			continue
		}
		if err != nil {
			fmt.Fprintf(diagnostics, "test-runner: retained %s: %v\n", dir, err)
			continue
		}
		run := testRun{dir: dir, id: id, lock: lock}
		if err := run.ready(); err != nil {
			fmt.Fprintf(diagnostics, "test-runner: retained %s: %v\n", dir, err)
		} else if err := run.removeFiles(); err != nil {
			// Recovery must not fail the new command: the stale directory is
			// proven complete, so keeping it with a diagnostic lets the next
			// run retry while this one proceeds.
			fmt.Fprintf(diagnostics, "test-runner: retained %s: %v\n", dir, err)
		}
		_ = lock.Close()
	}
	return nil
}

func (r *testRun) ready() error {
	receipt, err := externalsupervisor.LoadReceipt(r.dir, r.id)
	if err != nil {
		return fmt.Errorf("read process cleanup receipt: %w", err)
	}
	if !receipt.Cleanup.Proven {
		return fmt.Errorf("process group cleanup is unproven: %s", receipt.Cleanup.Note)
	}
	data, err := os.ReadFile(filepath.Join(r.dir, readyFile))
	if err != nil {
		return fmt.Errorf("cleanup evidence unavailable: %w", err)
	}
	if string(data) != r.id {
		return errors.New("cleanup evidence belongs to another run")
	}
	return nil
}

func (r *testRun) remove() error {
	registry, err := lockFile(filepath.Join(filepath.Dir(r.dir), registryLock), false)
	if err != nil {
		return err
	}
	defer registry.Close()
	return r.removeFiles()
}

func (r *testRun) removeFiles() error {
	// Keep ownership and cleanup evidence if removing test payloads fails,
	// so a later run can retry without guessing whether processes exited.
	if err := os.RemoveAll(r.work()); err != nil {
		return fmt.Errorf("remove test work directory: %w", err)
	}
	if err := os.RemoveAll(r.dir); err != nil {
		return fmt.Errorf("remove test run: %w", err)
	}
	return nil
}

func privateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect directory %s: %w", path, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 || !ok || stat.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("refusing non-private directory %s", path)
	}
	return nil
}

func lockFile(path string, nonblocking bool) (*os.File, error) {
	operation := syscall.LOCK_EX
	if nonblocking {
		operation |= syscall.LOCK_NB
	}
	return openLock(path, syscall.O_CREAT|syscall.O_RDWR, operation)
}

func openLock(path string, flags, operation int) (*os.File, error) {
	fd, err := syscall.Open(path, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock %s: %w", path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	if err := syscall.Flock(fd, operation); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return file, nil
}
