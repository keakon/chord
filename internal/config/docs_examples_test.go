package config

import (
	"path/filepath"
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
			cfg, err := LoadConfigFromPath(path)
			if err != nil {
				t.Fatalf("LoadConfigFromPath(%s): %v", path, err)
			}
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
					t.Fatalf("provider %q must define at least one model", providerName)
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
					if _, _, _, _, _, err := ResolveConfiguredModelRef(cfg.Providers, ref); err != nil {
						t.Fatalf("model pool %q ref %q does not resolve: %v", poolName, ref, err)
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
