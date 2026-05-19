package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/config"
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

func TestApplyInitAppStartupPlanCopiesResolvedState(t *testing.T) {
	globalCfg := &config.Config{Proxy: "https://global.example"}
	projectCfg := &config.Config{Proxy: "https://project.example"}
	mergedCfg := &config.Config{Proxy: "https://merged.example"}
	pathLocator := &config.PathLocator{ConfigHome: t.TempDir(), StateDir: t.TempDir(), CacheDir: t.TempDir(), LogsDir: t.TempDir()}
	projectLocator := &config.ProjectLocator{ProjectRoot: t.TempDir(), ProjectSessionsDir: t.TempDir()}
	plan := &initAppStartupPlan{
		ProjectRoot:    projectLocator.ProjectRoot,
		ChordDir:       filepath.Join(projectLocator.ProjectRoot, ".chord"),
		PathLocator:    pathLocator,
		ProjectLocator: projectLocator,
		ConfigHome:     pathLocator.ConfigHome,
		GlobalConfig:   globalCfg,
		ProjectConfig:  projectCfg,
		Config:         mergedCfg,
	}

	ac := &AppContext{}
	applyInitAppStartupPlan(ac, plan)

	if ac.ProjectRoot != plan.ProjectRoot || ac.ChordDir != plan.ChordDir || ac.ConfigHome != plan.ConfigHome {
		t.Fatalf("basic paths not copied: ac=%+v plan=%+v", ac, plan)
	}
	if ac.PathLocator != pathLocator || ac.ProjectLocator != projectLocator || ac.GlobalCfg != globalCfg || ac.ProjectCfg != projectCfg || ac.Cfg != mergedCfg {
		t.Fatalf("resolved pointers not copied: ac=%+v", ac)
	}

	applyInitAppStartupPlan(nil, plan)
	applyInitAppStartupPlan(ac, nil)
}

func TestResolveInitialModelSelectionUsesRuntimePoolPolicy(t *testing.T) {
	builder := &config.AgentConfig{
		Name:    "builder",
		Variant: "default",
		Models: map[string][]string{
			"base": {"openai/base"},
			"fast": {"openai/fast@low", "anthropic/fast"},
		},
	}
	policy := agent.NewRuntimeModelPoolPolicy()
	policy.SetCurrentModelPool("fast")

	providerModel, variant := resolveInitialModelSelection(map[string]*config.AgentConfig{"builder": builder}, policy)
	if providerModel != "openai/fast" || variant != "low" {
		t.Fatalf("selection = %q @ %q, want openai/fast @ low", providerModel, variant)
	}
}

func TestResolveInitialModelSelectionFallsBackToFirstPoolAndAgentVariant(t *testing.T) {
	builder := &config.AgentConfig{
		Name:    "builder",
		Variant: "default",
		Models: map[string][]string{
			"base": {"openai/base"},
		},
	}

	providerModel, variant := resolveInitialModelSelection(map[string]*config.AgentConfig{"builder": builder}, agent.NewRuntimeModelPoolPolicy())
	if providerModel != "openai/base" || variant != "default" {
		t.Fatalf("selection = %q @ %q, want openai/base @ default", providerModel, variant)
	}
}

func TestResolveInitialModelSelectionHandlesMissingBuilder(t *testing.T) {
	if providerModel, variant := resolveInitialModelSelection(nil, agent.NewRuntimeModelPoolPolicy()); providerModel != "" || variant != "" {
		t.Fatalf("nil configs selection = %q @ %q, want empty", providerModel, variant)
	}
	if providerModel, variant := resolveInitialModelSelection(map[string]*config.AgentConfig{}, agent.NewRuntimeModelPoolPolicy()); providerModel != "" || variant != "" {
		t.Fatalf("missing builder selection = %q @ %q, want empty", providerModel, variant)
	}
}
