package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
)

func TestConfigAddReplacesExistingCatalogChoice(t *testing.T) {
	for _, existing := range []string{"false", "openai/gpt-5.6-sol"} {
		t.Run(existing, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("CHORD_CONFIG_HOME", dir)
			path := filepath.Join(dir, "config.yaml")
			before := []byte("providers:\n  codex:\n    preset: codex\n    models:\n      gpt-6.1-sol:\n        catalog: " + existing + "\nmodel_pools:\n  default: [codex/gpt-6.1-sol]\n")
			if err := os.WriteFile(path, before, 0o600); err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			if err := runConfigAdd(context.Background(), &output, "codex/gpt-6.1-sol", configAddOptions{catalogID: "sample/missing"}); err == nil {
				t.Fatal("unknown catalog choice was accepted")
			}
			unchanged, err := os.ReadFile(path)
			if err != nil || string(unchanged) != string(before) {
				t.Fatal("failed selection changed config")
			}
			if err := runConfigAdd(context.Background(), &output, "codex/gpt-6.1-sol", configAddOptions{catalogID: "openai/gpt-6-sol"}); err != nil {
				t.Fatal(err)
			}
			resolved, err := config.LoadResolvedConfig(path, "")
			if err != nil {
				t.Fatal(err)
			}
			model := resolved.Config.Providers["codex"].Models["gpt-6.1-sol"]
			if model.Catalog == nil || !model.Catalog.Disabled || model.Limit.Context != 1050000 {
				t.Fatalf("saved model = %+v", model)
			}
		})
	}
}
