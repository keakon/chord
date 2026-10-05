package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/modelcatalog"
)

var errCatalogConnectionRequired = errors.New("catalog model needs an explicit API connection")

// prepareCatalogAdd recognizes only an explicitly selected, exact catalog ID.
// A documented official connection contributes its explicit protocol recipe.
// Custom URLs resolve model recipes through the config loader.
// Existing providers always keep their endpoint and credential choices.
func prepareCatalogAdd(ref string, provider config.ProviderConfig, exists bool, wire string, opts configAddOptions) (config.ProviderConfig, string, configAddOptions, error) {
	facts, known := modelcatalog.Model(strings.TrimSpace(ref))
	if !known {
		preset, wireID, _ := strings.Cut(strings.TrimSpace(ref), "/")
		if binding, ok := modelcatalog.LookupBinding(preset, wireID); ok {
			facts, known = modelcatalog.Model(binding.ModelID)
			if !exists && opts.url == "" {
				provider.Preset = preset
			}
		}
	}
	if !known {
		return provider, wire, opts, nil
	}
	autoCatalog := opts.catalogID == ""
	if autoCatalog {
		opts.catalogID = facts.ID
	}
	if exists || opts.url != "" || opts.catalogID != facts.ID {
		if autoCatalog {
			if binding, ok := modelcatalog.LookupBinding(provider.Preset, wire); ok && binding.ModelID == facts.ID {
				opts.catalogID = ""
			}
		}
		return provider, wire, opts, nil
	}
	var matches []modelcatalog.Binding
	for _, endpoint := range modelcatalog.EndpointContracts() {
		if provider.Preset != "" && endpoint.PresetID != provider.Preset {
			continue
		}
		if b, ok := modelcatalog.LookupBindingByModelID(endpoint.PresetID, facts.ID); ok {
			matches = append(matches, b)
		}
	}
	vendor, _, _ := strings.Cut(facts.ID, "/")
	for _, b := range matches {
		if b.Endpoint == vendor {
			matches = []modelcatalog.Binding{b}
			break
		}
	}
	if len(matches) == 1 {
		b := matches[0]
		endpoint, _ := modelcatalog.EndpointContract(b.Endpoint)

		provider.Preset = b.Endpoint
		opts.url = endpoint.RequestURL
		if autoCatalog {
			opts.catalogID = ""
		}
		if opts.envVar == "" {
			opts.envVar = endpoint.EnvVar
		}
		return provider, b.WireModelID, opts, nil
	}
	if facts.Connection != nil {
		opts.url = facts.Connection.RequestURL
		if opts.envVar == "" {
			opts.envVar = facts.Connection.EnvVar
		}
		return provider, facts.Connection.WireModelID, opts, nil
	}
	return provider, wire, opts, fmt.Errorf("%w: model %q has no unambiguous API connection; pass --url for your endpoint", errCatalogConnectionRequired, ref)
}
