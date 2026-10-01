package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/config"
)

func writeAuthFixture(t *testing.T, content string) (config.AuthConfig, config.CredentialDeclarations) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	auth, err := config.LoadAuthConfig(path)
	if err != nil {
		t.Fatalf("LoadAuthConfig: %v", err)
	}
	decls, err := config.LoadCredentialDeclarations(path)
	if err != nil {
		t.Fatalf("LoadCredentialDeclarations: %v", err)
	}
	return auth, decls
}

// A provider with no auth.yaml entry at all falls back to its preset's
// default environment variable when that variable is set.
func TestResolveProviderAPIKeysPresetFallback(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-openai-key")
	auth, decls := writeAuthFixture(t, "other: []\n")
	provider := config.ProviderConfig{Preset: "openai"}

	keys := resolveProviderAPIKeys("lab", provider, auth, decls)
	if len(keys) != 1 || keys[0] != "env-openai-key" {
		t.Fatalf("keys = %v, want the OPENAI_API_KEY fallback", keys)
	}
}

// No preset environment variable set: the fallback finds nothing.
func TestResolveProviderAPIKeysPresetEnvUnset(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	auth, decls := writeAuthFixture(t, "other: []\n")
	provider := config.ProviderConfig{Preset: "openai"}

	if keys := resolveProviderAPIKeys("lab", provider, auth, decls); len(keys) != 0 {
		t.Fatalf("keys = %v, want none", keys)
	}
}

// Declared-but-unavailable sources never engage the fallback, even when the
// preset environment variable is set. A declared literal or explicit empty
// string still passes through as the provider's key; an unset $VAR or an
// empty list yield no keys.
func TestResolveProviderAPIKeysDeclaredButUnavailable(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-openai-key")
	t.Setenv("CUSTOM_KEY", "")
	provider := config.ProviderConfig{Preset: "openai"}

	for name, tc := range map[string]struct {
		authYAML string
		want     []string
	}{
		"unset env var":  {authYAML: "lab:\n  - $CUSTOM_KEY\n", want: nil},
		"explicit empty": {authYAML: "lab:\n  - \"\"\n", want: []string{""}},
		"empty list":     {authYAML: "lab: []\n", want: nil},
		"literal":        {authYAML: "lab:\n  - sk-declared\n", want: []string{"sk-declared"}},
	} {
		t.Run(name, func(t *testing.T) {
			auth, decls := writeAuthFixture(t, tc.authYAML)
			keys := resolveProviderAPIKeys("lab", provider, auth, decls)
			if len(keys) != len(tc.want) {
				t.Fatalf("keys = %q, want %q: the preset env var must not join declared sources", keys, tc.want)
			}
			for i := range keys {
				if keys[i] != tc.want[i] {
					t.Fatalf("keys = %q, want %q", keys, tc.want)
				}
			}
		})
	}
}

// A declared, usable credential wins over the preset environment variable.
func TestResolveProviderAPIKeysDeclaredWins(t *testing.T) {
	t.Setenv("CUSTOM_KEY", "declared-key")
	auth, decls := writeAuthFixture(t, "lab:\n  - $CUSTOM_KEY\n")
	t.Setenv("OPENAI_API_KEY", "env-openai-key")
	provider := config.ProviderConfig{Preset: "openai"}

	keys := resolveProviderAPIKeys("lab", provider, auth, decls)
	if len(keys) != 1 || keys[0] != "declared-key" {
		t.Fatalf("keys = %q, want the declared credential", keys)
	}
}

// OAuth presets carry no default environment variable, and providers without
// a managed preset never fall back.
func TestResolveProviderAPIKeysNoFallbackSource(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-openai-key")
	auth, decls := writeAuthFixture(t, "other: []\n")

	codex := config.ProviderConfig{Preset: "codex"}
	if keys := resolveProviderAPIKeys("lab", codex, auth, decls); len(keys) != 0 {
		t.Fatalf("keys = %v, want none for the OAuth preset", keys)
	}
	custom := config.ProviderConfig{Preset: "custom-relay"}
	if keys := resolveProviderAPIKeys("lab", custom, auth, decls); len(keys) != 0 {
		t.Fatalf("keys = %v, want none for an unmanaged preset", keys)
	}
	blank := config.ProviderConfig{}
	if keys := resolveProviderAPIKeys("lab", blank, auth, decls); len(keys) != 0 {
		t.Fatalf("keys = %v, want none without a preset", keys)
	}
}

func TestResolveProviderAPIKeysNormalizesPreset(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sample-key")
	keys := resolveProviderAPIKeys("sample", config.ProviderConfig{Preset: " OpenAI "}, nil, nil)
	if len(keys) != 1 || keys[0] != "sample-key" {
		t.Fatalf("keys = %v", keys)
	}
}
