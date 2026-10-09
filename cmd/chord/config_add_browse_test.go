package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
)

func browseConfigAddTerminal(input string) (*setupTerminal, *strings.Builder) {
	out := &strings.Builder{}
	return &setupTerminal{reader: bufio.NewReader(strings.NewReader(input)), out: out}, out
}

func TestBrowseConfigAddExistingProvider(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.ProviderConfig{"openai": {Preset: "openai"}}}
	providers := catalogAddProviders(cfg)
	choice := slices.IndexFunc(providers, func(c configAddProviderChoice) bool { return c.name == "openai" && c.exists })
	if choice < 0 {
		t.Fatalf("configured openai provider missing from %+v", providers)
	}
	bindings := catalogAddBindings("openai")
	if len(bindings) == 0 {
		t.Fatal("openai preset has no verified bindings")
	}
	target := len(bindings) // exercise that model labels map back to their index
	input := fmt.Sprintf("%d%d", choice+1, target)
	terminal, out := browseConfigAddTerminal(input)
	provider, wire, err := browseConfigAdd(terminal, cfg)
	if err != nil {
		t.Fatalf("browseConfigAdd: %v", err)
	}
	if provider != "openai" || wire != bindings[target-1].WireModelID {
		t.Fatalf("selection = %s/%s, want openai/%s (output: %s)", provider, wire, bindings[target-1].WireModelID, out.String())
	}
	if !strings.Contains(out.String(), "context ") {
		t.Fatalf("model menu lacks fact hints: %s", out.String())
	}
}

func TestBrowseConfigAddNewProviderAndConfiguredMarker(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.ProviderConfig{
		"anthropic-work": {Preset: "anthropic"},
	}}
	providers := catalogAddProviders(cfg)
	presets := map[string]bool{}
	for _, choice := range providers {
		if preset := choice.preset; preset != "" {
			presets[preset] = true
		}
	}
	if !presets["anthropic"] || !presets["openai"] || !presets["codex"] || !presets["gemini"] {
		t.Fatalf("managed presets missing from %+v", providers)
	}
	choice := slices.IndexFunc(providers, func(c configAddProviderChoice) bool { return c.name == "anthropic-work" })
	if choice < 0 {
		t.Fatalf("configured anthropic provider missing from %+v", providers)
	}
	bindings := catalogAddBindings("anthropic")
	configured := bindings[0].WireModelID
	cfg.Providers["anthropic"] = config.ProviderConfig{Preset: "anthropic", Models: map[string]config.ModelConfig{configured: {}}}
	providers = catalogAddProviders(cfg)
	choice = slices.IndexFunc(providers, func(c configAddProviderChoice) bool { return c.name == "anthropic" && c.exists })
	if choice < 0 {
		t.Fatalf("literal anthropic provider missing from %+v", providers)
	}
	input := fmt.Sprintf("%d1", choice+1)
	terminal, out := browseConfigAddTerminal(input)
	provider, wire, err := browseConfigAdd(terminal, cfg)
	if err != nil {
		t.Fatalf("browseConfigAdd: %v", err)
	}
	if provider != "anthropic" || wire != configured {
		t.Fatalf("selection = %s/%s, want anthropic/%s", provider, wire, configured)
	}
	if !strings.Contains(out.String(), " — configured") {
		t.Fatalf("configured model not marked: %s", out.String())
	}

	// Without any configured provider the same preset is offered as a new one.
	terminal, out = browseConfigAddTerminal("11")
	if _, _, err := browseConfigAdd(terminal, &config.Config{}); err != nil {
		t.Fatalf("browseConfigAdd new provider: %v", err)
	}
	if !strings.Contains(out.String(), "(new provider)") {
		t.Fatalf("new provider not marked: %s", out.String())
	}
}

func TestBrowseConfigAddCancels(t *testing.T) {
	terminal, _ := browseConfigAddTerminal("q")
	if _, _, err := browseConfigAdd(terminal, &config.Config{}); err != errConfigAddCancelled {
		t.Fatalf("err = %v, want cancellation", err)
	}
}

func TestRunConfigAddNoArgsNeedsTerminal(t *testing.T) {
	t.Setenv("CHORD_CONFIG_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	err := runConfigAdd(context.Background(), io.Discard, "", configAddOptions{})
	if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("err = %v, want interactive terminal guidance", err)
	}
	err = runConfigAdd(context.Background(), io.Discard, "", configAddOptions{keepCurrent: true})
	if err == nil || !strings.Contains(err.Error(), "--keep-current") {
		t.Fatalf("err = %v, want explicit reference requirement", err)
	}
}
