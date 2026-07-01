package config

import (
	"fmt"
	"strings"
)

const (
	CodexTransportSourcePreset = "preset"
)

// CodexTransportResolution describes whether a provider should use the official
// ChatGPT/Codex OAuth transport and how that decision was reached.
type CodexTransportResolution struct {
	Enabled bool
	Strict  bool
	Source  string
}

// NormalizeProviderPreset applies known provider preset defaults and returns
// Codex transport metadata for a provider configuration.
//
// Current runtime behavior is intentionally strict: only providers with
// preset: codex are treated as OpenAI ChatGPT/Codex OAuth transport. Azure
// support is explicit via preset: azure; endpoint URLs are not auto-detected.
func NormalizeProviderPreset(cfg ProviderConfig) (ProviderConfig, CodexTransportResolution, error) {
	normalized := cfg
	resolution := CodexTransportResolution{}
	if authScheme, err := NormalizeAuthScheme(cfg.AuthScheme); err != nil {
		return cfg, resolution, err
	} else {
		normalized.AuthScheme = authScheme
	}

	preset := strings.TrimSpace(strings.ToLower(cfg.Preset))
	if preset != "" && preset != ProviderPresetCodex && preset != ProviderPresetAzure {
		return cfg, resolution, fmt.Errorf("unsupported provider preset %q", cfg.Preset)
	}
	if preset != "" {
		normalized.Preset = preset
	}
	// Only validate type if it's explicitly set.
	if (preset == ProviderPresetCodex || preset == ProviderPresetAzure) && cfg.Type != "" && cfg.Type != ProviderTypeResponses {
		return cfg, resolution, fmt.Errorf("preset %q requires provider.type to be %q", cfg.Preset, ProviderTypeResponses)
	}
	if preset == ProviderPresetAzure {
		if !APIURLPathHasSuffix(normalized.APIURL, "/responses") {
			return cfg, resolution, fmt.Errorf("preset=%s requires api_url path ending in /responses", ProviderPresetAzure)
		}
		normalized.Type = ProviderTypeResponses
		if normalized.Store == nil {
			store := true
			normalized.Store = &store
		}
		return normalized, resolution, nil
	}
	if preset != ProviderPresetCodex {
		return normalized, resolution, nil
	}

	resolution.Enabled = true
	resolution.Strict = true
	resolution.Source = CodexTransportSourcePreset

	if normalized.TokenURL == "" {
		normalized.TokenURL = OpenAIOAuthTokenURL
	}
	if normalized.ClientID == "" {
		normalized.ClientID = OpenAIOAuthClientID
	}
	if normalized.APIURL == "" {
		normalized.APIURL = OpenAICodexResponsesURL
	}
	normalized.Type = ProviderTypeResponses

	if normalized.TokenURL != OpenAIOAuthTokenURL ||
		normalized.ClientID != OpenAIOAuthClientID ||
		normalized.APIURL != OpenAICodexResponsesURL {
		return cfg, resolution, fmt.Errorf(
			"preset=%s requires api_url=%s, token_url=%s, client_id=%s",
			ProviderPresetCodex,
			OpenAICodexResponsesURL,
			OpenAIOAuthTokenURL,
			OpenAIOAuthClientID,
		)
	}

	// Leave Store unset so Responses requests default to store=false while
	// preserving any explicit provider/model override.

	return normalized, resolution, nil
}
