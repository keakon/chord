package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSubAgentPersistDebouncerTracksArmedSessionDir(t *testing.T) {
	sessionDir := "session-a"
	d := newSubAgentPersistDebouncer(func() {}, func() string { return sessionDir })

	d.markRegistryDirty()
	d.markSubDirty(&SubAgent{instanceID: "worker-1"})
	subs, registryDirty, pendingDir := d.takePending()
	if len(subs) != 1 || !registryDirty {
		t.Fatalf("takePending() = %d subs, registry_dirty=%v; want 1 sub and a dirty registry", len(subs), registryDirty)
	}
	if pendingDir != sessionDir {
		t.Fatalf("takePending() session dir = %q, want %q", pendingDir, sessionDir)
	}

	// A batch armed after a switch belongs to the session that is current then.
	sessionDir = "session-b"
	d.markRegistryDirty()
	if _, _, pendingDir := d.takePending(); pendingDir != sessionDir {
		t.Fatalf("takePending() session dir after switch = %q, want %q", pendingDir, sessionDir)
	}

	// reset drops the batch together with the directory it was armed under, so
	// a late flush cannot attribute it to any session.
	d.markRegistryDirty()
	d.reset()
	if subs, registryDirty, pendingDir := d.takePending(); len(subs) != 0 || registryDirty || pendingDir != "" {
		t.Fatalf("takePending() after reset = %d subs, registry_dirty=%v, dir=%q; want empty", len(subs), registryDirty, pendingDir)
	}
}

func TestFlushPendingSubPersistsWritesArmedSession(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.subPersists.reset()
	sessionDir := a.SessionDir()

	a.subPersists.markRegistryDirty()
	a.flushPendingSubPersists()

	if _, err := os.Stat(filepath.Join(sessionDir, "subagents", "tasks.json")); err != nil {
		t.Fatalf("task registry not flushed to the armed session: %v", err)
	}
}

// A session switch inside the debounce window must not rewrite the previous
// session's meta/registry files into the successor's directory.
func TestFlushPendingSubPersistsDropsBatchFromPreviousSession(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.subPersists.reset()
	previousDir := a.SessionDir()

	a.subPersists.markRegistryDirty()

	nextDir := t.TempDir()
	a.stateMu.Lock()
	a.sessionDir = nextDir
	a.stateMu.Unlock()

	a.flushPendingSubPersists()

	if _, err := os.Stat(filepath.Join(nextDir, "subagents", "tasks.json")); !os.IsNotExist(err) {
		t.Fatalf("previous session's registry was written into the successor session: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(previousDir, "subagents", "tasks.json")); !os.IsNotExist(err) {
		t.Fatalf("dropped batch was still written to the previous session: err=%v", err)
	}
}

// Shutdown flushes the pending batch before it marks the agent shutting down,
// which is what turns the debouncer's flush into a no-op.
func TestFlushPendingSubPersistsIgnoresShuttingDown(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.subPersists.reset()
	sessionDir := a.SessionDir()

	a.shuttingDown.Store(true)
	defer a.shuttingDown.Store(false)

	a.subPersists.markRegistryDirty()
	a.flushDirtySubPersists()
	if _, err := os.Stat(filepath.Join(sessionDir, "subagents", "tasks.json")); !os.IsNotExist(err) {
		t.Fatalf("shutting-down flush wrote the registry: err=%v", err)
	}

	a.flushPendingSubPersists()
	if _, err := os.Stat(filepath.Join(sessionDir, "subagents", "tasks.json")); err != nil {
		t.Fatalf("explicit shutdown drain did not write the registry: %v", err)
	}
}
