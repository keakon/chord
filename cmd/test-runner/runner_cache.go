//go:build unix

package main

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

const (
	cacheRunnerFlag = "--cache-runner"
	runnerBinary    = "test-runner"
	runnerUseLock   = "use.lock"
)

func runCached(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "test-runner: missing cache arguments")
		return 2
	}
	source, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "test-runner: locate bootstrap executable:", err)
		return 1
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		fmt.Fprintln(os.Stderr, "test-runner: resolve bootstrap executable:", err)
		return 1
	}
	root, err := filepath.EvalSymlinks(args[0])
	if err != nil || filepath.Dir(filepath.Dir(source)) != root || !strings.HasPrefix(filepath.Base(filepath.Dir(source)), ".build.") || filepath.Base(source) != runnerBinary {
		fmt.Fprintln(os.Stderr, "test-runner: executable is outside its bootstrap directory")
		return 1
	}
	// The bootstrap directory belongs to this invocation, including failures
	// before a supervisor can be launched.
	defer os.RemoveAll(filepath.Dir(source))
	executable, use, err := cacheRunner(root, args[1], source)
	if err != nil {
		fmt.Fprintln(os.Stderr, "test-runner:", err)
		return 1
	}
	defer func() {
		if err := releaseRunner(root, use); err != nil {
			fmt.Fprintln(os.Stderr, "test-runner: runner cache cleanup deferred:", err)
		}
	}()
	_ = os.RemoveAll(filepath.Dir(source))
	return runMain(args[2:], executable)
}

func releaseRunner(root string, use *os.File) error {
	defer use.Close()
	registry, err := lockFile(filepath.Join(root, registryLock), false)
	if err != nil {
		return err
	}
	defer registry.Close()
	if err := use.Close(); err != nil {
		return fmt.Errorf("release runner build: %w", err)
	}
	return pruneRunnerBuilds(root)
}

func validBuildID(id string) bool {
	if id == "" {
		return false
	}
	for _, ch := range id {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-') {
			return false
		}
	}
	return true
}

// cacheRunner holds the directory lock until the selected build has a use
// lock. Pruning takes both locks in the same order, so a lock file cannot be
// removed between another invocation opening it and acquiring its lock.
func cacheRunner(root, id, source string) (string, *os.File, error) {
	if !validBuildID(id) {
		return "", nil, fmt.Errorf("invalid runner build ID: %q", id)
	}
	registry, err := lockFile(filepath.Join(root, registryLock), false)
	if err != nil {
		return "", nil, err
	}
	defer registry.Close()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, fmt.Errorf("create runner build: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", nil, fmt.Errorf("inspect runner directory: %w", err)
	}
	if !info.IsDir() {
		return "", nil, fmt.Errorf("runner build is not a directory: %s", dir)
	}
	executable := filepath.Join(dir, runnerBinary)
	if info, err := os.Lstat(executable); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(source, executable); err != nil {
			return "", nil, fmt.Errorf("publish runner build: %w", err)
		}
	} else if err != nil {
		return "", nil, fmt.Errorf("inspect runner build: %w", err)
	} else if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", nil, fmt.Errorf("invalid runner executable: %s", executable)
	}
	use, err := openLock(filepath.Join(dir, runnerUseLock), syscall.O_CREAT|syscall.O_RDWR, syscall.LOCK_SH)
	if err != nil {
		return "", nil, err
	}
	now := time.Now()
	if err := os.Chtimes(dir, now, now); err != nil {
		_ = use.Close()
		return "", nil, fmt.Errorf("record runner use: %w", err)
	}
	if err := pruneRunnerBuilds(root); err != nil {
		fmt.Fprintln(os.Stderr, "test-runner: runner cache cleanup deferred:", err)
	}
	return executable, use, nil
}

func pruneRunnerBuilds(root string) error {
	dirs, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read runner builds: %w", err)
	}
	var builds []os.FileInfo
	for _, dir := range dirs {
		if !dir.IsDir() || !validBuildID(dir.Name()) {
			continue
		}
		info, err := dir.Info()
		if err != nil {
			return fmt.Errorf("inspect runner build: %w", err)
		}
		builds = append(builds, info)
	}
	slices.SortFunc(builds, func(a, b os.FileInfo) int {
		if order := b.ModTime().Compare(a.ModTime()); order != 0 {
			return order
		}
		return cmp.Compare(a.Name(), b.Name())
	})
	for _, build := range builds[min(1, len(builds)):] {
		dir := filepath.Join(root, build.Name())
		use, err := lockFile(filepath.Join(dir, runnerUseLock), true)
		if errors.Is(err, syscall.EWOULDBLOCK) {
			continue
		}
		if err != nil {
			return err
		}
		err = os.RemoveAll(dir)
		_ = use.Close()
		if err != nil {
			return fmt.Errorf("remove runner build: %w", err)
		}
	}
	return nil
}
