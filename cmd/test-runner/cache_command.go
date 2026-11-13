//go:build unix

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/keakon/chord/internal/buildcache"
	"github.com/keakon/chord/internal/externalsupervisor"
)

const (
	cacheStatusCommand = "--cache-status"
	cacheTrimCommand   = "--cache-trim"
)

func runCacheCommand(command string, args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 {
		fmt.Fprintf(stderr, "usage: test-runner %s [test-temp-root]\n", command)
		return 2
	}
	cacheBase, err := os.UserCacheDir()
	if err != nil {
		fmt.Fprintln(stderr, "test-runner: resolve cache directory:", err)
		return 1
	}
	runBase := os.TempDir()
	if len(args) == 1 {
		runBase = args[0]
	}
	return runCacheCommandAt(command, filepath.Join(cacheBase, "chord", "build-cache"), runBase, stdout, stderr)
}

func runCacheCommandAt(command, cacheRoot, runBase string, stdout, stderr io.Writer) int {
	cache, err := buildcache.OpenExisting(cacheRoot)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(stdout, "Build cache is not initialized.")
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, "test-runner:", err)
		return 1
	}
	switch command {
	case cacheStatusCommand:
		return reportCacheStatus(cache, runBase, stdout, stderr)
	case cacheTrimCommand:
		return reportCacheTrim(cache, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "usage: test-runner %s|%s [test-temp-root]\n", cacheStatusCommand, cacheTrimCommand)
		return 2
	}
}

func reportCacheStatus(cache *buildcache.Cache, runBase string, stdout, stderr io.Writer) int {
	inspection, err := cache.Inspect()
	if err != nil {
		fmt.Fprintln(stderr, "test-runner: inspect build cache:", err)
		return 1
	}
	if inspection.RegistryBusy {
		fmt.Fprintln(stdout, "Build cache status unavailable: its usage registry is busy.")
		return 0
	}
	fmt.Fprintf(stdout, "Build cache logical size: %.2f GiB\n", float64(inspection.LogicalBytes)/(1<<30))
	if inspection.Approximate {
		fmt.Fprintln(stdout, "Size may change while registered or unverified users are present.")
	}
	if !inspection.RegistryAvailable {
		fmt.Fprintln(stdout, "Usage registry lock is missing; user state was read without serialization.")
	}
	if len(inspection.Users) == 0 {
		fmt.Fprintln(stdout, "Usage records: none")
		return 0
	}
	fmt.Fprintf(stdout, "Usage records: %d\n", len(inspection.Users))
	for _, user := range inspection.Users {
		recordState := "valid"
		if !user.RecordOK {
			recordState = "invalid or unreadable"
		}
		runState := "not checked"
		if externalsupervisor.ValidRunID(user.ID) {
			runState = inspectRunState(runBase, user.ID)
		}
		fmt.Fprintf(stdout, "  %q: boot=%s age=%s record=%s run=%s\n",
			user.ID, user.Boot, user.Age.Round(time.Second), recordState, runState)
	}
	return 0
}

func reportCacheTrim(cache *buildcache.Cache, stdout, stderr io.Writer) int {
	result, err := cache.Trim()
	if err != nil {
		fmt.Fprintln(stderr, "test-runner: trim build cache:", err)
		return 1
	}
	if result.RegistryBusy {
		fmt.Fprintln(stdout, "Cache trim deferred: its usage registry is busy.")
		return 0
	}
	if result.Users > 0 {
		fmt.Fprintf(stdout, "Cache trim skipped: %d usage record(s) may still have live owners.\n", result.Users)
	} else if result.Removed > 0 {
		fmt.Fprintf(stdout, "Cache reclaimed %.2f GiB (%d entries); %.2f GiB retained.\n",
			float64(result.Before-result.After)/(1<<30), result.Removed, float64(result.After)/(1<<30))
	} else if result.Scanned {
		fmt.Fprintf(stdout, "Cache is within budget: %.2f GiB; no entries removed.\n", float64(result.After)/(1<<30))
	}
	if result.Reaped > 0 {
		fmt.Fprintf(stdout, "Removed %d previous-boot usage record(s).\n", result.Reaped)
	}
	if result.OverBudget {
		fmt.Fprintln(stdout, "Cache remains over budget after cleanup.")
	}
	return 0
}

func inspectRunState(base, id string) string {
	bases := []string{base}
	if runtime.GOOS == "darwin" {
		volumes, err := os.ReadDir("/Volumes")
		if err == nil {
			for _, volume := range volumes {
				if volume.IsDir() && strings.HasPrefix(volume.Name(), "ChordTestRam-") {
					bases = append(bases, filepath.Join("/Volumes", volume.Name()))
				}
			}
		}
	}
	for _, candidate := range bases {
		state, found := inspectRunAtBase(candidate, id)
		if found {
			return state
		}
	}
	return "run directory not found"
}

func inspectRunAtBase(base, id string) (string, bool) {
	root, err := filepath.Abs(filepath.Join(base, runsDirectory))
	if err != nil {
		return "run root unavailable", true
	}
	if err := privateDirectory(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false
		}
		return "run root unavailable", true
	}
	dir := filepath.Join(root, "run-"+id)
	if err := privateDirectory(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false
		}
		return "run directory unavailable", true
	}
	registry, err := openLock(filepath.Join(root, registryLock), syscall.O_RDONLY, syscall.LOCK_SH|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return "run registry busy", true
	}
	if errors.Is(err, os.ErrNotExist) {
		return "run registry missing", true
	}
	if err != nil {
		return "run registry unavailable", true
	}
	defer registry.Close()

	lock, err := openLock(filepath.Join(dir, activeLock), syscall.O_RDONLY, syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return "active", true
	}
	if errors.Is(err, os.ErrNotExist) {
		return "active lock missing", true
	}
	if err != nil {
		return "active lock unavailable", true
	}
	defer lock.Close()
	if err := (&testRun{dir: dir, id: id}).ready(); err != nil {
		return "cleanup unproven: " + err.Error(), true
	}
	return "reclaimable", true
}
