//go:build unix

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/keakon/chord/internal/buildcache"
)

func assertCacheUsers(t *testing.T, base string, want int) {
	t.Helper()
	users, err := os.ReadDir(filepath.Join(base, "cache", "users"))
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != want {
		t.Fatalf("cache users = %d, want %d", len(users), want)
	}
}

func TestCacheReclaimedOnlyAfterLastCommand(t *testing.T) {
	base := t.TempDir()
	markers := t.TempDir()
	first := runnerCommand(t, base, os.Args[0], "child-fixture", "hold", filepath.Join(markers, "first"))
	startCommand(t, first)
	awaitFile(t, filepath.Join(markers, "first"))
	second := runnerCommand(t, base, os.Args[0], "child-fixture", "hold", filepath.Join(markers, "second"))
	startCommand(t, second)
	awaitFile(t, filepath.Join(markers, "second"))
	assertCacheUsers(t, base, 2)
	third := runnerCommand(t, base, os.Args[0], "child-fixture", "success")
	output, err := third.CombinedOutput()
	if err != nil {
		t.Fatalf("concurrent command failed: %v: %s", err, output)
	}
	if !bytes.Contains(output, []byte("2 registered cache user(s)")) {
		t.Fatalf("missing active users diagnostic: %s", output)
	}
	if bytes.Contains(output, []byte("retained cache usage record")) {
		t.Fatalf("active peers reported as failed cleanup: %s", output)
	}
	assertCacheUsers(t, base, 2)

	entry := largeCacheEntry(t, base)
	_ = first.Process.Signal(syscall.SIGTERM)
	_ = waitCommand(t, first)
	assertCacheUsers(t, base, 1)
	if _, err := os.Stat(entry); err != nil {
		t.Fatalf("cache removed while another command was active: %v", err)
	}
	_ = second.Process.Signal(syscall.SIGTERM)
	_ = waitCommand(t, second)
	assertCacheUsers(t, base, 0)
	if _, err := os.Stat(entry); !os.IsNotExist(err) {
		t.Fatalf("idle cache not reclaimed: %v", err)
	}
}

func largeCacheEntry(t *testing.T, base string) string {
	t.Helper()
	bucket := filepath.Join(base, "cache", "go-build", "00")
	if err := os.MkdirAll(bucket, 0o700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(bucket, fmt.Sprintf("%064x-d", 1))
	f, err := os.Create(entry)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(9 << 30); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	return entry
}

func TestKilledRunnerReclaimsCacheWithoutNextCommand(t *testing.T) {
	base := t.TempDir()
	marker := filepath.Join(t.TempDir(), "started")
	cmd := runnerCommand(t, base, os.Args[0], "child-fixture", "hold", marker)
	startCommand(t, cmd)
	_ = string(awaitFile(t, marker))
	entry := largeCacheEntry(t, base)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = waitCommand(t, cmd)
	awaitFile(t, filepath.Join(onlyRunDir(t, base), readyFile))
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(entry); os.IsNotExist(err) {
			break
		}
		select {
		case <-timeout.C:
			t.Fatal("supervisor did not reclaim idle cache")
		case <-ticker.C:
		}
	}
	assertCacheUsers(t, base, 0)
}

func TestManagedEnvironmentOverridesOnlyOwnedVariables(t *testing.T) {
	t.Setenv("GOCACHE", "/sample/unmanaged")
	t.Setenv("GOCACHEPROG", "sample-helper")
	t.Setenv("GOFLAGS", "-tags=sample -trimpath")
	env := testEnvironment("/sample/work", "/sample/cache")
	for key, want := range map[string]string{
		"GOCACHE": "/sample/cache", "GOCACHEPROG": "",
		"TMPDIR": "/sample/work", "GOTMPDIR": "/sample/work",
		"GOFLAGS": "-tags=sample -trimpath", "CHORD_TEST_RUNNER_ACTIVE": "1",
	} {
		var matches []string
		for _, entry := range env {
			if strings.HasPrefix(entry, key+"=") {
				matches = append(matches, entry)
			}
		}
		if !slices.Equal(matches, []string{key + "=" + want}) {
			t.Fatalf("%s = %v", key, matches)
		}
	}
}

func TestTrimSkippedDiagnosticReportsRegisteredUsers(t *testing.T) {
	base := t.TempDir()
	cache, err := buildcache.Open(filepath.Join(base, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 32)
	if err := cache.Begin(id); err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	trimBuildCache(cache, &diagnostics)
	if output := diagnostics.String(); !strings.Contains(output, "1 registered cache user") {
		t.Fatalf("missing skipped trim diagnostic: %s", output)
	}
	if err := cache.End(id); err != nil {
		t.Fatal(err)
	}
	diagnostics.Reset()
	trimBuildCache(cache, &diagnostics)
	if output := diagnostics.String(); strings.Contains(output, "skipped") {
		t.Fatalf("unexpected skip diagnostic without registered users: %s", output)
	}
}

func TestCacheStatusIsReadOnlyAndReportsRunState(t *testing.T) {
	base := t.TempDir()
	cacheRoot := filepath.Join(base, "cache")
	cache, err := buildcache.Open(cacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	run, err := newRun(base, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer run.lock.Close()
	if err := cache.Begin(run.id); err != nil {
		t.Fatal(err)
	}
	defer cache.End(run.id)

	var output bytes.Buffer
	if code := runCacheCommandAt(cacheStatusCommand, cacheRoot, base, &output, &output); code != 0 {
		t.Fatalf("cache status exit = %d: %s", code, output.String())
	}
	if !strings.Contains(output.String(), "Usage records: 1") || !strings.Contains(output.String(), "run=active") {
		t.Fatalf("status omitted live run state: %s", output.String())
	}
}

func TestCacheStatusDoesNotCreateMissingCache(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing-cache")
	var output bytes.Buffer
	if code := runCacheCommandAt(cacheStatusCommand, root, t.TempDir(), &output, &output); code != 0 {
		t.Fatalf("cache status exit = %d: %s", code, output.String())
	}
	if !strings.Contains(output.String(), "not initialized") {
		t.Fatalf("missing cache status = %s", output.String())
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status created cache directory: %v", err)
	}
}

func TestExplicitCacheTrimPreservesCacheWithRegisteredUser(t *testing.T) {
	base := t.TempDir()
	cacheRoot := filepath.Join(base, "cache")
	cache, err := buildcache.Open(cacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 32)
	if err := cache.Begin(id); err != nil {
		t.Fatal(err)
	}
	defer cache.End(id)
	entry := largeCacheEntry(t, base)

	var output bytes.Buffer
	if code := runCacheCommandAt(cacheTrimCommand, cacheRoot, base, &output, &output); code != 0 {
		t.Fatalf("cache trim exit = %d: %s", code, output.String())
	}
	if !strings.Contains(output.String(), "may still have live owners") {
		t.Fatalf("missing safe-skip diagnostic: %s", output.String())
	}
	if _, err := os.Stat(entry); err != nil {
		t.Fatalf("explicit trim deleted active cache entry: %v", err)
	}
}
