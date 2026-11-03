//go:build unix

package buildcache

import (
	"errors"
	"fmt"
	"os"
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
	old := fixture(t, c, 1, 11<<20, now.Add(-8*time.Hour))
	ids := []string{strings.Repeat("a", 32), strings.Repeat("b", 32)}
	for _, id := range ids {
		if err := c.Begin(id); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range ids {
		result, err := c.Trim(now)
		if err != nil || result.Scanned || result.Users != len(ids)-i {
			t.Fatalf("trim while in use: %+v, %v", result, err)
		}
		if _, err := os.Stat(old); err != nil {
			t.Fatal("removed active cache entry:", err)
		}
		if err := c.End(id); err != nil {
			t.Fatal(err)
		}
	}
	result, err := c.Trim(now)
	if err != nil || result.Removed != 1 {
		t.Fatalf("idle trim: %+v, %v", result, err)
	}
	// Newly expired data is not scanned again until the interval passes.
	old = fixture(t, c, 2, 11<<20, now.Add(-8*time.Hour))
	result, err = c.Trim(now.Add(time.Minute))
	if err != nil || result.Scanned {
		t.Fatalf("unthrottled trim: %+v, %v", result, err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal(err)
	}
	result, err = c.Trim(now.Add(time.Hour))
	if err != nil || result.Removed != 1 {
		t.Fatalf("next trim: %+v, %v", result, err)
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
	result, err := reopened.Trim(time.Now())
	if err != nil || result.Users != 1 || result.Scanned {
		t.Fatalf("lost durable protection: %+v, %v", result, err)
	}
}

func TestTrimAgeSizeBudgetAndProtectedFiles(t *testing.T) {
	c := newCache(t)
	now := time.Now()
	old := now.Add(-8 * time.Hour)
	largeOld := fixture(t, c, 1, 11, old)
	smallOld := fixture(t, c, 2, 5, old)
	hot := fixture(t, c, 3, 40, now)
	atBoundary := fixture(t, c, 4, 11, now.Add(-7*time.Hour))
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
	result, err := c.trim(now, policy{large: 10, high: 30, low: 20, age: 7 * time.Hour})
	if err != nil || result.Before != 67 || result.After != 51 || result.Removed != 2 || !result.OverBudget {
		t.Fatalf("trim: %+v, %v", result, err)
	}
	for _, path := range []string{largeOld, smallOld} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("old entry retained: %s: %v", path, err)
		}
	}
	for _, path := range []string{hot, atBoundary, invalid, outside, link} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("protected entry changed: %s: %v", path, err)
		}
	}
}

func TestLargeEntriesExpireBelowBudget(t *testing.T) {
	c := newCache(t)
	now := time.Now()
	fixture(t, c, 1, 11, now.Add(-8*time.Hour))
	small := fixture(t, c, 2, 5, now.Add(-8*time.Hour))
	result, err := c.trim(now, policy{large: 10, high: 30, low: 20, age: 7 * time.Hour})
	if err != nil || result.Removed != 1 || result.After != 5 {
		t.Fatalf("trim: %+v, %v", result, err)
	}
	if _, err := os.Stat(small); err != nil {
		t.Fatal(err)
	}
}

func TestTrimDefersWhileRegistryLocked(t *testing.T) {
	c := newCache(t)
	lock, err := c.lock(false)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	result, err := c.Trim(time.Now())
	if err != nil || result.Scanned {
		t.Fatalf("trim did not defer: %+v %v", result, err)
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
}
