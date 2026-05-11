package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanInitAppStartupResolvesStorageAndProjectPaths(t *testing.T) {
	configHome := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	cacheDir := filepath.Join(t.TempDir(), "cache")
	logsDir := filepath.Join(t.TempDir(), "logs")
	projectRoot := t.TempDir()

	t.Setenv("CHORD_CONFIG_HOME", configHome)
	if err := os.WriteFile(filepath.Join(configHome, "config.yaml"), []byte("paths:\n  state_dir: "+stateDir+"\n  cache_dir: "+cacheDir+"\n  logs_dir: "+logsDir+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	plan, err := planInitAppStartup(projectRoot)
	if err != nil {
		t.Fatalf("planInitAppStartup: %v", err)
	}
	if plan.ProjectRoot != projectRoot {
		t.Fatalf("ProjectRoot = %q, want %q", plan.ProjectRoot, projectRoot)
	}
	if plan.ChordDir != filepath.Join(projectRoot, ".chord") {
		t.Fatalf("ChordDir = %q", plan.ChordDir)
	}
	if plan.PathLocator == nil || plan.PathLocator.StateDir != stateDir || plan.PathLocator.CacheDir != cacheDir || plan.PathLocator.LogsDir != logsDir {
		t.Fatalf("PathLocator = %+v, want configured dirs", plan.PathLocator)
	}
	if plan.ProjectLocator == nil || plan.ProjectLocator.ProjectSessionsDir == "" {
		t.Fatalf("ProjectLocator = %+v, want project sessions dir", plan.ProjectLocator)
	}
	if _, err := os.Stat(plan.ProjectLocator.ProjectMetaPath); err != nil {
		t.Fatalf("project metadata not written: %v", err)
	}
}

func TestPlanInitAppStartupReturnsSessionPathError(t *testing.T) {
	configHome := t.TempDir()
	projectRoot := t.TempDir()
	badSessionsRoot := filepath.Join(t.TempDir(), "sessions-file")

	t.Setenv("CHORD_CONFIG_HOME", configHome)
	if err := os.WriteFile(filepath.Join(configHome, "config.yaml"), []byte("paths:\n  sessions_dir: "+badSessionsRoot+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(badSessionsRoot, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write sessions-file: %v", err)
	}

	plan, err := planInitAppStartup(projectRoot)
	if plan != nil {
		t.Fatalf("plan = %+v, want nil", plan)
	}
	if err == nil || !strings.Contains(err.Error(), "resolve project storage paths") {
		t.Fatalf("err = %v, want project storage path error", err)
	}
}
