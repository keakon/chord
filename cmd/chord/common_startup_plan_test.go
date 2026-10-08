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
	contentRoot := t.TempDir()
	workDir := t.TempDir()

	t.Setenv("CHORD_CONFIG_HOME", configHome)
	if err := os.WriteFile(filepath.Join(configHome, "config.yaml"), []byte("paths:\n  state_dir: "+stateDir+"\n  cache_dir: "+cacheDir+"\n  logs_dir: "+logsDir+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	plan, err := planInitAppStartup(contentRoot, workDir)
	if err != nil {
		t.Fatalf("planInitAppStartup: %v", err)
	}
	if plan.ContentRoot != contentRoot || plan.WorkDir != workDir {
		t.Fatalf("ContentRoot/WorkDir = %q/%q, want %q/%q", plan.ContentRoot, plan.WorkDir, contentRoot, workDir)
	}
	if _, err := os.Stat(filepath.Join(contentRoot, ".chord")); err != nil {
		t.Fatalf(".chord directory not created under content root: %v", err)
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
	withTestStateDir(t)
	configHome := t.TempDir()
	projectRoot := t.TempDir()
	badSessionsRoot := filepath.Join(t.TempDir(), "sessions-file")

	t.Setenv("CHORD_CONFIG_HOME", configHome)
	t.Setenv("CHORD_SESSIONS_DIR", "")
	if err := os.WriteFile(filepath.Join(configHome, "config.yaml"), []byte("paths:\n  sessions_dir: "+badSessionsRoot+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(badSessionsRoot, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write sessions-file: %v", err)
	}

	plan, err := planInitAppStartup(projectRoot, projectRoot)
	if plan != nil {
		t.Fatalf("plan = %+v, want nil", plan)
	}
	if err == nil || !strings.Contains(err.Error(), "resolve project storage paths") {
		t.Fatalf("err = %v, want project storage path error", err)
	}
}

func TestPlanInitAppStartupCollectsCatalogRecommendationLogLines(t *testing.T) {
	withTestStateDir(t)
	if err := os.WriteFile(filepath.Join(flagConfigHome, "config.yaml"), []byte(`providers:
  openai:
    preset: openai
    models:
      gpt-6.1-sol:
        compaction:
          threshold: 0.8
model_pools:
  default: [openai/gpt-6.1-sol]
`), 0o644); err != nil {
		t.Fatalf("write global config: %v", err)
	}
	root := t.TempDir()
	plan, err := planInitAppStartup(root, root)
	if err != nil {
		t.Fatalf("planInitAppStartup: %v", err)
	}
	if len(plan.CatalogConfigLogLines) != 2 {
		t.Fatalf("log lines = %+v, want one compact detail and the summary", plan.CatalogConfigLogLines)
	}
	detail := plan.CatalogConfigLogLines[0]
	for _, want := range []string{
		"providers.openai.models.gpt-6.1-sol.compaction.threshold",
		"differs from verified catalog profile",
		"(current 0.8, recommended 0.25)",
		"declared at " + plan.PathLocator.ConfigHome,
	} {
		if !strings.Contains(detail, want) {
			t.Fatalf("detail = %q, want %q", detail, want)
		}
	}
	for _, command := range []string{"chord config advise", "--keep-current", "--accept"} {
		if strings.Contains(detail, command) {
			t.Fatalf("detail line must leave %q to the CLI: %s", command, detail)
		}
	}
	if !strings.Contains(plan.CatalogConfigLogLines[1], `run "chord config advise"`) {
		t.Fatalf("summary = %q, want the action pointer", plan.CatalogConfigLogLines[1])
	}
	if len(plan.CatalogAdvisories) != 1 {
		t.Fatalf("toast messages = %+v, want the profile recommendation", plan.CatalogAdvisories)
	}
}

func TestPlanInitAppStartupSkipsCatalogLogLinesWhenNothingIsOutstanding(t *testing.T) {
	withTestStateDir(t)
	if err := os.WriteFile(filepath.Join(flagConfigHome, "config.yaml"), []byte(`providers:
  openai:
    preset: openai
model_pools:
  default: [openai/gpt-6.1-sol]
`), 0o644); err != nil {
		t.Fatalf("write global config: %v", err)
	}
	root := t.TempDir()
	plan, err := planInitAppStartup(root, root)
	if err != nil {
		t.Fatalf("planInitAppStartup: %v", err)
	}
	if len(plan.CatalogConfigLogLines) != 0 {
		t.Fatalf("log lines = %+v, want silence when nothing is outstanding", plan.CatalogConfigLogLines)
	}
}

func TestApplyInitAppStartupPlanCopiesResolvedState(t *testing.T) {
	globalCfg := &config.Config{Proxy: "https://global.example"}
	projectCfg := &config.Config{Proxy: "https://project.example"}
	mergedCfg := &config.Config{Proxy: "https://merged.example"}
	pathLocator := &config.PathLocator{ConfigHome: t.TempDir(), StateDir: t.TempDir(), CacheDir: t.TempDir(), LogsDir: t.TempDir()}
	projectLocator := &config.ProjectLocator{ProjectRoot: t.TempDir(), ProjectSessionsDir: t.TempDir()}
	contentRoot := t.TempDir()
	workDir := t.TempDir()
	plan := &initAppStartupPlan{
		ContentRoot:    contentRoot,
		WorkDir:        workDir,
		PathLocator:    pathLocator,
		ProjectLocator: projectLocator,
		ConfigHome:     pathLocator.ConfigHome,
		GlobalConfig:   globalCfg,
		ProjectConfig:  projectCfg,
		Config:         mergedCfg,
	}

	ac := &AppContext{}
	applyInitAppStartupPlan(ac, plan)

	if ac.ContentRoot != plan.ContentRoot || ac.WorkDir != plan.WorkDir || ac.ConfigHome != plan.ConfigHome {
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
