//go:build unix

package buildcache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/keakon/chord/internal/jsonx"
)

func writeRecord(t *testing.T, c *Cache, id string, record userRecord) {
	t.Helper()
	data, err := jsonx.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.root, "users", id), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBeginRecordsCurrentBoot(t *testing.T) {
	c := newCache(t)
	id := strings.Repeat("a", 32)
	if err := c.Begin(id); err != nil {
		t.Fatal(err)
	}
	boot := systemBootID()
	if (runtime.GOOS == "darwin" || runtime.GOOS == "linux") && boot == "" {
		t.Fatal("system boot identity unavailable")
	}
	record, ok := readUserRecord(filepath.Join(c.root, "users", id))
	if record.BootID != boot || ok != (boot != "") {
		t.Fatalf("record=%+v valid=%v boot=%q", record, ok, boot)
	}
}

func TestBeginWriteFailureRemovesRecord(t *testing.T) {
	const rootEnv = "CHORD_BUILDCACHE_WRITE_FAILURE_ROOT"
	root := os.Getenv(rootEnv)
	if root == "" {
		// The file-size limit and signal disposition are process-wide.
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBeginWriteFailureRemovesRecord$", "-test.v")
		cmd.Env = append(os.Environ(), rootEnv+"="+filepath.Join(t.TempDir(), "cache"))
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("write-failure child: %v\n%s", err, output)
		}
		return
	}
	c, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	var original syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
		t.Fatal(err)
	}
	signal.Ignore(syscall.SIGXFSZ)
	for i, tc := range []struct {
		name  string
		limit uint64
	}{{"empty", 0}, {"partial", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			restricted := original
			restricted.Cur = tc.limit
			if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &restricted); err != nil {
				t.Fatal(err)
			}
			id := strings.Repeat(string(rune('a'+i)), 32)
			err := c.Begin(id)
			if restoreErr := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original); restoreErr != nil {
				t.Fatal(restoreErr)
			}
			if !errors.Is(err, syscall.EFBIG) {
				t.Fatalf("Begin error = %v, want EFBIG", err)
			}
			if _, statErr := os.Stat(filepath.Join(root, "users", id)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("failed registration retained its record: %v", statErr)
			}
			result, err := c.Trim()
			if err != nil || result.Users != 0 || !result.Scanned {
				t.Fatalf("failed registration blocked idle trim: %+v, %v", result, err)
			}
			if err := c.Begin(id); err != nil {
				t.Fatalf("registration after write recovery: %v", err)
			}
			if err := c.End(id); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReapUsersRequiresDifferentKnownBoot(t *testing.T) {
	for _, currentBoot := range []string{"current-boot", ""} {
		t.Run("boot="+currentBoot, func(t *testing.T) {
			c := newCache(t)
			writeRecord(t, c, strings.Repeat("a", 32), userRecord{BootID: "current-boot"})
			staleID := strings.Repeat("b", 32)
			writeRecord(t, c, staleID, userRecord{BootID: "previous-boot"})
			writeRecord(t, c, strings.Repeat("c", 32), userRecord{})
			// Neither age nor unreadable identity proves that users have stopped.
			for i, data := range []string{"", "{", `{"other":"value"}`} {
				path := filepath.Join(c.root, "users", strings.Repeat(string(rune('d'+i)), 32))
				if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
				old := time.Unix(1, 0)
				if err := os.Chtimes(path, old, old); err != nil {
					t.Fatal(err)
				}
			}
			live, reaped, err := c.reapUsers(currentBoot)
			wantReaped := 0
			if currentBoot != "" {
				wantReaped = 1
			}
			if err != nil || live != 6-wantReaped || reaped != wantReaped {
				t.Fatalf("live=%d reaped=%d err=%v", live, reaped, err)
			}
			entries, err := os.ReadDir(filepath.Join(c.root, "users"))
			if err != nil || len(entries) != live {
				t.Fatalf("entries=%v err=%v", entries, err)
			}
			_, err = os.Stat(filepath.Join(c.root, "users", staleID))
			if currentBoot != "" && !errors.Is(err, os.ErrNotExist) || currentBoot == "" && err != nil {
				t.Fatalf("stale record state: %v", err)
			}
			fixture(t, c, 1, 25<<30, time.Now())
			result, err := c.Trim()
			if err != nil || result.Users == 0 || result.Scanned || result.Removed != 0 {
				t.Fatalf("uncertain owners lost protection: %+v err=%v", result, err)
			}
		})
	}
}

func TestTrimReapsPreviousBootBeforeEviction(t *testing.T) {
	c := newCache(t)
	fixture(t, c, 1, 9<<30, time.Now())
	writeRecord(t, c, strings.Repeat("a", 32), userRecord{BootID: "previous-boot"})
	result, err := c.Trim()
	if err != nil {
		t.Fatal(err)
	}
	if systemBootID() == "" {
		if result.Users != 1 || result.Scanned || result.Reaped != 0 {
			t.Fatalf("unknown boot: %+v", result)
		}
		return
	}
	if !result.Scanned || result.Reaped != 1 || result.Users != 0 || result.Removed != 1 || result.After != 0 {
		t.Fatalf("previous boot prevented idle eviction: %+v", result)
	}
}

func TestTrimPreviousBootScansOnlyAfterReapingARecord(t *testing.T) {
	for _, tc := range []struct {
		name       string
		records    []userRecord
		bootID     string
		wantUsers  int
		wantReaped int
		wantScan   bool
	}{
		{name: "current owner", records: []userRecord{{BootID: "current"}}, bootID: "current", wantUsers: 1},
		{name: "unknown boot", records: []userRecord{{}}, bootID: "current", wantUsers: 1},
		{name: "unknown current boot", records: []userRecord{{BootID: "previous"}}, wantUsers: 1},
		{name: "previous owner with current owner", records: []userRecord{{BootID: "previous"}, {BootID: "current"}}, bootID: "current", wantUsers: 1, wantReaped: 1},
		{name: "only previous owner", records: []userRecord{{BootID: "previous"}}, bootID: "current", wantReaped: 1, wantScan: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCache(t)
			fixture(t, c, 1, 9<<30, time.Now())
			for i, record := range tc.records {
				writeRecord(t, c, strings.Repeat(string(rune('a'+i)), 32), record)
			}
			result, err := c.trimPreviousBoot(tc.bootID)
			if err != nil {
				t.Fatal(err)
			}
			if result.Users != tc.wantUsers || result.Reaped != tc.wantReaped || result.Scanned != tc.wantScan {
				t.Fatalf("trim previous boot: %+v", result)
			}
			if tc.wantScan && (result.Removed != 1 || result.After != 0) {
				t.Fatalf("stale-only trim: %+v", result)
			}
			if !tc.wantScan {
				cacheEntry := filepath.Join(c.Dir(), "00", fmt.Sprintf("%064x-d", 1))
				if _, err := os.Stat(cacheEntry); err != nil {
					t.Fatalf("cache changed without reaping a stale record: %v", err)
				}
			}
		})
	}
}
