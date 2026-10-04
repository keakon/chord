package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/modelcatalog"
)

// DecodeCatalogProfile uses the configuration schema and semantic validators.
// Invalid guidance must reject a snapshot before it replaces a usable catalog.
func DecodeCatalogProfile(profile *modelcatalog.ConfigProfile) (ModelConfig, error) {
	var model ModelConfig
	if profile == nil {
		return model, nil
	}
	values := make(map[string]any, len(profile.Model)+1)
	for key, value := range profile.Model {
		values[key] = value
	}
	if len(profile.Compat) > 0 {
		values["compat"] = profile.Compat
	}
	data, err := json.Marshal(values)
	if err != nil {
		return model, fmt.Errorf("encode catalog profile: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&model); err != nil {
		return model, fmt.Errorf("decode catalog profile: %w", err)
	}
	cfg := &Config{Providers: map[string]ProviderConfig{"catalog": {Models: map[string]ModelConfig{"model": model}}}}
	if issues := collectSemanticIssues(cfg); len(issues) > 0 {
		return model, fmt.Errorf("catalog profile: %s", strings.Join(issues, "; "))
	}
	if model.Thinking != nil {
		if t := model.Thinking; t.Type != "" && t.Type != ThinkingTypeEnabled && t.Type != ThinkingTypeAdaptive && t.Type != ThinkingTypeDisabled {
			return model, fmt.Errorf("catalog profile: invalid thinking type %q", t.Type)
		}
		if model.Thinking.Budget < 0 {
			return model, fmt.Errorf("catalog profile: thinking budget must not be negative")
		}
	}
	if p := model.PromptCache; p != nil {
		if p.Mode != "" && p.Mode != "off" && p.Mode != "auto" && p.Mode != "explicit" {
			return model, fmt.Errorf("catalog profile: invalid prompt_cache mode %q", p.Mode)
		}
		if p.TTL != "" && p.TTL != "5m" && p.TTL != "1h" {
			return model, fmt.Errorf("catalog profile: invalid prompt_cache ttl %q", p.TTL)
		}
	}
	if r := model.Reasoning; r != nil && r.Summary != "" && r.Summary != "auto" && r.Summary != "concise" && r.Summary != "detailed" && r.Summary != "none" {
		return model, fmt.Errorf("catalog profile: invalid reasoning summary %q", r.Summary)
	}
	if t := model.Text; t != nil && t.Verbosity != "" && t.Verbosity != "low" && t.Verbosity != "medium" && t.Verbosity != "high" {
		return model, fmt.Errorf("catalog profile: invalid text verbosity %q", t.Verbosity)
	}
	return model, nil
}

// ValidateCatalogProfiles is shared by source generation and cache installation.
func ValidateCatalogProfiles(catalog *modelcatalog.Catalog) error {
	for _, model := range catalog.Models {
		if _, err := DecodeCatalogProfile(model.Profile); err != nil {
			return fmt.Errorf("model %q: %w", model.ID, err)
		}
	}
	for _, binding := range catalog.Bindings {
		if _, err := DecodeCatalogProfile(binding.Profile); err != nil {
			return fmt.Errorf("binding %s/%s: %w", binding.Endpoint, binding.WireModelID, err)
		}
	}
	return nil
}
