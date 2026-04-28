package config

import (
	"fmt"
	"strings"
)

const (
	OpenAICodexSourcePreset = "preset"
)

// OpenAICodexResolution describes whether a provider should use the official
// ChatGPT/Codex OAuth transport and how that decision was reached.
type OpenAICodexResolution struct {
	Enabled bool
	Strict  bool
	Source  string
}

// NormalizeOpenAICodexProvider applies the Codex transport defaults and returns
// transport metadata for a provider configuration.
//
// Current runtime behavior is intentionally strict: only providers with
// preset: codex are treated as OpenAI ChatGPT/Codex OAuth transport.
func NormalizeOpenAICodexProvider(
	cfg ProviderConfig,
	hasOAuthCredential bool,
) (ProviderConfig, OpenAICodexResolution, error) {
	normalized := cfg
	resolution := OpenAICodexResolution{}

	preset := strings.TrimSpace(strings.ToLower(cfg.Preset))
	if preset != "" && preset != ProviderPresetCodex {
		return cfg, resolution, fmt.Errorf("unsupported provider preset %q", cfg.Preset)
	}
	// Only validate type if it's explicitly set.
	if preset == ProviderPresetCodex && cfg.Type != "" && cfg.Type != ProviderTypeResponses {
		return cfg, resolution, fmt.Errorf("preset %q requires provider.type to be %q", cfg.Preset, ProviderTypeResponses)
	}
	if preset != ProviderPresetCodex {
		return normalized, resolution, nil
	}

	resolution.Enabled = true
	resolution.Strict = true
	resolution.Source = OpenAICodexSourcePreset

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

	// Official ChatGPT/Codex OAuth Responses API rejects store=true (400:
	// "Store must be set to false"). Leave Store unset so EffectiveStore is false;
	// ResponsesProvider still forces store=false on the wire for OAuth keys.

	return normalized, resolution, nil
}
