package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/runtimecache"
)

func TestModelUsesCacheDirRuntimeCacheForCurrentSession(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	t.Setenv("CHORD_CACHE_DIR", cacheDir)

	backend := &sessionControlAgent{
		sessionSummary: &agent.SessionSummary{ID: "session-1"},
	}
	m := NewModelWithSize(backend, 80, 24)
	defer func() { _ = m.Close() }()

	if m.runtimeCacheHandle == nil {
		t.Fatal("expected runtime cache handle")
	}
	wantRoot := filepath.Join(cacheDir, "runtime", "session-cache")
	if got := m.runtimeCacheHandle.Dir(); !strings.HasPrefix(got, wantRoot) {
		t.Fatalf("runtime cache dir = %q, want prefix %q", got, wantRoot)
	}
}

func TestSessionRestoredEventSwitchesRuntimeCacheAndRemovesOldSessionDir(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	t.Setenv("CHORD_CACHE_DIR", cacheDir)

	backend := &sessionControlAgent{
		sessionSummary: &agent.SessionSummary{ID: "old-session"},
	}
	m := NewModelWithSize(backend, 80, 24)
	oldDir := m.runtimeCacheHandle.Dir()

	backend.sessionSummary = &agent.SessionSummary{ID: "new-session"}
	m.beginSessionSwitch("resume", "new-session")
	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	defer func() { _ = m.Close() }()

	if m.runtimeCacheHandle == nil || m.runtimeCacheHandle.SessionID() != "new-session" {
		t.Fatalf("runtime cache session = %#v, want new-session", m.runtimeCacheHandle)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("old runtime cache dir err = %v, want not exist", err)
	}
	if _, err := os.Stat(m.runtimeCacheHandle.Dir()); err != nil {
		t.Fatalf("new runtime cache dir stat: %v", err)
	}
}

func TestStartupRestoreResetsTargetRuntimeCacheBeforeOpen(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	t.Setenv("CHORD_CACHE_DIR", cacheDir)
	mgr, err := runtimecache.NewManager(cacheDir)
	if err != nil {
		t.Fatalf("NewManager(): %v", err)
	}

	contentRoot := t.TempDir()
	handle, err := mgr.OpenSession(contentRoot, "restored-session")
	if err != nil {
		t.Fatalf("OpenSession(): %v", err)
	}
	staleMarker := filepath.Join(handle.ViewportDir(), "stale.txt")
	if err := os.WriteFile(staleMarker, []byte("stale"), 0o600); err != nil {
		t.Fatalf("WriteFile(stale): %v", err)
	}
	staleDir := handle.Dir()
	if err := handle.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd(): %v", err)
	}
	if err := os.Chdir(contentRoot); err != nil {
		t.Fatalf("Chdir(contentRoot): %v", err)
	}
	defer func() { _ = os.Chdir(prevWD) }()

	backend := &sessionControlAgent{
		resumePending:   true,
		startupResumeID: "restored-session",
		sessionSummary:  &agent.SessionSummary{ID: "restored-session"},
	}
	m := NewModelWithSize(backend, 80, 24)
	m.workingDir = contentRoot
	if m.runtimeCacheHandle != nil {
		t.Fatal("startup restore should defer runtime cache open until SessionRestoredEvent")
	}

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}})
	applyTestCmd(t, &m, cmd)
	defer func() { _ = m.Close() }()

	if m.runtimeCacheHandle == nil || m.runtimeCacheHandle.SessionID() != "restored-session" {
		t.Fatalf("runtime cache session = %#v, want restored-session", m.runtimeCacheHandle)
	}
	if got := m.runtimeCacheHandle.Dir(); got != staleDir {
		t.Fatalf("runtime cache dir = %q, want %q", got, staleDir)
	}
	if _, err := os.Stat(staleMarker); !os.IsNotExist(err) {
		t.Fatalf("stale marker err = %v, want not exist", err)
	}
	if _, err := os.Stat(staleDir); err != nil {
		t.Fatalf("stale dir stat after reopen: %v", err)
	}
}

func TestModelCloseRemovesCurrentSessionRuntimeCache(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	t.Setenv("CHORD_CACHE_DIR", cacheDir)

	backend := &sessionControlAgent{
		sessionSummary: &agent.SessionSummary{ID: "session-close"},
	}
	m := NewModelWithSize(backend, 80, 24)
	if m.runtimeCacheHandle == nil {
		t.Fatal("expected runtime cache handle")
	}
	dir := m.runtimeCacheHandle.Dir()

	if err := m.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("runtime cache dir err = %v, want not exist", err)
	}
}
