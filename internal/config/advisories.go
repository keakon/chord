package config

import (
	"maps"
	"slices"
)

// Advisories reports settings that load exactly as written but are unlikely
// to behave as intended, such as a thinking block a Chat Completions gateway
// never receives. Unlike semantic issues it changes nothing: the values stay
// configured, so callers log these as warnings and `chord doctor config` lists
// them without failing. Pass the effective config, with the project layer
// merged over the global one: a provider type or a selector set in one layer
// decides what a model configured in the other one needs. A value the loader
// already reset as invalid reads as unset here.
func Advisories(cfg *Config) []string {
	if cfg == nil {
		return nil
	}
	var advisories []string
	for _, providerName := range slices.Sorted(maps.Keys(cfg.Providers)) {
		providerCfg := cfg.Providers[providerName]
		if advisory := gemini3ContractPlacementAdvisory(providerName, providerCfg); advisory != "" {
			advisories = append(advisories, advisory)
		}
		advisories = append(advisories, nativeThinkingSelectorAdvisories(providerName, providerCfg)...)
		advisories = append(advisories, deepSeekContractAdvisories(providerName, providerCfg)...)
	}
	// Catalog freshness hints ride the same advisory channel: they load as
	// written, never fail doctor, and the startup toast reports their count
	// separately from config problems.
	return append(advisories, CatalogFreshnessAdvisories(cfg)...)
}
