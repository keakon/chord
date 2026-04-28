package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePathLocatorXDGDefaultsAndChordHomeIgnored(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CHORD_HOME", filepath.Join(t.TempDir(), "legacy"))
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")

	locator, err := ResolvePathLocator(nil, PathOptions{})
	if err != nil {
		t.Fatalf("ResolvePathLocator: %v", err)
	}
	if locator.ConfigHome != filepath.Join(home, ".config", "chord") {
		t.Fatalf("ConfigHome = %q", locator.ConfigHome)
	}
	if locator.StateDir != filepath.Join(home, ".local", "state", "chord") {
		t.Fatalf("StateDir = %q", locator.StateDir)
	}
	if locator.CacheDir != filepath.Join(home, ".cache", "chord") {
		t.Fatalf("CacheDir = %q", locator.CacheDir)
	}
	if strings.Contains(locator.ConfigHome, "legacy") || strings.Contains(locator.StateDir, "legacy") || strings.Contains(locator.CacheDir, "legacy") {
		t.Fatalf("CHORD_HOME affected locator: %#v", locator)
	}
}

func TestResolvePathLocatorOverridePriority(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Paths.StateDir = filepath.Join(t.TempDir(), "cfg-state")
	cfg.Paths.CacheDir = filepath.Join(t.TempDir(), "cfg-cache")
	cfg.Paths.SessionsDir = filepath.Join(t.TempDir(), "cfg-sessions")
	cfg.Paths.LogsDir = filepath.Join(t.TempDir(), "cfg-logs")
	envState := filepath.Join(t.TempDir(), "env-state")
	envCache := filepath.Join(t.TempDir(), "env-cache")
	envSessions := filepath.Join(t.TempDir(), "env-sessions")
	envLogs := filepath.Join(t.TempDir(), "env-logs")
	t.Setenv("CHORD_STATE_DIR", envState)
	t.Setenv("CHORD_CACHE_DIR", envCache)
	t.Setenv("CHORD_SESSIONS_DIR", envSessions)
	t.Setenv("CHORD_LOGS_DIR", envLogs)
	explicitState := filepath.Join(t.TempDir(), "explicit-state")
	explicitCache := filepath.Join(t.TempDir(), "explicit-cache")
	explicitSessions := filepath.Join(t.TempDir(), "explicit-sessions")
	explicitLogs := filepath.Join(t.TempDir(), "explicit-logs")

	locator, err := ResolvePathLocator(cfg, PathOptions{StateDir: explicitState, CacheDir: explicitCache, SessionsDir: explicitSessions, LogsDir: explicitLogs})
	if err != nil {
		t.Fatalf("ResolvePathLocator: %v", err)
	}
	if locator.StateDir != explicitState || locator.CacheDir != explicitCache || locator.SessionsRoot != explicitSessions || locator.LogsDir != explicitLogs {
		t.Fatalf("explicit overrides not honored: %#v", locator)
	}

	locator, err = ResolvePathLocator(cfg, PathOptions{})
	if err != nil {
		t.Fatalf("ResolvePathLocator env: %v", err)
	}
	if locator.StateDir != envState || locator.CacheDir != envCache || locator.SessionsRoot != envSessions || locator.LogsDir != envLogs {
		t.Fatalf("env overrides not honored: %#v", locator)
	}
}

func TestProjectLocatorHomeAbsSanitizeMetadataAndCollision(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	state := t.TempDir()
	cache := t.TempDir()
	locator, err := ResolvePathLocator(nil, PathOptions{StateDir: state, CacheDir: cache})
	if err != nil {
		t.Fatalf("ResolvePathLocator: %v", err)
	}
	project := filepath.Join(home, "Workspace", "coding agents", "chord")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	pl, err := locator.EnsureProject(project)
	if err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	if pl.LogicalRoot != "HOME/Workspace/coding agents/chord" {
		t.Fatalf("LogicalRoot = %q", pl.LogicalRoot)
	}
	if pl.ProjectKey != "HOME-Workspace-coding-agents-chord" {
		t.Fatalf("ProjectKey = %q", pl.ProjectKey)
	}
	if pl.RuntimeCacheDir != filepath.Join(cache, "runtime", "session-cache", pl.ProjectKey) {
		t.Fatalf("RuntimeCacheDir = %q", pl.RuntimeCacheDir)
	}
	data, err := os.ReadFile(pl.ProjectMetaPath)
	if err != nil {
		t.Fatalf("ReadFile(project.json): %v", err)
	}
	var meta ProjectMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("Unmarshal project.json: %v", err)
	}
	if meta.CanonicalRoot != pl.CanonicalRoot || meta.ProjectKey != pl.ProjectKey {
		t.Fatalf("metadata mismatch: %#v locator=%#v", meta, pl)
	}

	other := filepath.Join(home, "Workspace", "coding+agents", "chord")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	collision, err := locator.EnsureProject(other)
	if err != nil {
		t.Fatalf("EnsureProject(collision): %v", err)
	}
	if !strings.HasPrefix(collision.ProjectKey, "HOME-Workspace-coding-agents-chord--") {
		t.Fatalf("collision ProjectKey = %q", collision.ProjectKey)
	}

	abs := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(abs, 0o755); err != nil {
		t.Fatal(err)
	}
	absPL, err := locator.EnsureProject(abs)
	if err != nil {
		t.Fatalf("EnsureProject(abs): %v", err)
	}
	if !strings.HasPrefix(absPL.LogicalRoot, "ABS/") || !strings.HasPrefix(absPL.ProjectKey, "ABS-") {
		t.Fatalf("ABS locator = %#v", absPL)
	}
}

func TestSanitizeProjectKeyLongPathGetsStableSuffix(t *testing.T) {
	key := SanitizeProjectKey("HOME/" + strings.Repeat("segment/", 80))
	if len(key) > 200 {
		t.Fatalf("key too long: %d", len(key))
	}
	if !strings.Contains(key, "--") {
		t.Fatalf("long key missing fingerprint suffix: %q", key)
	}
}
