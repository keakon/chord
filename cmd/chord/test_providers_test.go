package main

import (
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
