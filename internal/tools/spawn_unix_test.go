//go:build unix

package tools

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSpawnTimeoutForceKillsProcessGroupThatIgnoresSIGTERM(t *testing.T) {
	resetSpawnRegistryOnlyForTest(t)

	workdir := t.TempDir()
	pidFile := filepath.Join(workdir, "sleep.pid")
	start := time.Now()

	out, err := NewSpawnTool("").Execute(WithSessionDir(context.Background(), t.TempDir()), mustMarshal(t, map[string]any{
		"command":     "sh -c 'trap \"\" TERM; (trap \"\" TERM; sleep 60) & echo $! > sleep.pid; wait'",
		"description": "ignore-term timeout test",
		"timeout":     1,
		"workdir":     workdir,
	}))
	if err != nil {
		t.Fatalf("Spawn Execute: %v", err)
	}
	id := extractBackgroundID(t, out)

	// Wait until the job disappears from the registry (finished), but do not hang.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := globalSpawnRegistry.get(id); !ok {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, ok := globalSpawnRegistry.get(id); ok {
		t.Fatalf("spawned job %s still running after %s", id, time.Since(start))
	}

	pidBytes, readErr := exec.Command("cat", pidFile).Output()
	if readErr != nil {
		t.Fatalf("read child pid file: %v", readErr)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if parseErr != nil {
		t.Fatalf("parse child pid %q: %v", pidBytes, parseErr)
	}

	// The child should be gone shortly after job termination.
	childDeadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(childDeadline) {
		kerr := syscall.Kill(pid, 0)
		if errors.Is(kerr, syscall.ESRCH) {
			return
		}
		if kerr != nil && !errors.Is(kerr, syscall.EPERM) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("child pid %d still appears to be running after spawn timeout forced termination", pid)
}
