package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDocsExampleConfigsLoad(t *testing.T) {
	exampleDir := filepath.Join("..", "..", "docs", "examples")
	paths, err := filepath.Glob(filepath.Join(exampleDir, "*.yaml"))
	if err != nil {
		t.Fatalf("Glob docs examples: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no docs example configs found")
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			resolved, err := LoadResolvedConfig(path, "")
			if err != nil {
				t.Fatalf("LoadResolvedConfig(%s): %v", path, err)
			}
			for _, diagnostic := range resolved.Diagnostics {
				if diagnostic.Severity == DiagnosticSeverityError {
					t.Fatalf("invalid example config: %+v", diagnostic)
				}
			}
			cfg := resolved.Config
			if len(cfg.Providers) == 0 {
				t.Fatal("example must configure at least one provider")
			}
			if len(cfg.ModelPools) == 0 {
				t.Fatal("example must configure at least one model pool")
			}
			for providerName, provider := range cfg.Providers {
				if strings.TrimSpace(providerName) == "" {
					t.Fatal("provider name must not be empty")
				}
				if strings.TrimSpace(provider.Type) == "" && strings.TrimSpace(provider.Preset) == "" {
					t.Fatalf("provider %q must set type or preset", providerName)
				}
				if len(provider.Models) == 0 {
					t.Fatalf("provider %q must resolve at least one model", providerName)
				}
				for modelName, model := range provider.Models {
					if strings.TrimSpace(modelName) == "" {
						t.Fatalf("provider %q contains an empty model name", providerName)
					}
					// limit.input is intentionally optional: examples set it only
					// when the provider publishes a separate input cap, matching
					// the documented guidance.
					if model.Limit.Context <= 0 || model.Limit.Output <= 0 {
						t.Fatalf("provider %q model %q must define positive context/output limits: %+v", providerName, modelName, model.Limit)
					}
					if model.Limit.Input < 0 {
						t.Fatalf("provider %q model %q must not define a negative input limit: %+v", providerName, modelName, model.Limit)
					}
				}
			}
			for poolName, refs := range cfg.ModelPools {
				if strings.TrimSpace(poolName) == "" {
					t.Fatal("model pool name must not be empty")
				}
				if len(refs) == 0 {
					t.Fatalf("model pool %q must contain at least one model ref", poolName)
				}
				for _, ref := range refs {
					providerName, modelName, variant, _, model, err := ResolveConfiguredModelRef(cfg.Providers, ref)
					if err != nil {
						t.Fatalf("model pool %q ref %q does not resolve: %v", poolName, ref, err)
					}
					if err := ValidateConfiguredVariant(model, providerName, modelName, variant); err != nil {
						t.Fatalf("model pool %q ref %q has an invalid variant: %v", poolName, ref, err)
					}
				}
			}
			if poolName := strings.TrimSpace(cfg.Context.Compaction.ModelPool); poolName != "" {
				if _, ok := cfg.ModelPools[poolName]; !ok {
					t.Fatalf("context.compaction.model_pool %q does not exist in model_pools", poolName)
				}
			}
		})
	}
}

func TestDocsTeamExampleRoles(t *testing.T) {
	exampleDir := filepath.Join("..", "..", "docs", "examples")
	configPath := filepath.Join(exampleDir, "team-ready.yaml")
	standalone, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	_, providerBody, ok := strings.Cut(string(standalone), "providers:")
	if !ok {
		t.Fatal("team config must contain providers")
	}
	wantGlobal := strings.TrimSpace("providers:" + providerBody)
	wantPools := map[string]string{
		"orchestrator": "deep",
		"coder":        "coding",
		"explorer":     "fast",
		"reviewer":     "deep",
		"expert":       "deep",
	}
	canonicalRoles := make(map[string]string)
	for _, filename := range []string{"examples-team.md", "examples-team_CN.md"} {
		t.Run(filename, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(exampleDir, filename))
			if err != nil {
				t.Fatal(err)
			}
			block := func(heading, language string) string {
				t.Helper()
				marker := "## `" + heading + "`\n\n```" + language + "\n"
				_, rest, found := strings.Cut(string(data), marker)
				if !found {
					t.Fatalf("missing example block for %s", heading)
				}
				body, _, found := strings.Cut(rest, "\n```")
				if !found {
					t.Fatalf("unterminated example block for %s", heading)
				}
				return body
			}
			if got := strings.TrimSpace(block("~/.config/chord/config.yaml", "yaml")); got != wantGlobal {
				t.Fatal("global config block differs from team-ready.yaml")
			}
			dir := t.TempDir()
			agents := make(map[string]*AgentConfig)
			for name, pool := range wantPools {
				body := block("<repo>/.chord/agents/"+name+".md", "md")
				if previous, exists := canonicalRoles[name]; exists && previous != body {
					t.Fatalf("%s role differs between language examples", name)
				}
				canonicalRoles[name] = body
				path := filepath.Join(dir, name+".md")
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
				agent, err := LoadAgentConfig(path)
				if err != nil {
					t.Fatalf("load %s: %v", name, err)
				}
				wantMode := AgentModeSubAgent
				if name == "orchestrator" {
					wantMode = AgentModeMain
				}
				if agent.Mode != wantMode || !slices.Equal(agent.ModelPools, []string{pool}) {
					t.Fatalf("%s mode/pools = %s/%v, want %s/[%s]", name, agent.Mode, agent.ModelPools, wantMode, pool)
				}
				agents[name] = agent
			}
			projectPath := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(projectPath, []byte(block("<repo>/.chord/config.yaml", "yaml")), 0o600); err != nil {
				t.Fatal(err)
			}
			resolved, err := LoadResolvedConfig(configPath, projectPath)
			if err != nil {
				t.Fatalf("resolve combined config: %v", err)
			}
			for _, diagnostic := range resolved.Diagnostics {
				if diagnostic.Severity == DiagnosticSeverityError {
					t.Fatalf("invalid combined config: %+v", diagnostic)
				}
			}
			if err := ResolveAgentModelPools(agents, resolved.Config.ModelPools); err != nil {
				t.Fatalf("resolve team pools: %v", err)
			}
			if resolved.Project.Context.Compaction.ModelPool != "coding" || resolved.Global.Context.Compaction.ModelPool != "coding" {
				t.Fatal("global and project compaction must use the coding pool")
			}
		})
	}
}
