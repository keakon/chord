package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/modelcatalog"
)

// browseConfigAdd drives the no-argument discovery flow: choose a managed
// provider (an existing one or a new provider under its preset), then one of
// the verified wire models bound to that preset. It returns the provider name
// and wire model the regular guided add continues with. Unbound catalog
// recipes and custom endpoints stay on their explicit flags.
func browseConfigAdd(t *setupTerminal, cfg *config.Config) (string, string, error) {
	providers := catalogAddProviders(cfg)
	labels := make([]string, 0, len(providers))
	for _, choice := range providers {
		label := choice.name
		if choice.exists {
			if choice.name != choice.preset {
				label += " (preset " + choice.preset + ")"
			}
		} else {
			label += " (new provider)"
		}
		labels = append(labels, label)
	}
	choice, err := chooseConfigAddChoice(t, "Provider", labels)
	if err != nil {
		return "", "", err
	}
	provider := providers[choice]

	bindings := catalogAddBindings(provider.preset)
	configured := catalogAddConfiguredModels(cfg, provider.name, provider.preset)
	modelLabels := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		label := binding.WireModelID
		if facts, ok := modelcatalog.BindingFacts(binding); ok {
			label += fmt.Sprintf(" (context %d / output %d)", facts.Context, facts.Output)
		}
		if configured[binding.WireModelID] {
			label += " — configured"
		}
		modelLabels = append(modelLabels, label)
	}
	model, err := chooseConfigAddChoice(t, "Model on "+provider.preset, modelLabels)
	if err != nil {
		return "", "", err
	}
	return provider.name, bindings[model].WireModelID, nil
}

// configAddProviderChoice is one selectable target of the discovery flow: the
// provider name to write under, the managed preset whose catalog to browse,
// and whether the provider already exists.
type configAddProviderChoice struct {
	name   string
	preset string
	exists bool
}

func catalogAddProviders(cfg *config.Config) []configAddProviderChoice {
	presets := make([]string, 0, 4)
	for _, endpoint := range modelcatalog.EndpointContracts() {
		if !slices.Contains(presets, endpoint.PresetID) {
			presets = append(presets, endpoint.PresetID)
		}
	}
	slices.Sort(presets)
	configured := map[string][]string{}
	if cfg != nil {
		for name, provider := range cfg.Providers {
			preset := strings.ToLower(strings.TrimSpace(provider.Preset))
			if slices.Contains(presets, preset) {
				configured[preset] = append(configured[preset], name)
			}
		}
	}
	var out []configAddProviderChoice
	for _, preset := range presets {
		names := configured[preset]
		if len(names) == 0 {
			out = append(out, configAddProviderChoice{name: preset, preset: preset})
			continue
		}
		slices.Sort(names)
		for _, name := range names {
			out = append(out, configAddProviderChoice{name: name, preset: preset, exists: true})
		}
	}
	return out
}

func catalogAddBindings(preset string) []modelcatalog.Binding {
	bindings := slices.Clone(modelcatalog.BindingsForEndpoint(preset))
	slices.SortFunc(bindings, func(a, b modelcatalog.Binding) int {
		return strings.Compare(a.WireModelID, b.WireModelID)
	})
	return bindings
}

// catalogAddConfiguredModels lists the wire model IDs the named provider
// already uses, whether they are defined directly or referenced from a pool
// through an alias carrying a catalog binding.
func catalogAddConfiguredModels(cfg *config.Config, providerName, preset string) map[string]bool {
	out := map[string]bool{}
	if cfg == nil {
		return out
	}
	provider, ok := cfg.Providers[providerName]
	if !ok {
		return out
	}
	mark := func(wireModel string, model config.ModelConfig) {
		if model.Catalog != nil && model.Catalog.ID != "" {
			if binding, found := modelcatalog.LookupBindingByModelID(preset, model.Catalog.ID); found {
				wireModel = binding.WireModelID
			}
		}
		if wireModel != "" {
			out[wireModel] = true
		}
	}
	for name, model := range provider.Models {
		if _, bound := modelcatalog.LookupBinding(preset, name); bound {
			out[name] = true
		}
		mark(name, model)
	}
	for _, refs := range cfg.ModelPools {
		for _, ref := range refs {
			base, _ := config.ParseModelRef(strings.TrimSpace(ref))
			name, modelName := config.SplitProviderModelRef(base)
			if name != providerName {
				continue
			}
			if model, exists := provider.Models[modelName]; exists {
				mark(modelName, model)
				continue
			}
			if _, bound := modelcatalog.LookupBinding(preset, modelName); bound {
				out[modelName] = true
			}
		}
	}
	return out
}

// chooseConfigAddChoice shows a menu and maps the selected label back to its
// index. Callers pass unique labels so the mapping is unambiguous.
func chooseConfigAddChoice(t *setupTerminal, label string, labels []string) (int, error) {
	value, err := configAddMenu(t, label, labels, "", "")
	if err != nil {
		return 0, err
	}
	return slices.Index(labels, value), nil
}
