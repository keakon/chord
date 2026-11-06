//go:build unix

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func runnerBuild(t *testing.T, root, id string) (string, *os.File) {
	t.Helper()
	bootstrap, err := os.MkdirTemp(root, ".build.")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(bootstrap) })
	source := filepath.Join(bootstrap, runnerBinary)
	if err := os.WriteFile(source, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	path, use, err := cacheRunner(root, id, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = use.Close() })
	return path, use
}

func TestRunnerCacheProtectsSelectedBuildUntilReleased(t *testing.T) {
	root := t.TempDir()
	selected, use := runnerBuild(t, root, "selected")
	old := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(filepath.Dir(selected), old, old); err != nil {
		t.Fatal(err)
	}
	_, second := runnerBuild(t, root, "second")
	_ = second.Close()
	_, third := runnerBuild(t, root, "third")
	_ = third.Close()
	if _, err := os.Stat(selected); err != nil {
		t.Fatalf("selected executable removed before supervisor launch: %v", err)
	}
	// A second invocation of the same build can share its use lock.
	_, peer := runnerBuild(t, root, "selected")
	_ = use.Close()
	if err := os.Chtimes(filepath.Dir(selected), old, old); err != nil {
		t.Fatal(err)
	}
	_, fourth := runnerBuild(t, root, "fourth")
	_ = fourth.Close()
	if _, err := os.Stat(selected); err != nil {
		t.Fatalf("peer's executable removed: %v", err)
	}
	_ = peer.Close()
	_, fifth := runnerBuild(t, root, "fifth")
	_ = fifth.Close()
	if _, err := os.Stat(selected); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("idle old executable retained: %v", err)
	}
	dirs, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, dir := range dirs {
		if dir.IsDir() && validBuildID(dir.Name()) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("retained %d builds, want 1", count)
	}
	// Returning to an evicted build publishes that invocation's executable.
	rebuilt, latest := runnerBuild(t, root, "selected")
	if rebuilt != selected {
		t.Fatalf("rebuilt path = %s, want %s", rebuilt, selected)
	}
	if err := releaseRunner(root, latest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rebuilt); err != nil {
		t.Fatalf("latest executable removed after release: %v", err)
	}
}

func TestRunnerCacheRefreshesReuseAndRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	path, use := runnerBuild(t, root, "sample")
	_ = use.Close()
	old := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(filepath.Dir(path), old, old); err != nil {
		t.Fatal(err)
	}
	reused, _ := runnerBuild(t, root, "sample")
	info, err := os.Stat(filepath.Dir(reused))
	if err != nil || !info.ModTime().After(old) {
		t.Fatalf("reuse timestamp not refreshed: %v, %v", info, err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cacheRunner(root, "linked", "/unused"); err == nil {
		t.Fatal("followed build directory symlink")
	}
	for _, id := range []string{"", "../escape", ".hidden", "a/b"} {
		if _, _, err := cacheRunner(root, id, "/unused"); err == nil {
			t.Fatalf("accepted invalid build ID %q", id)
		}
	}
	lock, err := openLock(filepath.Join(root, "sample", runnerUseLock), syscall.O_RDWR, syscall.LOCK_EX|syscall.LOCK_NB)
	if lock != nil {
		_ = lock.Close()
	}
	if !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Fatalf("selected build not locked: %v", err)
	}
}

func TestRunnerCacheReleasesKilledOwners(t *testing.T) {
	root := t.TempDir()
	bootstrap := filepath.Join(root, ".build.fixture")
	if err := os.Mkdir(bootstrap, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(bootstrap, runnerBinary)
	if err := os.WriteFile(source, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "ready")
	cmd := exec.CommandContext(t.Context(), os.Args[0], "runner-cache-fixture", root, "held", source, marker)
	cmd.WaitDelay = 10 * time.Second
	startCommand(t, cmd)
	awaitFile(t, marker)
	selected := filepath.Join(root, "held", runnerBinary)
	old := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(filepath.Dir(selected), old, old); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"second", "third"} {
		_, use := runnerBuild(t, root, id)
		_ = use.Close()
	}
	if _, err := os.Stat(selected); err != nil {
		t.Fatal("active process lost executable:", err)
	}
	_ = cmd.Process.Kill()
	_ = waitCommand(t, cmd)
	_, use := runnerBuild(t, root, "fourth")
	_ = use.Close()
	if _, err := os.Stat(selected); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("killed owner's executable retained: %v", err)
	}
}

func TestRunnerCachePreservesUnrelatedDirectories(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".build.pending", "foreign.directory"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"first", "second", "third"} {
		_, use := runnerBuild(t, root, id)
		_ = use.Close()
	}
	for _, name := range []string{"linked", ".build.pending", "foreign.directory"} {
		if _, err := os.Lstat(filepath.Join(root, name)); err != nil {
			t.Fatalf("unrelated path removed: %s: %v", name, err)
		}
	}
}

func TestRunnerCachePrunesAfterLastUse(t *testing.T) {
	root := t.TempDir()
	path, first := runnerBuild(t, root, "first")
	old := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(filepath.Dir(path), old, old); err != nil {
		t.Fatal(err)
	}
	_, second := runnerBuild(t, root, "second")
	_, third := runnerBuild(t, root, "third")
	for _, use := range []*os.File{second, third, first} {
		if err := releaseRunner(root, use); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("idle build retained until another command: %v", err)
	}
}
