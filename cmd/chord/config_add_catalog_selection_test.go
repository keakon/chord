package main

import (
	"testing"

	"github.com/keakon/chord/internal/config"
)

func TestConfigAddAutomaticBindingDoesNotCreateBorrow(t *testing.T) {
	for _, exists := range []bool{false, true} {
		provider := config.ProviderConfig{}
		if exists {
			provider.Preset = "codex"
		}
		got, wire, opts, err := prepareCatalogAdd("codex/gpt-6.1-sol", provider, exists, "gpt-6.1-sol", configAddOptions{})
		if err != nil || opts.catalogID != "" {
			t.Fatalf("automatic selection = %+v, %v", opts, err)
		}
		mode, _, err := resolveConfigAddMode(got.Preset, wire, opts.catalogID)
		if err != nil || mode != configAddExact {
			t.Fatalf("automatic mode = %s, %v", mode, err)
		}
	}
}
