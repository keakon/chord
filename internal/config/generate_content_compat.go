package config

import "fmt"

// GenerateContentCompatConfig controls native Generate Content request handling.
// Nil leaves inherit defaults; false explicitly disables continuation.
type GenerateContentCompatConfig struct {
	AutomaticContinuation    *bool `json:"automatic_continuation,omitempty" yaml:"automatic_continuation,omitempty"`
	SingleRequestOutputLimit *int  `json:"single_request_output_limit,omitempty" yaml:"single_request_output_limit,omitempty"`
}

// ResolveGenerateContentCompat resolves materialized configuration into an owned snapshot.
func ResolveGenerateContentCompat(provider *ProviderCompatConfig, model ModelConfig) *GenerateContentCompatConfig {
	var result *GenerateContentCompatConfig
	merge := func(src *GenerateContentCompatConfig) {
		if src == nil {
			return
		}
		if result == nil {
			result = &GenerateContentCompatConfig{}
		}
		if src.AutomaticContinuation != nil {
			result.AutomaticContinuation = new(*src.AutomaticContinuation)
		}
		if src.SingleRequestOutputLimit != nil {
			result.SingleRequestOutputLimit = new(*src.SingleRequestOutputLimit)
		}
	}
	if provider != nil {
		merge(provider.GenerateContent)
	}
	if model.Compat != nil {
		merge(model.Compat.GenerateContent)
	}
	return result
}

func ValidateProviderGenerateContentCompat(name string, cfg ProviderConfig) error {
	check := func(path string, compat *GenerateContentCompatConfig) error {
		if compat != nil && compat.SingleRequestOutputLimit != nil && *compat.SingleRequestOutputLimit <= 0 {
			return fmt.Errorf("%s.compat.generate_content.single_request_output_limit must be positive", path)
		}
		return nil
	}
	if cfg.Compat != nil {
		if err := check(name, cfg.Compat.GenerateContent); err != nil {
			return err
		}
	}
	for id, model := range cfg.Models {
		if model.Compat != nil {
			if err := check(name+".models."+id, model.Compat.GenerateContent); err != nil {
				return err
			}
		}
	}
	return nil
}

func resetInvalidGenerateContentCompat(cfg ProviderConfig) ProviderConfig {
	reset := func(value *GenerateContentCompatConfig) *GenerateContentCompatConfig {
		result := ResolveGenerateContentCompat(&ProviderCompatConfig{GenerateContent: value}, ModelConfig{})
		if result != nil && result.SingleRequestOutputLimit != nil && *result.SingleRequestOutputLimit <= 0 {
			result.SingleRequestOutputLimit = nil
		}
		return result
	}
	if cfg.Compat != nil {
		copy := *cfg.Compat
		copy.GenerateContent = reset(copy.GenerateContent)
		cfg.Compat = &copy
	}
	for id, model := range cfg.Models {
		if model.Compat != nil {
			copy := *model.Compat
			copy.GenerateContent = reset(copy.GenerateContent)
			model.Compat = &copy
			cfg.Models[id] = model
		}
	}
	return cfg
}
