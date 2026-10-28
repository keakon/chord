package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Emulation client names selected by the provider- and model-level emulate key.
const (
	EmulationClientCodex  = "codex"
	EmulationClientClaude = "claude"
)

// EmulationConfig defines the per-client wire-identity payloads a provider or
// model can present as through its emulate key. Defining a payload enables
// nothing by itself: a provider or model must reference the client by name, so
// only the endpoints a user actually points at a client-expecting relay change
// their request face. Each payload is copied onto every provider at load, so
// there are no per-provider or per-model payload definitions.
type EmulationConfig struct {
	// Codex writes Responses requests the way the Codex client does.
	Codex *CodexEmulationConfig `json:"codex,omitempty" yaml:"codex,omitempty"`
	// Claude adds the Claude Code client's request identity on messages-wire
	// requests.
	Claude *ClaudeEmulationConfig `json:"claude,omitempty" yaml:"claude,omitempty"`
}

// payloadFor returns the payload a client name references, or nil when the name
// is unknown or the referenced block is missing.
func (e *EmulationConfig) payloadFor(client string) emulationPayload {
	if e == nil {
		return nil
	}
	switch client {
	case EmulationClientCodex:
		return e.Codex
	case EmulationClientClaude:
		return e.Claude
	default:
		return nil
	}
}

// emulationPayload is the completeness check a referenced client payload must
// pass before a provider or model may present as that client.
type emulationPayload interface {
	emulationEnabled() bool
}

// ApplyEmulation attaches the payload definitions to every provider so a
// request can resolve its emulate selector without reaching back into the
// global config, and validates every selector: one that names an unknown
// client, references a missing or incomplete payload, or targets a provider
// whose wire protocol the client does not speak cannot work and is reported as
// an error diagnostic. The provider and model stay loaded and simply do not
// emulate.
func ApplyEmulation(cfg *Config) []Diagnostic {
	if cfg == nil {
		return nil
	}
	// Trim the payload version fields so the wire always sees the values the
	// user meant; the completeness checks accept surrounding whitespace.
	if codex := cfg.Emulation.Codex; codex != nil {
		codex.Version = strings.TrimSpace(codex.Version)
	}
	if claude := cfg.Emulation.Claude; claude != nil {
		claude.Version = strings.TrimSpace(claude.Version)
		claude.SDKVersion = strings.TrimSpace(claude.SDKVersion)
		claude.RuntimeVersion = strings.TrimSpace(claude.RuntimeVersion)
	}
	// Sorted iteration keeps the diagnostic order stable across runs: doctor
	// output and test snapshots compare the sequence, not a set.
	var diags []Diagnostic
	for _, providerName := range slices.Sorted(maps.Keys(cfg.Providers)) {
		provider := cfg.Providers[providerName]
		provider.Emulation = &cfg.Emulation
		cfg.Providers[providerName] = provider
		diags = append(diags, validateEmulateSelector(cfg, providerName, provider, provider.Emulate, "")...)
		for _, modelName := range slices.Sorted(maps.Keys(provider.Models)) {
			diags = append(diags, validateEmulateSelector(cfg, providerName, provider, provider.Models[modelName].Emulate, modelName)...)
		}
	}
	return diags
}

// validateEmulateSelector checks one emulate value against the payload it
// references and the provider's effective wire type — the same three-tier
// resolution (explicit type, codex preset, api_url inference) the runtime
// applies, so a provider that omits type: is judged by the type its endpoint
// implies rather than flagged for an empty string. modelName is empty for the
// provider-level selector.
func validateEmulateSelector(cfg *Config, providerName string, provider ProviderConfig, emulate, modelName string) []Diagnostic {
	emulate = strings.ToLower(strings.TrimSpace(emulate))
	if emulate == "" {
		return nil
	}
	path := "providers." + providerName + ".emulate"
	scope := "provider " + providerName
	if modelName != "" {
		path = "providers." + providerName + ".models." + modelName + ".emulate"
		scope += ", model " + modelName
	}
	fail := func(format string, args ...any) []Diagnostic {
		return []Diagnostic{{
			Severity:     DiagnosticSeverityError,
			Path:         path,
			PathSegments: []string{"providers", providerName},
			Message:      fmt.Sprintf(format, args...),
			Continues:    true,
			Scope:        scope,
		}}
	}
	providerType := EffectiveProviderType(provider)
	switch emulate {
	case EmulationClientCodex:
		if !strings.EqualFold(strings.TrimSpace(providerType), ProviderTypeResponses) {
			return fail("codex emulation writes the Responses body shape and only applies to type: responses providers")
		}
	case EmulationClientClaude:
		if !strings.EqualFold(strings.TrimSpace(providerType), ProviderTypeMessages) {
			return fail("claude emulation adds the Claude Code request identity and only applies to type: messages providers")
		}
	default:
		return fail("unknown emulation client %q (supported: %s, %s)", emulate, EmulationClientCodex, EmulationClientClaude)
	}
	payload := cfg.Emulation.payloadFor(emulate)
	if payload == nil || !payload.emulationEnabled() {
		return fail("emulate: %s references an emulation payload that is missing or incomplete (fill emulation.%s at the top level first)", emulate, emulate)
	}
	return nil
}
