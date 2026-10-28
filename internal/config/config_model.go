package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// ModelModalities declares which input modalities a model supports.
type ModelModalities struct {
	Input []string `json:"input,omitempty" yaml:"input,omitempty"` // "text", "image", "pdf"
}

// ModelCatalogRef selects the verified model facts used to materialize a
// model. A string binds a custom wire model ID to a stable catalog model; the
// disabled form keeps the endpoint preset contract while opting the model out
// of all catalog filling.
type ModelCatalogRef struct {
	ID       string `json:"-"`
	Disabled bool   `json:"-"`
}

func (r *ModelCatalogRef) UnmarshalYAML(value *yaml.Node) error {
	if value == nil {
		return fmt.Errorf("catalog must be a non-empty string or false")
	}
	switch value.Tag {
	case "!!str":
		r.ID = strings.TrimSpace(value.Value)
		if r.ID == "" {
			return fmt.Errorf("catalog must be a non-empty string or false")
		}
		r.Disabled = false
	case "!!bool":
		if strings.EqualFold(strings.TrimSpace(value.Value), "false") {
			r.ID = ""
			r.Disabled = true
			return nil
		}
		return fmt.Errorf("catalog must be a non-empty string or false")
	default:
		return fmt.Errorf("catalog must be a non-empty string or false")
	}
	return nil
}

func (r ModelCatalogRef) MarshalYAML() (any, error) {
	if r.Disabled {
		return false, nil
	}
	return r.ID, nil
}

func (r ModelCatalogRef) MarshalJSON() ([]byte, error) {
	if r.Disabled {
		return []byte("false"), nil
	}
	if strings.TrimSpace(r.ID) == "" {
		return nil, fmt.Errorf("catalog must be a non-empty string or false")
	}
	return json.Marshal(r.ID)
}

func (r *ModelCatalogRef) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("false")) {
		r.ID, r.Disabled = "", true
		return nil
	}
	var id string
	if len(data) == 0 || data[0] != '"' || json.Unmarshal(data, &id) != nil {
		return fmt.Errorf("catalog must be a non-empty string or false")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("catalog must be a non-empty string or false")
	}
	r.ID, r.Disabled = id, false
	return nil
}

// ModelConfig specifies a model and its parameters.
type ModelConfig struct {
	ImageGeneration       *ImageModelConfig            `json:"image_generation,omitempty" yaml:"image_generation,omitempty"`
	NativeImageGeneration *NativeImageGenerationConfig `json:"native_image_generation,omitempty" yaml:"native_image_generation,omitempty"`
	NativeWebSearch       *NativeWebSearchConfig       `json:"native_web_search,omitempty" yaml:"native_web_search,omitempty"`
	Name                  string                       `json:"name" yaml:"name"`
	Limit                 ModelLimit                   `json:"limit" yaml:"limit"`
	Catalog               *ModelCatalogRef             `json:"catalog,omitempty" yaml:"catalog,omitempty"`
	Modalities            *ModelModalities             `json:"modalities,omitempty" yaml:"modalities,omitempty"`
	SupportedServiceTiers []ServiceTier                `json:"supported_service_tiers,omitempty" yaml:"supported_service_tiers,omitempty"` // explicit non-standard tiers supported by this model
	Compaction            *ModelCompactionConfig       `json:"compaction,omitempty" yaml:"compaction,omitempty"`                           // per-model compaction threshold/reminder overrides; nil inherits the global context.compaction values
	Thinking              *ThinkingConfig              `json:"thinking,omitempty" yaml:"thinking,omitempty"`
	Reasoning             *ReasoningConfig             `json:"reasoning,omitempty" yaml:"reasoning,omitempty"`
	Text                  *TextConfig                  `json:"text,omitempty" yaml:"text,omitempty"`
	ParallelToolCalls     *bool                        `json:"parallel_tool_calls,omitempty" yaml:"parallel_tool_calls,omitempty"` // nil = inherit provider default (itself defaulting to true); non-nil = explicit override
	PromptCache           *PromptCacheConfig           `json:"prompt_cache,omitempty" yaml:"prompt_cache,omitempty"`
	Compat                *ModelCompatConfig           `json:"compat,omitempty" yaml:"compat,omitempty"`
	Cost                  *ModelCost                   `json:"cost,omitempty" yaml:"cost,omitempty"`
	Store                 *bool                        `json:"store,omitempty" yaml:"store,omitempty"`     // model-level Responses storage preference; nil inherits provider/default false
	Emulate               string                       `json:"emulate,omitempty" yaml:"emulate,omitempty"` // client this model presents as on the wire ("codex", "claude"); empty inherits the provider-level emulate value
	Variants              map[string]ModelVariant      `json:"variants,omitempty" yaml:"variants,omitempty"`
}

// SplitProviderModelRef splits a model reference base into provider and model parts.
// The input should be in the form "provider/model" without any @variant suffix.
func SplitProviderModelRef(base string) (provider, model string) {
	parts := strings.SplitN(strings.TrimSpace(base), "/", 2)
	if len(parts) != 2 {
		return strings.TrimSpace(base), ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}

// CanonicalModelRef formats a provider/model reference with an optional @variant suffix.
func CanonicalModelRef(provider, model, variant string) string {
	ref := strings.TrimSpace(provider) + "/" + strings.TrimSpace(model)
	if strings.TrimSpace(variant) != "" {
		ref += "@" + strings.TrimSpace(variant)
	}
	return ref
}

// LookupConfiguredModel validates that provider/model exists in the configured providers.
func LookupConfiguredModel(providers map[string]ProviderConfig, providerName, modelName string) (ProviderConfig, ModelConfig, error) {
	providerName = strings.TrimSpace(providerName)
	modelName = strings.TrimSpace(modelName)
	providerCfg, ok := providers[providerName]
	if !ok {
		return ProviderConfig{}, ModelConfig{}, fmt.Errorf("provider %q not found in config", providerName)
	}
	modelCfg, ok := providerCfg.Models[modelName]
	if !ok {
		return ProviderConfig{}, ModelConfig{}, fmt.Errorf("model %q not found in provider %q", modelName, providerName)
	}
	return providerCfg, modelCfg, nil
}

// ValidateConfiguredVariant validates that the named variant exists for the model.
// An empty variant is always accepted.
func ValidateConfiguredVariant(modelCfg ModelConfig, providerName, modelName, variantName string) error {
	variantName = strings.TrimSpace(variantName)
	if variantName == "" {
		return nil
	}
	if _, ok := modelCfg.Variants[variantName]; !ok {
		return fmt.Errorf("variant %q not found for model %q in provider %q", variantName, modelName, providerName)
	}
	return nil
}

// LookupConfiguredModelVariant validates that provider/model[/@variant] exists in the configured providers.
func LookupConfiguredModelVariant(providers map[string]ProviderConfig, providerName, modelName, variantName string) (ProviderConfig, ModelConfig, error) {
	providerCfg, modelCfg, err := LookupConfiguredModel(providers, providerName, modelName)
	if err != nil {
		return ProviderConfig{}, ModelConfig{}, err
	}
	if err := ValidateConfiguredVariant(modelCfg, providerName, modelName, variantName); err != nil {
		return ProviderConfig{}, ModelConfig{}, err
	}
	return providerCfg, modelCfg, nil
}

// ResolveConfiguredModelRef parses and validates a configured model reference.
// It requires the ref to be in the form "provider/model[@variant]".
// The returned variant is parsed from the ref but intentionally not validated;
// callers that need strict variant checking should call ValidateConfiguredVariant
// or LookupConfiguredModelVariant on the returned model.
func ResolveConfiguredModelRef(providers map[string]ProviderConfig, rawRef string) (providerName, modelName, variantName string, providerCfg ProviderConfig, modelCfg ModelConfig, err error) {
	baseRef, variantName := ParseModelRef(strings.TrimSpace(rawRef))
	baseRef = strings.TrimSpace(baseRef)
	variantName = strings.TrimSpace(variantName)
	if baseRef == "" {
		return "", "", "", ProviderConfig{}, ModelConfig{}, fmt.Errorf("model reference must not be empty")
	}
	if !strings.Contains(baseRef, "/") {
		return "", "", variantName, ProviderConfig{}, ModelConfig{}, fmt.Errorf("model reference %q must include a provider; use provider/model[@variant]", rawRef)
	}

	providerName, modelName = SplitProviderModelRef(baseRef)
	providerCfg, modelCfg, err = LookupConfiguredModel(providers, providerName, modelName)
	if err != nil {
		return providerName, modelName, variantName, ProviderConfig{}, ModelConfig{}, err
	}
	return providerName, modelName, variantName, providerCfg, modelCfg, nil
}

// SupportsInput reports whether the model accepts the given input modality.
// When Modalities is unset, defaults to ["text"] only: image/pdf support must be
// declared explicitly so unconfigured models never receive binary parts they
// cannot process.
func (m *ModelConfig) SupportsInput(modality string) bool {
	if m.Modalities == nil {
		return modality == "text"
	}
	return slices.Contains(m.Modalities.Input, modality)
}

func serviceTierSet(rawTiers []ServiceTier) map[ServiceTier]bool {
	var out map[ServiceTier]bool
	add := func(tier ServiceTier) {
		if out == nil {
			out = make(map[ServiceTier]bool, 2)
		}
		out[tier] = true
	}
	for _, raw := range rawTiers {
		switch NormalizeServiceTier(string(raw)) {
		case ServiceTierFast:
			add(ServiceTierFast)
		case ServiceTierSlow:
			add(ServiceTierSlow)
		}
	}
	return out
}

// SupportedServiceTierSet returns the non-standard service tiers this model can accept.
// Explicit model supported_service_tiers wins; provider supported_service_tiers is
// the next default; preset: codex defaults to fast+slow because the first-party
// transport supports OpenAI service_tier fast/flex.
func (m *ModelConfig) SupportedServiceTierSet(providerPreset string, providerTiers []ServiceTier) map[ServiceTier]bool {
	if m != nil && len(m.SupportedServiceTiers) > 0 {
		return serviceTierSet(m.SupportedServiceTiers)
	}
	if len(providerTiers) > 0 {
		return serviceTierSet(providerTiers)
	}
	if strings.EqualFold(strings.TrimSpace(providerPreset), ProviderPresetCodex) {
		return serviceTierSet([]ServiceTier{ServiceTierFast, ServiceTierSlow})
	}
	return nil
}

// ThinkingConfig controls extended thinking for Anthropic models.
// Type selects one of three mutually-exclusive modes:
//   - "enabled": manual mode; parses Budget, optional Effort and Display
//   - "adaptive": adaptive mode; parses Effort and optional Display
//   - "disabled": thinking disabled; no extra thinking fields are parsed
//
// Budget belongs to manual mode only.
// Effort applies to adaptive mode and DeepSeek enabled thinking.
// Claude enabled thinking uses Budget and ignores Effort.
// Display is valid only for enabled/adaptive modes.
type ThinkingConfig struct {
	Type            string `json:"type,omitempty" yaml:"type,omitempty"`
	Budget          int    `json:"budget,omitempty" yaml:"budget,omitempty"`
	Effort          string `json:"effort,omitempty" yaml:"effort,omitempty"`
	Display         string `json:"display,omitempty" yaml:"display,omitempty"`
	IncludeThoughts *bool  `json:"include_thoughts,omitempty" yaml:"include_thoughts,omitempty"`
	Level           string `json:"level,omitempty" yaml:"level,omitempty"`
}

// ThinkingConfig.Type values. Enabled and adaptive turn thinking on.
const (
	ThinkingTypeEnabled  = "enabled"
	ThinkingTypeAdaptive = "adaptive"
	ThinkingTypeDisabled = "disabled"
)

// ThinkingEffortNone is the effort value that turns thinking off where the
// backend accepts it (DeepSeek, OpenAI reasoning_effort).
const ThinkingEffortNone = "none"

// EffectiveType returns the configured thinking type.
func (t *ThinkingConfig) EffectiveType() string {
	if t == nil {
		return ""
	}
	return t.Type
}

// PromptCacheConfig controls Anthropic prompt caching strategy.
// Mode: "off" | "auto" | "explicit" (default when empty: "explicit", preserving existing behaviour).
// TTL: "" | "5m" (both the 5-minute provider default) | "1h"; applies to every
// breakpoint Chord places, in both auto and explicit mode.
// CacheTools: when true, the last tool definition gets a cache breakpoint.
type PromptCacheConfig struct {
	Mode       string `json:"mode,omitempty" yaml:"mode,omitempty"`
	TTL        string `json:"ttl,omitempty" yaml:"ttl,omitempty"`
	CacheTools *bool  `json:"cache_tools,omitempty" yaml:"cache_tools,omitempty"`
}

// CacheToolsEnabled reports whether tool schema caching is enabled.
// Defaults to false.
func (p *PromptCacheConfig) CacheToolsEnabled() bool {
	if p == nil || p.CacheTools == nil {
		return false
	}
	return *p.CacheTools
}

// ReasoningConfig controls OpenAI reasoning models (o1, o3, o4-mini).
// When set, reasoning_effort is sent in the request and temperature is omitted.
type ReasoningConfig struct {
	Effort string `json:"effort" yaml:"effort"` // e.g. "low" | "medium" | "high" | "xhigh"
	// EffortMap maps the canonical effort value to the provider-specific wire
	// value for this model. Providers that accept only their own effort levels
	// (e.g. a gateway exposing "max" for canonical "high") use this instead of
	// passthrough. Variant-level maps replace the model-level map.
	EffortMap map[string]string `json:"effort_map,omitempty" yaml:"effort_map,omitempty"`
	// Summary defaults to "auto" when reasoning is active so that a visible
	// reasoning summary is captured for later cross-provider replay; "none"
	// disables the summary request explicitly.
	Summary string `json:"summary,omitempty" yaml:"summary,omitempty"` // "auto" | "concise" | "detailed" | "none" | ""
}

// ModelVariant defines a named parameter preset for a model.
// For Anthropic models: set Thinking to override type/budget/effort/display.
// For OpenAI Responses API models: set Reasoning to override effort/summary.
type ModelVariant struct {
	Thinking          *ThinkingConfig    `json:"thinking,omitempty" yaml:"thinking,omitempty"`
	Reasoning         *ReasoningConfig   `json:"reasoning,omitempty" yaml:"reasoning,omitempty"`
	Text              *TextConfig        `json:"text,omitempty" yaml:"text,omitempty"`
	ParallelToolCalls *bool              `json:"parallel_tool_calls,omitempty" yaml:"parallel_tool_calls,omitempty"` // nil = inherit model default; non-nil = override provider request hint
	PromptCache       *PromptCacheConfig `json:"prompt_cache,omitempty" yaml:"prompt_cache,omitempty"`
}

// TextConfig controls output presentation knobs shared by GPT-5 family APIs.
type TextConfig struct {
	Verbosity string `json:"verbosity,omitempty" yaml:"verbosity,omitempty"` // "low" | "medium" | "high"
}

// EffectiveTextVerbosity returns the configured text verbosity.
func (m ModelConfig) EffectiveTextVerbosity() string {
	if m.Text != nil && m.Text.Verbosity != "" {
		return m.Text.Verbosity
	}
	return ""
}

// EffectiveTextVerbosity returns the configured text verbosity.
func (v ModelVariant) EffectiveTextVerbosity() string {
	if v.Text != nil && v.Text.Verbosity != "" {
		return v.Text.Verbosity
	}
	return ""
}

// EffectiveThinkingType returns the resolved thinking type for a model.
func (m ModelConfig) EffectiveThinkingType() string {
	return m.Thinking.EffectiveType()
}

// EffectiveThinkingEffort returns the Anthropic effort setting for a model.
func (m ModelConfig) EffectiveThinkingEffort() string {
	if m.Thinking != nil {
		return m.Thinking.Effort
	}
	return ""
}

// EffectiveThinkingDisplay returns the Anthropic thinking display setting.
func (m ModelConfig) EffectiveThinkingDisplay() string {
	if m.Thinking != nil {
		return m.Thinking.Display
	}
	return ""
}

// EffectiveThinkingType returns the resolved thinking type for a variant.
func (v ModelVariant) EffectiveThinkingType() string {
	return v.Thinking.EffectiveType()
}

// EffectivePromptCacheMode returns the prompt cache mode, defaulting to "explicit".
func (m ModelConfig) EffectivePromptCacheMode() string {
	if m.PromptCache != nil && m.PromptCache.Mode != "" {
		return m.PromptCache.Mode
	}
	return "explicit"
}
