package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/keakon/chord/internal/config"
)

func TestSelectedProviderNames(t *testing.T) {
	providers := map[string]config.ProviderConfig{
		"zeta":  {},
		"alpha": {},
		"beta":  {},
	}

	t.Run("sorted without filter", func(t *testing.T) {
		got, err := selectedProviderNames(providers, "")
		if err != nil {
			t.Fatalf("selectedProviderNames: %v", err)
		}
		want := []string{"alpha", "beta", "zeta"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("selectedProviderNames() = %#v, want %#v", got, want)
		}
	})

	t.Run("returns exact filter", func(t *testing.T) {
		got, err := selectedProviderNames(providers, "beta")
		if err != nil {
			t.Fatalf("selectedProviderNames(filter): %v", err)
		}
		want := []string{"beta"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("selectedProviderNames(filter) = %#v, want %#v", got, want)
		}
	})

	t.Run("errors when filter missing", func(t *testing.T) {
		if _, err := selectedProviderNames(providers, "missing"); err == nil {
			t.Fatal("expected missing provider filter to return an error")
		}
	})

	t.Run("errors when none configured", func(t *testing.T) {
		if _, err := selectedProviderNames(nil, ""); err == nil {
			t.Fatal("expected no providers configured error")
		}
	})
}

func TestTestProvidersUsesOnlyCurrentWorkingDirectoryProjectConfig(t *testing.T) {
	configHome := t.TempDir()
	projectRoot := t.TempDir()
	nested := filepath.Join(projectRoot, "nested", "child")
	t.Setenv("CHORD_CONFIG_HOME", configHome)

	if err := os.WriteFile(filepath.Join(configHome, "config.yaml"), []byte(`providers:
  global:
    type: responses
    api_url: https://global.example/v1/responses
    models:
      gpt-5:
        limit:
          context: 8192
          output: 1024
`), 0o644); err != nil {
		t.Fatalf("write global config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(projectRoot, ".chord"), 0o755); err != nil {
		t.Fatalf("mkdir project .chord: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, ".chord", "config.yaml"), []byte(`providers:
  root:
    type: responses
    api_url: https://root.example/v1/responses
    models:
      gpt-5:
        limit:
          context: 4096
          output: 512
`), 0o644); err != nil {
		t.Fatalf("write root project config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(nested, ".chord"), 0o755); err != nil {
		t.Fatalf("mkdir nested .chord: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, ".chord", "config.yaml"), []byte(`providers:
  nested:
    type: responses
    api_url: https://nested.example/v1/responses
    models:
      gpt-5-mini:
        limit:
          context: 2048
          output: 256
`), 0o644); err != nil {
		t.Fatalf("write nested project config: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	defer func() {
		if chdirErr := os.Chdir(cwd); chdirErr != nil {
			t.Fatalf("restore cwd: %v", chdirErr)
		}
	}()
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("Chdir(nested): %v", err)
	}

	cfg, err := config.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	_, mergedCfg, err := config.MergeProjectConfig(cfg, config.ProjectConfigPath(nested))
	if err != nil {
		t.Fatalf("MergeProjectConfig: %v", err)
	}
	got, err := selectedProviderNames(mergedCfg.Providers, "")
	if err != nil {
		t.Fatalf("selectedProviderNames: %v", err)
	}
	want := []string{"global", "nested"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged provider names = %#v, want %#v", got, want)
	}
	if _, ok := mergedCfg.Providers["root"]; ok {
		t.Fatal("did not expect parent-directory project config to be merged")
	}
}

func TestFirstSortedModelID(t *testing.T) {
	models := map[string]config.ModelConfig{
		"gpt-5.5":       {},
		"gpt-4.1-mini":  {},
		"claude-sonnet": {},
	}
	if got := firstSortedModelID(models); got != "claude-sonnet" {
		t.Fatalf("firstSortedModelID() = %q, want claude-sonnet", got)
	}
	if got := firstSortedModelID(nil); got != "" {
		t.Fatalf("firstSortedModelID(nil) = %q, want empty", got)
	}
}
