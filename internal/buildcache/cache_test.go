//go:build unix

package buildcache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func fixture(t *testing.T, c *Cache, key int, size int64, modified time.Time) string {
	t.Helper()
	name := fmt.Sprintf("%064x-d", key)
	path := filepath.Join(c.Dir(), name[:2], name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
	return path
}

func newCache(t *testing.T) *Cache {
	t.Helper()
	c, err := Open(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTrimProtectsEveryRegisteredUser(t *testing.T) {
	c := newCache(t)
	now := time.Now()
	big := fixture(t, c, 1, 25<<30, now.Add(-8*time.Hour))
	ids := []string{strings.Repeat("a", 32), strings.Repeat("b", 32)}
	for _, id := range ids {
		if err := c.Begin(id); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range ids {
		result, err := c.Trim()
		if err != nil || result.Scanned || result.Users != len(ids)-i || result.Removed != 0 {
			t.Fatalf("trim while in use: %+v, %v", result, err)
		}
		if _, err := os.Stat(big); err != nil {
			t.Fatal("removed active cache entry:", err)
		}
		if err := c.End(id); err != nil {
			t.Fatal(err)
		}
	}
	result, err := c.Trim()
	if err != nil || result.Removed != 1 || result.After != 0 {
		t.Fatalf("idle trim: %+v, %v", result, err)
	}
	// The next run is scanned immediately; under budget nothing is evicted.
	small := fixture(t, c, 2, 11, now)
	result, err = c.Trim()
	if err != nil || result.Removed != 0 || result.After != 11 {
		t.Fatalf("under-budget trim: %+v, %v", result, err)
	}
	if _, err := os.Stat(small); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownUsageSurvivesReopen(t *testing.T) {
	c := newCache(t)
	if err := c.Begin(strings.Repeat("a", 32)); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(c.root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := reopened.Trim()
	if err != nil || result.Users != 1 || result.Scanned {
		t.Fatalf("lost durable protection: %+v, %v", result, err)
	}
}

func TestTrimPreservesSelectedExecutableWhileInUse(t *testing.T) {
	c := newCache(t)
	now := time.Now()
	id := strings.Repeat("a", 32)
	if err := c.Begin(id); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("%064x-d", 1)
	dir := filepath.Join(c.Dir(), name[:2], name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sample")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dir, now.Add(-time.Minute), now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	// Go resolves a cache hit before later opening or executing its path.
	selected := exec.CommandContext(ctx, path)
	fixture(t, c, 2, 25<<30, now)
	result, err := c.Trim()
	if err != nil || result.Users != 1 || result.Scanned || result.Removed != 0 {
		t.Fatalf("active trim: %+v, %v", result, err)
	}
	if err = selected.Run(); err != nil {
		t.Fatalf("selected executable was not preserved: %v", err)
	}
	if err = c.End(id); err != nil {
		t.Fatal(err)
	}
	result, err = c.Trim()
	if err != nil || !result.Scanned || result.Users != 0 || result.Removed != 2 || result.After != 0 {
		t.Fatalf("idle trim: %+v, %v", result, err)
	}
}

func TestTrimEvictsLeastRecentlyUsedAndProtectsForeignFiles(t *testing.T) {
	c := newCache(t)
	now := time.Now()
	old := now.Add(-8 * time.Hour)
	oldest := fixture(t, c, 1, 11, old)
	second := fixture(t, c, 2, 5, old)
	middle := fixture(t, c, 4, 11, now.Add(-7*time.Hour))
	hot := fixture(t, c, 3, 40, now)
	invalid := filepath.Join(c.Dir(), "00", "not-a-cache-entry-d")
	if err := os.WriteFile(invalid, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(c.Dir(), "00", fmt.Sprintf("%064x-d", 5))
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"fuzz", "chord-gopls"} {
		if err := os.Mkdir(filepath.Join(c.Dir(), dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	result, err := c.trim(policy{high: 60, low: 45})
	if err != nil || result.Before != 67 || result.After != 40 || result.Removed != 3 || result.OverBudget {
		t.Fatalf("trim: %+v, %v", result, err)
	}
	for _, path := range []string{oldest, second, middle} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("old entry retained: %s: %v", path, err)
		}
	}
	for _, path := range []string{hot, invalid, outside, link} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("protected entry changed: %s: %v", path, err)
		}
	}
}

func TestTrimKeepsEntriesBelowBudget(t *testing.T) {
	c := newCache(t)
	now := time.Now()
	fixture(t, c, 1, 11, now.Add(-8*time.Hour))
	small := fixture(t, c, 2, 5, now.Add(-8*time.Hour))
	result, err := c.trim(policy{high: 100, low: 50})
	if err != nil || result.Removed != 0 || result.After != 16 || result.OverBudget {
		t.Fatalf("trim: %+v, %v", result, err)
	}
	if _, err := os.Stat(small); err != nil {
		t.Fatal(err)
	}
}

func TestTrimKeepsNewestUnderFrequentBuilds(t *testing.T) {
	c := newCache(t)
	now := time.Now()
	var paths []string
	for i := range 10 {
		paths = append(paths, fixture(t, c, i+1, 10, now.Add(time.Duration(i)*time.Minute)))
	}
	result, err := c.trim(policy{high: 30, low: 10})
	if err != nil || result.Before != 100 || result.After != 10 || result.Removed != 9 || result.OverBudget {
		t.Fatalf("trim: %+v, %v", result, err)
	}
	for _, path := range paths[:9] {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("old entry retained: %s: %v", path, err)
		}
	}
	if _, err := os.Stat(paths[9]); err != nil {
		t.Fatalf("newest entry evicted: %v", err)
	}
}

func TestTrimDefersWhileRegistryLocked(t *testing.T) {
	c := newCache(t)
	lock, err := c.lock(false)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	result, err := c.Trim()
	if err != nil || result.Scanned {
		t.Fatalf("trim did not defer: %+v %v", result, err)
	}
}

func TestTrimCountsExecutableDirectoriesByLastUse(t *testing.T) {
	c := newCache(t)
	now := time.Now()
	executable := func(key int, size int, used time.Time) string {
		t.Helper()
		name := fmt.Sprintf("%064x-d", key)
		dir := filepath.Join(c.Dir(), name[:2], name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		binary := filepath.Join(dir, "sample")
		if err := os.WriteFile(binary, make([]byte, size), 0o700); err != nil {
			t.Fatal(err)
		}
		// Only the outer directory tracks executable cache use.
		if err := os.Chtimes(binary, now.Add(-48*time.Hour), now.Add(-48*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(dir, used, used); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	old := executable(1, 40, now.Add(-24*time.Hour))
	hot := executable(2, 40, now)
	ordinary := fixture(t, c, 3, 30, now.Add(-time.Hour))
	result, err := c.trim(policy{high: 100, low: 50})
	if err != nil || result.Before != 110 || result.After != 40 || result.Removed != 2 {
		t.Fatalf("executable budget: %+v, %v", result, err)
	}
	for _, path := range []string{old, ordinary} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("old entry retained: %s: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(hot, "sample")); err != nil {
		t.Fatalf("hot executable removed: %v", err)
	}
}

func TestTrimProtectsForeignExecutableShapes(t *testing.T) {
	c := newCache(t)
	outside := filepath.Join(t.TempDir(), "sample")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	var protected []string
	for i, shape := range []string{"symlink", "nested", "multiple", "action"} {
		name := fmt.Sprintf("%064x-d", i+1)
		if shape == "action" {
			name = fmt.Sprintf("%064x-a", i+1)
		}
		dir := filepath.Join(c.Dir(), name[:2], name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "sample")
		switch shape {
		case "symlink":
			if err := os.Symlink(outside, path); err != nil {
				t.Fatal(err)
			}
		case "nested":
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		default:
			if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			if shape == "multiple" {
				if err := os.WriteFile(filepath.Join(dir, "other"), []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}
		protected = append(protected, path)
	}
	fixture(t, c, 5, 100, time.Now())
	result, err := c.trim(policy{high: 10, low: 5})
	if err != nil || result.Before != 100 || result.After != 0 || result.Removed != 1 {
		t.Fatalf("foreign entries counted: %+v, %v", result, err)
	}
	for _, path := range append(protected, outside) {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("protected path removed: %s: %v", path, err)
		}
	}
}

func TestRefusesUnsafeRootAndUserNames(t *testing.T) {
	for _, mode := range []string{"relative", "shared", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "cache")
			switch mode {
			case "relative":
				root = "."
			case "shared":
				if err := os.Mkdir(root, 0o755); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(t.TempDir(), root); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Open(root); err == nil {
				t.Fatal("unsafe root accepted")
			}
		})
	}
	c := newCache(t)
	for _, id := range []string{"", "../entry", strings.Repeat("g", 32)} {
		if c.Begin(id) == nil || c.End(id) == nil {
			t.Fatal("invalid usage identity accepted")
		}
	}
	id := strings.Repeat("a", 32)
	if err := c.Begin(id); err != nil {
		t.Fatal(err)
	}
	if err := c.Begin(id); !errors.Is(err, syscall.EEXIST) {
		t.Fatalf("duplicate registration: %v", err)
	}
	if result, err := c.Trim(); err != nil || result.Users != 1 || result.Scanned {
		t.Fatalf("duplicate registration lost existing protection: %+v, %v", result, err)
	}
}
