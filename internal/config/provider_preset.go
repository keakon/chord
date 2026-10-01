package config

import (
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/modelcatalog"
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
// Current runtime behavior is intentionally strict: preset: codex selects the
// official OpenAI ChatGPT/Codex OAuth transport with fixed endpoint constants,
// and the catalog-managed presets (openai, anthropic, gemini) pin the provider
// to their verified official endpoint contract. Endpoint URLs are not
// auto-detected; pointing a preset at a different gateway is a contract error
// — remove the preset and configure the endpoint explicitly instead.
func NormalizeProviderPreset(cfg ProviderConfig) (ProviderConfig, CodexTransportResolution, error) {
	normalized := cfg
	resolution := CodexTransportResolution{}
	if authScheme, err := NormalizeAuthScheme(cfg.AuthScheme); err != nil {
		return cfg, resolution, err
	} else {
		normalized.AuthScheme = authScheme
	}

	preset := strings.TrimSpace(strings.ToLower(cfg.Preset))
	if preset != "" {
		normalized.Preset = preset
	}
	if preset != "" && preset != ProviderPresetCodex {
		endpoint, ok := modelcatalog.EndpointContract(preset)
		if !ok {
			return cfg, resolution, fmt.Errorf("unsupported provider preset %q", cfg.Preset)
		}
		if err := applyEndpointContract(&normalized, endpoint); err != nil {
			return cfg, resolution, err
		}
		return normalized, resolution, nil
	}
	if preset == ProviderPresetCodex && cfg.Type != "" && cfg.Type != ProviderTypeResponses {
		return cfg, resolution, fmt.Errorf("preset %q requires provider.type to be %q", cfg.Preset, ProviderTypeResponses)
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

// applyEndpointContract pins a provider to one catalog endpoint contract. A
// preset is an endpoint contract declaration, not a URL default: explicit
// values that contradict the verified contract are errors, while unset
// endpoint fields are filled from it.
func applyEndpointContract(cfg *ProviderConfig, endpoint modelcatalog.Endpoint) error {
	if cfg.Type != "" && cfg.Type != endpoint.Protocol {
		return fmt.Errorf("preset %q requires provider.type %q; got %q", endpoint.PresetID, endpoint.Protocol, cfg.Type)
	}
	if cfg.APIURL != "" && cfg.APIURL != endpoint.RequestURL {
		return fmt.Errorf("preset %q requires api_url %q; got %q — remove the preset and configure the endpoint explicitly to use a different gateway",
			endpoint.PresetID, endpoint.RequestURL, cfg.APIURL)
	}
	if cfg.TokenURL != "" || cfg.ClientID != "" {
		return fmt.Errorf("preset %q does not carry custom OAuth settings; remove token_url/client_id or the preset", endpoint.PresetID)
	}
	switch endpoint.AuthMethod {
	case AuthSchemeBearer, AuthSchemeAnthropicAPIKey, AuthSchemeAPIKey:
		if cfg.AuthScheme != "" && cfg.AuthScheme != endpoint.AuthMethod {
			return fmt.Errorf("preset %q requires auth_scheme %q; got %q", endpoint.PresetID, endpoint.AuthMethod, cfg.AuthScheme)
		}
		if cfg.AuthScheme == "" {
			cfg.AuthScheme = endpoint.AuthMethod
		}
	default:
		// Transport-inferred auth (e.g. x-goog-api-key on generate-content) is
		// not a config auth_scheme; an explicit value cannot match the contract.
		if cfg.AuthScheme != "" {
			return fmt.Errorf("preset %q infers its authentication from the protocol; remove auth_scheme %q", endpoint.PresetID, cfg.AuthScheme)
		}
	}
	cfg.Type = endpoint.Protocol
	if cfg.APIURL == "" {
		cfg.APIURL = endpoint.RequestURL
	}
	return nil
}
